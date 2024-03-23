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

	// have to use our new kernel
	PseudoMMDrvPath          = "/dev/pseudo_mm"
	CRIUPseudoMMDrvInheritID = "pseudo-mm-drv"
	// metrics
	SwitchLatencyMetric    = "switch-latency"
	SwitchCountMetric      = "switch-count"
	StartNewLatencyMetric  = "start-new-latency"
	StartNewCountMetric    = "start-new-count"
	ReuseCountMetric       = "reuse-count"
	ReuseLatencyMetric     = "reuse-latency"
	InvokeCountMetric      = "invoke-count"
	PrepareSwitchFSLatency = "prepare-switch-fs-latency"
	CRIUSwrkLatencyMetric  = "criu-swrk-latency"
	CRIUHandleNsMetric     = "criu-handle-ns-latency"
	SwitchKillMetric       = "criu-switch-kill-latency"
	CRIUSwrkCmdStartMetric = "criu-cmd-start-latency"

	// NOTE by huang-jl: only the following params need be tuned
	DefaultDaxDevicePath   = "/dev/dax0.1"
	DefaultRDMABufSockPath = "/run/pseudo-mm-rdma-server-buf.sock"

	// default checkpoint image directory
	FaasdCheckpointDirPrefix = "/var/lib/faasd/checkpoints/images"
	// default CRIU work directory (i.e. generate criu log)
	FaasdCRIUCheckpointWorkPrefix = "/var/lib/faasd/checkpoints/criu-c-workdir"
	FaasdCRIUResotreWorkPrefix    = "/var/lib/faasd/checkpoints/criu-r-workdir"

	FaasdPackageDirPrefix  = "/var/lib/faasd/pkgs" // lower dir for app overlay
	FaasdAppWorkDirPrefix  = "/var/lib/faasd/app/work"
	FaasdAppUpperDirPrefix = "/var/lib/faasd/app/upper"
	FaasdAppMergeDirPrefix = "/var/lib/faasd/app/merged"

	// The initial app overlay cache size for each service
	AppOverlayCacheInitNum = 1
	// The upper bound of app overlay cache size for each service
	AppOverlayCacheLimit = 16

	BaselineGCInterval  = 5 * time.Second
	BaselineGCCriterion = 10

	PopulateCtrNum = 80

	StartNewCtrConcurrencyLimit   = 10
	CtrGCConcurrencyLimit         = 5
	KillInstancesConcurrencyLimit = 4

	// default memory bound is 32 GB
	DefaultMemoryBound int64 = 32

	// FaasnapSnapshotIdFile is the file path to load snapshot ids for Faasnap
	FaasnapSnapshotIdFile = "/mnt/data/faasnap/snapshot/snapshots.json"
)
