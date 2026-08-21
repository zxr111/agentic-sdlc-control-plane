package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/agents"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"github.com/google/uuid"
)

// planningModule owns requirement analysis and product/test planning.
type planningModule struct{ engine *Engine }

func (e *Engine) planning() planningModule { return planningModule{engine: e} }

func (m planningModule) analyzeRequirement(ctx context.Context, event AnalyzeRequirementEvent) error {
	return m.engine.analyzeRequirement(ctx, event)
}

func (m planningModule) generatePlans(ctx context.Context, event GeneratePlansEvent) error {
	return m.engine.generatePlans(ctx, event)
}

func (e *Engine) publishRequirementGate(ctx context.Context, workflow domain.Workflow, project domain.ProjectConfig,
	snapshots []domain.Snapshot, feedback string) error {
	runID, agentContext, err := e.startAgentRun(ctx, workflow, "REQUIREMENT", combinedHash(snapshots), snapshots)
	if err != nil {
		return err
	}
	runCtx, cancelRun := e.cancellableAgentContext(ctx, runID)
	defer cancelRun()
	runtimePrompt, promptLabel, err := e.runtimePromptForRun(ctx, runID, "requirement-review-v1")
	if err != nil {
		_ = e.store.FinishAgentRun(ctx, runID, "FAILED", "", err)
		return err
	}
	review, trace, err := e.agents.ReviewRequirementWithPrompt(runCtx, runID, agentContext, feedback, runtimePrompt)
	if err != nil {
		_ = e.store.FinishAgentRunWithTrace(ctx, runID, "FAILED", "", storeTrace(trace), err)
		return err
	}
	raw, err := json.Marshal(review)
	if err != nil {
		return err
	}
	multiAgentMarkdown := ""
	if e.v3.MultiAgent {
		synthesis, reviewErr := e.runGovernedReview(ctx, workflow, "REQUIREMENT", snapshots, raw)
		if reviewErr != nil {
			_ = e.store.FinishAgentRunWithTrace(ctx, runID, "FAILED", "", storeTrace(trace), reviewErr)
			return reviewErr
		}
		multiAgentMarkdown = renderGovernedReview(synthesis)
	}
	version, err := e.store.NextArtifactVersion(ctx, workflow.ID, domain.ArtifactRequirement)
	if err != nil {
		return err
	}
	artifact := domain.Artifact{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactRequirement,
		Version: version, SourceHash: combinedHash(snapshots), Content: raw,
		Markdown: agents.RenderRequirement(review, snapshots) + multiAgentMarkdown, Model: e.agents.Model(),
		Prompt: promptLabel, GeneratedAt: time.Now().UTC(),
	}
	gate := domain.NewGate(workflow.ID, domain.GateRequirement, artifact.ID, version, project.ReviewerIDs[domain.GateRequirement])
	body := artifactHeader(workflow, snapshots, artifact) + artifact.Markdown +
		gateInstructions(gate, project.ReviewerMentions[domain.GateRequirement])
	note := outboxNote(workflow, gate, body)
	err = e.store.PublishGate(ctx, workflow, artifact, gate, domain.StateWaitingRequirementReview, note)
	if err != nil {
		_ = e.store.FinishAgentRun(ctx, runID, "FAILED", "", err)
		return err
	}
	return e.store.FinishAgentRunWithTrace(ctx, runID, "COMPLETED", artifact.ID, storeTrace(trace), nil)
}

func (e *Engine) analyzeRequirement(ctx context.Context, event AnalyzeRequirementEvent) error {
	workflow, err := e.store.GetWorkflow(ctx, event.WorkflowID)
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
	return e.publishRequirementGate(ctx, workflow, project, snapshots, event.Feedback)
}

func (e *Engine) generatePlans(ctx context.Context, event GeneratePlansEvent) error {
	workflow, err := e.store.GetWorkflow(ctx, event.WorkflowID)
	if err != nil {
		return err
	}
	if workflow.State != domain.StateMaterializingWorkItems {
		return nil
	}
	pending, err := e.store.PendingOutboxPrefix(ctx, "work-item:"+workflow.ID+":")
	if err != nil {
		return err
	}
	if pending != 0 {
		return fmt.Errorf("%d approved work items are not materialized yet", pending)
	}
	if err := e.store.Transition(ctx, workflow.ID, domain.StatePRDGenerating,
		"approved work items materialized", nil); err != nil {
		return err
	}
	workflow.State = domain.StatePRDGenerating
	snapshots, err := e.store.LatestSnapshots(ctx, workflow.ID)
	if err != nil {
		return err
	}
	requirement, err := e.store.LatestArtifact(ctx, workflow.ID, domain.ArtifactRequirement)
	if err != nil {
		return err
	}
	return e.publishPlanningGates(ctx, workflow, e.projects[workflow.GitLabProjectID], snapshots, requirement, "", "")
}

func (e *Engine) publishPlanningGates(ctx context.Context, workflow domain.Workflow, project domain.ProjectConfig,
	snapshots []domain.Snapshot, requirement domain.Artifact, prdFeedback, testFeedback string) error {
	var review agents.RequirementReview
	if err := json.Unmarshal(requirement.Content, &review); err != nil {
		return err
	}
	reviewJSON, _ := json.Marshal(review)
	prdRunID, source, err := e.startAgentRun(ctx, workflow, "PRD", workflow.SourceHash, snapshots)
	if err != nil {
		return err
	}
	testRunID, testSource, err := e.startAgentRun(ctx, workflow, "TEST", workflow.SourceHash, snapshots)
	if err != nil {
		_ = e.store.FinishAgentRun(ctx, prdRunID, "FAILED", "", err)
		return err
	}
	prdPrompt, prdPromptLabel, err := e.runtimePromptForRun(ctx, prdRunID, "prd-v1")
	if err != nil {
		_ = e.store.FinishAgentRun(ctx, prdRunID, "FAILED", "", err)
		_ = e.store.FinishAgentRun(ctx, testRunID, "FAILED", "", err)
		return err
	}
	testPrompt, testPromptLabel, err := e.runtimePromptForRun(ctx, testRunID, "test-plan-v1")
	if err != nil {
		_ = e.store.FinishAgentRun(ctx, prdRunID, "FAILED", "", err)
		_ = e.store.FinishAgentRun(ctx, testRunID, "FAILED", "", err)
		return err
	}
	var prd agents.PRD
	var tests agents.TestPlan
	var prdTrace, testTrace agents.Trace
	var prdErr, testErr error
	var wait sync.WaitGroup
	prdCtx, cancelPRD := e.cancellableAgentContext(ctx, prdRunID)
	defer cancelPRD()
	testCtx, cancelTest := e.cancellableAgentContext(ctx, testRunID)
	defer cancelTest()
	wait.Add(2)
	go func() {
		defer wait.Done()
		prd, prdTrace, prdErr = e.agents.GeneratePRDWithPrompt(prdCtx, prdRunID, source, string(reviewJSON), prdFeedback, prdPrompt)
	}()
	go func() {
		defer wait.Done()
		tests, testTrace, testErr = e.agents.GenerateTestPlanWithPrompt(testCtx, testRunID, testSource, string(reviewJSON), testFeedback, testPrompt)
	}()
	wait.Wait()
	if prdErr != nil {
		_ = e.store.FinishAgentRunWithTrace(ctx, prdRunID, "FAILED", "", storeTrace(prdTrace), prdErr)
		_ = e.store.FinishAgentRunWithTrace(ctx, testRunID, "FAILED", "", storeTrace(testTrace), testErr)
		return prdErr
	}
	if testErr != nil {
		_ = e.store.FinishAgentRunWithTrace(ctx, prdRunID, "COMPLETED", "", storeTrace(prdTrace), nil)
		_ = e.store.FinishAgentRunWithTrace(ctx, testRunID, "FAILED", "", storeTrace(testTrace), testErr)
		return testErr
	}
	prdRaw, _ := json.Marshal(prd)
	testRaw, _ := json.Marshal(tests)
	prdVersion, err := e.store.NextArtifactVersion(ctx, workflow.ID, domain.ArtifactPRD)
	if err != nil {
		return err
	}
	testVersion, err := e.store.NextArtifactVersion(ctx, workflow.ID, domain.ArtifactTestPlan)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	prdArtifact := domain.Artifact{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactPRD,
		Version: prdVersion, SourceHash: workflow.SourceHash, Content: prdRaw,
		Markdown: agents.RenderPRD(prd), Model: e.agents.Model(), Prompt: prdPromptLabel, GeneratedAt: now,
	}
	testArtifact := domain.Artifact{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactTestPlan,
		Version: testVersion, SourceHash: workflow.SourceHash, Content: testRaw,
		Markdown: agents.RenderTestPlan(tests), Model: e.agents.Model(), Prompt: testPromptLabel, GeneratedAt: now,
	}
	prdGate := domain.NewGate(workflow.ID, domain.GatePRD, prdArtifact.ID, prdVersion, project.ReviewerIDs[domain.GatePRD])
	testGate := domain.NewGate(workflow.ID, domain.GateTest, testArtifact.ID, testVersion, project.ReviewerIDs[domain.GateTest])
	prdBody := artifactHeader(workflow, snapshots, prdArtifact) + prdArtifact.Markdown +
		gateInstructions(prdGate, project.ReviewerMentions[domain.GatePRD])
	testBody := artifactHeader(workflow, snapshots, testArtifact) + testArtifact.Markdown +
		gateInstructions(testGate, project.ReviewerMentions[domain.GateTest])
	err = e.store.PublishPlanningGates(ctx, workflow,
		[]domain.Artifact{prdArtifact, testArtifact}, []domain.Gate{prdGate, testGate},
		[]domain.OutboxMessage{outboxNote(workflow, prdGate, prdBody), outboxNote(workflow, testGate, testBody)})
	if err != nil {
		_ = e.store.FinishAgentRun(ctx, prdRunID, "FAILED", "", err)
		_ = e.store.FinishAgentRun(ctx, testRunID, "FAILED", "", err)
		return err
	}
	if err := e.store.FinishAgentRunWithTrace(ctx, prdRunID, "COMPLETED", prdArtifact.ID, storeTrace(prdTrace), nil); err != nil {
		return err
	}
	return e.store.FinishAgentRunWithTrace(ctx, testRunID, "COMPLETED", testArtifact.ID, storeTrace(testTrace), nil)
}
