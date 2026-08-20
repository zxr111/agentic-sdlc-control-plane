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
		return e.intake().issueChanged(ctx, payload)
	case "gitlab.gate.command":
		var payload webhook.GateNote
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.quality().gateCommand(ctx, payload)
	case "gitlab.control.command":
		var payload webhook.ControlNote
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.execution().control(ctx, payload)
	case "gitlab.lifecycle":
		var payload webhook.LifecycleEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.execution().lifecycle(ctx, payload)
	case "workflow.generate_plans":
		var payload GeneratePlansEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.planning().generatePlans(ctx, payload)
	case "workflow.analyze_requirement":
		var payload AnalyzeRequirementEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.planning().analyzeRequirement(ctx, payload)
	case "workflow.generate_architecture":
		var payload GenerateArchitectureEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.architecture().generate(ctx, payload)
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
		return e.release().callback(ctx, payload)
	case "external.incident":
		var payload webhook.ExternalCallback
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.incident().callback(ctx, payload)
	case "system.reconcile":
		return e.reconcile(ctx)
	default:
		return fmt.Errorf("unsupported event type %q", event.Type)
	}
}
