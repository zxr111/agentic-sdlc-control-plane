package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
	"github.com/google/uuid"
)

// incidentModule owns production-locked incident intake and remediation Gates.
type incidentModule struct{ engine *Engine }

func (e *Engine) incident() incidentModule { return incidentModule{engine: e} }

func (m incidentModule) callback(ctx context.Context, event webhook.ExternalCallback) error {
	return m.engine.handleIncidentCallback(ctx, event)
}

func (e *Engine) handleIncidentCallback(ctx context.Context, callback webhook.ExternalCallback) error {
	if err := e.store.RecordIncident(ctx, callback.ExternalID, callback.Source, callback.WorkflowID,
		callback.Severity, callback.Title, callback.Status, callback); err != nil {
		return err
	}
	if callback.WorkflowID == "" {
		return nil
	}
	workflow, err := e.store.GetWorkflow(ctx, callback.WorkflowID)
	if err != nil {
		return err
	}
	if e.v3.RAG {
		content := fmt.Sprintf("Incident %s\nSource: %s\nSeverity: %s\nStatus: %s\nTitle: %s",
			callback.ExternalID, callback.Source, callback.Severity, callback.Status, callback.Title)
		if _, _, indexErr := e.store.IngestKnowledge(ctx, store.KnowledgeSource{
			ProjectID: workflow.GitLabProjectID, SourceType: "INCIDENT",
			SourceKey:     callback.Source + ":" + callback.ExternalID,
			SourceVersion: callback.Status + ":" + callback.Severity, Title: callback.Title,
			AuthorityLevel: 80, AccessScope: map[string]any{"gitlab_project_id": workflow.GitLabProjectID},
			Content: content, ParentPath: "Incident",
		}); indexErr != nil {
			return fmt.Errorf("index incident: %w", indexErr)
		}
	}
	if callback.Severity == "high" || callback.Severity == "critical" {
		return e.openIncidentGate(ctx, workflow, callback, "")
	}
	return e.queueStatusNote(ctx, workflow, "incident:"+callback.Source+":"+callback.ExternalID,
		"<!-- ai-factory:incident:"+callback.Source+":"+callback.ExternalID+" -->",
		fmt.Sprintf("## Incident detected\n\n- Source: `%s`\n- Severity: `%s`\n- Status: `%s`\n- Title: %s\n\n"+
			"High/critical remediation requires an Incident Gate; production mutation is never automatic.",
			callback.Source, callback.Severity, callback.Status, callback.Title))
}

func (e *Engine) openIncidentGate(ctx context.Context, workflow domain.Workflow,
	callback webhook.ExternalCallback, feedback string) error {
	open, err := e.hasOpenGate(ctx, workflow.ID, domain.GateIncident)
	if err != nil || open {
		return err
	}
	raw, err := json.Marshal(callback)
	if err != nil {
		return err
	}
	version, err := e.store.NextArtifactVersion(ctx, workflow.ID, domain.ArtifactIncidentReport)
	if err != nil {
		return err
	}
	markdown := fmt.Sprintf("## High-risk incident authorization\n\n- Source: `%s`\n- Severity: `%s`\n- Status: `%s`\n- Title: %s\n\n"+
		"AI may analyze and propose remediation, but it cannot mutate production before this Gate is approved.",
		callback.Source, callback.Severity, callback.Status, callback.Title)
	if strings.TrimSpace(feedback) != "" {
		markdown += "\n\n### Previous Engineer feedback\n\n" + feedback
	}
	artifact := domain.Artifact{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactIncidentReport,
		Version: version, SourceHash: workflow.SourceHash, Content: raw, Markdown: markdown,
		Model: "factory", Prompt: "incident-evidence-v2", GeneratedAt: time.Now().UTC(),
	}
	project := e.projects[workflow.GitLabProjectID]
	gate := domain.NewGate(workflow.ID, domain.GateIncident, artifact.ID, version,
		project.ReviewerIDs[domain.GateIncident])
	body := markdown + gateInstructions(gate, project.ReviewerMentions[domain.GateIncident])
	return e.store.PublishOperationalGate(ctx, artifact, gate, outboxNote(workflow, gate, body))
}
