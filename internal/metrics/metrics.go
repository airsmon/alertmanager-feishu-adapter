package metrics

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

type DeliveryResult uint8

const (
	DeliverySuccess DeliveryResult = iota
	DeliveryPermanentError
	DeliveryTemporaryError
)

// Registry is a small, dependency-free Prometheus text collector. All fields
// have a fixed label set so untrusted alert data can never create high-cardinality
// time series.
type Registry struct {
	http2xx atomic.Uint64
	http4xx atomic.Uint64
	http5xx atomic.Uint64

	deliverySuccess        atomic.Uint64
	deliveryPermanentError atomic.Uint64
	deliveryTemporaryError atomic.Uint64
	deliveryDurationNanos  atomic.Uint64
	deliveryDurationCount  atomic.Uint64

	inFlight        atomic.Int64
	lastSuccessUnix atomic.Int64
}

func New() *Registry { return &Registry{} }

// BeginWebhookRequest increments the in-flight gauge and returns a function
// that must be deferred by the caller.
func (r *Registry) BeginWebhookRequest() func() {
	r.inFlight.Add(1)
	return func() { r.inFlight.Add(-1) }
}

func (r *Registry) ObserveHTTPStatus(status int) {
	switch {
	case status >= 200 && status < 300:
		r.http2xx.Add(1)
	case status >= 400 && status < 500:
		r.http4xx.Add(1)
	case status >= 500:
		r.http5xx.Add(1)
	}
}

func (r *Registry) ObserveDelivery(result DeliveryResult) {
	switch result {
	case DeliverySuccess:
		r.deliverySuccess.Add(1)
		r.lastSuccessUnix.Store(time.Now().Unix())
	case DeliveryPermanentError:
		r.deliveryPermanentError.Add(1)
	case DeliveryTemporaryError:
		r.deliveryTemporaryError.Add(1)
	}
}

func (r *Registry) ObserveDeliveryDuration(duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	r.deliveryDurationNanos.Add(uint64(duration))
	r.deliveryDurationCount.Add(1)
}

// WritePrometheus writes a complete Prometheus text-format response.
func (r *Registry) WritePrometheus(writer io.Writer) error {
	lines := []struct {
		format string
		value  any
	}{
		{"# HELP alertmanager_feishu_adapter_http_requests_total Webhook requests by response class.\n", nil},
		{"# TYPE alertmanager_feishu_adapter_http_requests_total counter\n", nil},
		{"alertmanager_feishu_adapter_http_requests_total{code=\"2xx\"} %d\n", r.http2xx.Load()},
		{"alertmanager_feishu_adapter_http_requests_total{code=\"4xx\"} %d\n", r.http4xx.Load()},
		{"alertmanager_feishu_adapter_http_requests_total{code=\"5xx\"} %d\n", r.http5xx.Load()},
		{"# HELP alertmanager_feishu_adapter_delivery_attempts_total Feishu delivery attempts by result.\n", nil},
		{"# TYPE alertmanager_feishu_adapter_delivery_attempts_total counter\n", nil},
		{"alertmanager_feishu_adapter_delivery_attempts_total{result=\"success\"} %d\n", r.deliverySuccess.Load()},
		{"alertmanager_feishu_adapter_delivery_attempts_total{result=\"permanent_error\"} %d\n", r.deliveryPermanentError.Load()},
		{"alertmanager_feishu_adapter_delivery_attempts_total{result=\"temporary_error\"} %d\n", r.deliveryTemporaryError.Load()},
		{"# HELP alertmanager_feishu_adapter_delivery_duration_seconds Time spent performing downstream Feishu delivery attempts.\n", nil},
		{"# TYPE alertmanager_feishu_adapter_delivery_duration_seconds summary\n", nil},
		{"alertmanager_feishu_adapter_delivery_duration_seconds_sum %.9f\n", float64(r.deliveryDurationNanos.Load()) / float64(time.Second)},
		{"alertmanager_feishu_adapter_delivery_duration_seconds_count %d\n", r.deliveryDurationCount.Load()},
		{"# HELP alertmanager_feishu_adapter_in_flight_requests Current webhook requests being processed.\n", nil},
		{"# TYPE alertmanager_feishu_adapter_in_flight_requests gauge\n", nil},
		{"alertmanager_feishu_adapter_in_flight_requests %d\n", r.inFlight.Load()},
		{"# HELP alertmanager_feishu_adapter_last_success_timestamp_seconds Unix timestamp of the last successful Feishu delivery.\n", nil},
		{"# TYPE alertmanager_feishu_adapter_last_success_timestamp_seconds gauge\n", nil},
		{"alertmanager_feishu_adapter_last_success_timestamp_seconds %d\n", r.lastSuccessUnix.Load()},
	}

	for _, line := range lines {
		var err error
		if line.value == nil {
			_, err = io.WriteString(writer, line.format)
		} else {
			_, err = fmt.Fprintf(writer, line.format, line.value)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
