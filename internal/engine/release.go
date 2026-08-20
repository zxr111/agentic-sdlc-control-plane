package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
	"github.com/google/uuid"
)

// releaseModule owns staging, release approval, deployment, and observation.
type releaseModule struct{ engine *Engine }

func (e *Engine) release() releaseModule { return releaseModule{engine: e} }

func (m releaseModule) callback(ctx context.Context, event webhook.ExternalCallback) error {
	return m.engine.handleDeliveryCallback(ctx, event)
}

func (e *Engine) queueStatusNote(ctx context.Context, workflow domain.Workflow, dedupeKey, marker, body string) error {
	return e.store.EnqueueOutbox(ctx, dedupeKey, MessageUpsertNote, UpsertNotePayload{
		ProjectID: workflow.GitLabProjectID, IssueIID: workflow.IssueIID, Marker: marker, Body: body,
	})
}

func (e *Engine) queueDeliveryRequest(ctx context.Context, workflow domain.Workflow, action, sha, environment string) error {
	requestID := strings.Join([]string{workflow.ID, action, sha, environment}, ":")
	return e.store.EnqueueOutbox(ctx, "delivery:"+requestID, MessageTriggerDelivery, DeliveryRequestPayload{
		RequestID: requestID, Action: action, WorkflowID: workflow.ID,
		ProjectID: workflow.GitLabProjectID, IssueIID: workflow.IssueIID,
		CommitSHA: sha, Environment: environment,
	})
}

func (e *Engine) queueWorkItemNote(ctx context.Context, workflow domain.Workflow, workItemID, dedupeKey, marker, body string) error {
	item, err := e.store.GetWorkItem(ctx, workItemID)
	if err != nil {
		return err
	}
	if item.GitLabIssueIID == 0 {
		return errors.New("work item has no GitLab Issue")
	}
	return e.store.EnqueueOutbox(ctx, dedupeKey, MessageUpsertNote, UpsertNotePayload{
		ProjectID: workflow.GitLabProjectID, IssueIID: item.GitLabIssueIID, Marker: marker, Body: body,
	})
}

func (e *Engine) handleDeliveryCallback(ctx context.Context, callback webhook.ExternalCallback) error {
	if callback.Source == "quality" {
		item, err := e.store.GetWorkItem(ctx, callback.WorkItemID)
		if err != nil {
			return err
		}
		workflow, err := e.store.GetWorkflow(ctx, item.WorkflowID)
		if err != nil {
			return err
		}
		mr, err := e.store.GetMergeRequestByWorkItem(ctx, item.ID)
		if err != nil {
			return err
		}
		if callback.CommitSHA == "" || callback.CommitSHA != mr.HeadSHA {
			return errors.New("quality result SHA does not match current MR head")
		}
		runID, err := e.store.StartAgentRun(ctx, workflow.ID, item.ID, "QUALITY",
			"visible-codex-quality-task", callback.CommitSHA)
		if err != nil {
			return err
		}
		var verdict domain.QualityVerdict
		if err := json.Unmarshal(callback.Payload, &verdict); err != nil {
			_ = e.store.FinishAgentRun(ctx, runID, "FAILED", "", err)
			return fmt.Errorf("decode quality verdict: %w", err)
		}
		status := "FAILED"
		if verdict.Passes() {
			status = "PASSED"
		}
		attempt, err := e.store.RecordQualityRun(ctx, item.ID, mr.HeadSHA, status, verdict, "")
		if err != nil {
			_ = e.store.FinishAgentRun(ctx, runID, "FAILED", "", err)
			return err
		}
		if e.v3.RAG {
			qualityContent, marshalErr := json.Marshal(map[string]any{
				"work_item_id": item.ID, "merge_request_iid": mr.GitLabMRIID, "head_sha": mr.HeadSHA,
				"status": status, "attempt": attempt, "verdict": verdict,
			})
			if marshalErr != nil {
				_ = e.store.FinishAgentRun(ctx, runID, "FAILED", "", marshalErr)
				return marshalErr
			}
			_, _, indexErr := e.store.IngestKnowledge(ctx, store.KnowledgeSource{
				ProjectID: workflow.GitLabProjectID, SourceType: "QUALITY_FINDING",
				SourceKey: item.ID + ":" + mr.HeadSHA, SourceVersion: fmt.Sprintf("%d", attempt),
				Title: fmt.Sprintf("Quality evidence for work item %s", item.ID), AuthorityLevel: 70,
				AccessScope: map[string]any{"gitlab_project_id": workflow.GitLabProjectID},
				Content:     string(qualityContent), ParentPath: "Quality Evidence",
			})
			if indexErr != nil {
				_ = e.store.FinishAgentRun(ctx, runID, "FAILED", "", indexErr)
				return fmt.Errorf("index quality evidence: %w", indexErr)
			}
		}
		if !verdict.Passes() {
			next := domain.WorkItemRework
			if attempt >= 3 {
				next = domain.WorkItemBlocked
			}
			if err := e.store.SetWorkItemState(ctx, item.ID, next,
				map[string]any{"quality_attempt": attempt, "verdict": verdict}); err != nil {
				_ = e.store.FinishAgentRun(ctx, runID, "FAILED", "", err)
				return err
			}
			_ = e.store.FinishAgentRun(ctx, runID, "COMPLETED", "", nil)
			return e.queueWorkItemNote(ctx, workflow, item.ID, fmt.Sprintf("quality-failed:%s:%d", mr.HeadSHA, attempt),
				fmt.Sprintf("<!-- ai-factory:quality:%s:%d -->", mr.HeadSHA, attempt),
				fmt.Sprintf("## AI Quality failed (attempt %d/3)\n\nThe structured quality contract did not pass. "+
					"After three failed attempts the item is blocked for an engineer decision.", attempt))
		}
		if err := e.openCodeReviewGate(ctx, workflow, item, mr, verdict, attempt); err != nil {
			_ = e.store.FinishAgentRun(ctx, runID, "FAILED", "", err)
			return err
		}
		return e.store.FinishAgentRun(ctx, runID, "COMPLETED", "", nil)
	}
	return e.advanceDelivery(ctx, callback)
}

func (e *Engine) openCodeReviewGate(ctx context.Context, workflow domain.Workflow, item domain.WorkItem,
	mr store.MergeRequestRecord, verdict domain.QualityVerdict, attempt int) error {
	value := qualityArtifact{WorkItemID: item.ID, MergeRequestIID: mr.GitLabMRIID, HeadSHA: mr.HeadSHA, Verdict: verdict, Attempt: attempt}
	raw, _ := json.Marshal(value)
	version, err := e.store.NextArtifactVersion(ctx, workflow.ID, domain.ArtifactQualityReport)
	if err != nil {
		return err
	}
	markdown := fmt.Sprintf("## Code Review evidence\n\n- Work item: `%s` / Issue #%d\n- MR: !%d\n- Exact head SHA: `%s`\n"+
		"- Acceptance criteria coverage: `%.2f%%`\n- Approved test evidence coverage: `%.2f%%`\n- Required CI passed: `%t`\n"+
		"- P0/P1 findings: `%d/%d`\n- High/Critical security findings: `%d/%d`\n- Architecture deviations: `%d`\n"+
		"- Out-of-scope changes: `%d`\n- Blockers: `%d`\n- Migration/Rollback validated: `%t/%t`\n\n"+
		"Any new commit invalidates this Gate.",
		item.ID, item.GitLabIssueIID, mr.GitLabMRIID, mr.HeadSHA, verdict.AcceptanceCoverage,
		verdict.TestEvidenceCoverage, verdict.RequiredCIPassed, verdict.P0Findings, verdict.P1Findings,
		verdict.HighSecurityFindings, verdict.CriticalSecurityFindings, verdict.ArchitectureDeviations,
		verdict.OutOfScopeChanges, verdict.Blockers, verdict.MigrationValidated, verdict.RollbackValidated)
	artifact := domain.Artifact{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactQualityReport,
		Version: version, SourceHash: workflow.SourceHash, Content: raw, Markdown: markdown,
		Model: "independent-codex-quality-task", Prompt: "quality-contract-v2", GeneratedAt: time.Now().UTC(),
	}
	project := e.projects[workflow.GitLabProjectID]
	gate := domain.NewGate(workflow.ID, domain.GateCodeReview, artifact.ID, version,
		project.ReviewerIDs[domain.GateCodeReview])
	body := artifactHeader(workflow, nil, artifact) + markdown +
		gateInstructions(gate, project.ReviewerMentions[domain.GateCodeReview])
	if err := e.store.SetWorkItemState(ctx, item.ID, domain.WorkItemWaitingCodeReview,
		map[string]any{"mr_iid": mr.GitLabMRIID, "head_sha": mr.HeadSHA}); err != nil {
		return err
	}
	return e.store.PublishOperationalGate(ctx, artifact, gate, outboxNote(workflow, gate, body))
}

func (e *Engine) approveCodeReview(ctx context.Context, workflow domain.Workflow, gate domain.Gate) error {
	artifact, err := e.store.GetArtifact(ctx, gate.ArtifactID)
	if err != nil {
		return err
	}
	var report qualityArtifact
	if err := json.Unmarshal(artifact.Content, &report); err != nil {
		return err
	}
	mr, err := e.gitlab.GetMergeRequest(ctx, workflow.GitLabProjectID, report.MergeRequestIID)
	if err != nil {
		return err
	}
	if mr.SHA != report.HeadSHA {
		return errors.New("MR head changed after Code Review evidence was generated; approval is invalid")
	}
	if mr.Draft {
		return errors.New("draft MR cannot be merged")
	}
	if err := e.store.SetWorkItemState(ctx, report.WorkItemID, domain.WorkItemMergeQueued,
		map[string]any{"gate_id": gate.ID, "head_sha": report.HeadSHA}); err != nil {
		return err
	}
	if _, err := e.gitlab.MergeWhenPipelineSucceeds(ctx, workflow.GitLabProjectID, report.MergeRequestIID, report.HeadSHA); err != nil {
		return err
	}
	return e.queueWorkItemNote(ctx, workflow, report.WorkItemID, "merge-queued:"+gate.ID,
		"<!-- ai-factory:merge-queued:"+gate.ID+" -->",
		fmt.Sprintf("## Merge queued\n\nEngineer Code Review approved exact head `%s`. GitLab may merge only after required CI succeeds.", report.HeadSHA))
}

func (e *Engine) publishNewlyReadyWorkItems(ctx context.Context, workflow domain.Workflow) error {
	items, err := e.store.ListWorkItems(ctx, workflow.ID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.State != domain.WorkItemReadyForCodex {
			continue
		}
		if item.Key == "__integration" {
			if err := e.store.SetWorkItemState(ctx, item.ID, domain.WorkItemDraftMR,
				map[string]any{"reason": "all integration dependencies merged"}); err != nil {
				return err
			}
			description := fmt.Sprintf("<!-- ai-factory:integration-mr:%s -->\n\n"+
				"## AI Native workflow integration\n\n- Parent Issue: #%d\n- Workflow: `%s`\n\n"+
				"This MR assembles only Engineer-approved child work. It requires the same independent Quality task and Code Review Gate.",
				workflow.ID, workflow.IssueIID, workflow.ID)
			if err := e.store.EnqueueOutbox(ctx, "integration-mr:"+workflow.ID, MessageCreateMR,
				CreateMergeRequestPayload{
					ProjectID: workflow.GitLabProjectID, WorkItemID: item.ID,
					Title:  fmt.Sprintf("[AI-Native][#%d] Integrate approved work items", workflow.IssueIID),
					Source: item.BranchName, Target: item.TargetBranch, Description: description,
				}); err != nil {
				return err
			}
			continue
		}
		if item.GitLabIssueIID == 0 {
			continue
		}
		if err := e.store.EnqueueOutbox(ctx, "ready-work-item:"+item.ID, MessageUpdateIssue, UpdateIssuePayload{
			ProjectID: workflow.GitLabProjectID, IssueIID: item.GitLabIssueIID,
			AddLabels: []string{"ai::ready-for-codex"}, RemoveLabels: []string{"stage::planning"},
		}); err != nil {
			return err
		}
		body := fmt.Sprintf("## Ready for Codex\n\nAll dependencies are complete.\n\n- Branch: `%s`\n- MR target: `%s`\n\n"+
			"`/start-codex task:%s client:<client-id>`", item.BranchName, item.TargetBranch, item.ID)
		if err := e.store.EnqueueOutbox(ctx, "ready-note:"+item.ID, MessageUpsertNote, UpsertNotePayload{
			ProjectID: workflow.GitLabProjectID, IssueIID: item.GitLabIssueIID,
			Marker: "<!-- ai-factory:ready:" + item.ID + " -->", Body: body,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) advanceDelivery(ctx context.Context, callback webhook.ExternalCallback) error {
	workflow, err := e.store.GetWorkflow(ctx, callback.WorkflowID)
	if err != nil {
		return err
	}
	switch callback.Status {
	case "ci_success":
		if workflow.State != domain.StateReleaseCIRunning {
			return nil
		}
		version := callback.ExternalID
		if version == "" {
			version = callback.CommitSHA
		}
		if _, err := e.store.SaveReleaseCandidate(ctx, workflow.ID, version, callback.CommitSHA, "CI_PASSED"); err != nil {
			return err
		}
		if err := e.store.Transition(ctx, workflow.ID, domain.StateStagingDeploying, "release CI passed", callback); err != nil {
			return err
		}
		return e.queueDeliveryRequest(ctx, workflow, "staging_deploy", callback.CommitSHA, "test")
	case "staging_deployed":
		if workflow.State != domain.StateStagingDeploying {
			return nil
		}
		releaseID, _, _, _, err := e.store.LatestReleaseCandidate(ctx, workflow.ID)
		if err != nil {
			return err
		}
		environment := callback.Environment
		if environment == "" {
			environment = "test"
		}
		if _, err := e.store.SaveDeployment(ctx, workflow.ID, releaseID, environment,
			callback.ExternalID, "success", false); err != nil {
			return err
		}
		if err := e.store.Transition(ctx, workflow.ID, domain.StateStagingVerifying, "staging deployment completed", callback); err != nil {
			return err
		}
		return e.queueDeliveryRequest(ctx, workflow, "staging_verify", callback.CommitSHA, environment)
	case "staging_verified":
		if workflow.State != domain.StateStagingVerifying {
			return nil
		}
		if err := e.store.Transition(ctx, workflow.ID, domain.StateWaitingReleaseApproval,
			"staging verification passed", callback); err != nil {
			return err
		}
		if callback.RequiresProductionMigration {
			return e.openProductionMigrationGate(ctx, workflow, callback, "")
		}
		return e.openReleaseGate(ctx, workflow, callback)
	case "production_deployed":
		if workflow.State != domain.StateProductionDeploying {
			return nil
		}
		releaseID, _, _, _, err := e.store.LatestReleaseCandidate(ctx, workflow.ID)
		if err != nil {
			return err
		}
		deploymentID, err := e.store.SaveDeployment(ctx, workflow.ID, releaseID, "production",
			callback.ExternalID, "success", true)
		if err != nil {
			return err
		}
		if err := e.store.Transition(ctx, workflow.ID, domain.StateObserving, "production deployment completed", callback); err != nil {
			return err
		}
		return e.store.StartObservation(ctx, workflow.ID, deploymentID, 30*time.Minute,
			map[string]any{"mode": "production", "commit_sha": callback.CommitSHA})
	case "observation_success":
		if workflow.State != domain.StateObserving {
			return nil
		}
		if err := e.store.CompleteObservation(ctx, workflow.ID, callback); err != nil {
			return err
		}
		if err := e.store.Transition(ctx, workflow.ID, domain.StateCompleted, "observation criteria passed", callback); err != nil {
			return err
		}
		return e.queueStatusNote(ctx, workflow, "completed:"+workflow.ID,
			"<!-- ai-factory:completed -->", "## Workflow completed\n\nThe approved release passed its observation window.")
	default:
		return nil
	}
}

func (e *Engine) openReleaseGate(ctx context.Context, workflow domain.Workflow, callback webhook.ExternalCallback) error {
	open, err := e.hasOpenGate(ctx, workflow.ID, domain.GateRelease)
	if err != nil || open {
		return err
	}
	value := map[string]any{
		"workflow_id": workflow.ID, "commit_sha": callback.CommitSHA,
		"environment": callback.Environment, "external_id": callback.ExternalID,
		"production_enabled": e.projects[workflow.GitLabProjectID].ProductionEnabled,
	}
	raw, _ := json.Marshal(value)
	version, err := e.store.NextArtifactVersion(ctx, workflow.ID, domain.ArtifactReleasePlan)
	if err != nil {
		return err
	}
	markdown := fmt.Sprintf("## Release candidate\n\n- Commit: `%s`\n- Verified environment: `%s`\n- External delivery: `%s`\n"+
		"- Production adapter enabled: `%t`\n\nRelease approval does not bypass the production lock.",
		callback.CommitSHA, callback.Environment, callback.ExternalID, e.projects[workflow.GitLabProjectID].ProductionEnabled)
	artifact := domain.Artifact{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactReleasePlan, Version: version,
		SourceHash: workflow.SourceHash, Content: raw, Markdown: markdown, Model: "factory",
		Prompt: "release-evidence-v2", GeneratedAt: time.Now().UTC(),
	}
	project := e.projects[workflow.GitLabProjectID]
	gate := domain.NewGate(workflow.ID, domain.GateRelease, artifact.ID, version, project.ReviewerIDs[domain.GateRelease])
	body := markdown + gateInstructions(gate, project.ReviewerMentions[domain.GateRelease])
	return e.store.PublishOperationalGate(ctx, artifact, gate, outboxNote(workflow, gate, body))
}

func (e *Engine) openProductionMigrationGate(ctx context.Context, workflow domain.Workflow,
	callback webhook.ExternalCallback, feedback string) error {
	open, err := e.hasOpenGate(ctx, workflow.ID, domain.GateProductionMigration)
	if err != nil || open {
		return err
	}
	if strings.TrimSpace(callback.MigrationPlan) == "" || strings.TrimSpace(callback.RollbackPlan) == "" {
		return errors.New("production migration requires both migration_plan and rollback_plan")
	}
	raw, err := json.Marshal(callback)
	if err != nil {
		return err
	}
	version, err := e.store.NextArtifactVersion(ctx, workflow.ID, domain.ArtifactProductionMigration)
	if err != nil {
		return err
	}
	markdown := fmt.Sprintf("## Production migration plan\n\n- Commit: `%s`\n- Change window: `%s`\n\n"+
		"### Migration\n\n%s\n\n### Rollback\n\n%s\n\n"+
		"This Gate authorizes only the reviewed migration plan. Release approval remains a separate Gate.",
		callback.CommitSHA, callback.ChangeWindow, callback.MigrationPlan, callback.RollbackPlan)
	if strings.TrimSpace(feedback) != "" {
		markdown += "\n\n### Previous Engineer feedback\n\n" + feedback
	}
	artifact := domain.Artifact{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactProductionMigration,
		Version: version, SourceHash: workflow.SourceHash, Content: raw, Markdown: markdown,
		Model: "factory", Prompt: "production-migration-evidence-v2", GeneratedAt: time.Now().UTC(),
	}
	project := e.projects[workflow.GitLabProjectID]
	gate := domain.NewGate(workflow.ID, domain.GateProductionMigration, artifact.ID, version,
		project.ReviewerIDs[domain.GateProductionMigration])
	body := markdown + gateInstructions(gate, project.ReviewerMentions[domain.GateProductionMigration])
	return e.store.PublishOperationalGate(ctx, artifact, gate, outboxNote(workflow, gate, body))
}
