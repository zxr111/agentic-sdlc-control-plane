package engine

import (
	"context"
	"encoding/json"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/agents"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"github.com/google/uuid"
)

// architectureModule owns architecture generation and governed review.
type architectureModule struct{ engine *Engine }

func (e *Engine) architecture() architectureModule { return architectureModule{engine: e} }

func (m architectureModule) generate(ctx context.Context, event GenerateArchitectureEvent) error {
	return m.engine.generateArchitecture(ctx, event)
}

func (e *Engine) generateArchitecture(ctx context.Context, event GenerateArchitectureEvent) error {
	workflow, err := e.store.GetWorkflow(ctx, event.WorkflowID)
	if err != nil {
		return err
	}
	if workflow.State == domain.StateReadyForArchitecture {
		if err := e.store.Transition(ctx, workflow.ID, domain.StateArchitectureGenerating,
			"approved product artifacts ready for architecture", nil); err != nil {
			return err
		}
		workflow.State = domain.StateArchitectureGenerating
	}
	if workflow.State != domain.StateArchitectureGenerating {
		return nil
	}
	snapshots, err := e.store.LatestSnapshots(ctx, workflow.ID)
	if err != nil {
		return err
	}
	requirement, err := e.store.LatestArtifact(ctx, workflow.ID, domain.ArtifactRequirement)
	if err != nil {
		return err
	}
	prd, err := e.store.LatestArtifact(ctx, workflow.ID, domain.ArtifactPRD)
	if err != nil {
		return err
	}
	testPlan, err := e.store.LatestArtifact(ctx, workflow.ID, domain.ArtifactTestPlan)
	if err != nil {
		return err
	}
	runID, agentContext, err := e.startAgentRun(ctx, workflow, "ARCHITECTURE", workflow.SourceHash, snapshots)
	if err != nil {
		return err
	}
	runCtx, cancelRun := e.cancellableAgentContext(ctx, runID)
	defer cancelRun()
	value, trace, err := e.agents.GenerateArchitecture(runCtx, runID, agentContext,
		string(requirement.Content), string(prd.Content), string(testPlan.Content), event.Feedback)
	if err != nil {
		_ = e.store.FinishAgentRunWithTrace(ctx, runID, "FAILED", "", storeTrace(trace), err)
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	multiAgentMarkdown := ""
	if e.v3.MultiAgent {
		synthesis, reviewErr := e.runGovernedReview(ctx, workflow, "ARCHITECTURE", snapshots, raw)
		if reviewErr != nil {
			_ = e.store.FinishAgentRunWithTrace(ctx, runID, "FAILED", "", storeTrace(trace), reviewErr)
			return reviewErr
		}
		multiAgentMarkdown = renderGovernedReview(synthesis)
	}
	version, err := e.store.NextArtifactVersion(ctx, workflow.ID, domain.ArtifactArchitecture)
	if err != nil {
		return err
	}
	artifact := domain.Artifact{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactArchitecture,
		Version: version, SourceHash: workflow.SourceHash, Content: raw,
		Markdown: agents.RenderArchitecture(value) + multiAgentMarkdown, Model: e.agents.Model(),
		Prompt: "architecture-v2", GeneratedAt: time.Now().UTC(),
	}
	project := e.projects[workflow.GitLabProjectID]
	gate := domain.NewGate(workflow.ID, domain.GateArchitecture, artifact.ID, version,
		project.ReviewerIDs[domain.GateArchitecture])
	body := artifactHeader(workflow, snapshots, artifact) + artifact.Markdown +
		gateInstructions(gate, project.ReviewerMentions[domain.GateArchitecture])
	err = e.store.PublishGate(ctx, workflow, artifact, gate, domain.StateWaitingArchitectureReview,
		outboxNote(workflow, gate, body))
	if err != nil {
		_ = e.store.FinishAgentRun(ctx, runID, "FAILED", "", err)
		return err
	}
	return e.store.FinishAgentRunWithTrace(ctx, runID, "COMPLETED", artifact.ID, storeTrace(trace), nil)
}
