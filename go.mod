module github.com/openfaas/faasd

go 1.20

require (
	github.com/alexellis/arkade v0.0.0-20230705083451-a4dd6013ddcd
	github.com/alexellis/go-execute v0.6.0
	github.com/compose-spec/compose-go v0.0.0-20200528042322-36d8ce368e05
	github.com/containerd/containerd v1.7.0
	github.com/containerd/go-cni v1.1.9
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf
	github.com/docker/cli v24.0.2+incompatible
	github.com/docker/distribution v2.8.2+incompatible
	github.com/docker/docker v24.0.2+incompatible // indirect
	github.com/docker/go-units v0.5.0
	github.com/gorilla/mux v1.8.0
	github.com/morikuni/aec v1.0.0
	github.com/opencontainers/runtime-spec v1.1.0-rc.3
	github.com/openfaas/faas-provider v0.24.0
	github.com/pkg/errors v0.9.1
	github.com/sethvargo/go-password v0.2.0
	github.com/spf13/cobra v1.7.0
	github.com/spf13/pflag v1.0.5
	github.com/vishvananda/netlink v1.2.1-beta.2
	github.com/vishvananda/netns v0.0.4
	golang.org/x/sys v0.18.0
	k8s.io/apimachinery v0.27.3
)

require (
	github.com/checkpoint-restore/go-criu/v5 v5.3.0
	github.com/rs/zerolog v1.31.0
	google.golang.org/protobuf v1.33.0
)

require (
	github.com/Microsoft/go-winio v0.6.1 // indirect
	github.com/Microsoft/hcsshim v0.10.0-rc.7 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/containerd/cgroups v1.1.0 // indirect
	github.com/containerd/continuity v0.3.0 // indirect
	github.com/containerd/fifo v1.1.0 // indirect
	github.com/containerd/ttrpc v1.2.1 // indirect
	github.com/containerd/typeurl v1.0.2 // indirect
	github.com/containernetworking/cni v1.1.2 // indirect
	github.com/docker/docker-credential-helpers v0.7.0 // indirect
	github.com/docker/go-connections v0.4.0 // indirect
	github.com/docker/go-events v0.0.0-20190806004212-e31b211e4f1c // indirect
	github.com/gogo/googleapis v1.4.1 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/imdario/mergo v0.3.14 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/compress v1.16.5 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/mattn/go-shellwords v1.0.12 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.4 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/moby/locker v1.0.1 // indirect
	github.com/moby/sys/mountinfo v0.6.2 // indirect
	github.com/moby/sys/signal v0.7.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.0-rc3 // indirect
	github.com/opencontainers/runc v1.1.5 // indirect
	github.com/opencontainers/selinux v1.11.0 // indirect
	github.com/prometheus/client_golang v1.16.0 // indirect
	github.com/prometheus/client_model v0.4.0 // indirect
	github.com/prometheus/common v0.42.0 // indirect
	github.com/prometheus/procfs v0.10.1 // indirect
	github.com/rogpeppe/go-internal v1.6.1 // indirect
	github.com/sirupsen/logrus v1.9.0 // indirect
	github.com/stretchr/testify v1.8.2 // indirect
	github.com/xeipuuv/gojsonpointer v0.0.0-20190905194746-02993c407bfb // indirect
	github.com/xeipuuv/gojsonreference v0.0.0-20180127040603-bd5ef7bd5415 // indirect
	github.com/xeipuuv/gojsonschema v1.2.0 // indirect
	go.opencensus.io v0.24.0 // indirect
	golang.org/x/mod v0.12.0 // indirect
	golang.org/x/net v0.22.0 // indirect
	golang.org/x/oauth2 v0.18.0 // indirect
	golang.org/x/sync v0.3.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	golang.org/x/tools v0.8.0 // indirect
	google.golang.org/appengine v1.6.7 // indirect
	google.golang.org/genproto v0.0.0-20230306155012-7f2fa6fef1f4 // indirect
	google.golang.org/grpc v1.53.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
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
