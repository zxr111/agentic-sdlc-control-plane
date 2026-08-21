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

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/config"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/dashboard"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/hello"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration invalid", "error", err)
		os.Exit(1)
	}
	repository, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		logger.Error("database open failed", "error", err)
		os.Exit(1)
	}
	defer repository.Close()

	mux := http.NewServeMux()
	mux.Handle("/", webhook.NewWithCallbackSecret(
		cfg.GitLabWebhookSecret, cfg.CallbackSharedSecret, cfg.Projects, repository, logger,
	).Routes())
	dashboard.New(repository, cfg.Projects, cfg.GitLabAPIURL, logger).Register(mux)
	hello.Register(mux)
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		logger.Info("factory API listening", "address", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("factory API stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}()
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	<-stop.Done()
	ctx, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("factory API shutdown failed", "error", err)
	}
}
