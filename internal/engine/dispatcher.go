package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
)

type GeneratePlansEvent struct {
	WorkflowID string `json:"workflow_id"`
	GateID     string `json:"gate_id"`
}

type AnalyzeRequirementEvent struct {
	WorkflowID string `json:"workflow_id"`
	Feedback   string `json:"feedback,omitempty"`
}

type GenerateArchitectureEvent struct {
	WorkflowID string `json:"workflow_id"`
	Feedback   string `json:"feedback,omitempty"`
}

type EvaluationRunEvent struct {
	SuiteID         string `json:"suite_id"`
	PromptVersionID string `json:"prompt_version_id"`
}

func (e *Engine) HandleEvent(ctx context.Context, event domain.QueueEvent) error {
	switch event.Type {
	case "gitlab.issue.changed":
		var payload webhook.IssueChanged
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.handleIssueChanged(ctx, payload)
	case "gitlab.gate.command":
		var payload webhook.GateNote
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.handleGateCommand(ctx, payload)
	case "gitlab.control.command":
		var payload webhook.ControlNote
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.handleControlCommand(ctx, payload)
	case "gitlab.lifecycle":
		var payload webhook.LifecycleEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.handleLifecycleEvent(ctx, payload)
	case "workflow.generate_plans":
		var payload GeneratePlansEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.generatePlans(ctx, payload)
	case "workflow.analyze_requirement":
		var payload AnalyzeRequirementEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		workflow, err := e.store.GetWorkflow(ctx, payload.WorkflowID)
		if err != nil {
			return err
		}
		snapshots, err := e.store.LatestSnapshots(ctx, workflow.ID)
		if err != nil {
			return err
		}
		project, ok := e.projects[workflow.GitLabProjectID]
		if !ok {
			return fmt.Errorf("project %d is not configured", workflow.GitLabProjectID)
		}
		return e.publishRequirementGate(ctx, workflow, project, snapshots, payload.Feedback)
	case "workflow.generate_architecture":
		var payload GenerateArchitectureEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.generateArchitecture(ctx, payload)
	case "evaluation.run":
		if !e.v3.Evaluation {
			return errors.New("V3 evaluation is disabled")
		}
		var payload EvaluationRunEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		_, err := e.RunPromptEvaluation(ctx, payload.SuiteID, payload.PromptVersionID)
		return err
	case "external.delivery":
		var payload webhook.ExternalCallback
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.handleDeliveryCallback(ctx, payload)
	case "external.incident":
		var payload webhook.ExternalCallback
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.handleIncidentCallback(ctx, payload)
	case "system.reconcile":
		return e.reconcile(ctx)
	default:
		return fmt.Errorf("unsupported event type %q", event.Type)
	}
}
