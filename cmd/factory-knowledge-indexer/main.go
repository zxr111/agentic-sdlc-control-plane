package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/knowledge"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	repository, err := store.Open(databaseURL)
	if err != nil {
		logger.Error("database open failed", "error", err)
		os.Exit(1)
	}
	defer repository.Close()
	if embeddingURL := os.Getenv("RAG_EMBEDDING_URL"); embeddingURL != "" {
		embeddingModel := os.Getenv("RAG_EMBEDDING_MODEL")
		if embeddingModel == "" {
			logger.Error("RAG_EMBEDDING_MODEL is required when RAG_EMBEDDING_URL is configured")
			os.Exit(1)
		}
		repository.SetKnowledgeProviders(knowledge.HTTPEmbedder{BaseURL: embeddingURL,
			Token: os.Getenv("RAG_EMBEDDING_TOKEN"), Model: embeddingModel,
			Client: &http.Client{Timeout: 60 * time.Second}}, nil)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	interval := 30 * time.Second
	if raw := os.Getenv("KNOWLEDGE_INDEX_INTERVAL"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed >= time.Second {
			interval = parsed
		}
	}
	logger.Info("knowledge indexer started", "interval", interval.String())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := indexBatch(ctx, repository, logger); err != nil {
			logger.Error("knowledge indexing batch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func indexBatch(ctx context.Context, repository *store.Store, logger *slog.Logger) error {
	sources, err := repository.PendingKnowledgeSources(ctx, 100)
	if err != nil {
		return err
	}
	for _, source := range sources {
		_, created, err := repository.IngestKnowledge(ctx, source)
		if err != nil {
			return err
		}
		if created {
			logger.Info("knowledge source indexed", "source_type", source.SourceType, "source_key", source.SourceKey, "source_version", source.SourceVersion, "project_id", strconv.FormatInt(source.ProjectID, 10))
		}
	}
	backfilled, err := repository.BackfillKnowledgeEmbeddings(ctx, 100)
	if err != nil {
		return err
	}
	if backfilled > 0 {
		logger.Info("knowledge embeddings backfilled", "chunks", backfilled)
	}
	return nil
}
