package handlers

import (
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/containerd/containerd"
	faasd "github.com/openfaas/faasd/pkg"
)

const watchdogPort = "8080"

type InvokeResolver struct {
	client *containerd.Client
}

func NewInvokeResolver(client *containerd.Client) *InvokeResolver {
	return &InvokeResolver{client: client}
}

func (i *InvokeResolver) Resolve(functionName string) (url.URL, error) {
	actualFunctionName := functionName
	log.Printf("Resolve: %q\n", functionName)

	namespace := getNamespaceOrDefault(functionName, faasd.DefaultFunctionNamespace)

	if strings.Contains(functionName, ".") {
		functionName = strings.TrimSuffix(functionName, "."+namespace)
	}

	function, err := GetFunction(i.client, actualFunctionName, namespace)
  if err != nil {
		return url.URL{}, err
  }

	serviceIP := function.IP
	port, ok := function.envVars["port"]
	if !ok {
		port = watchdogPort
	}

	urlStr := fmt.Sprintf("http://%s:%s", serviceIP, port)
  log.Printf("resolve %s to %s", functionName, urlStr)

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
