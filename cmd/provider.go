package cmd

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"sync"
	"syscall"

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
	"github.com/spf13/cobra"
)

const secretDirPermission = 0755

func makeProviderCmd() *cobra.Command {
	var command = &cobra.Command{
		Use:   "provider",
		Short: "Run the faasd-provider",
	}

	command.Flags().String("pull-policy", "Always", `Set to "Always" to force a pull of images upon deployment, or "IfNotPresent" to try to use a cached image.`)

	command.RunE = func(_ *cobra.Command, _ []string) error {
		pullPolicy, flagErr := command.Flags().GetString("pull-policy")
		if flagErr != nil {
			return flagErr
		}

		alwaysPull := false
		if pullPolicy == "Always" {
			alwaysPull = true
		}

		config, providerConfig, err := config.ReadFromEnv(types.OsEnv{})
		if err != nil {
			return err
		}

		log.Printf("faasd-provider starting..\tService Timeout: %s\n", config.WriteTimeout.String())
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

		m, err := provider.NewLambdaManager(client, cni, provider.NaiveSwitchPolicy{})
		if err != nil {
			return err
		}
		// init metric
		metricLogger := metrics.GetMetricLogger()
		for _, m := range []struct {
			name string
			ty   metrics.MetricType
		}{
			{pkg.SwitchLatencyMetric, metrics.LATENCY_METRIC},
			{pkg.SwitchCountMetric, metrics.FIND_GRAINED_COUNTER},
			{pkg.ColdStartLatencyMetric, metrics.LATENCY_METRIC},
			{pkg.ColdStartCountMetric, metrics.FIND_GRAINED_COUNTER},
			{pkg.InvokeCountMetric, metrics.SINGLE_COUNTER},

			{pkg.PrepareSwitchFSLatency, metrics.LATENCY_METRIC},
			{pkg.CRIUSwrkLatencyMetric, metrics.LATENCY_METRIC},
			{pkg.CRIUHandleNsMetric, metrics.LATENCY_METRIC},
			{pkg.SwitchKillMetric, metrics.LATENCY_METRIC},
			{pkg.CRIUSwrkCmdStartMetric, metrics.LATENCY_METRIC},
		} {
			metricLogger.RegisterMetric(m.name, m.ty)
		}

		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		wg := sync.WaitGroup{}
		wg.Add(1)
		go func() {
			<-sig
			log.Println("Signal received.. shutting down server")
			m.ShutdownAll()
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
			MetricFunction:   provider.MakeMetricReader(),
		}

		log.Printf("Listening on: 0.0.0.0:%d\n", *config.TCPPort)
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
