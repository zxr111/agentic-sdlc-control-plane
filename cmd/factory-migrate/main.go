package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		logger.Error("MYSQL_DSN is required")
		os.Exit(1)
	}
	repository, err := store.Open(dsn)
	if err != nil {
		logger.Error("database open failed", "error", err)
		os.Exit(1)
	}
	defer repository.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := repository.Migrate(ctx); err != nil {
		logger.Error("migration failed", "error", err)
		os.Exit(1)
	}
	logger.Info("database migrations applied")
}
