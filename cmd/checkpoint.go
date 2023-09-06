package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"time"

	"github.com/containerd/containerd"
	containerderrors "github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/plugin"
	"github.com/containerd/containerd/runtime/v2/runc/options"
	"github.com/openfaas/faasd/pkg"
	faasd "github.com/openfaas/faasd/pkg"
	"github.com/openfaas/faasd/pkg/provider/handlers"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type checkpointConfig struct {
	containerID    string
	checkpointDir  string
	workDir        string
	containerdAddr string
	// namespace of container in containerd
	namespace string
}

func parseCheckpointConfig(cmd *cobra.Command, args []string) (checkpointConfig, error) {
	containerID := args[0]
	parsed := checkpointConfig{}

	// for now we only support static checkpoint dir
	checkpointDir := pkg.FaasdCheckpointDirPrefix
	// checkpointDir, err := cmd.Flags().GetString("checkpoint-dir")
	// if err != nil {
	// 	return parsed, errors.Wrap(err, "can not parse checkpoint-dir flag")
	// }
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

	if len(checkpointDir) == 0 || len(workDir) == 0 || len(namespace) == 0 ||
		len(containerID) == 0 || len(containerdAddr) == 0 {
		return parsed, fmt.Errorf("zero length field detected in config: %+v", parsed)
	}

	serviceName, _, err := handlers.ParseFunctionName(containerID)
	if err != nil {
		return parsed, errors.Wrapf(err, "invalid container id %s", containerID)
	}

	parsed.containerID = containerID
	parsed.checkpointDir = path.Join(checkpointDir, serviceName)
	parsed.workDir = path.Join(workDir, serviceName)
	parsed.containerdAddr = containerdAddr
	parsed.namespace = namespace
	return parsed, nil
}

var checkpointCmd = &cobra.Command{
	Use:   "checkpoint",
	Short: "tools to create a checkpoint of a running containerd",
	Long:  "faasd checkpoint <ContainerID>",
	Args:  cobra.ExactArgs(1),
	RunE:  runCheckpoint,
}

func init() {
	flags := checkpointCmd.Flags()
	flags.String("checkpoint-dir", pkg.FaasdCheckpointDirPrefix, "path of the checkpoint image")
	flags.String("work-dir", pkg.FaasdCRIUCheckpointWorkPrefix, "work dir of CRIU (e.g., log path)")
	flags.String("containerd-sock", "/run/containerd/containerd.sock", "sock address of containerd daemon")
	flags.String("namespace", faasd.DefaultFunctionNamespace, "namespace of the target container in containerd")
}

func runCheckpoint(cmd *cobra.Command, args []string) error {
	config, err := parseCheckpointConfig(cmd, args)
	if err != nil {
		return err
	}
	// create a new containerd client
	// the timeout here is time of dial (i.e. establish connection)
	client, err := containerd.New(config.containerdAddr, containerd.WithTimeout(15*time.Second))
	if err != nil {
		return err
	}

	return createCheckpoint(client, config)
}

func getContainer(ctx context.Context, client *containerd.Client, containerID string) (containerd.Container, error) {
	ctr, err := client.LoadContainer(ctx, containerID)
	if err != nil {
		if containerderrors.IsNotFound(err) {
			log.Printf("get Init process failed: not found containerd %s\n", containerID)
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

func getCheckpointOptions(rt string, config checkpointConfig) containerd.CheckpointTaskOpts {
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
		opts.ImagePath = config.checkpointDir
		opts.WorkPath = config.workDir
		return nil
	}
}

// rely on containerd -> runtime (runc) to do Checkpoint
func createCheckpoint(client *containerd.Client, config checkpointConfig) error {
	// prepare img and work directory
	if err := os.MkdirAll(config.checkpointDir, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(config.workDir, 0755); err != nil {
		return err
	}

	ctx := namespaces.WithNamespace(context.Background(), config.namespace)
	ctr, err := getContainer(ctx, client, config.containerID)
	if err != nil {
		return err
	}
	p, err := getInitProcess(ctx, ctr, config.containerID)
	if err != nil {
		return err
	}
	info, err := ctr.Info(ctx)
	if err != nil {
		return err
	}
	opts := []containerd.CheckpointTaskOpts{getCheckpointOptions(info.Runtime.Name, config)}
	// since we pass ImagePath to containerd,
	// containerd will not create image in ImageService.
	// And the img returned here is supposed to be empty
	img, err := p.(containerd.Task).Checkpoint(ctx, opts...)
	log.Printf("checkpoint img: %+v\n", img)
	if err != nil {
		return err
	}
	return nil
}
