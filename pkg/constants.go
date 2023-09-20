package pkg

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
	PseudoMMDrvPath               = "/dev/pseudo_mm"
	CRIUPseudoMMDrvInheritID      = "pseudo-mm-drv"
)
