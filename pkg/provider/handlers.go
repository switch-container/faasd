package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/openfaas/faas-provider/httputil"
	"github.com/openfaas/faas-provider/proxy"
	"github.com/openfaas/faas-provider/types"
	"github.com/openfaas/faasd/pkg"
	"github.com/openfaas/faasd/pkg/metrics"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

// This part is almost copied from faas-provider/proxy
// Here contains the handler I add for evaluation purpose:
//
// - register: Register the types.FunctionDeployment for specific lambdas
// - invoke: the user should not care about which containers to handle the request
//    so here comse the invoke API, the faasd will choose/start a container to run
//    the user only need specify the lambdaName and payload

const (
	watchdogPort       = "8080"
	defaultContentType = "text/plain"
)

// TODO(huang-jl) we need reuse latency metric, since in current implementation, cold start
// may failed due to concurrency limitation, so even reuse may consume some time (e.g., waiting to retry)
func recordStartupMetric(dur time.Duration, instance *CtrInstance) error {
	lambdaName := instance.LambdaName
	switch instance.depolyDecision {
	case COLD_START:
		if err := metrics.GetMetricLogger().Emit(pkg.ColdStartLatencyMetric, lambdaName, dur); err != nil {
			return err
		}
		if err := metrics.GetMetricLogger().Emit(pkg.ColdStartCountMetric, lambdaName, 1); err != nil {
			return err
		}
	case REUSE:
		if err := metrics.GetMetricLogger().Emit(pkg.ReuseCountMetric, lambdaName, 1); err != nil {
			return err
		}
	case SWITCH:
		if err := metrics.GetMetricLogger().Emit(pkg.SwitchLatencyMetric, lambdaName, dur); err != nil {
			return err
		}
		if err := metrics.GetMetricLogger().Emit(pkg.SwitchCountMetric, lambdaName, 1); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown decision")
	}
	if err := metrics.GetMetricLogger().Emit(pkg.InvokeCountMetric, lambdaName, 1); err != nil {
		return err
	}
	return nil
}

func proxyRequest(ctx context.Context, originalReq *http.Request, proxyClient *http.Client,
	targetUrl url.URL, lambdaName string) (retry_times int, response *http.Response, err error) {

	// buffer originalReq body for retry
	originalBody, err := io.ReadAll(originalReq.Body)
	if err != nil {
		err = errors.Wrap(err, "read original request body failed")
		return
	}
	originalReq.Body.Close()
	// build proxy request
	pathVars := mux.Vars(originalReq)
	proxyReq, err := buildProxyRequest(originalReq, targetUrl, pathVars["params"])
	if err != nil {
		err = errors.Wrapf(err, "Failed to resolve service %s", lambdaName)
		return
	}
	// the faasd watchdog inside the container may not be initialized
	// here (e.g., cold-start)
	// we need retry here.
	for retry_times = 0; ; retry_times++ {
		if retry_times > 0 {
			select {
			case <-ctx.Done():
				err = ctx.Err()
				return
			case <-time.After(200 * time.Millisecond):
			}
		}

		proxyReq.Body = io.NopCloser(bytes.NewReader(originalBody))
		response, err = proxyClient.Do(proxyReq.WithContext(ctx))
		if err == nil {
			if response.StatusCode != http.StatusServiceUnavailable {
				// here means we send request succeed but there is runtime error in container

				// NOTE by huang-jl: StatusServiceUnavailable means the upstream app not initialize
				// please refer to of-watchdog to see when will return StatusServiceUnavailable
				return
			}
		} else if errors.Is(err, syscall.ECONNREFUSED) {
			// here means container's watchdog not ready
		} else {
			// here means some real error happen: should not retry
			return
		}
	}
}

// proxyRequest handles the actual resolution of and then request to the function service.
func handleInvokeRequest(w http.ResponseWriter, originalReq *http.Request, m *LambdaManager, proxyClient *http.Client) {
	var err error

	start := time.Now()
	ctx := originalReq.Context()
	// add timeout
	ctx, cancelFunc := context.WithTimeout(ctx, time.Second*55)
	defer cancelFunc()

	// parse lambdaName
	pathVars := mux.Vars(originalReq)
	lambdaName := pathVars["name"]
	if lambdaName == "" {
		httputil.Errorf(w, http.StatusBadRequest, "Provide lambda name in the request path")
		return
	}

	begin := time.Now()
	instance, err := m.MakeCtrInstanceFor(ctx, lambdaName)
	if err != nil {
		log.Error().Err(err).Str("lambda name", lambdaName).Msg("MakeCtrInstanceFor failed")
		httputil.Errorf(w, http.StatusInternalServerError, "Failed to make ctr instance for %s: %s", lambdaName, err)
		return
	}
	if err = recordStartupMetric(time.Since(begin), instance); err != nil {
		httputil.Errorf(w, http.StatusInternalServerError, "failed to recordStartupMetric for %s: %s", lambdaName, err)
		return
	}

	instanceID := GetInstanceID(instance.LambdaName, instance.ID)
	// move instance to busy pool
	pool, err := m.GetCtrPool(lambdaName)
	if err != nil {
		httputil.Errorf(w, http.StatusInternalServerError, "Failed to get ctr pool for %s: %s", lambdaName, err)
		return
	}
	instance.status = RUNNING
	pool.PushIntoBusy(instance)

	// resolve the container's http address
	port, ok := pool.requirement.EnvVars["port"]
	if !ok {
		port = watchdogPort
	}
	urlStr := fmt.Sprintf("http://%s:%s", instance.IpAddress, port)
	lambdaAddr, err := url.Parse(urlStr)
	if err != nil {
		httputil.Errorf(w, http.StatusInternalServerError, "Failed to parse url for %s: %s", lambdaName, err)
		return
	}
	// start proxy request
	retry_times, response, err := proxyRequest(ctx, originalReq, proxyClient, *lambdaAddr, lambdaName)
	if err != nil {
		// TODO(huang-jl) garbage collect this ctr instance
		log.Error().Err(err).Str("instance", instanceID).Str("url", urlStr).
			Str("depoly decision", instance.depolyDecision.String()).Msg("invoke failed")
		instance.status = INVALID
		httputil.Errorf(w, http.StatusInternalServerError, "[%s] invoke to %s failed: %s", instanceID, urlStr, err)
		return
	}

	instance.status = FINISHED
	instance.lastActive = time.Now()
	pool.MoveFromBusyToFree(instance.ID)

	if response.Body != nil {
		defer response.Body.Close()
	}

	log.Debug().Str("instance", instanceID).Str("url", urlStr).Int("retry times", retry_times).
		Str("depoly decision", instance.depolyDecision.String()).
		Dur("total overhead", time.Since(start)).Msg("invoke succeed")

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
// NOTE by huang-jl: it will not set the request.Body
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
			handleInvokeRequest(w, r, m, proxyClient)

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
		log.Info().Str("body", string(body)).Msg("Register request")

		req := types.FunctionDeployment{}
		err := json.Unmarshal(body, &req)
		if err != nil {
			log.Error().Err(err).Msg("Register parsing input failed")
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
