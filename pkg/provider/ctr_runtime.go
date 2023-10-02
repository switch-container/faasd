package provider

import (
	"context"
	"fmt"
	"log"
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
	"github.com/openfaas/faasd/pkg/metrics"
	"github.com/openfaas/faasd/pkg/provider/switcher"
	"github.com/openfaas/faasd/pkg/service"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/resource"
)

const annotationLabelPrefix = "com.openfaas.annotations."

type CtrRuntime struct {
	Client *containerd.Client
	cni    gocni.CNI
	// always try pull from registry for most up-to-date docker images
	alwaysPull      bool
	rootfsManager   *RootfsManager
	checkpointCache *CheckpointCache
}

func (r CtrRuntime) ColdStart(req types.FunctionDeployment, id uint64) (*CtrInstance, error) {
	var err error
	begin := time.Now()
	defer func() {
		if err == nil {
			if err = metrics.GetMetricLogger().Emit(pkg.ColdStartLatencyMetric, req.Service, time.Since(begin)); err != nil {
				log.Printf("[Error] Emit ColdStartLatencyMetric failed %s", err)
			}
			if err = metrics.GetMetricLogger().Emit(pkg.ColdStartCountMetric, req.Service, 1); err != nil {
				log.Printf("[Error] Emit ColdStartCountMetric failed %s", err)
			}
		}
	}()
	instanceID := GetInstanceID(req.Service, id)
	namespace := GetRequestNamespace(req.Namespace)
	// Check if namespace exists, and it has the openfaas label
	valid, err := ValidNamespace(r.Client.NamespaceService(), namespace) // [825us]
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, fmt.Errorf("namespace not valid")
	}
	ctx := namespaces.WithNamespace(context.Background(), namespace)
	ctr, err := r.coldStartInstance(ctx, req, instanceID)
	if err != nil {
		return nil, err
	}
	return r.InitCtrInstance(ctx, ctr, req.Service, id)
}

func (runtime CtrRuntime) prepull(ctx context.Context, req types.FunctionDeployment) (containerd.Image, error) {
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

	image, err := service.PrepareImage(ctx, runtime.Client, imgRef, snapshotter, runtime.alwaysPull)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to pull image %s", imgRef)
	}

	size, _ := image.Size(ctx)
	log.Printf("Image for: %s size: %d, took: %fs\n", image.Name(), size, time.Since(start).Seconds())

	return image, nil
}

func (r CtrRuntime) coldStartInstance(ctx context.Context, req types.FunctionDeployment, instanceID string) (containerd.Container, error) {
	log.Printf("cold start %s for request %+v", instanceID, req)
	snapshotter := ""
	if val, ok := os.LookupEnv("snapshotter"); ok {
		snapshotter = val
	}

	// no always pull
	image, err := r.prepull(ctx, req)
	if err != nil {
		return nil, err
	}

	envs := prepareEnv(req.EnvProcess, req.EnvVars)
	mounts := getOSMounts()

	labels, err := buildLabels(&req)
	if err != nil {
		return nil, fmt.Errorf("unable to apply labels to container: %s, error: %w", instanceID, err)
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

	wrapper := func(ctx context.Context, client *containerd.Client, c *containers.Container) error {
		start := time.Now()
		defer func() {
			log.Printf("Snapshot prepare() spent %s\n", time.Since(start))
		}()
		return containerd.WithNewSnapshot(instanceID+"-snapshot", image)(ctx, client, c)
	}

	// By huang-jl: probably to use oci.WithRootFSPath() to use costomized rootfs
	container, err := r.Client.NewContainer(
		ctx,
		instanceID,
		containerd.WithImage(image),
		containerd.WithSnapshotter(snapshotter),
		// containerd.WithNewSnapshot(instanceID+"-snapshot", image),
		wrapper,
		containerd.WithNewSpec(oci.WithImageConfig(image),
			oci.WithHostname(instanceID),
			oci.WithCapabilities([]string{"CAP_NET_RAW"}),
			oci.WithMounts(mounts),
			oci.WithEnv(envs),
			withMemory(memory)),
		containerd.WithContainerLabels(labels),
	)

	if err != nil {
		return nil, errors.Wrapf(err, "unable to create container: %s", instanceID)
	}

	if err := r.createTask(ctx, container); err != nil {
		return nil, err
	}

	return container, nil
}

// generate CtrInstance from containerd.Container
func (r CtrRuntime) InitCtrInstance(ctx context.Context, ctr containerd.Container, serviceName string,
	id uint64) (*CtrInstance, error) {
	task, err := ctr.Task(ctx, nil)
	if err != nil {
		return nil, err
	}
	pid := task.Pid()
	name := ctr.ID()
	ip, err := cninetwork.GetIPAddress(name, pid) // [95us]
	if err != nil {
		return nil, err
	}
	i, err := ctr.Info(ctx) // [235us]
	if err != nil {
		return nil, err
	}
	s := r.Client.SnapshotService(i.Snapshotter)
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

	cniID := cninetwork.NetID(task)
	return &CtrInstance{
		LambdaName: serviceName,
		ID:         id,
		Pid:        int(pid),
		rootfs:     &rootOverlay,
		IpAddress:  ip,
		cniID:      cniID,
		status:     IDLE,
	}, nil
}

// By huang-jl: CRIU will restore the property of cgroup, we do not need to care about that here.
func (r CtrRuntime) SwitchStart(req types.FunctionDeployment, id uint64, candidate *CtrInstance) (*CtrInstance, error) {
	var (
		err        error
		appOverlay *OverlayInfo
	)

	if len(req.Secrets) > 0 {
		return nil, fmt.Errorf("switch do not support secrets for now")
	}
	begin := time.Now()
	defer func() {
		if err == nil {
			if err = metrics.GetMetricLogger().Emit(pkg.SwitchLatencyMetric, req.Service, time.Since(begin)); err != nil {
				log.Printf("[Error] Emit SwitchLatencyMetric failed %s", err)
			}
			if err = metrics.GetMetricLogger().Emit(pkg.SwitchCountMetric, req.Service, 1); err != nil {
				log.Printf("[Error] Emit SwitchCountMetric failed %s", err)
			}
		}
	}()
	// add instance to lambda manager
	serviceName := req.Service

	start := time.Now()
	appOverlay, err = r.rootfsManager.PrepareSwitchRootfs(serviceName, candidate)
	if err != nil {
		return nil, err
	}
	if err = metrics.GetMetricLogger().Emit(pkg.PrepareSwitchFSLatency, serviceName, time.Since(start)); err != nil {
		log.Printf("emit PrepareSwitchFSLatency metric failed: %s", err)
	}

	config := switcher.SwitcherConfig{
		CRIUWorkDirectory: path.Join(pkg.FaasdCRIUResotreWorkPrefix, GetInstanceID(serviceName, id)),
		CRIULogFileName:   "restore.log",
		// TODO(huang-jl) for better performance, we need modify it to 0
		CRIULogLevel: 4,
	}
	switcher, err := switcher.SwitchFor(serviceName, r.checkpointCache.checkpointDir,
		int(candidate.Pid), config)
	if err != nil {
		return nil, errors.Wrapf(err, "switch from %s to %s failed", candidate.LambdaName, serviceName)
	}
	newPid := switcher.PID()
	if newPid <= 0 {
		return nil, fmt.Errorf("switchDeploy get wierd process id %d", newPid)
	}

	if appOverlay == nil {
		return nil, fmt.Errorf("appOverlay is null when switch deploy!\n")
	}

	newInstance := candidate
	newInstance.LambdaName = serviceName
	newInstance.ID = id
	newInstance.Pid = newPid
	newInstance.appOverlay = appOverlay
	// ipaddress, cniID, rootfs will not change
	return newInstance, nil
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

func (r CtrRuntime) createTask(ctx context.Context, container containerd.Container) error {

	name := container.ID()

	task, taskErr := container.NewTask(ctx, cio.BinaryIO("/usr/local/bin/faasd", nil))

	if taskErr != nil {
		return fmt.Errorf("unable to start task: %s, error: %w", name, taskErr)
	}

	log.Printf("Container ID: %s\tTask ID %s:\tTask PID: %d\t\n", name, task.ID(), task.Pid())

	start := time.Now()
	labels := map[string]string{}
	_, err := cninetwork.CreateCNINetwork(ctx, r.cni, task, labels)
	log.Printf("create network latency: %s", time.Since(start))

	if err != nil {
		return err
	}

	ip, err := cninetwork.GetIPAddress(name, task.Pid())
	if err != nil {
		return err
	}

	log.Printf("%s has IP: %s.\n", name, ip)

	_, waitErr := task.Wait(ctx)
	if waitErr != nil {
		return errors.Wrapf(waitErr, "Unable to wait for task to start: %s", name)
	}

	if startErr := task.Start(ctx); startErr != nil {
		return errors.Wrapf(startErr, "Unable to start task: %s", name)
	}
	return nil
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

// getOSMounts provides a mount for os-specific files such
// as the hosts file and resolv.conf
func getOSMounts() []specs.Mount {
	// Prior to hosts_dir env-var, this value was set to
	// os.Getwd()
	hostsDir := "/var/lib/faasd"
	mounts := []specs.Mount{}
	mounts = append(mounts, specs.Mount{
		Destination: "/etc/resolv.conf",
		Type:        "bind",
		Source:      path.Join(hostsDir, "resolv.conf"),
		Options:     []string{"rbind", "ro"},
	})

	mounts = append(mounts, specs.Mount{
		Destination: "/etc/hosts",
		Type:        "bind",
		Source:      path.Join(hostsDir, "hosts"),
		Options:     []string{"rbind", "ro"},
	})
	return mounts
}
