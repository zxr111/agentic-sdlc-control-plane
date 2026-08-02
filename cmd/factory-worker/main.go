package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/agents"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/config"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/connectors/confluence"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/connectors/delivery"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/connectors/gitlab"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/engine"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
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
	runner := engine.New(
		repository,
		gitlab.New(cfg.GitLabAPIURL, cfg.GitLabToken),
		confluence.New(cfg.ConfluenceBaseURL, cfg.ConfluenceEmail, cfg.ConfluenceToken),
		agents.New(cfg.OpenAIAPIURL, cfg.OpenAIAPIKey, cfg.OpenAIModel),
		cfg.Projects,
		logger,
	)
	if cfg.DeliveryTriggerURL != "" {
		runner.SetDeliveryClient(delivery.New(cfg.DeliveryTriggerURL, cfg.DeliveryTriggerToken))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	logger.Info("factory worker started", "worker_id", cfg.WorkerID)
	run(ctx, cfg, repository, runner, logger)
}

func run(ctx context.Context, cfg config.Config, repository *store.Store, runner *engine.Engine, logger *slog.Logger) {
	reconcile := time.NewTicker(cfg.ReconcileInterval)
	defer reconcile.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcile.C:
			if err := repository.EnqueueEvent(ctx, "reconcile:"+time.Now().UTC().Format("200601021504"),
				"system.reconcile", map[string]any{"scheduled_at": time.Now().UTC()}, time.Now().UTC()); err != nil {
				logger.Error("could not schedule reconciliation", "error", err)
			}
		default:
		}

		message, err := repository.ClaimOutbox(ctx, cfg.WorkerID, cfg.LeaseDuration)
		if err == nil {
			externalID, deliveryErr := runner.DeliverOutbox(ctx, *message)
			if deliveryErr != nil {
				logger.Warn("outbox delivery failed", "message_id", message.ID, "type", message.Type,
					"attempts", message.Attempts, "error", deliveryErr)
				_ = repository.RetryOutbox(ctx, message.ID, message.Attempts, cfg.MaxAttempts, deliveryErr)
			} else {
				_ = repository.CompleteOutbox(ctx, message.ID, externalID)
			}
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			logger.Error("outbox claim failed", "error", err)
			wait(ctx, cfg.WorkerPollInterval)
			continue
		}

		event, err := repository.ClaimEvent(ctx, cfg.WorkerID, cfg.LeaseDuration)
		if err == nil {
			handleErr := runner.HandleEvent(ctx, *event)
			if handleErr != nil {
				logger.Warn("event handling failed", "event_id", event.ID, "type", event.Type,
					"attempts", event.Attempts, "error", handleErr)
				_ = repository.RetryEvent(ctx, event.ID, event.Attempts, cfg.MaxAttempts, handleErr)
			} else {
				_ = repository.CompleteEvent(ctx, event.ID)
			}
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			logger.Error("event claim failed", "error", err)
		}
		wait(ctx, cfg.WorkerPollInterval)
	}
}

func wait(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
