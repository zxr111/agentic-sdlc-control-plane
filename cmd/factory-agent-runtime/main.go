package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/agentruntime"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	runtime, err := agentruntime.New(env("OPENAI_API_URL", "https://api.openai.com/v1"), os.Getenv("OPENAI_API_KEY"),
		os.Getenv("AGENT_RUNTIME_SHARED_SECRET"), logger)
	if err != nil {
		logger.Error("Agent Runtime configuration invalid", "error", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: env("HTTP_ADDR", ":8090"), Handler: runtime.Routes(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 6 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		logger.Info("Agent Runtime listening", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("Agent Runtime stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}()
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	<-stop.Done()
	ctx, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	_ = agentruntime.Shutdown(ctx, server)
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
