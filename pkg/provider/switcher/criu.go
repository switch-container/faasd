package switcher

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	criurpc "github.com/checkpoint-restore/go-criu/v5/rpc"
	"github.com/openfaas/faasd/pkg"
	"github.com/openfaas/faasd/pkg/metrics"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
)

const descriptorsFilename = "descriptors.json"

type CriuOpts struct {
	ImagesDirectory         string             // directory for storing image files
	WorkDirectory           string             // directory to cd and write logs/pidfiles/stats to
	ParentImage             string             // directory for storing parent image files in pre-dump and dump
	LeaveRunning            bool               // leave container in running state after checkpoint
	TcpEstablished          bool               // checkpoint/restore established TCP connections
	ExternalUnixConnections bool               // allow external unix connections
	ShellJob                bool               // allow to dump and restore shell jobs
	FileLocks               bool               // handle file locks, for safety
	PreDump                 bool               // call criu predump to perform iterative checkpoint
	ManageCgroupsMode       criurpc.CriuCgMode // dump or restore cgroup mode
	EmptyNs                 uint32             // don't c/r properties for namespace from this mask
	AutoDedup               bool               // auto deduplication for incremental dumps
	LazyPages               bool               // restore memory pages lazily using userfaultfd
	StatusFd                int                // fd for feedback when lazy server is ready
	LsmProfile              string             // LSM profile used to restore the container
	LsmMountContext         string             // LSM mount context value to use during restore
	Switch                  bool               // switch to an existing container
	CgroupFile              *os.File           // File Descriptor to Set when exec CRIU swrk (i.e. CLONE_INTO_CGROUP)
}

func bindLoggingPipe(pipe io.Reader, output io.WriteCloser) {
	logger := zerolog.New(output).With().Timestamp().Logger()
	scanner := bufio.NewScanner(pipe)
	go func() {
		for scanner.Scan() {
			logger.Log().Msg(scanner.Text())
			if err := scanner.Err(); err != nil {
				log.Error().Err(err).Msg("Error scanning")
			}
		}
		output.Close()
	}()
}

func redirectSwitchOutput(cmd *exec.Cmd, opts *CriuOpts) error {
	cmdOut, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmdErr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	outFile, err := os.Create(filepath.Join(opts.WorkDirectory, "stdout"))
	if err != nil {
		return err
	}
	errFile, err := os.Create(filepath.Join(opts.WorkDirectory, "stderr"))
	if err != nil {
		return err
	}
	bindLoggingPipe(cmdOut, outFile)
	bindLoggingPipe(cmdErr, errFile)

	return nil
}

func (switcher *Switcher) criuSwrk(req *criurpc.CriuReq, opts *CriuOpts, extraFiles []*os.File) error {
	// start := time.Now()
	if opts == nil {
		return fmt.Errorf("CriuOpts cannot be null")
	}

	fds, err := unix.Socketpair(unix.AF_LOCAL, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}

	logPath := filepath.Join(opts.WorkDirectory, req.GetOpts().GetLogFile())
	criuClient := os.NewFile(uintptr(fds[0]), "criu-transport-client")
	criuClientFileCon, err := net.FileConn(criuClient)
	criuClient.Close()
	if err != nil {
		return err
	}

	criuClientCon := criuClientFileCon.(*net.UnixConn)
	defer criuClientCon.Close()

	criuServer := os.NewFile(uintptr(fds[1]), "criu-transport-server")
	defer criuServer.Close()

	args := []string{"swrk", "3"}
	cmd := exec.Command("criu", args...)

	cmd.Stdin = nil
	// NOTE By huang-jl: if want to see the ouput from container after restoring
	// then use redirectSwitchOutput(). By default, the stdout, stderr will
	// be directed to /dev/null
	// if err := redirectSwitchOutput(cmd, opts); err != nil {
	// 	return errors.Wrap(err, "redirectSwitchOutput")
	// }

	// NOTE by huang-jl: I choose not to clone_into_cgroup here, but
	// using it in CRIU when clone process.
	// Because it will bring CRIU process itself into cgroup, which might cause OOM.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		UseCgroupFD: true,
		CgroupFD:    int(opts.CgroupFile.Fd()),
	}

	cmd.ExtraFiles = append(cmd.ExtraFiles, criuServer)
	if extraFiles != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, extraFiles...)
	}

	// [from beginning to here: xx - xxx us]
	// logEntry := logrus.WithField("prepare cmd", time.Since(start).String())
  start := time.Now()
	// log.Printf("start criu swrk ts: %d", start.UnixMicro())
	// [start itself: 391us]
	if err := cmd.Start(); err != nil {
		return errors.Wrap(err, "criu cmd Start()")
	}
	err = metrics.GetMetricLogger().Emit(pkg.CRIUSwrkCmdStartMetric, switcher.config.TargetServiceName, time.Since(start))
	if err != nil {
		return err
	}

	switcher.process.cmd = cmd

	criuServer.Close()
	// cmd.Process will be replaced by a restored init.
	criuProcess := cmd.Process

	var criuProcessState *os.ProcessState
	// [this defer: < 1 us]
	defer func() {
		// start_ := time.Now()
		if criuProcessState == nil {
			criuClientCon.Close()
			_, err := criuProcess.Wait()
			if err != nil {
				swlogger.Warn().Err(err).Msg("wait on criuProcess returned %v")
			}
		}
		// log.Printf("defer function took %s", time.Since(start_))
	}()

	// swlogger.Debug().Str("mode", req.GetType().String()).Msg("CRIU mode")

	// [reflect took: 53.6ms]
	// if logrus.GetLevel() >= logrus.DebugLevel &&
	// 	!(req.GetType() == criurpc.CriuReqType_FEATURE_CHECK ||
	// 		req.GetType() == criurpc.CriuReqType_VERSION) {

	// 	val := reflect.ValueOf(req.GetOpts())
	// 	v := reflect.Indirect(val)
	// 	for i := 0; i < v.NumField(); i++ {
	// 		st := v.Type()
	// 		name := st.Field(i).Name
	// 		if 'A' <= name[0] && name[0] <= 'Z' {
	// 			value := val.MethodByName("Get" + name).Call([]reflect.Value{})
	// 			logrus.Debugf("CRIU option %s with value %v", name, value[0])
	// 		}
	// 	}
	// }
	// logEntry = logEntry.WithField("reflectReq", time.Since(start).String())

	data, err := proto.Marshal(req)
	if err != nil {
		return err
	}

	// start = time.Now()
	_, err = criuClientCon.Write(data)
	if err != nil {
		return err
	}

	// swlogger.Debug().Dur("overhead", time.Since(start)).Msg("write criu request")
	// start = time.Now()

	buf := make([]byte, 10*4096)
	oob := make([]byte, 4096)
	for {
		n, oobn, _, _, err := criuClientCon.ReadMsgUnix(buf, oob)
		if req.Opts != nil && req.Opts.StatusFd != nil {
			// Close status_fd as soon as we got something back from criu,
			// assuming it has consumed (reopened) it by this time.
			// Otherwise it will might be left open forever and whoever
			// is waiting on it will wait forever.
			fd := int(*req.Opts.StatusFd)
			_ = unix.Close(fd)
			req.Opts.StatusFd = nil
		}
		if err != nil {
			return errors.Wrap(err, "criuClientCon.ReadMsgUnix()")
		}
		if n == 0 {
			return errors.New("unexpected EOF")
		}
		if n == len(buf) {
			return errors.New("buffer is too small")
		}

		resp := new(criurpc.CriuResp)
		err = proto.Unmarshal(buf[:n], resp)
		if err != nil {
			return err
		}
		if !resp.GetSuccess() {
			typeString := req.GetType().String()
			return fmt.Errorf("criu failed: type %s errno %d\nlog file: %s", typeString, resp.GetCrErrno(), logPath)
		}

		t := resp.GetType()
		switch {
		case t == criurpc.CriuReqType_FEATURE_CHECK:
			// swlogger.Debug().Str("resp", resp.String()).Msg("Feature check")
			// criuFeatures = resp.GetFeatures()
		case t == criurpc.CriuReqType_NOTIFY:
			// log.Printf("until recv notify spent %s, ", time.Since(start))
			// start = time.Now()
			if err := switcher.criuNotifications(resp, cmd, opts, oob[:oobn]); err != nil {
				return err
			}
			t = criurpc.CriuReqType_NOTIFY
			req = &criurpc.CriuReq{
				Type:          &t,
				NotifySuccess: proto.Bool(true),
			}
			data, err = proto.Marshal(req)
			if err != nil {
				return err
			}
			_, err = criuClientCon.Write(data)
			if err != nil {
				return errors.Wrap(err, "criuClientCon.Write()")
			}
			continue
		case t == criurpc.CriuReqType_RESTORE:
			// swlogger.Debug().Dur("overhead", time.Since(start)).
			// 	Msg("receive RESTORE response from loop start")
		case t == criurpc.CriuReqType_DUMP:
		case t == criurpc.CriuReqType_PRE_DUMP:
		default:
			return fmt.Errorf("unable to parse the response %s", resp.String())
		}

		break
	}

	// swlogger.Debug().Dur("overhead", time.Since(start)).Msg("break from criu conn loop")

	// start = time.Now()
	_ = criuClientCon.CloseWrite()
	// cmd.Wait() waits cmd.goroutines which are used for proxying file descriptors.
	// Here we want to wait only the CRIU process.
	criuProcessState, err = criuProcess.Wait()
	if err != nil {
		return errors.Wrap(err, "criuProcess.Wait()")
	}

	// swlogger.Debug().Dur("overhead", time.Since(start)).Msg("wait for criu")

	// In pre-dump mode CRIU is in a loop and waits for
	// the final DUMP command.
	// The current runc pre-dump approach, however, is
	// start criu in PRE_DUMP once for a single pre-dump
	// and not the whole series of pre-dump, pre-dump, ...m, dump
	// If we got the message CriuReqType_PRE_DUMP it means
	// CRIU was successful and we need to forcefully stop CRIU
	if !criuProcessState.Success() && *req.Type != criurpc.CriuReqType_PRE_DUMP {
		return fmt.Errorf("criu failed: %s\nlog file: %s", criuProcessState.String(), logPath)
	}
	return nil
}

func (switcher *Switcher) criuNotifications(resp *criurpc.CriuResp, cmd *exec.Cmd, opts *CriuOpts, oob []byte) error {
	notify := resp.GetNotify()
	if notify == nil {
		return fmt.Errorf("invalid response: %s", resp.String())
	}
	script := notify.GetScript()
	// swlogger.Debug().Str("notify", script).Msg("criu notification")
	switch script {
	case "post-restore":
		pid := notify.GetPid()

		p, err := os.FindProcess(int(pid))
		if err != nil {
			return err
		}
		switcher.process.cmd.Process = p
	// case "orphan-pts-master":
	// case "status-ready":
	default:
		return fmt.Errorf("unsupport notification type")
	}
	return nil
}

// get the fd of stdin, stdout, and stderr of target process pid
func getPipeFds(pid int) ([]string, error) {
	fds := make([]string, 3)

	dirPath := filepath.Join("/proc", strconv.Itoa(pid), "/fd")
	for i := 0; i < 3; i++ {
		// XXX: This breaks if the path is not a valid symlink (which can
		//      happen in certain particularly unlucky mount namespace setups).
		f := filepath.Join(dirPath, strconv.Itoa(i))
		target, err := os.Readlink(f)
		if err != nil {
			// Ignore permission errors, for rootless containers and other
			// non-dumpable processes. if we can't get the fd for a particular
			// file, there's not much we can do.
			if os.IsPermission(err) {
				continue
			}
			return fds, err
		}
		fds[i] = target
	}
	return fds, nil
}
