package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"time"

	"github.com/containerd/containerd"
	containerderrors "github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/plugin"
	"github.com/containerd/containerd/runtime/v2/runc/options"
	"github.com/openfaas/faasd/pkg"
	faasd "github.com/openfaas/faasd/pkg"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

type checkpointConfig struct {
	globalWorkDir string
	// sock address of containerd
	containerdAddr string
	// namespace of container in containerd
	namespace       string
	pseudoMMDrv     *os.File
	daxDevPath      string
	rdmaBufSockAddr string

	daxPgoff  int
	rdmaPgoff int

	memPool string
}

// checkpoint info that specific to one container
type checkpointInfo struct {
	containerID   string
	checkpointDir string
	workDir       string
	serviceName   string
	mntNsFile     *os.File
	// runtimeName is get from containerd
	runtimeName string
	process     containerd.Process
}

var functionRegex = regexp.MustCompile(`([-a-zA-Z_0-9.]*?)(-\d+)?$`)

func parseLambdaName(ctrName string) (string, error) {
	var (
		serviceName string
	)
	temp := functionRegex.FindStringSubmatch(ctrName)
	if len(temp) == 2 || len(temp) == 3 {
		serviceName = temp[1]
	} else {
		return serviceName, fmt.Errorf("invalid ctr name pattern: %s", ctrName)
	}
	return serviceName, nil
}

func initCheckpointInfo(client *containerd.Client, ctrId string, config *checkpointConfig) (*checkpointInfo, error) {
	var res *checkpointInfo

	ctx := namespaces.WithNamespace(context.Background(), config.namespace)
	serviceName, err := parseLambdaName(ctrId)
	if err != nil {
		return res, errors.Wrapf(err, "invalid container id %s", ctrId)
	}

	ctr, err := getContainer(ctx, client, ctrId)
	if err != nil {
		return res, err
	}
	p, err := getInitProcess(ctx, ctr, ctrId)
	if err != nil {
		return res, err
	}
	pid := p.Pid()
	mntNsFile, err := os.Open(fmt.Sprintf("/proc/%d/ns/mnt", pid))
	if err != nil {
		return res, err
	}

	info, err := ctr.Info(ctx)
	if err != nil {
		return res, err
	}

	res = &checkpointInfo{
		containerID:   ctrId,
		checkpointDir: path.Join(pkg.FaasdCheckpointDirPrefix, serviceName),
		workDir:       path.Join(config.globalWorkDir, serviceName),
		serviceName:   serviceName,
		mntNsFile:     mntNsFile,
		runtimeName:   info.Runtime.Name,
		process:       p,
	}

	return res, nil
}

func parseCheckpointConfig(cmd *cobra.Command) (*checkpointConfig, error) {
	var parsed *checkpointConfig

	// for now we only support static checkpoint dir
	checkpointDir := pkg.FaasdCheckpointDirPrefix
	workDir, err := cmd.Flags().GetString("work-dir")
	if err != nil {
		return parsed, errors.Wrap(err, "can not parse work-dir flag")
	}
	containerdAddr, err := cmd.Flags().GetString("containerd-sock")
	if err != nil {
		return parsed, errors.Wrap(err, "can not parse containerd-sock flag")
	}
	namespace, err := cmd.Flags().GetString("namespace")
	if err != nil {
		return parsed, errors.Wrap(err, "can not parse namespace flag")
	}
	pseudoMMDrvFile, err := os.Open("/dev/pseudo_mm")
	if err != nil {
		log.Error().Err(err).Msg("cannot open /dev/pseudo_mm (%s), are you using our linux kernel ?")
		return parsed, err
	}
	daxDevPath, err := cmd.Flags().GetString("dax-device")
	if err != nil {
		return parsed, errors.Wrap(err, "can not parse dax-device flag")
	}
	daxPgoff, err := cmd.Flags().GetInt("dax-pgoff")
	if err != nil {
		return parsed, errors.Wrap(err, "can not parse dax-pgoff flag")
	}
	rdmaBufSockAddr, err := cmd.Flags().GetString("rdma-buf-sock-path")
	if err != nil {
		return parsed, errors.Wrap(err, "can not parse rdma-buf-sock-path flag")
	}
	rdmaPgoff, err := cmd.Flags().GetInt("rdma-pgoff")
	if err != nil {
		return parsed, errors.Wrap(err, "can not parse rdma-pgoff flag")
	}
	memPoolType, err := cmd.Flags().GetString("mem-pool")
	if err != nil {
		return parsed, errors.Wrap(err, "can not parse mem-pool flag")
	}
	if memPoolType != "dax" && memPoolType != "rdma" {
		return parsed, errors.Errorf("mem-pool only support rdma and dax, get %s", memPoolType)
	}

	if len(checkpointDir) == 0 || len(workDir) == 0 || len(namespace) == 0 ||
		len(containerdAddr) == 0 {
		return parsed, fmt.Errorf("zero length field detected in config: %+v", parsed)
	}

	parsed = &checkpointConfig{
		globalWorkDir:   workDir,
		containerdAddr:  containerdAddr,
		namespace:       namespace,
		pseudoMMDrv:     pseudoMMDrvFile,
		daxDevPath:      daxDevPath,
		daxPgoff:        daxPgoff,
		rdmaBufSockAddr: rdmaBufSockAddr,
		rdmaPgoff:       rdmaPgoff,
		memPool:         memPoolType,
	}
	return parsed, nil
}

var checkpointCmd = &cobra.Command{
	Use:   "checkpoint",
	Short: "tools to create a checkpoint of a running containerd",
	Long:  "faasd checkpoint <ContainerID>",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runCheckpoint,
}

func init() {
	flags := checkpointCmd.Flags()
	flags.String("work-dir", pkg.FaasdCRIUCheckpointWorkPrefix, "work dir of CRIU (e.g., log path)")
	flags.String("containerd-sock", "/run/containerd/containerd.sock", "sock address of containerd daemon")
	flags.String("namespace", faasd.DefaultFunctionNamespace, "namespace of the target container in containerd")
	flags.Int("dax-pgoff", 0, "page offset on dax device to place the image (e.g., 16 means 64K offset)")
	flags.Int("rdma-pgoff", 0, "page offset on dax device to place the image (e.g., 16 means 64K offset)")
	flags.String("rdma-buf-sock-path", pkg.DefaultRDMABufSockPath, "path to the rdma buf socket address")
	flags.String("dax-device", pkg.DefaultDaxDevicePath, "path to the dax device used as memory pool")
	flags.String("mem-pool", "", "memory pool type, currently only support dax and rdma")
}

func runCheckpoint(cmd *cobra.Command, args []string) error {
	config, err := parseCheckpointConfig(cmd)
	if err != nil {
		return err
	}
	defer func() {
		config.pseudoMMDrv.Close()
	}()
	log.Info().Msgf("checkpoint config: %+v", config)
	// create a new containerd client
	// the timeout here is time of dial (i.e. establish connection)
	client, err := containerd.New(config.containerdAddr, containerd.WithTimeout(15*time.Second))
	if err != nil {
		return err
	}

	ckptInfos := make(map[string]*checkpointInfo)
	defer func() {
		for _, info := range ckptInfos {
			info.mntNsFile.Close()
		}
	}()
	for _, ctrId := range args {
		info, err := initCheckpointInfo(client, ctrId, config)
		if err != nil {
			log.Error().Err(err).Str("container id", ctrId).Msg("init checkpoint info failed")
			return err
		}
		ckptInfos[ctrId] = info

		log.Info().Str("container id", ctrId).Msg("start checkpoint...")
		if err := createOneCheckpoint(client, config, info); err != nil {
			log.Error().Err(err).Str("container id", ctrId).Msg("create checkpoint failed")
			return err
		}
	}

	// when we create all checkpoint, we start to convert them one by one
	// NOTE: we follow the order in args
	for _, ctrId := range args {
		info := ckptInfos[ctrId]
		// first convert checkpoint
		// then we can get the dax page num
		log.Info().Str("container id", ctrId).Int("dax pgoff", config.daxPgoff).Int("rdma pgoff", config.rdmaPgoff).Msg("start CONVERT checkpoint")
		if err := convertOneCheckpoint(config, info); err != nil {
			return err
		}
		pageNum, err := getCkptDaxPageNum(info)
		if err != nil {
			return err
		}
		config.daxPgoff += pageNum
		config.rdmaPgoff += pageNum
	}

	return nil
}

func getCkptDaxPageNum(info *checkpointInfo) (int, error) {
	pageNumImgPath := path.Join(info.checkpointDir, "convert-pgnum.img")
	content, err := os.ReadFile(pageNumImgPath)
	if err != nil {
		return -1, err
	}
	pageNum, err := strconv.Atoi(string(content))
	if err != nil {
		log.Error().Err(err).Msg("get dax page num after convert failed")
		return -1, err
	}
	if pageNum < 0 {
		return -1, fmt.Errorf("invalid convert-pagenum.img file")
	}
	return pageNum, nil
}

func getContainer(ctx context.Context, client *containerd.Client, containerID string) (containerd.Container, error) {
	ctr, err := client.LoadContainer(ctx, containerID)
	if err != nil {
		if containerderrors.IsNotFound(err) {
			log.Error().Str("container id", containerID).Msg("get Init process fail to found containerd")
		}
		return nil, err
	}
	return ctr, nil
}

func getInitProcess(ctx context.Context, ctr containerd.Container, containerID string) (containerd.Process, error) {
	task, err := ctr.Task(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("get containerd %s Task failed: %w", containerID, err)
	}
	return task, nil
}

func getCheckpointOptions(rt string, info *checkpointInfo) containerd.CheckpointTaskOpts {
	return func(r *containerd.CheckpointTaskInfo) error {
		if rt != plugin.RuntimeRuncV2 {
			return fmt.Errorf("weired runtime detect %s", rt)
		}
		if r.Options == nil {
			r.Options = &options.CheckpointOptions{}
		}
		opts, ok := r.Options.(*options.CheckpointOptions)
		if !ok {
			return fmt.Errorf("weired type of Options of %+v", r)
		}

		opts.Exit = true
		opts.ImagePath = info.checkpointDir
		opts.WorkPath = info.workDir
		return nil
	}
}

func getConvertArgs(config *checkpointConfig, info *checkpointInfo) []string {
	args := []string{"convert"}
	args = append(args, "-D", info.checkpointDir)
	args = append(args, "-v4")
	args = append(args, "-o", path.Join(info.workDir, "convert.log"))
	args = append(args, "--inherit-fd", "fd[3]:switch-ns-mnt")
	args = append(args, "--inherit-fd", fmt.Sprintf("fd[4]:%s", pkg.CRIUPseudoMMDrvInheritID))
	args = append(args, "--dax-device", config.daxDevPath)
	args = append(args, "--dax-pgoff", strconv.Itoa(config.daxPgoff))
	args = append(args, "--rdma-buf-sock-path", config.rdmaBufSockAddr)
	args = append(args, "--rdma-pgoff", strconv.Itoa(config.rdmaPgoff))
	args = append(args, "--mem-pool", config.memPool)
	return args
}

// rely on containerd -> runtime (runc) to do Checkpoint
func createOneCheckpoint(client *containerd.Client, config *checkpointConfig, info *checkpointInfo) error {
	// prepare img and work directory
	if err := os.MkdirAll(info.checkpointDir, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(info.workDir, 0755); err != nil {
		return err
	}
	// genereate the checkpoint
	opts := []containerd.CheckpointTaskOpts{getCheckpointOptions(info.runtimeName, info)}
	// since we pass ImagePath to containerd,
	// containerd will not create image in ImageService.
	// And the img returned here is supposed to be empty
	ctx := namespaces.WithNamespace(context.Background(), config.namespace)
	img, err := info.process.(containerd.Task).Checkpoint(ctx, opts...)
	log.Debug().Msgf("checkpoint img: %+v\n", img)
	if err != nil {
		return err
	}

	return nil
}

func convertOneCheckpoint(config *checkpointConfig, info *checkpointInfo) error {
	extraFiles := []*os.File{info.mntNsFile, config.pseudoMMDrv}
	args := getConvertArgs(config, info)
	cmd := exec.Command("criu", args...)
	cmd.ExtraFiles = extraFiles

	if err := cmd.Start(); err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		return err
	}

	return nil
}
