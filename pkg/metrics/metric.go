package metrics

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type MetricType int

const (
	LATENCY_METRIC MetricType = iota
	FIND_GRAINED_COUNTER
	SINGLE_COUNTER
)

type MetricLogger struct {
	metrics map[string]Metric
	mu      sync.RWMutex
}

func NewMetricLogger() *MetricLogger {
	return &MetricLogger{
		metrics: map[string]Metric{},
	}
}

type Metric interface {
	// add a new entry into metric
	Emit(lambdaName string, val any) error
	// cleanup all entries in metric
	Cleanup()
	// show metric entries in string format
	String() string
}

var metric = NewMetricLogger()

func GetMetricLogger() *MetricLogger {
	return metric
}

func (m *MetricLogger) RegisterMetric(metricName string, ty MetricType) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.metrics[metricName]; !ok {
		switch ty {
		case LATENCY_METRIC:
			m.metrics[metricName] = &LatencyMetric{
				data:  make(map[string][]time.Duration),
				label: metricName,
			}
		case FIND_GRAINED_COUNTER:
			m.metrics[metricName] = &FineGrainedCounterMetric{
				data:  map[string]int{},
				label: metricName,
			}
		case SINGLE_COUNTER:
			m.metrics[metricName] = &CounterMetric{label: metricName}
		}
	}
}

func (m *MetricLogger) Output() string {
	str := fmt.Sprintf("Metrics:\n")
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, metric := range m.metrics {
		str += metric.String()
		str += "\n"
	}
	return str
}

func (m *MetricLogger) Emit(metricName string, lambdaName string, val any) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	metric, ok := m.metrics[metricName]
	if !ok {
		return fmt.Errorf("[Metric] could not found metric %s", metricName)
	}
	return metric.Emit(lambdaName, val)
}

func (m *MetricLogger) Cleanup() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, metric := range m.metrics {
		metric.Cleanup()
	}
}

type LatencyMetric struct {
	data  map[string][]time.Duration
	mu    sync.Mutex
	label string
}

func (lm *LatencyMetric) Emit(lambdaName string, val any) error {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	dur, ok := val.(time.Duration)
	if !ok {
		return fmt.Errorf("wrong val type for LatencyMetric: %+v", val)
	}
	lm.data[lambdaName] = append(lm.data[lambdaName], dur)
	return nil
}

func (lm *LatencyMetric) Cleanup() {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.data = make(map[string][]time.Duration)
}

func (lm *LatencyMetric) String() string {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	// convert it to json
	entry := struct {
		Label   string               `json:"label"`
		Latency map[string][]float32 `json:"data"`
	}{
		Label:   lm.label,
		Latency: make(map[string][]float32),
	}
	for lambdaName, latencies := range lm.data {
		dur := make([]float32, 0, len(latencies))
		for _, lat := range latencies {
			dur = append(dur, float32(lat.Microseconds())/1000.0)
		}
		entry.Latency[lambdaName] = dur
	}

	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Sprintf("invalid metrics %s: %v", lm.label, err)
	}
	return string(b)
}

type FineGrainedCounterMetric struct {
	data  map[string]int
	mu    sync.Mutex
	label string
}

func (cm *FineGrainedCounterMetric) Emit(lambdaName string, val any) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delta, ok := val.(int)
	if !ok {
		return fmt.Errorf("wrong val type for FineGrainedCounterMetric: %+v", val)
	}
	cm.data[lambdaName] += delta
	return nil
}

func (cm *FineGrainedCounterMetric) Cleanup() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.data = make(map[string]int)
}

func (cm *FineGrainedCounterMetric) String() string {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	entry := struct {
		Label string         `json:"label"`
		Data  map[string]int `json:"data"`
	}{
		Label: cm.label,
		Data:  make(map[string]int),
	}
	for lambdaName, counter := range cm.data {
		entry.Data[lambdaName] = counter
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Sprintf("invalid metrics %s: %v", cm.label, err)
	}
	return string(b)
}

type CounterMetric struct {
	data  atomic.Int64
	label string
}

func (cm *CounterMetric) Emit(lambdaName string, val any) error {
	// ignore lambdaName
	switch delta := val.(type) {
	case int:
		cm.data.Add(int64(delta))
	case int32:
		cm.data.Add(int64(delta))
	case uint32:
		cm.data.Add(int64(delta))
	case int64:
		cm.data.Add(delta)
	case uint64:
		cm.data.Add(int64(delta))
	default:
		return fmt.Errorf("wrong val type for CounterMetric: %+v", val)
	}
	return nil
}

func (cm *CounterMetric) Cleanup() {
	cm.data.Store(0)
}

func (cm *CounterMetric) String() string {
	entry := struct {
		Label string `json:"label"`
		Val   int64  `json:"data"`
	}{
		Label: cm.label,
		Val:   cm.data.Load(),
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Sprintf("invalid metrics %s: %v", cm.label, err)
	}
	return string(b)
}
