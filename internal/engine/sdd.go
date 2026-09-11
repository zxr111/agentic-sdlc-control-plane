package engine

import (
	"context"
	"encoding/json"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/agents"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/knowledge"
	"github.com/google/uuid"
)

type sddModule struct{ engine *Engine }

func (e *Engine) sdd() sddModule { return sddModule{engine: e} }

func (m sddModule) generate(ctx context.Context, event GenerateSDDEvent) error {
	workflow, err := m.engine.store.GetWorkflow(ctx, event.WorkflowID)
	if err != nil {
		return err
	}
	if workflow.State != domain.StateSDDGenerating {
		return nil
	}
	snapshots, err := m.engine.store.LatestSnapshots(ctx, workflow.ID)
	if err != nil {
		return err
	}
	requirement, err := m.engine.store.LatestArtifact(ctx, workflow.ID, domain.ArtifactRequirement)
	if err != nil {
		return err
	}
	prd, err := m.engine.store.LatestArtifact(ctx, workflow.ID, domain.ArtifactPRD)
	if err != nil {
		return err
	}
	testPlan, err := m.engine.store.LatestArtifact(ctx, workflow.ID, domain.ArtifactTestPlan)
	if err != nil {
		return err
	}
	architecture, err := m.engine.store.LatestArtifact(ctx, workflow.ID, domain.ArtifactArchitecture)
	if err != nil {
		return err
	}
	runID, agentContext, err := m.engine.startAgentRun(ctx, workflow, "SDD", workflow.SourceHash, snapshots)
	if err != nil {
		return err
	}
	runCtx, cancel := m.engine.cancellableAgentContext(ctx, runID)
	defer cancel()
	prompt, promptLabel, err := m.engine.runtimePromptForRun(ctx, runID, "software-design-v1")
	if err != nil {
		_ = m.engine.store.FinishAgentRun(ctx, runID, "FAILED", "", err)
		return err
	}
	value, trace, err := m.engine.agents.GenerateSDDWithPrompt(runCtx, runID, agentContext,
		string(requirement.Content), string(prd.Content), string(testPlan.Content), string(architecture.Content), event.Feedback, prompt)
	if err != nil {
		_ = m.engine.store.FinishAgentRunWithTrace(ctx, runID, "FAILED", "", storeTrace(trace), err)
		return err
	}
	if err := knowledge.ValidateCitationReferences(value.Citations, agentContext); err != nil {
		_ = m.engine.store.FinishAgentRunWithTrace(ctx, runID, "FAILED", "", storeTrace(trace), err)
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	version, err := m.engine.store.NextArtifactVersion(ctx, workflow.ID, domain.ArtifactSDD)
	if err != nil {
		return err
	}
	artifact := domain.Artifact{ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactSDD,
		Version: version, SourceHash: workflow.SourceHash, Content: raw, Markdown: agents.RenderSDD(value),
		Model: m.engine.agents.Model(), Prompt: promptLabel, GeneratedAt: time.Now().UTC()}
	project := m.engine.projects[workflow.GitLabProjectID]
	reviewers := project.ReviewerIDs[domain.GateSDD]
	mentions := project.ReviewerMentions[domain.GateSDD]
	if len(reviewers) == 0 {
		reviewers = project.ReviewerIDs[domain.GateArchitecture]
	}
	if len(mentions) == 0 {
		mentions = project.ReviewerMentions[domain.GateArchitecture]
	}
	gate := domain.NewGate(workflow.ID, domain.GateSDD, artifact.ID, version, reviewers)
	body := artifactHeader(workflow, snapshots, artifact) + artifact.Markdown + gateInstructions(gate, mentions)
	if err := m.engine.store.PublishGate(ctx, workflow, artifact, gate, domain.StateWaitingSDDReview,
		outboxNote(workflow, gate, body)); err != nil {
		_ = m.engine.store.FinishAgentRun(ctx, runID, "FAILED", "", err)
		return err
	}
	return m.engine.store.FinishAgentRunWithTrace(ctx, runID, "COMPLETED", artifact.ID, storeTrace(trace), nil)
}
