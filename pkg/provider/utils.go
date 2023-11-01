package provider

import (
	"context"
	"net/http"
	"path"
	"strings"

	"github.com/openfaas/faasd/pkg"
	faasd "github.com/openfaas/faasd/pkg"
)

func GetRequestNamespace(namespace string) string {

	if len(namespace) > 0 {
		return namespace
	}
	return faasd.DefaultFunctionNamespace
}

func ReadNamespaceFromQuery(r *http.Request) string {
	q := r.URL.Query()
	return q.Get("namespace")
}

func GetNamespaceSecretMountPath(userSecretPath string, namespace string) string {
	return path.Join(userSecretPath, namespace)
}

// validNamespace indicates whether the namespace is eligable to be
// used for OpenFaaS functions.
func ValidNamespace(store Labeller, namespace string) (bool, error) {
	if namespace == faasd.DefaultFunctionNamespace {
		return true, nil
	}

	labels, err := store.Labels(context.Background(), namespace)
	if err != nil {
		return false, err
	}

	if value, found := labels[pkg.NamespaceLabel]; found && value == "true" {
		return true, nil
	}

	return false, nil
}

func ServiceName2LambdaName(serviceName string) string {
	delimiterIdx := strings.Index(serviceName, "_")
	if delimiterIdx == -1 {
		return serviceName
	}
	return serviceName[:delimiterIdx]
}
