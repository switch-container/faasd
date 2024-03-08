package cmd

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd"
	bootstrap "github.com/openfaas/faas-provider"
	"github.com/openfaas/faas-provider/logs"
	"github.com/openfaas/faas-provider/proxy"
	"github.com/openfaas/faas-provider/types"
	"github.com/openfaas/faasd/pkg"
	faasd "github.com/openfaas/faasd/pkg"
	"github.com/openfaas/faasd/pkg/cninetwork"
	faasdlogs "github.com/openfaas/faasd/pkg/logs"
	"github.com/openfaas/faasd/pkg/metrics"
	"github.com/openfaas/faasd/pkg/provider"
	"github.com/openfaas/faasd/pkg/provider/config"
	"github.com/openfaas/faasd/pkg/provider/handlers"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

const secretDirPermission = 0755

func makeProviderCmd() *cobra.Command {
	var command = &cobra.Command{
		Use:   "provider",
		Short: "Run the faasd-provider",
	}

	command.Flags().String("pull-policy", "Always", `Set to "Always" to force a pull of images upon deployment, or "IfNotPresent" to try to use a cached image.`)
	command.Flags().Bool("baseline", false, `Set true to running in baseline mode (e.g., do container GC and disable switch).`)
	command.Flags().Bool("no-bgtask", false, `Set true to to disable background task (e.g. legacy faasd mode)`)
	command.Flags().String("start-method", "cold", `cold (cold start), criu (criu restore) or lazy (criu lazy restore), only enbale in baseline mode`)
	command.Flags().Int64("mem", pkg.DefaultMemoryBound, `memory bound for all containers in faasd (GB)`)
	command.Flags().Int64("gc", pkg.BaselineGCCriterion, `gc criterion in minutes`)

	command.RunE = func(_ *cobra.Command, _ []string) error {
		pullPolicy, flagErr := command.Flags().GetString("pull-policy")
		if flagErr != nil {
			return flagErr
		}
		isBaseline, flagErr := command.Flags().GetBool("baseline")
		if flagErr != nil {
			return flagErr
		}
		noBgTask, flagErr := command.Flags().GetBool("no-bgtask")
		if flagErr != nil {
			return flagErr
		}
		startMethod, flagErr := command.Flags().GetString("start-method")
		if flagErr != nil {
			return flagErr
		}
		gcMinute, flagErr := command.Flags().GetInt64("gc")
		if flagErr != nil {
			return flagErr
		}
		memoryBound, flagErr := command.Flags().GetInt64("mem")
		if flagErr != nil {
			return flagErr
		}
		memoryBound = memoryBound * 1024 * 1024 * 1024

		// check args
		if memoryBound >= 256*1024*1024*1024 {
			log.Fatal().Int64("mem bound", memoryBound).Msg("memory bound exceed 256GB!")
		}
		startMethod = strings.ToLower(startMethod)
		if startMethod != "cold" && !isBaseline {
			log.Fatal().Str("start method", startMethod).Msg("cannot enable non-default start-method for non-baseline!")
		}

		alwaysPull := false
		if pullPolicy == "Always" {
			alwaysPull = true
		}

		config, providerConfig, err := config.ReadFromEnv(types.OsEnv{})
		if err != nil {
			return err
		}

		log.Info().Int64("mem bound", memoryBound).Str("start method", startMethod).Bool("isBaseline", isBaseline).
			Str("Service Timeout", config.WriteTimeout.String()).Msg("faasd-provider starting...")
		printVersion()

		wd, err := os.Getwd()
		if err != nil {
			return err
		}

		writeHostsErr := ioutil.WriteFile(path.Join(wd, "hosts"),
			[]byte(`127.0.0.1	localhost`), workingDirectoryPermission)

		if writeHostsErr != nil {
			return fmt.Errorf("cannot write hosts file: %s", writeHostsErr)
		}

		writeResolvErr := ioutil.WriteFile(path.Join(wd, "resolv.conf"),
			[]byte(`nameserver 8.8.8.8`), workingDirectoryPermission)

		if writeResolvErr != nil {
			return fmt.Errorf("cannot write resolv.conf file: %s", writeResolvErr)
		}

		cni, err := cninetwork.InitNetwork()
		if err != nil {
			return err
		}
		client, err := containerd.New(providerConfig.Sock)
		if err != nil {
			return err
		}
		defer client.Close()

		var (
			m      *provider.LambdaManager
			bgTask []provider.BackgroundTask
		)
		if isBaseline {
			bgTask = []provider.BackgroundTask{
				// only for baseline
				provider.NewInstanceGCBackgroundTask(pkg.BaselineGCInterval,
					time.Duration(gcMinute)*time.Minute, pkg.CtrGCConcurrencyLimit),
			}
			baselinePolicy, err := provider.NewBaselinePolicy(startMethod)
			if err != nil {
				return err
			}
			m, err = provider.NewLambdaManager(client, cni, baselinePolicy, memoryBound)
		} else {
			bgTask = []provider.BackgroundTask{
				provider.NewPopulateCtrBackgroundTask(pkg.PopulateCtrNum),
			}
			m, err = provider.NewLambdaManager(client, cni, provider.NaiveSwitchPolicy{}, memoryBound)
		}
		if err != nil {
			return err
		}

		// start background task
		if !noBgTask {
			for _, t := range bgTask {
				go t.Run(m)
			}
		}

		// init metric
		metricLogger := metrics.GetMetricLogger()
		for _, m := range []struct {
			name string
			ty   metrics.MetricType
		}{
			{pkg.SwitchLatencyMetric, metrics.LATENCY_METRIC},
			{pkg.SwitchCountMetric, metrics.FIND_GRAINED_COUNTER},
			{pkg.StartNewLatencyMetric, metrics.LATENCY_METRIC},
			{pkg.StartNewCountMetric, metrics.FIND_GRAINED_COUNTER},
			{pkg.ReuseCountMetric, metrics.FIND_GRAINED_COUNTER},
			{pkg.ReuseLatencyMetric, metrics.LATENCY_METRIC},
			{pkg.InvokeCountMetric, metrics.SINGLE_COUNTER},

			{pkg.PrepareSwitchFSLatency, metrics.LATENCY_METRIC},
			{pkg.CRIUSwrkLatencyMetric, metrics.LATENCY_METRIC},
			{pkg.CRIUHandleNsMetric, metrics.LATENCY_METRIC},
			{pkg.SwitchKillMetric, metrics.LATENCY_METRIC},
			{pkg.CRIUSwrkCmdStartMetric, metrics.LATENCY_METRIC},
		} {
			metricLogger.RegisterMetric(m.name, m.ty)
		}

		// handler for linux signal
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		wg := sync.WaitGroup{}
		wg.Add(1)
		go func() {
			<-sig
			log.Info().Msg("Signal received.. shutting down server")
			m.Shutdown()
			wg.Done()
		}()

		invokeResolver := handlers.NewInvokeResolver(client)

		baseUserSecretsPath := path.Join(wd, "secrets")
		if err := moveSecretsToDefaultNamespaceSecrets(
			baseUserSecretsPath,
			faasd.DefaultFunctionNamespace); err != nil {
			return err
		}

		bootstrapHandlers := types.FaaSHandlers{
			FunctionProxy:  proxy.NewHandlerFunc(*config, invokeResolver),
			DeleteFunction: handlers.MakeDeleteHandler(client, cni),
			DeployFunction: handlers.MakeDeployHandler(client, cni, baseUserSecretsPath,
				providerConfig.CheckpointDir, alwaysPull),
			FunctionLister:  handlers.MakeReadHandler(m),
			FunctionStatus:  handlers.MakeReplicaReaderHandler(client),
			ScaleFunction:   handlers.MakeReplicaUpdateHandler(client, cni),
			UpdateFunction:  handlers.MakeUpdateHandler(client, cni, baseUserSecretsPath, alwaysPull),
			Health:          func(w http.ResponseWriter, r *http.Request) {},
			Info:            handlers.MakeInfoHandler(Version, GitCommit),
			ListNamespaces:  handlers.MakeNamespacesLister(client),
			Secrets:         handlers.MakeSecretHandler(client.NamespaceService(), baseUserSecretsPath),
			Logs:            logs.NewLogHandlerFunc(faasdlogs.New(), config.ReadTimeout),
			MutateNamespace: handlers.MakeMutateNamespace(client),

			ListCheckpoint:   provider.MakeCheckpointReader(m),
			RegisterFunction: provider.MakeRegisterHandler(m),
			InvokeFunction:   provider.MakeInvokeHandler(m, *config),
			MetricFunction:   provider.MakeMetricHandler(m),
		}

		log.Info().Int("port", *config.TCPPort).Msg("Listening on: 0.0.0.0")
		bootstrap.Serve(&bootstrapHandlers, config)
		wg.Wait()
		return nil
	}

	return command
}

/*
* Mutiple namespace support was added after release 0.13.0
* Function will help users to migrate on multiple namespace support of faasd
 */
func moveSecretsToDefaultNamespaceSecrets(baseSecretPath string, defaultNamespace string) error {
	newSecretPath := path.Join(baseSecretPath, defaultNamespace)

	err := ensureSecretsDir(newSecretPath)
	if err != nil {
		return err
	}

	files, err := ioutil.ReadDir(baseSecretPath)
	if err != nil {
		return err
	}

	for _, f := range files {
		if !f.IsDir() {

			newPath := path.Join(newSecretPath, f.Name())

			// A non-nil error means the file wasn't found in the
			// destination path
			if _, err := os.Stat(newPath); err != nil {
				oldPath := path.Join(baseSecretPath, f.Name())

				if err := copyFile(oldPath, newPath); err != nil {
					return err
				}

				log.Printf("[Migration] Copied %s to %s", oldPath, newPath)
			}
		}
	}

	return nil
}

func copyFile(src, dst string) error {
	inputFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %s failed %w", src, err)
	}
	defer inputFile.Close()

	outputFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_APPEND, secretDirPermission)
	if err != nil {
		return fmt.Errorf("opening %s failed %w", dst, err)
	}
	defer outputFile.Close()

	// Changed from os.Rename due to issue in #201
	if _, err := io.Copy(outputFile, inputFile); err != nil {
		return fmt.Errorf("writing into %s failed %w", outputFile.Name(), err)
	}

	return nil
}
