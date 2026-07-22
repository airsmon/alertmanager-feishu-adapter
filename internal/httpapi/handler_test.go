package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/airsmon/alertmanager-feishu-adapter/internal/alertmanager"
	adaptermetrics "github.com/airsmon/alertmanager-feishu-adapter/internal/metrics"
)

const validPayloadJSON = `{
	"version":"4",
	"status":"firing",
	"alerts":[{"status":"firing","startsAt":"2026-07-22T01:02:03Z"}]
}`

type senderFunc func(context.Context, []byte) error

func (f senderFunc) Send(ctx context.Context, body []byte) error { return f(ctx, body) }

type classifiedError struct{ permanent bool }

func (e classifiedError) Error() string   { return "delivery failed" }
func (e classifiedError) Permanent() bool { return e.permanent }

func TestAlertmanagerWebhookSuccess(t *testing.T) {
	const wantMessage = `{"msg_type":"interactive","card":{"schema":"2.0"}}`
	var gotMessage string
	handler, err := New(
		senderFunc(func(_ context.Context, body []byte) error {
			gotMessage = string(body)
			return nil
		}),
		func(alertmanager.Payload) ([]byte, error) { return []byte(wantMessage), nil },
		"correct-token",
		adaptermetrics.New(),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/alertmanager", strings.NewReader(validPayloadJSON))
	request.Header.Set("Authorization", "Bearer correct-token")
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if gotMessage != wantMessage {
		t.Fatalf("sent message = %q, want %q", gotMessage, wantMessage)
	}
}

func TestAlertmanagerWebhookRejectsBadAuthentication(t *testing.T) {
	called := false
	handler := newTestHandler(t, senderFunc(func(context.Context, []byte) error {
		called = true
		return nil
	}), func(alertmanager.Payload) ([]byte, error) {
		called = true
		return []byte(`{}`), nil
	})

	for _, authorization := range []string{"", "Bearer wrong-token", "Basic correct-token", "Bearer correct-token extra"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/alertmanager", strings.NewReader(`{}`))
		request.Header.Set("Authorization", authorization)
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q status = %d, want 401", authorization, response.Code)
		}
		if got := response.Header().Get("WWW-Authenticate"); got == "" {
			t.Errorf("Authorization %q missing WWW-Authenticate", authorization)
		}
	}
	if called {
		t.Fatal("unauthorized request reached builder or sender")
	}
}

func TestAlertmanagerWebhookValidatesBody(t *testing.T) {
	handler := newTestHandler(t, senderFunc(func(context.Context, []byte) error {
		t.Fatal("sender called")
		return nil
	}), func(alertmanager.Payload) ([]byte, error) {
		t.Fatal("builder called")
		return nil, nil
	})

	tests := []struct {
		name   string
		body   string
		status int
	}{
		{name: "malformed", body: `{`, status: http.StatusBadRequest},
		{name: "multiple JSON values", body: `{} {}`, status: http.StatusBadRequest},
		{name: "too large", body: `{"value":"` + strings.Repeat("x", int(MaxRequestBodyBytes)) + `"}`, status: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := authorizedRequest(tt.body)
			handler.ServeHTTP(response, request)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %q", response.Code, tt.status, response.Body.String())
			}
		})
	}
}

func TestAlertmanagerWebhookRequiresJSONContentType(t *testing.T) {
	handler := newTestHandler(t, senderFunc(func(context.Context, []byte) error { return nil }), func(alertmanager.Payload) ([]byte, error) {
		return []byte(`{}`), nil
	})
	tests := []struct {
		contentType string
		wantStatus  int
	}{
		{contentType: "", wantStatus: http.StatusUnsupportedMediaType},
		{contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType},
		{contentType: "application/json", wantStatus: http.StatusNoContent},
		{contentType: "application/json; charset=utf-8", wantStatus: http.StatusNoContent},
	}
	for _, tt := range tests {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/alertmanager", strings.NewReader(validPayloadJSON))
		request.Header.Set("Authorization", "Bearer correct-token")
		if tt.contentType != "" {
			request.Header.Set("Content-Type", tt.contentType)
		}
		handler.ServeHTTP(response, request)
		if response.Code != tt.wantStatus {
			t.Errorf("Content-Type %q status = %d, want %d", tt.contentType, response.Code, tt.wantStatus)
		}
	}
}

func TestAlertmanagerWebhookRejectsInvalidV4Contract(t *testing.T) {
	handler := newTestHandler(t, senderFunc(func(context.Context, []byte) error {
		t.Fatal("sender called")
		return nil
	}), func(alertmanager.Payload) ([]byte, error) {
		t.Fatal("builder called")
		return nil, nil
	})
	tests := []string{
		`{"version":"3","status":"firing","alerts":[{"status":"firing","startsAt":"2026-07-22T01:02:03Z"}]}`,
		`{"version":"4","status":"pending","alerts":[{"status":"firing","startsAt":"2026-07-22T01:02:03Z"}]}`,
		`{"version":"4","status":"firing","alerts":[]}`,
		`{"version":"4","status":"firing","alerts":[{"status":"pending","startsAt":"2026-07-22T01:02:03Z"}]}`,
		`{"version":"4","status":"firing","alerts":[{"status":"firing"}]}`,
	}
	for _, body := range tests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authorizedRequest(body))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("body %s status = %d, want 422", body, response.Code)
		}
	}
}

func TestAlertmanagerWebhookMapsBuilderAndDeliveryErrors(t *testing.T) {
	tests := []struct {
		name        string
		buildErr    error
		deliveryErr error
		wantStatus  int
		wantRetry   bool
	}{
		{name: "builder", buildErr: errors.New("bad alert"), wantStatus: http.StatusUnprocessableEntity},
		{name: "permanent delivery", deliveryErr: classifiedError{permanent: true}, wantStatus: http.StatusUnprocessableEntity},
		{name: "temporary delivery", deliveryErr: classifiedError{}, wantStatus: http.StatusServiceUnavailable, wantRetry: true},
		{name: "unknown delivery", deliveryErr: errors.New("network"), wantStatus: http.StatusServiceUnavailable, wantRetry: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := newTestHandler(t, senderFunc(func(context.Context, []byte) error {
				return tt.deliveryErr
			}), func(alertmanager.Payload) ([]byte, error) {
				return []byte(`{}`), tt.buildErr
			})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, authorizedRequest(validPayloadJSON))
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
			if got := response.Header().Get("Retry-After"); (got != "") != tt.wantRetry {
				t.Fatalf("Retry-After = %q, wantRetry %v", got, tt.wantRetry)
			}
		})
	}
}

func TestOperationalEndpointsAndMethods(t *testing.T) {
	handler := newTestHandler(t, senderFunc(func(context.Context, []byte) error { return nil }), func(alertmanager.Payload) ([]byte, error) {
		return []byte(`{}`), nil
	})

	assertStatus(t, handler, http.MethodGet, "/healthz", http.StatusOK)
	assertStatus(t, handler, http.MethodGet, "/readyz", http.StatusServiceUnavailable)
	handler.SetReady(true)
	assertStatus(t, handler, http.MethodGet, "/readyz", http.StatusOK)
	assertStatus(t, handler, http.MethodGet, "/metrics", http.StatusOK)
	assertStatus(t, handler, http.MethodGet, "/missing", http.StatusNotFound)
	assertStatus(t, handler, http.MethodGet, "/api/v1/alertmanager", http.StatusMethodNotAllowed)
	assertStatus(t, handler, http.MethodPost, "/healthz", http.StatusMethodNotAllowed)
}

func TestMetricsReflectWebhookResult(t *testing.T) {
	registry := adaptermetrics.New()
	handler, err := New(
		senderFunc(func(context.Context, []byte) error { return nil }),
		func(alertmanager.Payload) ([]byte, error) { return []byte(`{}`), nil },
		"correct-token",
		registry,
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	handler.ServeHTTP(httptest.NewRecorder(), authorizedRequest(validPayloadJSON))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(response.Result().Body)
	for _, want := range []string{
		`alertmanager_feishu_adapter_http_requests_total{code="2xx"} 1`,
		`alertmanager_feishu_adapter_delivery_attempts_total{result="success"} 1`,
		`alertmanager_feishu_adapter_delivery_duration_seconds_count 1`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
}

func newTestHandler(t *testing.T, sender Sender, builder BuildFunc) *Handler {
	t.Helper()
	handler, err := New(sender, builder, "correct-token", adaptermetrics.New())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}

func authorizedRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/alertmanager", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer correct-token")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func assertStatus(t *testing.T, handler http.Handler, method, path string, want int) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
	if response.Code != want {
		t.Fatalf("%s %s status = %d, want %d", method, path, response.Code, want)
	}
}
