package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsAndTrimsSecrets(t *testing.T) {
	files := map[string]string{
		DefaultWebhookFile:   " https://open.feishu.cn/open-apis/bot/v2/hook/test \n",
		DefaultAuthTokenFile: " test-token \n",
	}

	got, err := load(func(string) string { return "" }, func(path string) ([]byte, error) {
		value, ok := files[path]
		if !ok {
			return nil, errors.New("unexpected path")
		}
		return []byte(value), nil
	})
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if got.ListenAddress != DefaultListenAddress {
		t.Fatalf("ListenAddress = %q, want %q", got.ListenAddress, DefaultListenAddress)
	}
	if got.WebhookURL != "https://open.feishu.cn/open-apis/bot/v2/hook/test" {
		t.Fatalf("WebhookURL was not trimmed")
	}
	if got.AuthToken != "test-token" {
		t.Fatalf("AuthToken was not trimmed")
	}
	if got.RequestTimeout != defaultRequestTimeout || got.ShutdownTimeout != defaultShutdownTimeout {
		t.Fatalf("unexpected default timeouts: %v, %v", got.RequestTimeout, got.ShutdownTimeout)
	}
}

func TestLoadOverridesNonSecretSettings(t *testing.T) {
	values := map[string]string{
		"LISTEN_ADDRESS":          "127.0.0.1:9090",
		"FEISHU_WEBHOOK_FILE":     "/webhook",
		"ADAPTER_AUTH_TOKEN_FILE": "/token",
		"FEISHU_REQUEST_TIMEOUT":  "3s",
		"SHUTDOWN_TIMEOUT":        "4s",
	}
	files := map[string]string{
		"/webhook": "https://open.feishu.cn/open-apis/bot/v2/hook/test",
		"/token":   "token",
	}

	got, err := load(func(name string) string { return values[name] }, func(path string) ([]byte, error) {
		return []byte(files[path]), nil
	})
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if got.ListenAddress != "127.0.0.1:9090" || got.RequestTimeout != 3*time.Second || got.ShutdownTimeout != 4*time.Second {
		t.Fatalf("unexpected overrides: %+v", got)
	}
}

func TestLoadRejectsInvalidConfigurationWithoutLeakingSecret(t *testing.T) {
	const secret = "super-secret-value"
	tests := []struct {
		name    string
		values  map[string]string
		files   map[string]string
		wantErr string
	}{
		{
			name:    "invalid timeout",
			values:  map[string]string{"FEISHU_REQUEST_TIMEOUT": "never"},
			files:   map[string]string{DefaultWebhookFile: "https://open.feishu.cn/hook", DefaultAuthTokenFile: secret},
			wantErr: "FEISHU_REQUEST_TIMEOUT",
		},
		{
			name:    "non HTTPS webhook",
			files:   map[string]string{DefaultWebhookFile: "http://example.com/" + secret, DefaultAuthTokenFile: secret},
			wantErr: "open.feishu.cn",
		},
		{
			name:    "unexpected webhook host",
			files:   map[string]string{DefaultWebhookFile: "https://example.com/" + secret, DefaultAuthTokenFile: secret},
			wantErr: "open.feishu.cn",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(func(name string) string { return tt.values[name] }, func(path string) ([]byte, error) {
				return []byte(tt.files[path]), nil
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("load() error = %v, want containing %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("load() leaked secret in error: %v", err)
			}
		})
	}
}
