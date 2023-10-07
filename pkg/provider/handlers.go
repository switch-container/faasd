package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/gorilla/mux"
	"github.com/openfaas/faas-provider/httputil"
	"github.com/openfaas/faas-provider/proxy"
	"github.com/openfaas/faas-provider/types"
	"github.com/openfaas/faasd/pkg"
	"github.com/openfaas/faasd/pkg/metrics"
)

// This part is almost copied from faas-provider/proxy

const (
	watchdogPort       = "8080"
	defaultContentType = "text/plain"
)

// proxyRequest handles the actual resolution of and then request to the function service.
func proxyRequest(w http.ResponseWriter, originalReq *http.Request, m *LambdaManager, proxyClient *http.Client) {
	start := time.Now()
	ctx := originalReq.Context()

	pathVars := mux.Vars(originalReq)
	lambdaName := pathVars["name"]
	if lambdaName == "" {
		httputil.Errorf(w, http.StatusBadRequest, "Provide lambda name in the request path")
		return
	}

	originalBody, err := io.ReadAll(originalReq.Body)
	if err != nil {
		httputil.Errorf(w, http.StatusInternalServerError, "read original request body for %s failed: %s", lambdaName, err)
		return
	}
	originalReq.Body.Close()

	instance, err := m.MakeCtrInstanceFor(lambdaName)
	if err != nil {
    log.Printf("MakeCtrInstanceFor %s failed: %s", lambdaName, err)
		httputil.Errorf(w, http.StatusInternalServerError, "Failed to make ctr instance for %s: %s", lambdaName, err)
		return
	}
	// move instance to busy pool
	pool, err := m.GetCtrPool(lambdaName)
	if err != nil {
		httputil.Errorf(w, http.StatusInternalServerError, "Failed to get ctr pool for %s: %s", lambdaName, err)
		return
	}
	instance.status = RUNNING
	instance.lastActive = time.Now()
	pool.PushIntoBusy(instance)

	if err = metrics.GetMetricLogger().Emit(pkg.InvokeCountMetric, lambdaName, 1); err != nil {
		httputil.Errorf(w, http.StatusInternalServerError, "Failed to emit metric for %s: %s", lambdaName, err)
		return
	}

	urlStr := fmt.Sprintf("http://%s:%s", instance.IpAddress, watchdogPort)
	lambdaAddr, err := url.Parse(urlStr)
	if err != nil {
		httputil.Errorf(w, http.StatusInternalServerError, "Failed to parse url for %s: %s", lambdaName, err)
		return
	}

	proxyReq, err := buildProxyRequest(originalReq, *lambdaAddr, pathVars["params"])
	if err != nil {
		httputil.Errorf(w, http.StatusInternalServerError, "Failed to resolve service: %s", lambdaName)
		return
	}

	// the faasd watchdog inside the container may not be initialized
	// here (e.g., cold-start)
	// we need retry here.
	var (
		response  *http.Response
		seconds   time.Duration
		success   bool          = false
		sleepTime time.Duration = time.Millisecond * 500
	)
	for i := 0; i < 3; i++ {
		proxyReq.Body = io.NopCloser(bytes.NewReader(originalBody))
		response, err = proxyClient.Do(proxyReq.WithContext(ctx))
		seconds = time.Since(start)

		if err == nil {
			success = true
			break
		} else if os.IsTimeout(err) {
			log.Printf("timeout when proxy to %s-%d", instance.LambdaName, instance.ID)
			break
		}

		log.Printf("[Attemp %d] error with proxy request for %s-%d  to %s, %s\n", i,
			instance.LambdaName, instance.ID, proxyReq.URL.String(), err.Error())
		time.Sleep(sleepTime)
		sleepTime *= 4
	}

	if !success {
		// TODO(huang-jl) garbage collect this ctr instance
		instance.status = INVALID
		httputil.Errorf(w, http.StatusInternalServerError, "Can't reach service for: %s.", lambdaName)
		return
	}

	pool.MoveFromBusyToFree(instance.ID)
	instance.status = FINISHED

	if response.Body != nil {
		defer response.Body.Close()
	}

	log.Printf("%s-%d took %f seconds\n", lambdaName, instance.ID, seconds.Seconds())

	clientHeader := w.Header()
	copyHeaders(clientHeader, &response.Header)
	w.Header().Set("Content-Type", getContentType(originalReq.Header, response.Header))

	w.WriteHeader(response.StatusCode)
	if response.Body != nil {
		io.Copy(w, response.Body)
	}
}

// buildProxyRequest creates a request object for the proxy request, it will ensure that
// the original request headers are preserved as well as setting openfaas system headers
func buildProxyRequest(originalReq *http.Request, baseURL url.URL, extraPath string) (*http.Request, error) {

	host := baseURL.Host
	if baseURL.Port() == "" {
		host = baseURL.Host + ":" + watchdogPort
	}

	url := url.URL{
		Scheme:   baseURL.Scheme,
		Host:     host,
		Path:     extraPath,
		RawQuery: originalReq.URL.RawQuery,
	}

	upstreamReq, err := http.NewRequest(originalReq.Method, url.String(), nil)
	if err != nil {
		return nil, err
	}
	copyHeaders(upstreamReq.Header, &originalReq.Header)

	if len(originalReq.Host) > 0 && upstreamReq.Header.Get("X-Forwarded-Host") == "" {
		upstreamReq.Header["X-Forwarded-Host"] = []string{originalReq.Host}
	}
	if upstreamReq.Header.Get("X-Forwarded-For") == "" {
		upstreamReq.Header["X-Forwarded-For"] = []string{originalReq.RemoteAddr}
	}

	return upstreamReq, nil
}

// copyHeaders clones the header values from the source into the destination.
func copyHeaders(destination http.Header, source *http.Header) {
	for k, v := range *source {
		vClone := make([]string, len(v))
		copy(vClone, v)
		destination[k] = vClone
	}
}

// getContentType resolves the correct Content-Type for a proxied function.
func getContentType(request http.Header, proxyResponse http.Header) (headerContentType string) {
	responseHeader := proxyResponse.Get("Content-Type")
	requestHeader := request.Get("Content-Type")

	if len(responseHeader) > 0 {
		headerContentType = responseHeader
	} else if len(requestHeader) > 0 {
		headerContentType = requestHeader
	} else {
		headerContentType = defaultContentType
	}

	return headerContentType
}

func MakeInvokeHandler(m *LambdaManager, config types.FaaSConfig) func(w http.ResponseWriter, r *http.Request) {
	proxyClient := proxy.NewProxyClientFromConfig(config)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			defer r.Body.Close()
		}

		switch r.Method {
		case http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
			http.MethodGet,
			http.MethodOptions,
			http.MethodHead:
			proxyRequest(w, r, m, proxyClient)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func MakeRegisterHandler(m *LambdaManager) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			http.Error(w, "expected a body", http.StatusBadRequest)
			return
		}

		defer r.Body.Close()

		body, _ := io.ReadAll(r.Body)
		log.Printf("[Register] request: %s\n", string(body))

		req := types.FunctionDeployment{}
		err := json.Unmarshal(body, &req)
		if err != nil {
			log.Printf("[Register] - error parsing input: %s\n", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		m.RegisterLambda(req)
		w.WriteHeader(http.StatusOK)
	}
}

func MakeMetricReader() func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		response := metrics.GetMetricLogger().Output()
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(response))
		w.WriteHeader(http.StatusOK)
	}
}
