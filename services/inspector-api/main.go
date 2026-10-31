package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg := loadConfig()
	s := NewServer(cfg)
	// Tracing is off unless OTEL_EXPORTER_OTLP_ENDPOINT points at an OpenTelemetry Collector.
	s.tracer = newTracerFromEnv(cfg.Version)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           s.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("inspector-api starting", "port", cfg.Port, "version", cfg.Version, "startupDelay", cfg.StartupDelay.String())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	<-ctx.Done()

	// Fail readiness first so endpoints controllers stop routing traffic before we close.
	slog.Info("SIGTERM received, draining", "delay", cfg.ShutdownDelay.String())
	s.ready.Store(false)
	time.Sleep(cfg.ShutdownDelay)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	}
	s.tracer.Shutdown(shutdownCtx)
	slog.Info("shutdown complete")
}
