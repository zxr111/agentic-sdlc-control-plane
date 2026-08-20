package main

import (
	"context"
	"encoding/json"
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
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/routing"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/tooling"
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
	if cfg.V3.Registry {
		definitions := agents.BuiltinDefinitions()
		seeds := make([]store.RegistryDefinition, 0, len(definitions))
		for _, definition := range definitions {
			seeds = append(seeds, store.RegistryDefinition{
				AgentType: definition.AgentType, PromptKey: definition.PromptKey,
				DisplayName: definition.DisplayName, Instructions: definition.Instructions,
				OutputSchema: definition.OutputSchema,
			})
		}
		for _, model := range cfg.ModelCatalog {
			if err := repository.BootstrapRegistry(context.Background(), model.Key, seeds); err != nil {
				logger.Error("V3 registry bootstrap failed", "model", model.Key, "error", err)
				os.Exit(1)
			}
		}
		toolSeeds := make([]store.ToolSeed, 0, len(tooling.BuiltinTools()))
		for _, tool := range tooling.BuiltinTools() {
			toolSeeds = append(toolSeeds, store.ToolSeed{Key: tool.Key, DisplayName: tool.DisplayName,
				Description: tool.Description, RiskLevel: tool.RiskLevel, AdapterType: tool.AdapterType,
				DefaultDecision: tool.DefaultDecision, RequiresGate: tool.RequiresGate,
				InputSchema: json.RawMessage(tool.InputSchema), OutputSchema: json.RawMessage(tool.OutputSchema)})
		}
		skillSeeds := make([]store.SkillSeed, 0, len(agents.BuiltinSkills()))
		for _, skill := range agents.BuiltinSkills() {
			skillSeeds = append(skillSeeds, store.SkillSeed{Key: skill.Key, DisplayName: skill.DisplayName,
				Description: skill.Description, Instructions: skill.Instructions,
				TriggerRules: map[string]any{"agent_types": skill.AgentTypes}, Scope: map[string]any{"project_allowlist_required": true}})
		}
		if err := repository.BootstrapGovernance(context.Background(), toolSeeds, skillSeeds); err != nil {
			logger.Error("V3 tool and skill registry bootstrap failed", "error", err)
			os.Exit(1)
		}
	}
	agentClient := agents.New(cfg.OpenAIAPIURL, cfg.OpenAIAPIKey, cfg.OpenAIModel)
	if cfg.V3.ModelRouter {
		models := make([]routing.Model, 0, len(cfg.ModelCatalog))
		for _, model := range cfg.ModelCatalog {
			models = append(models, routing.Model{ID: model.Key, Key: model.Key, Healthy: model.Healthy,
				Active: model.Active, Quality: model.Quality, InputCost: model.InputCost, OutputCost: model.OutputCost, Capabilities: model.Capabilities})
		}
		agentClient.ConfigureRouting(models, cfg.ModelFallbackEnabled, cfg.ModelBudgetMicrounits)
	}
	runner := engine.New(
		repository,
		gitlab.New(cfg.GitLabAPIURL, cfg.GitLabToken),
		confluence.New(cfg.ConfluenceBaseURL, cfg.ConfluenceEmail, cfg.ConfluenceToken),
		agentClient,
		cfg.Projects,
		logger,
	)
	runner.SetV3Features(cfg.V3)
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
