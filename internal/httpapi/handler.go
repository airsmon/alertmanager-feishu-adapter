package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/airsmon/alertmanager-feishu-adapter/internal/alertmanager"
	"github.com/airsmon/alertmanager-feishu-adapter/internal/config"
	adaptermetrics "github.com/airsmon/alertmanager-feishu-adapter/internal/metrics"
)

const MaxRequestBodyBytes = config.MaxRequestBodyBytes

// Sender performs exactly one downstream delivery attempt.
type Sender interface {
	Send(context.Context, []byte) error
}

// BuildFunc validates one Alertmanager webhook payload and returns a complete,
// marshaled Feishu custom-bot request body.
type BuildFunc func(alertmanager.Payload) ([]byte, error)

type Handler struct {
	sender            Sender
	build             BuildFunc
	expectedTokenHash [sha256.Size]byte
	metrics           *adaptermetrics.Registry
	logger            *slog.Logger
	ready             atomic.Bool
}

func New(sender Sender, build BuildFunc, authToken string, registry *adaptermetrics.Registry) (*Handler, error) {
	if sender == nil {
		return nil, errors.New("sender is required")
	}
	if build == nil {
		return nil, errors.New("card builder is required")
	}
	if authToken == "" {
		return nil, errors.New("authentication token is empty")
	}
	if registry == nil {
		registry = adaptermetrics.New()
	}
	return &Handler{
		sender:            sender,
		build:             build,
		expectedTokenHash: sha256.Sum256([]byte(authToken)),
		metrics:           registry,
	}, nil
}

func (h *Handler) SetReady(ready bool) { h.ready.Store(ready) }

// SetLogger enables structured delivery logs. It must be called before the
// server starts accepting requests.
func (h *Handler) SetLogger(logger *slog.Logger) { h.logger = logger }

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/api/v1/alertmanager":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, http.MethodPost)
			return
		}
		h.handleAlertmanager(writer, request)
	case "/healthz":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		writePlain(writer, http.StatusOK, "ok\n")
	case "/readyz":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		if !h.ready.Load() {
			writePlain(writer, http.StatusServiceUnavailable, "not ready\n")
			return
		}
		writePlain(writer, http.StatusOK, "ready\n")
	case "/metrics":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_ = h.metrics.WritePrometheus(writer)
	default:
		writePlain(writer, http.StatusNotFound, "not found\n")
	}
}

func (h *Handler) handleAlertmanager(writer http.ResponseWriter, request *http.Request) {
	done := h.metrics.BeginWebhookRequest()
	defer done()

	if !h.authorized(request.Header.Get("Authorization")) {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="alertmanager-feishu-adapter"`)
		h.respond(writer, http.StatusUnauthorized, "unauthorized\n")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		h.respond(writer, http.StatusUnsupportedMediaType, "Content-Type must be application/json\n")
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, MaxRequestBodyBytes)
	decoder := json.NewDecoder(request.Body)
	var payload alertmanager.Payload
	if err := decoder.Decode(&payload); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			h.respond(writer, http.StatusRequestEntityTooLarge, "request body too large\n")
			return
		}
		h.respond(writer, http.StatusBadRequest, "invalid JSON payload\n")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			h.respond(writer, http.StatusRequestEntityTooLarge, "request body too large\n")
			return
		}
		h.respond(writer, http.StatusBadRequest, "request body must contain one JSON value\n")
		return
	}

	if err := payload.Validate(); err != nil {
		h.logDelivery(payload, "invalid_payload", 0, err)
		h.respond(writer, http.StatusUnprocessableEntity, "unsupported Alertmanager payload\n")
		return
	}
	message, err := h.build(payload)
	if err != nil {
		h.logDelivery(payload, "build_error", 0, err)
		h.respond(writer, http.StatusUnprocessableEntity, "unsupported alert payload\n")
		return
	}
	deliveryStarted := time.Now()
	err = h.sender.Send(request.Context(), message)
	h.metrics.ObserveDeliveryDuration(time.Since(deliveryStarted))
	if err != nil {
		if isPermanent(err) {
			h.metrics.ObserveDelivery(adaptermetrics.DeliveryPermanentError)
			h.logDelivery(payload, "permanent_error", time.Since(deliveryStarted), err)
			h.respond(writer, http.StatusUnprocessableEntity, "Feishu permanently rejected message\n")
			return
		}
		h.metrics.ObserveDelivery(adaptermetrics.DeliveryTemporaryError)
		h.logDelivery(payload, "temporary_error", time.Since(deliveryStarted), err)
		writer.Header().Set("Retry-After", "5")
		h.respond(writer, http.StatusServiceUnavailable, "Feishu delivery temporarily unavailable\n")
		return
	}

	h.metrics.ObserveDelivery(adaptermetrics.DeliverySuccess)
	h.logDelivery(payload, "success", time.Since(deliveryStarted), nil)
	h.respond(writer, http.StatusNoContent, "")
}

func (h *Handler) logDelivery(payload alertmanager.Payload, result string, duration time.Duration, err error) {
	if h.logger == nil {
		return
	}
	attributes := []any{
		"result", result,
		"group_key_hash", shortHash(payload.GroupKey),
		"status", payload.Status,
		"alert_count", len(payload.Alerts),
		"severity", payload.CommonLabels.Get("severity"),
		"duration_ms", duration.Milliseconds(),
	}
	if err != nil {
		attributes = append(attributes, "error", err)
		h.logger.Warn("alert delivery completed", attributes...)
		return
	}
	h.logger.Info("alert delivery completed", attributes...)
}

func shortHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:4])
}

func (h *Handler) authorized(header string) bool {
	parts := strings.SplitN(header, " ", 2)
	validFormat := len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] != "" && !strings.ContainsAny(parts[1], " \t")
	token := ""
	if len(parts) == 2 {
		token = parts[1]
	}
	candidateHash := sha256.Sum256([]byte(token))
	equal := subtle.ConstantTimeCompare(candidateHash[:], h.expectedTokenHash[:])
	return validFormat && equal == 1
}

func (h *Handler) respond(writer http.ResponseWriter, status int, body string) {
	h.metrics.ObserveHTTPStatus(status)
	writePlain(writer, status, body)
}

type permanentError interface {
	Permanent() bool
}

func isPermanent(err error) bool {
	var classified permanentError
	return errors.As(err, &classified) && classified.Permanent()
}

func methodNotAllowed(writer http.ResponseWriter, allowed string) {
	writer.Header().Set("Allow", allowed)
	writePlain(writer, http.StatusMethodNotAllowed, "method not allowed\n")
}

func writePlain(writer http.ResponseWriter, status int, body string) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(status)
	if body != "" {
		_, _ = io.WriteString(writer, body)
	}
}
