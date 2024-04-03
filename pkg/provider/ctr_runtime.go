package provider

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"syscall"
	"time"

	"github.com/antihax/optional"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/runtime/v2/runc/options"
	gocni "github.com/containerd/go-cni"
	"github.com/docker/distribution/reference"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/openfaas/faas-provider/types"
	"github.com/openfaas/faasd/pkg"
	cninetwork "github.com/openfaas/faasd/pkg/cninetwork"
	"github.com/openfaas/faasd/pkg/metrics"
	"github.com/openfaas/faasd/pkg/provider/config"
	"github.com/openfaas/faasd/pkg/provider/switcher"
	"github.com/openfaas/faasd/pkg/service"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"github.com/switch-container/faasd/pkg/provider/faasnap"
	"github.com/switch-container/faasd/pkg/provider/faasnap/api/swagger"
	"golang.org/x/sys/unix"
	"k8s.io/apimachinery/pkg/api/resource"
)

const annotationLabelPrefix = "com.openfaas.annotations."

var crlogger = log.With().
	Str("component", "[CtrRuntime]").
	Logger()

type StartNewCtrReq struct {
	types.FunctionDeployment
	id       uint64
	decision DeployDecision
	notify   chan<- StartNewCtrRes
}

type StartNewCtrRes struct {
	instance *CtrInstance
	err      error
}

type KillCtrReq struct {
	instance *CtrInstance
	notify   chan<- error
}

type CtrRuntime struct {
	Client *containerd.Client
	cni    gocni.CNI
	// always try pull from registry for most up-to-date docker images
	alwaysPull      bool
	rootfsManager   *RootfsManager
	checkpointCache *CheckpointCache
	fnm             *faasnap.FaasnapNetworkManager
	killCh          chan<- KillCtrReq
	workerCh        chan<- StartNewCtrReq
	reapCh          chan<- int
}

func NewCtrRuntime(client *containerd.Client, cni gocni.CNI, rootfsManager *RootfsManager,
	checkpointCache *CheckpointCache, config *config.ProviderConfig) CtrRuntime {
	var fnm *faasnap.FaasnapNetworkManager
	if config.EnableFaasnap {
		fnm = faasnap.NewFaasnapNetworkManager()
	}
	// TODO(huang-jl) Is an 2-element channel enough ?
	workerCh := make(chan StartNewCtrReq, 2)
	reapCh := make(chan int, 1024)
	// kill channel has higher buffer capacity than start, as
	// we try to not block users
	killCh := make(chan KillCtrReq, 16)
	r := CtrRuntime{
		Client:          client,
		cni:             cni,
		alwaysPull:      config.AlwaysPull,
		rootfsManager:   rootfsManager,
		checkpointCache: checkpointCache,
		killCh:          killCh,
		workerCh:        workerCh,
		reapCh:          reapCh,
		fnm:             fnm,
	}
	for i := 0; i < config.StartConcurrency; i++ {
		go r.startWork(workerCh)
	}
	for i := 0; i < config.KillConcurrency; i++ {
		go r.killWork(killCh)
	}
	go r.reap(reapCh)
	return r
}

// NOTE by huang-jl:
// kill instance actually has two sub-steps:
// 1. Push the kill request into request queue (i.e., kill channel)
// 2. kill worker actually kill the instance
//
// Since our kill channel has size limit, so even
// step 1 might blocked (under high kill pressure).
//
// So the async here has two meanings:
// 1. whether wait for step 1 to finish
// 2. whether wait for step 2 to finish
//
// If `blockUntilEnqueu` is true, means that we have to wait step 1 to succeed.
//
// Return value indicate that enqueue is succeed or not (if `blockUntilEnqueu`
// is true, the return value should always be true).
func (r CtrRuntime) KillInstanceAsync(instance *CtrInstance, blockUntilEnqueue bool) bool {
	req := KillCtrReq{
		instance: instance,
	}
	if blockUntilEnqueue {
		r.killCh <- req
		crlogger.Debug().Str("service name", instance.ServiceName).
			Str("instance id", instance.GetInstanceID()).Msg("kill instance async enqueue")
		return true
	}
	select {
	case r.killCh <- req:
		crlogger.Debug().Str("service name", instance.ServiceName).
			Str("instance id", instance.GetInstanceID()).Msg("kill instance async enqueue")
		return true
	default:
		return false
	}
}

func (r CtrRuntime) KillInstanceSync(instance *CtrInstance) error {
	notify := make(chan error)
	req := KillCtrReq{
		instance: instance,
		notify:   notify,
	}
	r.killCh <- req
	err := <-notify
	return err
}

func (r CtrRuntime) killInstance(ctrInstance *CtrInstance) error {
	ctrInstance.status = INVALID
	return ctrInstance.Kill()
}

func (r CtrRuntime) killWork(ch <-chan KillCtrReq) {
	for killReq := range ch {
		var err error
		instance := killReq.instance // supposed not to be empty
		instanceID := instance.GetInstanceID()
		if err = r.killInstance(instance); err != nil {
			crlogger.Error().Err(err).Str("service name", instance.ServiceName).
				Str("instance", instanceID).Msg("kill instance failed")
		} else {
			crlogger.Info().Str("service name", instance.ServiceName).
				Str("instance", instanceID).Msg("kill instance succeed")
		}
		if killReq.notify != nil {
			killReq.notify <- err
		}
	}
}

// reap switched container process
func (r CtrRuntime) reap(reapCh <-chan int) {
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGCHLD)
	var pidsToWait []int
	for {
		select {
		case <-sigc:
			// This is a trick for delete element while loop
			count := 0
			for _, pid := range pidsToWait {
				wpid, err := syscall.Wait4(pid, nil, syscall.WNOHANG, nil)
				if err == nil && wpid == pid {
					// wait succeed
					continue
				} else {
					// pid has not terminate
					// we need wait4 it next time
					pidsToWait[count] = pid
					count++
				}
			}
			pidsToWait = pidsToWait[:count]
		case pid, hasMore := <-reapCh:
			if !hasMore {
				return
			}
			if pid > 0 {
				pidsToWait = append(pidsToWait, pid)
			}
		}
	}
}

// This is the worker that cold start container
func (r CtrRuntime) startWork(workerCh <-chan StartNewCtrReq) {
	var (
		stage          = 0
		lastActiveTime = time.Now()
		dur            time.Duration
	)
	for req := range workerCh {
		if r.fnm != nil {
			dur = time.Since(lastActiveTime)
		}
		instance, err := r.startNewCtr(req)
		res := StartNewCtrRes{instance: instance, err: err}
		req.notify <- res
		if r.fnm != nil {
			// we are using faasnap:
			// as faasnap start latency is relatively low
			// if start with high concurrency, it might overwhelm
			// the system
			switch stage {
			case 0:
				time.Sleep(100 * time.Millisecond)
				lastActiveTime = time.Now()
				stage += 1
			default:
				if dur >= 100*time.Millisecond {
					// we wait for at least 100 ms to get a new start request
					// which means the system load is relatively low
					stage -= 1
					lastActiveTime = time.Now()
				} else {
					// we get a new request within 200ms
					// system startup pressure is high
					time.Sleep((1 << stage) * 100 * time.Millisecond)
					lastActiveTime = time.Now()
					stage += 1
					if stage > 3 {
						stage = 3
					}
				}
			}
		}
	}
}

// NOTE by huang-jl: cold-start of the entire system has concurrency limitation.
// There will only be fixed amount of worker to do cold-start jobs. (refer to work() above)
func (r CtrRuntime) StartNewCtr(d types.FunctionDeployment, id uint64, decision DeployDecision) (*CtrInstance, error) {
	notify := make(chan StartNewCtrRes)
	req := StartNewCtrReq{
		FunctionDeployment: d,
		id:                 id,
		decision:           decision,
		notify:             notify,
	}
	select {
	case r.workerCh <- req:
		res := <-notify
		return res.instance, res.err
	default:
		// when workerCh is full, we have to wait/retry to prevent
		// starting too much containers concurrently
		return nil, ErrColdStartTooMuch
	}
}

func (r CtrRuntime) startNewCtr(req StartNewCtrReq) (*CtrInstance, error) {
	var (
		err error
		ctr Ctr
	)
	namespace := GetRequestNamespace(req.Namespace)
	// Check if namespace exists, and it has the openfaas label
	valid, err := ValidNamespace(r.Client.NamespaceService(), namespace) // [825us]
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, errors.New("namespace not valid")
	}
	ctx := namespaces.WithNamespace(context.Background(), namespace)

	// start container
	switch req.decision {
	case COLD_START:
		ctr, err = r.coldStartInstance(ctx, req)
	case CR_START:
		ctr, err = r.criuStartInstance(ctx, req)
	case CR_LAZY_START:
		ctr, err = r.criuLazyStartInstance(ctx, req)
	case FAASNAP_START:
		ctr, err = r.faasnapStartInstance(ctx, req)
	default:
		return nil, errors.Errorf("invalid decision: %v", req.decision)
	}
	if err != nil {
		return nil, err
	}
	return r.InitCtrInstance(ctr, req), nil
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
	crlogger.Debug().Str("name", image.Name()).Int64("size", size).
		Float64("overhead (s)", time.Since(start).Seconds()).Msg("Image for")

	return image, nil
}

// NOTE by huang-jl: this is different from switch start
// it only use the containerd's C/R interface to start new task
func (r CtrRuntime) criuStartInstance(ctx context.Context, req StartNewCtrReq) (Ctr, error) {
	instanceID := GetInstanceID(req.Service, req.id)
	crlogger.Debug().Str("instance", instanceID).Msg("raw criu cold start")
	snapshotter := ""
	if val, ok := os.LookupEnv("snapshotter"); ok {
		snapshotter = val
	}

	// no always pull
	image, err := r.prepull(ctx, req.FunctionDeployment)
	if err != nil {
		return nil, err
	}

	envs := prepareEnv(req.EnvProcess, req.EnvVars)
	mounts := getOSMounts()

	labels, err := buildLabels(&req.FunctionDeployment)
	if err != nil {
		return nil, fmt.Errorf("unable to apply labels to container: %s, error: %w", instanceID, err)
	}

	// NOTE by huang-jl: This is a workaround for one bug:
	// If we follow the memory limit in config here (e.g., 128MiB for pyaes)
	// Then when doing switch (espeically from higher memory limit lambda to lower one)
	// it is possible to OOM.
	//
	// So we just use a loose memory bound for all containers for now.
	// Note: the raw criu need more memory.
	var memory *specs.LinuxMemory
	{
		qty, err := resource.ParseQuantity("4G")
		if err != nil {
			crlogger.Error().Err(err).Msg("parsing 1G as quantity failed")
			return nil, err
		}
		v := qty.Value()
		memory = &specs.LinuxMemory{Limit: &v}
	}

	// cpu limits
	var (
		period uint64 = uint64(100000)
		quota  int64  = 0
	)
	container, err := r.Client.NewContainer(
		ctx,
		instanceID,
		containerd.WithImage(image),
		containerd.WithSnapshotter(snapshotter),
		containerd.WithNewSnapshot(instanceID+"-snapshot", image),
		// wrapper,
		containerd.WithNewSpec(oci.WithImageConfig(image),
			oci.WithHostname(instanceID),
			oci.WithCapabilities([]string{"CAP_NET_RAW"}),
			oci.WithMounts(mounts),
			oci.WithEnv(envs),
			withCPU(quota, period),
			withMemory(memory),
		),
		containerd.WithContainerLabels(labels),
	)

	if err != nil {
		return nil, errors.Wrapf(err, "unable to create container: %s", instanceID)
	}

	if err := r.createTaskByCRIU(ctx, container, req.Service, instanceID); err != nil {
		return nil, err
	}

	return r.InitContainerdCtr(ctx, container)
}

// NOTE by huang-jl: this is different from switch start
// it only use the containerd's C/R interface to start new task
func (r CtrRuntime) criuLazyStartInstance(ctx context.Context, req StartNewCtrReq) (Ctr, error) {
	instanceID := GetInstanceID(req.Service, req.id)
	crlogger.Debug().Str("instance", instanceID).Msg("criu lazy start")
	snapshotter := ""
	if val, ok := os.LookupEnv("snapshotter"); ok {
		snapshotter = val
	}

	// no always pull
	image, err := r.prepull(ctx, req.FunctionDeployment)
	if err != nil {
		return nil, err
	}

	envs := prepareEnv(req.EnvProcess, req.EnvVars)
	mounts := getOSMounts()

	labels, err := buildLabels(&req.FunctionDeployment)
	if err != nil {
		return nil, fmt.Errorf("unable to apply labels to container: %s, error: %w", instanceID, err)
	}

	// NOTE by huang-jl: This is a workaround for one bug:
	// If we follow the memory limit in config here (e.g., 128MiB for pyaes)
	// Then when doing switch (espeically from higher memory limit lambda to lower one)
	// it is possible to OOM.
	//
	// So we just use a loose memory bound for all containers for now.
	// Note: the raw criu need more memory.
	var memory *specs.LinuxMemory
	{
		qty, err := resource.ParseQuantity("4G")
		if err != nil {
			crlogger.Error().Err(err).Msg("parsing 1G as quantity failed")
			return nil, err
		}
		v := qty.Value()
		memory = &specs.LinuxMemory{Limit: &v}
	}

	// cpu limits
	var (
		period uint64 = uint64(100000)
		quota  int64  = 0
	)
	container, err := r.Client.NewContainer(
		ctx,
		instanceID,
		containerd.WithImage(image),
		containerd.WithSnapshotter(snapshotter),
		containerd.WithNewSnapshot(instanceID+"-snapshot", image),
		// wrapper,
		containerd.WithNewSpec(oci.WithImageConfig(image),
			oci.WithHostname(instanceID),
			oci.WithCapabilities([]string{"CAP_NET_RAW"}),
			oci.WithMounts(mounts),
			oci.WithEnv(envs),
			withCPU(quota, period),
			withMemory(memory),
		),
		containerd.WithContainerLabels(labels),
	)

	if err != nil {
		return nil, errors.Wrapf(err, "unable to create container: %s", instanceID)
	}

	// start lazy page daemon first
	if err := r.startLazyPageDaemon(ctx, req.Service, instanceID); err != nil {
		return nil, err
	}
	if err := r.createTaskByLazyCRIU(ctx, container, req.Service, instanceID); err != nil {
		return nil, err
	}

	return r.InitContainerdCtr(ctx, container)
}

func (r CtrRuntime) coldStartInstance(ctx context.Context, req StartNewCtrReq) (Ctr, error) {
	instanceID := GetInstanceID(req.Service, req.id)
	crlogger.Debug().Str("instance", instanceID).Msg("traditional cold start")
	snapshotter := ""
	if val, ok := os.LookupEnv("snapshotter"); ok {
		snapshotter = val
	}

	// no always pull
	image, err := r.prepull(ctx, req.FunctionDeployment)
	if err != nil {
		return nil, err
	}

	envs := prepareEnv(req.EnvProcess, req.EnvVars)
	mounts := getOSMounts()

	labels, err := buildLabels(&req.FunctionDeployment)
	if err != nil {
		return nil, fmt.Errorf("unable to apply labels to container: %s, error: %w", instanceID, err)
	}

	// NOTE by huang-jl: This is a workaround for one bug:
	// If we follow the memory limit in config here (e.g., 128MiB for pyaes)
	// Then when doing switch (espeically from higher memory limit lambda to lower one)
	// it is possible to OOM.
	//
	// So we just use a loose memory bound for all containers for now.
	var memory *specs.LinuxMemory
	{
		qty, err := resource.ParseQuantity("1G")
		if err != nil {
			crlogger.Error().Err(err).Msg("parsing 1G as quantity failed")
			return nil, err
		}
		v := qty.Value()
		memory = &specs.LinuxMemory{Limit: &v}
	}

	// cpu limits
	var (
		period uint64 = uint64(100000)
		quota  int64  = 0
	)
	// if req.Limits != nil && len(req.Limits.CPU) > 0 {
	// 	cpuLimits, err := strconv.ParseFloat(req.Limits.CPU, 32)
	// 	if err != nil {
	// 		return nil, errors.Wrap(err, "parse cpu limit in FunctionDeployment failed")
	// 	}
	// 	quota = int64(cpuLimits * 100000.0)
	// }

	// wrapper := func(ctx context.Context, client *containerd.Client, c *containers.Container) error {
	// 	start := time.Now()
	// 	defer func() {
	// 		log.Printf("Snapshot prepare() spent %s\n", time.Since(start))
	// 	}()
	// 	return containerd.WithNewSnapshot(instanceID+"-snapshot", image)(ctx, client, c)
	// }

	// By huang-jl: probably to use oci.WithRootFSPath() to use costomized rootfs
	container, err := r.Client.NewContainer(
		ctx,
		instanceID,
		containerd.WithImage(image),
		containerd.WithSnapshotter(snapshotter),
		containerd.WithNewSnapshot(instanceID+"-snapshot", image),
		// wrapper,
		containerd.WithNewSpec(oci.WithImageConfig(image),
			oci.WithHostname(instanceID),
			oci.WithCapabilities([]string{"CAP_NET_RAW"}),
			oci.WithMounts(mounts),
			oci.WithEnv(envs),
			withCPU(quota, period),
			withMemory(memory),
		),
		containerd.WithContainerLabels(labels),
	)

	if err != nil {
		return nil, errors.Wrapf(err, "unable to create container: %s", instanceID)
	}

	if err := r.createTask(ctx, container); err != nil {
		return nil, err
	}

	return r.InitContainerdCtr(ctx, container)
}

func (r CtrRuntime) faasnapStartInstance(ctx context.Context, req StartNewCtrReq) (Ctr, error) {
	client := swagger.NewAPIClient(swagger.NewConfiguration())
	api := client.DefaultApi

	// find a free namespace
	start := time.Now()
	netNs, isNewNs, err := r.fnm.GetNetwork(ctx)
	if err != nil {
		return nil, errors.Errorf("failed to get network from FaasnapNetworkManager: %v", err)
	}
	if isNewNs {
		dur := time.Since(start)
		if err := metrics.GetMetricLogger().Emit(pkg.InitNetworkOverhead, "faasnap", dur); err != nil {
			crlogger.Err(err).Msg("log metric for create faasnap network failed")
		}
		crlogger.Debug().Dur("ovehead", dur).Msg("create faasnap network")
	}

	snapshotId := req.SnapshotIds[0]
	invocation := swagger.Invocation{
		FuncName:       req.Service,
		SsId:           snapshotId,
		EnableReap:     false,
		Namespace:      netNs,
		UseMemFile:     false,
		OverlayRegions: true,
		UseWsFile:      true,
	}
	vm, _, err := api.SnapshotsSsIdPost(ctx, snapshotId, &swagger.DefaultApiSnapshotsSsIdPostOpts{
		Body: optional.NewInterface(invocation),
	})
	if err != nil {
		return nil, errors.Errorf("failed to load snapshot: %v", err)
	}

	return &FaasnapCtr{
		vmId:      vm.VmId,
		Pid:       int(vm.Pid),
		IpAddress: vm.Ip,
		network:   netNs,
		fnm:       r.fnm,
	}, nil
}

func (r CtrRuntime) InitContainerdCtr(ctx context.Context, ctr containerd.Container) (*ContainerdCtr, error) {
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

	return &ContainerdCtr{
		Pid:           int(pid),
		rootfs:        &rootOverlay,
		IpAddress:     ip,
		cniID:         cniID,
		originalCtrID: name,
		fsManager:     r.rootfsManager,
		cni:           r.cni,
		client:        r.Client,
	}, nil
}

// generate CtrInstance from containerd.Container
func (r CtrRuntime) InitCtrInstance(ctr Ctr, req StartNewCtrReq) *CtrInstance {
	return &CtrInstance{
		Ctr:            ctr,
		ServiceName:    req.Service,
		ID:             req.id,
		status:         IDLE,
		depolyDecision: req.decision,
		lastActive:     time.Now(),
		localPQIndex:   -1,
		globalPQIndex:  -1,
	}
}

// By huang-jl: CRIU will restore the property of cgroup, we do not need to care about that here.
func (r CtrRuntime) SwitchStart(req types.FunctionDeployment, id uint64, candidate *CtrInstance) (*CtrInstance, error) {

	if len(req.Secrets) > 0 {
		return nil, fmt.Errorf("switch do not support secrets for now")
	}
	serviceName := req.Service
	// TODO(huang-jl) change the work directory structure ?
	config := switcher.SwitcherConfig{
		TargetServiceName: serviceName,
		CRImageDir:        path.Join(r.checkpointCache.checkpointDir, serviceName),
		CRWorkDir:         path.Join(pkg.FaasdCRIUResotreWorkPrefix, GetInstanceID(serviceName, id)),
		CRLogFileName:     "restore.log",
		// TODO(huang-jl) for better performance, we need modify it to 0
		CRLogLevel:   0,
		CandidatePID: candidate.GetPid(),
	}

	if err := candidate.Switch(config); err != nil {
		return nil, errors.Errorf("switch from %s to %s failed: %v", candidate.ServiceName, serviceName, err)
	}
	newPid := candidate.GetPid()
	// reap the child
	r.reapCh <- newPid

	// candidate.Ctr has already updated by candidate.Switch()
	newInstance := candidate
	newInstance.ServiceName = serviceName
	newInstance.ID = id
	newInstance.depolyDecision = SWITCH
	newInstance.localPQIndex = -1
	newInstance.globalPQIndex = -1
	// ipaddress, cniID, rootfs, originalCtrID will not change
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

	crlogger.Info().Str("Task ID", task.ID()).Str("Container ID", name).Uint32("Task PID", task.Pid()).Send()

	start := time.Now()
	labels := map[string]string{}
	_, err := cninetwork.CreateCNINetwork(ctx, r.cni, task, labels)
	dur := time.Since(start)
	if err := metrics.GetMetricLogger().Emit(pkg.InitNetworkOverhead, "cni", dur); err != nil {
		crlogger.Err(err).Msg("log metric for create cni network failed")
	} else {
		crlogger.Debug().Dur("overhead", time.Since(start)).Msg("create cni network")
	}

	if err != nil {
		return err
	}

	ip, err := cninetwork.GetIPAddress(name, task.Pid())
	if err != nil {
		return err
	}

	crlogger.Info().Str("IP", ip).Str("Container ID", name).Send()

	_, waitErr := task.Wait(ctx)
	if waitErr != nil {
		return errors.Wrapf(waitErr, "Unable to wait for task to start: %s", name)
	}

	if startErr := task.Start(ctx); startErr != nil {
		return errors.Wrapf(startErr, "Unable to start task: %s", name)
	}
	return nil
}

func (r CtrRuntime) createTaskByCRIU(ctx context.Context, container containerd.Container, serviceName, instanceID string) error {

	name := container.ID()

	taskOpts := []containerd.NewTaskOpts{
		func(_ context.Context, _ *containerd.Client, info *containerd.TaskInfo) error {
			info.Options = &options.Options{
				CriuImagePath: path.Join(r.checkpointCache.checkpointDir, serviceName),
				CriuWorkPath:  path.Join(pkg.FaasdCRIUResotreWorkPrefix, instanceID),
			}
			return nil
		},
	}
	start := time.Now()
	task, taskErr := container.NewTask(ctx, cio.BinaryIO("/usr/local/bin/faasd", nil), taskOpts...)

	if taskErr != nil {
		return fmt.Errorf("unable to start task: %s, error: %w", name, taskErr)
	}

	// we need first start task, then we can get pid
	if startErr := task.Start(ctx); startErr != nil {
		return errors.Wrapf(startErr, "Unable to start task: %s", name)
	}

	crlogger.Info().Dur("criu overhead", time.Since(start)).Str("Task ID", task.ID()).
		Str("Container ID", name).Uint32("Task PID", task.Pid()).Send()

	start = time.Now()
	labels := map[string]string{}
	_, err := cninetwork.CreateCNINetwork(ctx, r.cni, task, labels)
	if err != nil {
		return err
	}
	crlogger.Debug().Dur("overhead", time.Since(start)).Msg("create cni network")

	ip, err := cninetwork.GetIPAddress(name, task.Pid())
	if err != nil {
		return err
	}
	crlogger.Info().Str("IP", ip).Str("Container ID", name).Send()

	return nil
}

func (r CtrRuntime) startLazyPageDaemon(_ context.Context, serviceName, instanceID string) error {
	workDir := path.Join(pkg.FaasdCRIUResotreWorkPrefix, instanceID)
	imgDir := path.Join(r.checkpointCache.checkpointDir, serviceName)
	args := []string{
		"lazy-pages",
		"--images-dir", imgDir,
		"--work-dir", workDir,
		// TODO(huang-jl) daemon mode or not ? "-d",
		"--log-file", "lazy-page-daemon.log",
		"-v4",
	}
	if err := os.Mkdir(workDir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	cmd := exec.Command("criu", args...)
	cmd.Stdin = nil
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	crlogger.Debug().Int("daemon pid", cmd.Process.Pid).Str("lambda", serviceName).Msg("start lazy page daemon")
	go func() {
		output, err := os.OpenFile(path.Join(workDir, "page-daemon-output"), unix.O_CREAT|unix.O_EXCL|unix.O_RDWR, 0644)
		if err != nil {
			crlogger.Err(err).Msg("open page-daemon-output failed")
		}
		go func() {
			if _, err := io.Copy(output, outPipe); err != nil {
				crlogger.Err(err).Msg("copy lazy-pages out error")
			}
		}()
		go func() {
			if _, err := io.Copy(output, errPipe); err != nil {
				crlogger.Err(err).Msg("copy lazy-pages err error")
			}
		}()
		if err := cmd.Wait(); err != nil {
			crlogger.Err(err).Msg("criu lazy-pages error")
		}
	}()
	return nil
}

func (r CtrRuntime) createTaskByLazyCRIU(ctx context.Context, container containerd.Container, serviceName, instanceID string) error {

	name := container.ID()

	taskOpts := []containerd.NewTaskOpts{
		func(_ context.Context, _ *containerd.Client, info *containerd.TaskInfo) error {
			info.Options = &options.Options{
				CriuImagePath: path.Join(r.checkpointCache.checkpointDir, serviceName),
				CriuWorkPath:  path.Join(pkg.FaasdCRIUResotreWorkPrefix, instanceID),
				CriuLazyPages: true,
			}
			return nil
		},
	}
	start := time.Now()
	task, taskErr := container.NewTask(ctx, cio.BinaryIO("/usr/local/bin/faasd", nil), taskOpts...)

	if taskErr != nil {
		return fmt.Errorf("unable to start task: %s, error: %w", name, taskErr)
	}

	// we need first start task, then we can get pid
	if startErr := task.Start(ctx); startErr != nil {
		return errors.Wrapf(startErr, "Unable to start task: %s", name)
	}

	crlogger.Info().Dur("criu overhead", time.Since(start)).Str("Task ID", task.ID()).
		Str("Container ID", name).Uint32("Task PID", task.Pid()).Send()

	start = time.Now()
	labels := map[string]string{}
	_, err := cninetwork.CreateCNINetwork(ctx, r.cni, task, labels)
	if err != nil {
		return err
	}
	crlogger.Debug().Dur("overhead", time.Since(start)).Msg("create cni network")

	ip, err := cninetwork.GetIPAddress(name, task.Pid())
	if err != nil {
		return err
	}
	crlogger.Info().Str("IP", ip).Str("Container ID", name).Send()

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

func withCPU(quota int64, period uint64) oci.SpecOpts {
	if quota > 0 {
		return oci.WithCPUCFS(quota, period)
	}
	return func(ctx context.Context, _ oci.Client, c *containers.Container, s *oci.Spec) error {
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
