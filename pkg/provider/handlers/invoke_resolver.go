package handlers

import (
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/containerd/containerd"
	faasd "github.com/openfaas/faasd/pkg"
)

const watchdogPort = 8080

type InvokeResolver struct {
	client *containerd.Client
}

func NewInvokeResolver(client *containerd.Client) *InvokeResolver {
	return &InvokeResolver{client: client}
}

func (i *InvokeResolver) Resolve(functionName string) (url.URL, error) {
	serviceName, id, err := ParseFunctionName(functionName)
	if err != nil {
		return url.URL{}, err
	}
	log.Printf("Resolve: %q\n", functionName)

	namespace := getNamespaceOrDefault(serviceName, faasd.DefaultFunctionNamespace)

	if strings.Contains(serviceName, ".") {
		serviceName = strings.TrimSuffix(serviceName, "."+namespace)
	}

	// nil updator means read only
	info, err := lambdaManager.UpdateInstance(serviceName, id, nil)
	if err != nil {
		return url.URL{}, err
	}

	serviceIP := info.IpAddress

	urlStr := fmt.Sprintf("http://%s:%d", serviceIP, watchdogPort)

	urlRes, err := url.Parse(urlStr)
	if err != nil {
		return url.URL{}, err
	}

	return *urlRes, nil
}

func getNamespaceOrDefault(name, defaultNamespace string) string {
	namespace := defaultNamespace
	if strings.Contains(name, ".") {
		namespace = name[strings.LastIndexAny(name, ".")+1:]
	}
	return namespace
}
