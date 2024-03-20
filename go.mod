module github.com/openfaas/faasd

go 1.22.1

require (
	github.com/alexellis/arkade v0.0.0-20240315144638-acb1bd999ac8
	github.com/alexellis/go-execute v0.6.0
	github.com/compose-spec/compose-go v1.20.2
	github.com/containerd/containerd v1.7.14
	github.com/containerd/go-cni v1.1.9
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf
	github.com/docker/cli v25.0.4+incompatible
	github.com/docker/distribution v2.8.3+incompatible
	github.com/docker/docker v25.0.4+incompatible // indirect
	github.com/docker/go-units v0.5.0
	github.com/gorilla/mux v1.8.1
	github.com/morikuni/aec v1.0.0
	github.com/opencontainers/runtime-spec v1.2.0
	github.com/openfaas/faas-provider v0.25.3
	github.com/pkg/errors v0.9.1
	github.com/sethvargo/go-password v0.2.0
	github.com/spf13/cobra v1.8.0
	github.com/spf13/pflag v1.0.5
	github.com/vishvananda/netlink v1.2.1-beta.2
	github.com/vishvananda/netns v0.0.4
	golang.org/x/sys v0.18.0
	k8s.io/apimachinery v0.29.3
)

require (
	github.com/checkpoint-restore/go-criu/v5 v5.3.0
	github.com/rs/zerolog v1.32.0
	google.golang.org/protobuf v1.33.0
)

require (
	github.com/Microsoft/go-winio v0.6.1 // indirect
	github.com/Microsoft/hcsshim v0.12.0 // indirect
	github.com/alexellis/go-execute/v2 v2.2.1 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/containerd/cgroups/v3 v3.0.3 // indirect
	github.com/containerd/continuity v0.4.3 // indirect
	github.com/containerd/errdefs v0.1.0 // indirect
	github.com/containerd/fifo v1.1.0 // indirect
	github.com/containerd/ttrpc v1.2.3 // indirect
	github.com/containerd/typeurl v1.0.2 // indirect
	github.com/containernetworking/cni v1.1.2 // indirect
	github.com/distribution/reference v0.5.0 // indirect
	github.com/docker/docker-credential-helpers v0.8.1 // indirect
	github.com/docker/go-connections v0.5.0 // indirect
	github.com/docker/go-events v0.0.0-20190806004212-e31b211e4f1c // indirect
	github.com/gogo/googleapis v1.4.1 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/imdario/mergo v0.3.16 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/compress v1.17.7 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-shellwords v1.0.12 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/moby/locker v1.0.1 // indirect
	github.com/moby/sys/mountinfo v0.7.1 // indirect
	github.com/moby/sys/signal v0.7.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.0 // indirect
	github.com/opencontainers/runc v1.1.12 // indirect
	github.com/opencontainers/selinux v1.11.0 // indirect
	github.com/prometheus/client_golang v1.19.0 // indirect
	github.com/prometheus/client_model v0.6.0 // indirect
	github.com/prometheus/common v0.50.0 // indirect
	github.com/prometheus/procfs v0.13.0 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/xeipuuv/gojsonpointer v0.0.0-20190905194746-02993c407bfb // indirect
	github.com/xeipuuv/gojsonreference v0.0.0-20180127040603-bd5ef7bd5415 // indirect
	github.com/xeipuuv/gojsonschema v1.2.0 // indirect
	go.opencensus.io v0.24.0 // indirect
	golang.org/x/exp v0.0.0-20240318143956-a85f2c67cd81 // indirect
	golang.org/x/mod v0.16.0 // indirect
	golang.org/x/net v0.22.0 // indirect
	golang.org/x/oauth2 v0.18.0 // indirect
	golang.org/x/sync v0.6.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	golang.org/x/tools v0.19.0 // indirect
	google.golang.org/appengine v1.6.8 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240318140521-94a12d6c2237 // indirect
	google.golang.org/grpc v1.62.1 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

require (
	github.com/antihax/optional v1.0.0
	github.com/switch-container/faasd/pkg/provider/faasnap v0.0.0
	github.com/switch-container/faasd/pkg/provider/faasnap/api/swagger v0.0.0
)

replace (
	github.com/checkpoint-restore/go-criu/v5 => ../../checkpoint-restore/go-criu/
	github.com/containerd/containerd => ../../containerd/containerd/
	github.com/openfaas/faas-provider => ../faas-provider/
	github.com/switch-container/faasd/pkg/provider/faasnap => ./pkg/provider/faasnap/
	github.com/switch-container/faasd/pkg/provider/faasnap/api/swagger => ./pkg/provider/faasnap/api/
)
