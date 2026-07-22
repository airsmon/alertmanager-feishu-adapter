package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRegistryWritesFixedPrometheusMetrics(t *testing.T) {
	registry := New()
	done := registry.BeginWebhookRequest()
	registry.ObserveHTTPStatus(204)
	registry.ObserveHTTPStatus(401)
	registry.ObserveHTTPStatus(503)
	registry.ObserveDelivery(DeliverySuccess)
	registry.ObserveDelivery(DeliveryPermanentError)
	registry.ObserveDelivery(DeliveryTemporaryError)
	registry.ObserveDeliveryDuration(1500 * time.Millisecond)

	var output bytes.Buffer
	if err := registry.WritePrometheus(&output); err != nil {
		t.Fatalf("WritePrometheus() error = %v", err)
	}
	beforeDone := output.String()
	wants := []string{
		`alertmanager_feishu_adapter_http_requests_total{code="2xx"} 1`,
		`alertmanager_feishu_adapter_http_requests_total{code="4xx"} 1`,
		`alertmanager_feishu_adapter_http_requests_total{code="5xx"} 1`,
		`alertmanager_feishu_adapter_delivery_attempts_total{result="success"} 1`,
		`alertmanager_feishu_adapter_delivery_attempts_total{result="permanent_error"} 1`,
		`alertmanager_feishu_adapter_delivery_attempts_total{result="temporary_error"} 1`,
		`alertmanager_feishu_adapter_delivery_duration_seconds_sum 1.500000000`,
		`alertmanager_feishu_adapter_delivery_duration_seconds_count 1`,
		`alertmanager_feishu_adapter_in_flight_requests 1`,
	}
	for _, want := range wants {
		if !strings.Contains(beforeDone, want) {
			t.Errorf("metrics output missing %q:\n%s", want, beforeDone)
		}
	}
	if strings.Contains(beforeDone, "last_success_timestamp_seconds 0") {
		t.Fatalf("successful delivery did not update timestamp:\n%s", beforeDone)
	}

	done()
	output.Reset()
	if err := registry.WritePrometheus(&output); err != nil {
		t.Fatalf("WritePrometheus() after done error = %v", err)
	}
	if !strings.Contains(output.String(), `alertmanager_feishu_adapter_in_flight_requests 0`) {
		t.Fatalf("in-flight gauge was not decremented:\n%s", output.String())
	}
}
