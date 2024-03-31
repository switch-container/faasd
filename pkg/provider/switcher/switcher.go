package switcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	criurpc "github.com/checkpoint-restore/go-criu/v5/rpc"
	"github.com/containerd/containerd"
	"github.com/openfaas/faasd/pkg"
	"github.com/openfaas/faasd/pkg/metrics"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"google.golang.org/protobuf/proto"
)

var swlogger = log.With().
	Str("component", "[Switcher]").
	Logger()

type Process struct {
	cmd *exec.Cmd
}

type Switcher struct {
	// refer to the criu process
	process *Process
	config  SwitcherConfig
}

func (switcher *Switcher) switchCriuOptions() *CriuOpts {
	return &CriuOpts{
		ImagesDirectory:         switcher.config.CRImageDir,
		WorkDirectory:           switcher.config.CRWorkDir,
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
func (switcher *Switcher) doSwitch() error {
	lambdaName := switcher.config.TargetServiceName
	criuOpts := switcher.switchCriuOptions()
	pid := switcher.config.CandidatePID

	imageDir, err := os.Open(criuOpts.ImagesDirectory)
	if err != nil {
		return err
	}
	defer imageDir.Close()
	t := criurpc.CriuReqType_RESTORE
	rpcOpts := &criurpc.CriuOpts{
		ImagesDirFd:    proto.Int32(int32(imageDir.Fd())),
		EvasiveDevices: proto.Bool(true),
		LogLevel:       proto.Int32(int32(switcher.config.CRLogLevel)),
		LogFile:        proto.String(switcher.config.CRLogFileName),
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
	defer func() {
		// close all fds
		// start := time.Now()
		for _, fd := range extraFiles {
			fd.Close()
		}
		// swlogger.Debug().Dur("overhead", time.Since(start)).
		// 	Str("lambda name", lambdaName).Msg("close fd for criu switch")
	}()

	// start := time.Now()
	// [40us]
	if err = handleSwitchNamespaces(rpcOpts, pid, &extraFiles); err != nil {
		return errors.Wrap(err, "handle switch namespace failed")
	}
	// metrics.GetMetricLogger().Emit(pkg.CRIUHandleNsMetric, switcher.checkpoint, time.Since(start))
	// swlogger.Debug().Dur("overhead", time.Since(start)).
	// 	Str("lambda name", lambdaName).Msg("handle switch namespace")

	// start = time.Now()
	// [10us]
	if err = handlePseudoMMDrv(rpcOpts, &extraFiles); err != nil {
		return errors.Wrap(err, "handle pseudo mm drv failed")
	}
	// swlogger.Debug().Dur("overhead", time.Since(start)).
	// 	Str("lambda name", lambdaName).Msg("handle pseudo mm drv")

	// start = time.Now()
	// [50us]
	if err = applyCgroup(pid, rpcOpts, criuOpts); err != nil {
		return errors.Wrap(err, "apply cgroup failed")
	}
	if criuOpts.CgroupFile != nil {
		defer criuOpts.CgroupFile.Close()
	}
	// swlogger.Debug().Dur("overhead", time.Since(start)).
	// 	Str("lambda name", lambdaName).Msg("apply cgroup")

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
	err = metrics.GetMetricLogger().Emit(pkg.SwitchKillMetric, lambdaName, time.Since(start))
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

	return metrics.GetMetricLogger().Emit(pkg.CRIUSwrkLatencyMetric, lambdaName, time.Since(start))
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
	var cgroupFile *os.File

	path := fmt.Sprintf("/proc/%d/cgroup", pid)
	cgroupsPaths, err := ParseCgroupFile(path)
	if err != nil {
		return err
	}
	if len(cgroupsPaths) != 1 {
		return fmt.Errorf("Please make sure you are using cgroup v2")
	}
	logEvent := swlogger.Debug()
	for c, p := range cgroupsPaths {
		logEvent = logEvent.Str("cgroup path ctrl", c).Str("cgroup path path", p)
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
	logEvent.Send()

	if cgroupFile == nil {
		return fmt.Errorf("empty cgroup for pid %d", pid)
	}
	criuOpts.CgroupFile = cgroupFile

	// we use cgroup yard to accelerate the speed of prepare cgroup
	rpcOpts.CgroupYard = proto.String("/sys/fs/cgroup")
	// we need restore props
	mode := criurpc.CriuCgMode_PROPS
	rpcOpts.ManageCgroupsMode = &mode

	return nil
}

// (process id, take over from REMOTE or not, err)
func getPidOfContainer(client *containerd.Client, containerID string) (int, containerd.Container, error) {
	var ctr containerd.Container = nil
	ctr, err := client.LoadContainer(context.TODO(), containerID)
	if err != nil {
		swlogger.Error().Err(err).Msg("load container failed")
		return -1, nil, err
	}
	// [500us]
	pid, err := ctr.TakeOver(context.TODO(), -1)
	if err != nil || pid <= 0 {
		swlogger.Error().Err(err).Msg("start take over of container failed")
		return -1, nil, err
	}
	return pid, ctr, nil
}

// checkpoint: the name of image directory of the target container
// checkpointDir: the upper-level directory which containers all function's checkpoint image
// pid: the init pid of original container
func SwitchFor(config SwitcherConfig) (*Switcher, error) {
	// pid, ctr, err := lambda.getPidOfContainer(switchReq.ContainerID)

	switcher := &Switcher{
		process: &Process{},
		config:  config,
	}

	if err := switcher.doSwitch(); err != nil {
		return nil, err
	}
	return switcher, nil
}
