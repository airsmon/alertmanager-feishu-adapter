package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/airsmon/alertmanager-feishu-adapter/internal/alertmanager"
	"github.com/airsmon/alertmanager-feishu-adapter/internal/card"
	"github.com/airsmon/alertmanager-feishu-adapter/internal/config"
	"github.com/airsmon/alertmanager-feishu-adapter/internal/feishu"
	"github.com/airsmon/alertmanager-feishu-adapter/internal/httpapi"
	adaptermetrics "github.com/airsmon/alertmanager-feishu-adapter/internal/metrics"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration error", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("adapter stopped with error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	downstreamHTTPClient := &http.Client{Timeout: cfg.RequestTimeout}
	feishuClient, err := feishu.NewClient(cfg.WebhookURL, downstreamHTTPClient)
	if err != nil {
		return fmt.Errorf("initialize Feishu client: %w", err)
	}

	registry := adaptermetrics.New()
	build := func(payload alertmanager.Payload) ([]byte, error) {
		message := card.Build(payload, card.Options{})
		return json.Marshal(message)
	}
	handler, err := httpapi.New(feishuClient, build, cfg.AuthToken, registry)
	if err != nil {
		return fmt.Errorf("initialize HTTP API: %w", err)
	}
	handler.SetLogger(logger)

	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on configured address: %w", err)
	}
	defer listener.Close()

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      9 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	handler.SetReady(true)
	logger.Info(
		"adapter started",
		"listen_address", cfg.ListenAddress,
		"feishu_request_timeout", cfg.RequestTimeout.String(),
		"shutdown_timeout", cfg.ShutdownTimeout.String(),
	)
	serveError := make(chan error, 1)
	go func() {
		serveError <- server.Serve(listener)
	}()

	select {
	case err := <-serveError:
		handler.SetReady(false)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		handler.SetReady(false)
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			_ = server.Close()
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		if err := <-serveError; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP during shutdown: %w", err)
		}
		logger.Info("adapter stopped gracefully")
		return nil
	}
}
