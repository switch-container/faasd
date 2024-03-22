module github.com/switch-container/faasd/pkg/provider/faasnap

go 1.20

require (
	github.com/antihax/optional v1.0.0
	github.com/pkg/errors v0.9.1
	github.com/switch-container/faasd/pkg/provider/faasnap/api/swagger v0.0.0
)

require (
	golang.org/x/net v0.22.0 // indirect
	golang.org/x/oauth2 v0.18.0 // indirect
	google.golang.org/appengine v1.6.7 // indirect
	google.golang.org/protobuf v1.33.0 // indirect
)

replace github.com/switch-container/faasd/pkg/provider/faasnap/api/swagger => ./api/
