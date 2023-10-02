package metrics

import (
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
	Emit(lambdaName string, val any) error
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
	str := fmt.Sprintf("[MetricLogger]:\n")
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

func (lm *LatencyMetric) String() string {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	str := fmt.Sprintf("latency metric %s: ", lm.label)
	for lambdaName, latencies := range lm.data {
		str += fmt.Sprintf("%s -> %+v ", lambdaName, latencies)
	}
	return str
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

func (cm *FineGrainedCounterMetric) String() string {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	str := fmt.Sprintf("find grained counter metric %s: ", cm.label)
	for lambdaName, counter := range cm.data {
		str += fmt.Sprintf("%s -> %+v ", lambdaName, counter)
	}
	return str
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

func (cm *CounterMetric) String() string {
	return fmt.Sprintf("single counter metric %s: %d", cm.label, cm.data.Load())
}
