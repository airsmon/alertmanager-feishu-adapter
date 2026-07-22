package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	DefaultListenAddress   = ":8080"
	DefaultWebhookFile     = "/var/run/secrets/feishu/webhook-url"
	DefaultAuthTokenFile   = "/var/run/secrets/adapter-auth/token"
	MaxRequestBodyBytes    = int64(1 << 20)
	defaultRequestTimeout  = 8 * time.Second
	defaultShutdownTimeout = 10 * time.Second
)

// Config contains the adapter's runtime settings. WebhookURL and AuthToken are
// secrets: callers must never log Config with a formatting verb that exposes
// field values.
type Config struct {
	ListenAddress   string
	WebhookURL      string
	AuthToken       string
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
}

// Load reads configuration from environment variables and mounted secret
// files. Secret values are intentionally not supported directly in environment
// variables, keeping them out of process listings and pod specifications.
func Load() (Config, error) {
	return load(os.Getenv, os.ReadFile)
}

type getenvFunc func(string) string
type readFileFunc func(string) ([]byte, error)

func load(getenv getenvFunc, readFile readFileFunc) (Config, error) {
	listenAddress := valueOrDefault(getenv("LISTEN_ADDRESS"), DefaultListenAddress)
	webhookFile := valueOrDefault(getenv("FEISHU_WEBHOOK_FILE"), DefaultWebhookFile)
	authTokenFile := valueOrDefault(getenv("ADAPTER_AUTH_TOKEN_FILE"), DefaultAuthTokenFile)

	requestTimeout, err := parseDuration(
		"FEISHU_REQUEST_TIMEOUT",
		getenv("FEISHU_REQUEST_TIMEOUT"),
		defaultRequestTimeout,
	)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := parseDuration(
		"SHUTDOWN_TIMEOUT",
		getenv("SHUTDOWN_TIMEOUT"),
		defaultShutdownTimeout,
	)
	if err != nil {
		return Config{}, err
	}

	webhookURL, err := readSecret(readFile, webhookFile, "Feishu webhook")
	if err != nil {
		return Config{}, err
	}
	authToken, err := readSecret(readFile, authTokenFile, "adapter authentication token")
	if err != nil {
		return Config{}, err
	}

	parsedWebhookURL, err := url.Parse(webhookURL)
	if err != nil || parsedWebhookURL.Scheme != "https" || parsedWebhookURL.User != nil ||
		!strings.EqualFold(parsedWebhookURL.Hostname(), "open.feishu.cn") || parsedWebhookURL.Port() != "" {
		return Config{}, errors.New("Feishu webhook file must contain an HTTPS URL for open.feishu.cn")
	}

	return Config{
		ListenAddress:   listenAddress,
		WebhookURL:      webhookURL,
		AuthToken:       authToken,
		RequestTimeout:  requestTimeout,
		ShutdownTimeout: shutdownTimeout,
	}, nil
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func parseDuration(name, value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return duration, nil
}

func readSecret(readFile readFileFunc, path, description string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s file path is empty", description)
	}
	contents, err := readFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s file: %w", description, err)
	}
	value := strings.TrimSpace(string(contents))
	if value == "" {
		return "", fmt.Errorf("%s file is empty", description)
	}
	return value, nil
}
