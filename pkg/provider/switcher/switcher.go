package switcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	criurpc "github.com/checkpoint-restore/go-criu/v5/rpc"
	"github.com/openfaas/faasd/pkg"
	"github.com/openfaas/faasd/pkg/metrics"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/defaults"
	"github.com/containerd/containerd/pkg/dialer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

type Process struct {
	cmd *exec.Cmd
}

type Switcher struct {
	// refer to the criu process
	process       *Process
	checkpoint    string
	checkpointDir string
	config        SwitcherConfig
}

func (switcher *Switcher) switchCriuOptions() *CriuOpts {
	return &CriuOpts{
		ImagesDirectory:         filepath.Join(switcher.checkpointDir, switcher.checkpoint),
		WorkDirectory:           switcher.config.CRIUWorkDirectory,
		ParentImage:             "",
		LeaveRunning:            false,
		TcpEstablished:          false,
		ExternalUnixConnections: false,
		ShellJob:                false,
		FileLocks:               false,
		PreDump:                 false,
		AutoDedup:               false,
		LazyPages:               false,
		StatusFd:                -1,
		LsmProfile:              "",
		LsmMountContext:         "",
	}
}

func (switcher *Switcher) PID() int {
	if switcher.process == nil || switcher.process.cmd == nil {
		return -1
	}
	return switcher.process.cmd.Process.Pid
}

// - pid: original process id in container
func (switcher *Switcher) doSwitch(pid int) error {
	criuOpts := switcher.switchCriuOptions()

	imageDir, err := os.Open(criuOpts.ImagesDirectory)
	if err != nil {
		return err
	}
	defer imageDir.Close()
	t := criurpc.CriuReqType_RESTORE
	rpcOpts := &criurpc.CriuOpts{
		ImagesDirFd:    proto.Int32(int32(imageDir.Fd())),
		EvasiveDevices: proto.Bool(true),
		LogLevel:       proto.Int32(int32(switcher.config.CRIULogLevel)),
		LogFile:        proto.String(switcher.config.CRIULogFileName),
		RstSibling:     proto.Bool(true),
		// Root:            proto.String(root),
		ManageCgroups:  proto.Bool(true),
		NotifyScripts:  proto.Bool(true),
		ShellJob:       proto.Bool(criuOpts.ShellJob),
		ExtUnixSk:      proto.Bool(criuOpts.ExternalUnixConnections),
		TcpEstablished: proto.Bool(criuOpts.TcpEstablished),
		FileLocks:      proto.Bool(criuOpts.FileLocks),
		// EmptyNs:         proto.Uint32(criuOpts.EmptyNs),
		OrphanPtsMaster: proto.Bool(true),
		AutoDedup:       proto.Bool(criuOpts.AutoDedup),
		LazyPages:       proto.Bool(criuOpts.LazyPages),
		Switch:          proto.Bool(true),
	}

	if criuOpts.WorkDirectory != "" {
		if err := os.MkdirAll(criuOpts.WorkDirectory, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		workDir, err := os.Open(criuOpts.WorkDirectory)
		if err != nil {
			return err
		}
		defer workDir.Close()
		rpcOpts.WorkDirFd = proto.Int32(int32(workDir.Fd()))
	}

	var extraFiles []*os.File

	// start := time.Now()
	// [40us]
	if err = handleSwitchNamespaces(rpcOpts, pid, &extraFiles); err != nil {
		return errors.Wrap(err, "handle switch namespace failed")
	}
	// metrics.GetMetricLogger().Emit(pkg.CRIUHandleNsMetric, switcher.checkpoint, time.Since(start))

	// start = time.Now()
	// [10us]
	if err = handlePseudoMMDrv(rpcOpts, &extraFiles); err != nil {
		return errors.Wrap(err, "handle pseudo mm drv failed")
	}
	// log.Printf("handle pseudo mm drv %s, ", time.Since(start))

	// start = time.Now()
	// [50us]
	if err = applyCgroup(pid, rpcOpts, criuOpts); err != nil {
		return errors.Wrap(err, "apply cgroup failed")
	}
	// log.Printf("apply cgroup %s, ", time.Since(start))

	// TODO(huang-jl) kill process in another goroutine (async)
	// kill process sometimes takes about 4ms - 5ms.
	//
	// But pay attention: if we do it async, we have to use another
	// port (e.g., 5001) for restored process. Or else it may conflict
	// with original process in the same net namespace.
	start := time.Now()
	if err = syscall.Kill(pid, syscall.SIGKILL); err != nil {
		return errors.Wrapf(err, "kill original process %d failed", pid)
	}
	err = metrics.GetMetricLogger().Emit(pkg.SwitchKillMetric, switcher.checkpoint, time.Since(start))
	if err != nil {
		return err
	}

	// log.Printf("handle ns + apply cgroup + kill %d took %s", pid, time.Since(start))

	// the descriptors handling is copying from runc checkpoint
	var (
		fds    []string
		fdJSON []byte
	)
	if fdJSON, err = os.ReadFile(filepath.Join(criuOpts.ImagesDirectory, descriptorsFilename)); err != nil {
		return err
	}

	if err := json.Unmarshal(fdJSON, &fds); err != nil {
		return err
	}
	for i := range fds {
		if s := fds[i]; strings.Contains(s, "pipe:") {
			inheritFd := new(criurpc.InheritFd)
			inheritFd.Key = proto.String(s)
			inheritFd.Fd = proto.Int32(int32(i))
			rpcOpts.InheritFd = append(rpcOpts.InheritFd, inheritFd)
		}
	}

	start = time.Now()
	if err = switcher.criuSwrk(&criurpc.CriuReq{Type: &t, Opts: rpcOpts}, criuOpts, extraFiles); err != nil {
		return errors.Wrapf(err, "criuSwrk failed")
	}
	return metrics.GetMetricLogger().Emit(pkg.CRIUSwrkLatencyMetric, switcher.checkpoint, time.Since(start))
}

func getNsPath(pid int, ns string) string {
	return fmt.Sprintf("/proc/%d/ns/%s", pid, ns)
}

func handlePseudoMMDrv(rpcOpts *criurpc.CriuOpts, extraFiles *[]*os.File) error {
	drvFile, err := os.Open(pkg.PseudoMMDrvPath)
	if err != nil {
		return fmt.Errorf("cannot open %s: %s", pkg.PseudoMMDrvPath, err)
	}
	inheritFd := &criurpc.InheritFd{
		Key: proto.String(pkg.CRIUPseudoMMDrvInheritID),
		// The offset of four is necessary because 0, 1, 2 and 3 are
		// already used by stdin, stdout, stderr, 'criu swrk' socket.
		Fd: proto.Int32(int32(4 + len(*extraFiles))),
	}
	rpcOpts.InheritFd = append(rpcOpts.InheritFd, inheritFd)
	// All open FDs need to be transferred to CRIU via extraFiles
	*extraFiles = append(*extraFiles, drvFile)
	return nil
}

// - pid: process id of original container's process
func handleSwitchNamespaces(rpcOpts *criurpc.CriuOpts, pid int, extraFiles *[]*os.File) error {
	// we need skip pid namespace here
	// since current kernel does not allowed to restore (i.e. create process) into an empty pid namesapce
	namespaces := []string{"mnt", "net", "ipc", "uts"}
	for _, ns := range namespaces {
		nsPath := getNsPath(pid, ns)
		nsFd, err := os.Open(nsPath)
		if err != nil {
			return fmt.Errorf("namespace %s do not exist when switch: %s", ns, nsPath)
		}
		inheritFd := &criurpc.InheritFd{
			Key: proto.String(fmt.Sprintf("switch-ns-%s", ns)),
			// The offset of four is necessary because 0, 1, 2 and 3 are
			// already used by stdin, stdout, stderr, 'criu swrk' socket.
			Fd: proto.Int32(int32(4 + len(*extraFiles))),
		}
		rpcOpts.InheritFd = append(rpcOpts.InheritFd, inheritFd)
		// All open FDs need to be transferred to CRIU via extraFiles
		*extraFiles = append(*extraFiles, nsFd)
	}

	return nil
}

// Args:
//
// - pid: pid of original container's process
//
// This methods does two thing
// 1. parse the cgroup of process pid, adding them to CgroupRoot (for CRIU to rewrite)
// 2. Add CgroupYard, so CRIU will not need mount CGROUP2 fs
func applyCgroup(pid int, rpcOpts *criurpc.CriuOpts, criuOpts *CriuOpts) error {
	path := fmt.Sprintf("/proc/%d/cgroup", pid)
	cgroupsPaths, err := ParseCgroupFile(path)
	var cgroupFile *os.File
	if err != nil {
		return err
	}

	log.Printf("cgroup paths: %+v\n", cgroupsPaths)
	for c, p := range cgroupsPaths {
		cgroupRoot := &criurpc.CgroupRoot{
			Ctrl: proto.String(c),
			Path: proto.String(p),
		}
		rpcOpts.CgRoot = append(rpcOpts.CgRoot, cgroupRoot)
		if cgroupFile != nil {
			return fmt.Errorf("multiple cgroup controllers find, now only support cgroup v2: %s %s", c, p)
		}
		cgroupFile, err = os.Open(filepath.Join("/sys/fs/cgroup", p))
		if err != nil {
			return err
		}
	}

	if cgroupFile == nil {
		return fmt.Errorf("empty cgroup for pid %d", pid)
	}
	criuOpts.CgroupFD = cgroupFile.Fd()

	// we use cgroup yard to accelerate the speed of prepare cgroup
	rpcOpts.CgroupYard = proto.String("/sys/fs/cgroup")
	// we need restore props
	mode := criurpc.CriuCgMode_PROPS
	rpcOpts.ManageCgroupsMode = &mode

	return nil
}

// TODO(huang-jl) use some of the parameters here (copied from moby)
func initContainerdClient(addr string) (client *containerd.Client, err error) {
	var (
		possibleAddr []string
	)
	backoffConfig := backoff.DefaultConfig
	backoffConfig.MaxDelay = 3 * time.Second
	connParams := grpc.ConnectParams{
		Backoff: backoffConfig,
	}
	gopts := []grpc.DialOption{
		// WithBlock makes sure that the following containerd request
		// is reliable.
		//
		// NOTE: In one edge case with high load pressure, kernel kills
		// dockerd, containerd and containerd-shims caused by OOM.
		// When both dockerd and containerd restart, but containerd
		// will take time to recover all the existing containers. Before
		// containerd serving, dockerd will failed with gRPC error.
		// That bad thing is that restore action will still ignore the
		// any non-NotFound errors and returns running state for
		// already stopped container. It is unexpected behavior. And
		// we need to restart dockerd to make sure that anything is OK.
		//
		// It is painful. Add WithBlock can prevent the edge case. And
		// n common case, the containerd will be serving in shortly.
		// It is not harm to add WithBlock for containerd connection.
		grpc.WithBlock(),

		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithConnectParams(connParams),
		grpc.WithContextDialer(dialer.ContextDialer),

		// TODO(stevvooe): We may need to allow configuration of this on the client.
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(defaults.DefaultMaxRecvMsgSize)),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(defaults.DefaultMaxSendMsgSize)),
	}
	if addr == "" {
		possibleAddr = []string{"/run/docker/containerd/containerd.sock", "/run/containerd/containerd.sock"}
	} else {
		possibleAddr = []string{addr}
	}
	for _, addr := range possibleAddr {
		client, err = containerd.New(addr, containerd.WithDefaultNamespace("moby"), containerd.WithDialOpts(gopts), containerd.WithTimeout(60*time.Second))
		if err == nil {
			return client, nil
		}
	}
	return nil, err
}

// (process id, take over from REMOTE or not, err)
func getPidOfContainer(client *containerd.Client, containerID string) (int, containerd.Container, error) {
	var ctr containerd.Container = nil
	ctr, err := client.LoadContainer(context.TODO(), containerID)
	if err != nil {
		logrus.Errorf("load container failed %s", err)
		return -1, nil, err
	}
	// [500us]
	pid, err := ctr.TakeOver(context.TODO(), -1)
	if err != nil || pid <= 0 {
		logrus.Errorf("start take over of container failed %s", err)
		return -1, nil, err
	}
	return pid, ctr, nil
}

// exported main function
func SwitchFor(checkpoint, checkpointDir string, pid int, config SwitcherConfig) (*Switcher, error) {
	// pid, ctr, err := lambda.getPidOfContainer(switchReq.ContainerID)

	switcher := &Switcher{
		process:       &Process{},
		checkpoint:    checkpoint,
		checkpointDir: checkpointDir,
		config:        config,
	}

	if err := switcher.doSwitch(pid); err != nil {
		return nil, err
	}

	{
		// we need reap the new process by our own
		process := switcher.process.cmd.Process
		go process.Wait()
	}
	return switcher, nil
}
