package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	gocni "github.com/containerd/go-cni"
	"github.com/docker/distribution/reference"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/openfaas/faas-provider/types"
	"github.com/openfaas/faasd/pkg"
	cninetwork "github.com/openfaas/faasd/pkg/cninetwork"
	"github.com/openfaas/faasd/pkg/provider/handlers/switcher"
	"github.com/openfaas/faasd/pkg/service"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/resource"
)

const annotationLabelPrefix = "com.openfaas.annotations."

func GetInstanceID(serviceName string, id uint64) string {
	return fmt.Sprintf("%s-%d", serviceName, id)
}

// MakeDeployHandler returns a handler to deploy a function
func MakeDeployHandler(client *containerd.Client, cni gocni.CNI,
	secretMountPath, checkpointDir string, alwaysPull bool) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		start_ := time.Now()
		defer func() {
			log.Printf("Total deploy handler tooks %s\n", time.Since(start_))
		}()
		if r.Body == nil {
			http.Error(w, "expected a body", http.StatusBadRequest)
			return
		}

		defer r.Body.Close()

		body, _ := ioutil.ReadAll(r.Body)
		log.Printf("[Deploy] request: %s\n", string(body))

		req := types.FunctionDeployment{}
		err := json.Unmarshal(body, &req)
		if err != nil {
			log.Printf("[Deploy] - error parsing input: %s\n", err)
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		namespace := getRequestNamespace(req.Namespace)

		// Check if namespace exists, and it has the openfaas label
		valid, err := validNamespace(client.NamespaceService(), namespace) // [825us]

		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if !valid {
			http.Error(w, "namespace not valid", http.StatusBadRequest)
			return
		}

		namespaceSecretMountPath := getNamespaceSecretMountPath(secretMountPath, namespace)
		err = validateSecrets(namespaceSecretMountPath, req.Secrets)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		name := req.Service
		ctx := namespaces.WithNamespace(context.Background(), namespace)

		instanceID, deployErr := deploy(ctx, req, client, cni,
			namespaceSecretMountPath, checkpointDir, alwaysPull)
		if deployErr != nil {
			log.Printf("[Deploy] error deploying %s, error: %s\n", name, deployErr)
			http.Error(w, deployErr.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(instanceID))
	}
}

// prepull is an optimization which means an image can be pulled before a deployment
// request, since a deployment request first deletes the active function before
// trying to deploy a new one.
func prepull(ctx context.Context, req types.FunctionDeployment, client *containerd.Client, alwaysPull bool) (containerd.Image, error) {
	start := time.Now()
	r, err := reference.ParseNormalizedNamed(req.Image)
	if err != nil {
		return nil, err
	}

	imgRef := reference.TagNameOnly(r).String()

	snapshotter := ""
	if val, ok := os.LookupEnv("snapshotter"); ok {
		snapshotter = val
	}

	image, err := service.PrepareImage(ctx, client, imgRef, snapshotter, alwaysPull)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to pull image %s", imgRef)
	}

	size, _ := image.Size(ctx)
	log.Printf("Image for: %s size: %d, took: %fs\n", image.Name(), size, time.Since(start).Seconds())

	return image, nil
}

func classicalDeploy(ctx context.Context, req types.FunctionDeployment, client *containerd.Client,
	cni gocni.CNI, secretMountPath string, alwaysPull bool) (string, error) {

	log.Printf("classical deploy for request %+v", req)
	snapshotter := ""
	if val, ok := os.LookupEnv("snapshotter"); ok {
		snapshotter = val
	}

	image, err := prepull(ctx, req, client, alwaysPull)
	if err != nil {
		return "", err
	}

	envs := prepareEnv(req.EnvProcess, req.EnvVars)
	mounts := getOSMounts()

	for _, secret := range req.Secrets {
		mounts = append(mounts, specs.Mount{
			Destination: path.Join("/var/openfaas/secrets", secret),
			Type:        "bind",
			Source:      path.Join(secretMountPath, secret),
			Options:     []string{"rbind", "ro"},
		})
	}

	serviceName := req.Service
	id, err := lambdaManager.AddNewInstance(serviceName)
	if err != nil {
		return "", err
	}

	instanceID := GetInstanceID(serviceName, id)
	labels, err := buildLabels(&req)
	if err != nil {
		return "", fmt.Errorf("unable to apply labels to container: %s, error: %w", instanceID, err)
	}

	var memory *specs.LinuxMemory
	if req.Limits != nil && len(req.Limits.Memory) > 0 {
		memory = &specs.LinuxMemory{}

		qty, err := resource.ParseQuantity(req.Limits.Memory)
		if err != nil {
			log.Printf("error parsing (%q) as quantity: %s", req.Limits.Memory, err.Error())
		}
		v := qty.Value()
		memory.Limit = &v
	}

	appOverlay, err := rootfsManager.PrepareAppOverlay(serviceName, id)
	if err != nil {
		return "", errors.Wrapf(err, "prepare app overlay when classicalDeploy for %s failed", instanceID)
	}
	// we prepare an overlayfs to mount into container
	// so that it can be read-write
	mounts = append(mounts, specs.Mount{
		Destination: "/home/app",
		Type:        "bind",
		Source:      appOverlay,
		Options:     []string{"bind"},
	})

	log.Println(mounts)

  // By huang-jl: probably to use oci.WithRootFSPath() to use costomized rootfs
	container, err := client.NewContainer(
		ctx,
		instanceID,
		containerd.WithImage(image),
		containerd.WithSnapshotter(snapshotter),
		containerd.WithNewSnapshot(instanceID+"-snapshot", image),
		containerd.WithNewSpec(oci.WithImageConfig(image),
			oci.WithHostname(instanceID),
			oci.WithCapabilities([]string{"CAP_NET_RAW"}),
			oci.WithMounts(mounts),
			oci.WithEnv(envs),
			withMemory(memory)),
		containerd.WithContainerLabels(labels),
	)

	if err != nil {
		return "", errors.Wrapf(err, "unable to create container: %s", instanceID)
	}

	t, err := createTask(ctx, container, cni)
	if err != nil {
		return "", err
	}

	// By huang-jl: we do not take over the control of container, since it will
	// broken the containerd
	// if _, err = container.TakeOver(ctx, -1); err != nil {
	// 	return errors.Wrapf(err, "take over container %s failed", instanceID)
	// }
	updater, err := initContainerInfo(ctx, client, container, serviceName, id, t)
	if err != nil {
		return "", err
	}
	_, err = lambdaManager.UpdateInstance(serviceName, id, updater)
	return instanceID, err
}

// By huang-jl: CRIU will restore the property of cgroup, we do not need to care about that here.
func switchDeploy(ctx context.Context, req types.FunctionDeployment, client *containerd.Client,
	checkpointDir string, candidate ContainerInfo, alwaysPull bool) (string, error) {
	wrapLambdaManagerErr := func(e error) error {
		return errors.Wrap(e, "switcher update lambdaManager failed")
	}

	// _, err := prepull(ctx, req, client, alwaysPull) // [7.5ms]
	if len(req.Secrets) > 0 {
		return "", fmt.Errorf("switch do not support secrets for now")
	}
	// add instance to lambda manager
	serviceName := req.Service
	id, err := lambdaManager.AddNewInstance(serviceName)
	if err != nil {
		return "", wrapLambdaManagerErr(err)
	}
	defer func() {
		// clean up
		if err != nil {
			lambdaManager.RemoveInstance(serviceName, id)
		}
	}()

	start := time.Now()
	if err := rootfsManager.PrepareSwitchRootfs(serviceName, id, candidate); err != nil {
		return "", err
	}
	log.Printf("PrepareSwitchRootfs spent %s\n", time.Since(start))

	config := switcher.SwitcherConfig{
		CRIUWorkDirectory: path.Join(pkg.FaasdCRIUResotreWorkPrefix, GetInstanceID(serviceName, id)),
		CRIULogFileName:   "restore.log",
		// TODO(huang-jl) for better performance, we need modify it to 0
		CRIULogLevel: 4,
	}
	switcher, err := switcher.SwitchFor(serviceName, checkpointDir, int(candidate.pid), config)
	if err != nil {
		return "", errors.Wrapf(err, "switch from %s to %s failed", candidate.serviceName, serviceName)
	}
	// we need update lambdaManager
	old, err := lambdaManager.RemoveInstance(candidate.serviceName, candidate.id)
	if err != nil {
		return "", wrapLambdaManagerErr(err)
	}
	if old.status != SWITCHING {
		log.Printf("find weird instance when switching: %+v", *old)
	}
	newPid := switcher.PID()
	if newPid <= 0 {
		return "", fmt.Errorf("switchDeploy get wierd process id %d", newPid)
	}

	_, err = lambdaManager.UpdateInstance(serviceName, id, func(info *ContainerInfo) error {
		info.pid = newPid
		info.status = FINISHED
		// new lambda instance share the same rootfs
		// and ipaddress as candidate
		info.IpAddress = candidate.IpAddress
		info.rootfs = candidate.rootfs
		info.cniID = candidate.cniID
		return nil
	})
	return GetInstanceID(serviceName, id), err
}

// Return the ID of the instance, which can be used for post request
func deploy(ctx context.Context, req types.FunctionDeployment, client *containerd.Client,
	cni gocni.CNI, secretMountPath, checkpointDir string, alwaysPull bool) (string, error) {
	// first check whether we have checkpoint
	if !hasCheckpoint(req.Service) {
		return classicalDeploy(ctx, req, client, cni, secretMountPath, alwaysPull)
	}
	// then check whether we have candidate to switch from
	candidate, err := lambdaManager.ChooseSwitchCandidate()
	if err != nil {
		if errors.Is(err, ErrNotFoundIdleInstance) {
			return classicalDeploy(ctx, req, client, cni, secretMountPath, alwaysPull)
		}
		return "", err
	}
	// we find a candidate to switch
	return switchDeploy(ctx, req, client, checkpointDir, candidate, alwaysPull)
}

func buildLabels(request *types.FunctionDeployment) (map[string]string, error) {
	// Adapted from faas-swarm/handlers/deploy.go:buildLabels
	labels := map[string]string{}

	if request.Labels != nil {
		for k, v := range *request.Labels {
			labels[k] = v
		}
	}

	if request.Annotations != nil {
		for k, v := range *request.Annotations {
			key := fmt.Sprintf("%s%s", annotationLabelPrefix, k)
			if _, ok := labels[key]; !ok {
				labels[key] = v
			} else {
				return nil, errors.New(fmt.Sprintf("Key %s cannot be used as a label due to a conflict with annotation prefix %s", k, annotationLabelPrefix))
			}
		}
	}

	return labels, nil
}

func createTask(ctx context.Context, container containerd.Container, cni gocni.CNI) (containerd.Task, error) {

	name := container.ID()

	task, taskErr := container.NewTask(ctx, cio.BinaryIO("/usr/local/bin/faasd", nil))

	if taskErr != nil {
		return nil, fmt.Errorf("unable to start task: %s, error: %w", name, taskErr)
	}

	log.Printf("Container ID: %s\tTask ID %s:\tTask PID: %d\t\n", name, task.ID(), task.Pid())

	start := time.Now()
	labels := map[string]string{}
	_, err := cninetwork.CreateCNINetwork(ctx, cni, task, labels)
	log.Printf("create network latency: %s", time.Since(start))

	if err != nil {
		return nil, err
	}

	ip, err := cninetwork.GetIPAddress(name, task.Pid())
	if err != nil {
		return nil, err
	}

	log.Printf("%s has IP: %s.\n", name, ip)

	_, waitErr := task.Wait(ctx)
	if waitErr != nil {
		return nil, errors.Wrapf(waitErr, "Unable to wait for task to start: %s", name)
	}

	if startErr := task.Start(ctx); startErr != nil {
		return nil, errors.Wrapf(startErr, "Unable to start task: %s", name)
	}
	return task, nil
}

func prepareEnv(envProcess string, reqEnvVars map[string]string) []string {
	envs := []string{}
	fprocessFound := false
	fprocess := "fprocess=" + envProcess
	if len(envProcess) > 0 {
		fprocessFound = true
	}

	for k, v := range reqEnvVars {
		if k == "fprocess" {
			fprocessFound = true
			fprocess = v
		} else {
			envs = append(envs, k+"="+v)
		}
	}
	if fprocessFound {
		envs = append(envs, fprocess)
	}
	return envs
}

func validateSecrets(secretMountPath string, secrets []string) error {
	for _, secret := range secrets {
		if _, err := os.Stat(path.Join(secretMountPath, secret)); err != nil {
			return fmt.Errorf("unable to find secret: %s", secret)
		}
	}
	return nil
}

func withMemory(mem *specs.LinuxMemory) oci.SpecOpts {
	return func(ctx context.Context, _ oci.Client, c *containers.Container, s *oci.Spec) error {
		if mem != nil {
			if s.Linux == nil {
				s.Linux = &specs.Linux{}
			}
			if s.Linux.Resources == nil {
				s.Linux.Resources = &specs.LinuxResources{}
			}
			if s.Linux.Resources.Memory == nil {
				s.Linux.Resources.Memory = &specs.LinuxMemory{}
			}
			s.Linux.Resources.Memory.Limit = mem.Limit
		}
		return nil
	}
}

func initContainerInfo(ctx context.Context, client *containerd.Client, ctr containerd.Container,
	serviceName string, id uint64, t containerd.Task) (InstanceInfoUpdateFunc, error) {
	pid := t.Pid()
	name := ctr.ID()
	ip, err := cninetwork.GetIPAddress(name, pid) // [95us]
	if err != nil {
		return nil, err
	}
	i, err := ctr.Info(ctx) // [235us]
	if err != nil {
		return nil, err
	}
	s := client.SnapshotService(i.Snapshotter)
	ms, err := s.Mounts(ctx, i.SnapshotKey) // [241us]
	if err != nil {
		return nil, err
	}
	ns, err := namespaces.NamespaceRequired(ctx)
	if err != nil {
		return nil, err
	}
	rootOverlay, err := parseRootFromSnapshotter(ns, ctr.ID(), ms)
	if err != nil {
		return nil, err
	}

	cniID := cninetwork.NetID(t)
	// try to decrease the work need to do in updateFunc
	return func(info *ContainerInfo) error {
		info.serviceName = serviceName
		info.id = id
		info.pid = int(pid)
		info.rootfs.work = rootOverlay.workdir
		info.rootfs.upper = rootOverlay.upperdir
		info.rootfs.merged = rootOverlay.mergedir
		info.status = FINISHED
		info.IpAddress = ip
		info.cniID = cniID
		return nil
	}, nil
}
