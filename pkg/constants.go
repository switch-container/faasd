package pkg

import "time"

const (
	// DefaultFunctionNamespace is the default containerd namespace functions are created
	DefaultFunctionNamespace = "openfaas-fn"

	// NamespaceLabel indicates that a namespace is managed by faasd
	NamespaceLabel = "openfaas"

	// FaasdNamespace is the containerd namespace services are created
	FaasdNamespace = "openfaas"

	faasServicesPullAlways = false

	defaultSnapshotter = "overlayfs"

	// default checkpoint image directory
	FaasdCheckpointDirPrefix = "/var/lib/faasd/checkpoints/images"
	// default CRIU work directory (i.e. generate criu log)
	FaasdCRIUCheckpointWorkPrefix = "/var/lib/faasd/checkpoints/criu-c-workdir"
	FaasdCRIUResotreWorkPrefix    = "/var/lib/faasd/checkpoints/criu-r-workdir"

	// pkg means /home/dir at
	FaasdPackageDirPrefix  = "/var/lib/faasd/pkgs"
	FaasdAppWorkDirPrefix  = "/var/lib/faasd/app/work"
	FaasdAppUpperDirPrefix = "/var/lib/faasd/app/upper"
	FaasdAppMergeDirPrefix = "/var/lib/faasd/app/merged"

	// have to use our new kernel
	PseudoMMDrvPath          = "/dev/pseudo_mm"
	CRIUPseudoMMDrvInheritID = "pseudo-mm-drv"
	DaxDevicePath            = "/dev/dax0.0"

	// The initial app overlay cache size for each service
	AppOverlayCacheInitNum = 1
	// The upper bound of app overlay cache size for each service
	AppOverlayCacheLimit = 16

	SwitchLatencyMetric    = "switch-latency"
	SwitchCountMetric      = "switch-count"
	ColdStartLatencyMetric = "cold-start-latency"
	ColdStartCountMetric   = "cold-start-count"
	ReuseCountMetric       = "reuse-count"
	ReuseLatencyMetric     = "reuse-latency"
	InvokeCountMetric      = "invoke-count"
	PrepareSwitchFSLatency = "prepare-switch-fs-latency"
	CRIUSwrkLatencyMetric  = "criu-swrk-latency"
	CRIUHandleNsMetric     = "criu-handle-ns-latency"
	SwitchKillMetric       = "criu-switch-kill-latency"
	CRIUSwrkCmdStartMetric = "criu-cmd-start-latency"

	BaselineGCInterval  = 5 * time.Second
	BaselineGCCriterion = 2 * time.Minute

	PopulateCtrNum = 60

	ColdStartConcurrencyLimit     = 10
	CtrGCConcurrencyLimit         = 5
	KillInstancesConcurrencyLimit = 4

	MemoryBound int64 = 32 * 1024 * 1024 * 1024
)
