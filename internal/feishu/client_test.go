package feishu

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestClientSendRequiresBusinessSuccess(t *testing.T) {
	tests := []struct {
		name          string
		response      string
		wantErr       bool
		wantPermanent bool
	}{
		{name: "current success", response: `{"code":0,"msg":"success"}`},
		{name: "legacy success", response: `{"StatusCode":0,"StatusMessage":"success"}`},
		{name: "missing code", response: `{"msg":"success"}`, wantErr: true},
		{name: "malformed card", response: `{"code":11246,"data":{"err_code":200621}}`, wantErr: true, wantPermanent: true},
		{name: "unknown business failure", response: `{"code":99999}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, tt.response), nil
			})}
			client, err := NewClient("https://example.com/webhook", httpClient)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			err = client.Send(context.Background(), []byte(`{"msg_type":"interactive"}`))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Send() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && IsPermanent(err) != tt.wantPermanent {
				t.Fatalf("IsPermanent(%v) = %v, want %v", err, IsPermanent(err), tt.wantPermanent)
			}
		})
	}
}

func TestClientSendClassifiesHTTPStatus(t *testing.T) {
	tests := []struct {
		status        int
		wantPermanent bool
	}{
		{status: http.StatusBadRequest, wantPermanent: true},
		{status: http.StatusUnauthorized, wantPermanent: true},
		{status: http.StatusRequestTimeout, wantPermanent: false},
		{status: http.StatusTooManyRequests, wantPermanent: false},
		{status: http.StatusInternalServerError, wantPermanent: false},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(tt.status, `{}`), nil
			})}
			client, err := NewClient("https://example.com/webhook", httpClient)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			err = client.Send(context.Background(), []byte(`{}`))
			if err == nil {
				t.Fatal("Send() error = nil")
			}
			if IsPermanent(err) != tt.wantPermanent {
				t.Fatalf("IsPermanent(%v) = %v, want %v", err, IsPermanent(err), tt.wantPermanent)
			}
		})
	}
}

func TestClientSendPostsExactBodyAndDoesNotLeakWebhook(t *testing.T) {
	const body = `{"schema":"2.0"}`
	var gotBody string
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q", got)
		}
		contents, _ := io.ReadAll(request.Body)
		gotBody = string(contents)
		return jsonResponse(http.StatusOK, `{"code":0}`), nil
	})}

	webhookURL := "https://example.com/private-secret"
	client, err := NewClient(webhookURL, httpClient)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.Send(context.Background(), []byte(body)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotBody != body {
		t.Fatalf("body = %q, want %q", gotBody, body)
	}

	httpClient.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection failed")
	})
	// NewClient copied the http.Client, but both clients intentionally share the
	// transport value assigned above only at construction time. Construct a
	// failing client for the error-message assertion.
	client, err = NewClient(webhookURL, httpClient)
	if err != nil {
		t.Fatalf("NewClient() for failure error = %v", err)
	}
	err = client.Send(context.Background(), []byte(body))
	if err == nil {
		t.Fatal("Send() after close error = nil")
	}
	if strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("error leaked webhook URL: %v", err)
	}
}

func TestNewClientRejectsInvalidURL(t *testing.T) {
	_, err := NewClient("not-a-url", nil)
	if err == nil {
		t.Fatal("NewClient() error = nil")
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
