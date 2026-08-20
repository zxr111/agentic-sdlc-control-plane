package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/agents"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
	"github.com/google/uuid"
)

// qualityModule owns Engineer Gate commands and exact-evidence decisions.
type qualityModule struct{ engine *Engine }

func (e *Engine) quality() qualityModule { return qualityModule{engine: e} }

func (m qualityModule) gateCommand(ctx context.Context, event webhook.GateNote) error {
	return m.engine.handleGateCommand(ctx, event)
}

func artifactHeader(workflow domain.Workflow, snapshots []domain.Snapshot, artifact domain.Artifact) string {
	var sourceVersions []string
	for _, snapshot := range snapshots {
		sourceVersions = append(sourceVersions, fmt.Sprintf("Page %s v%d `%s`", snapshot.ConfluencePageID, snapshot.Version, snapshot.ContentHash))
	}
	return fmt.Sprintf("<!-- source-hash:%s -->\n**Workflow:** `%s` · **Artifact:** `%s` v%d · **Model:** `%s`\n\n**Sources:** %s\n\n",
		artifact.SourceHash, workflow.ID, artifact.Type, artifact.Version, artifact.Model, strings.Join(sourceVersions, "; "))
}

func gateInstructions(gate domain.Gate, mentions []string) string {
	var mentionText string
	if len(mentions) > 0 {
		for _, mention := range mentions {
			mention = strings.TrimPrefix(mention, "@")
			mentionText += " @" + mention
		}
	}
	return fmt.Sprintf("\n---\n\n## Engineer Gate: %s%s\n\nThis workflow is stopped at this Gate. Review the artifact and comment with exactly one command:\n\n"+
		"- `/approve gate:%s`\n- `/request-changes gate:%s` followed by feedback\n- `/reject gate:%s` followed by a reason\n\n"+
		"`Reject` also returns the artifact for rework; it does not authorize cancellation or later stages.\n",
		gate.Type, mentionText, gate.ID, gate.ID, gate.ID)
}

func outboxNote(workflow domain.Workflow, gate domain.Gate, body string) domain.OutboxMessage {
	payload, _ := json.Marshal(UpsertNotePayload{
		ProjectID: workflow.GitLabProjectID, IssueIID: workflow.IssueIID,
		Marker: fmt.Sprintf("<!-- ai-factory:gate:%s -->", gate.ID), Body: body,
	})
	return domain.OutboxMessage{
		DedupeKey: "gate-note:" + gate.ID, Type: MessageUpsertNote, Payload: payload,
	}
}

func (e *Engine) handleGateCommand(ctx context.Context, event webhook.GateNote) error {
	gate, err := e.store.GetGate(ctx, event.Command.GateID)
	if err != nil {
		return err
	}
	workflow, err := e.store.GetWorkflow(ctx, gate.WorkflowID)
	if err != nil {
		return err
	}
	if workflow.GitLabProjectID != event.ProjectID || workflow.IssueIID != event.IssueIID {
		return errors.New("gate does not belong to the commented Issue")
	}
	if event.EmailRelayHash != "" {
		command := fmt.Sprintf("/%s gate:%s", event.Command.Action, event.Command.GateID)
		if err := e.store.RecordEmailRelay(ctx, event.EmailRelayHash, event.UserID, event.NoteID, command, "RECEIVED"); err != nil {
			return err
		}
		if gate.Type == domain.GateRelease || gate.Type == domain.GateIncident || gate.Type == domain.GateProductionMigration {
			return e.rejectUnauthorizedGateCommand(ctx, workflow, gate, event,
				"email relay cannot decide a production-risk Gate; post the command directly in GitLab")
		}
	}
	if gate.Status != domain.GateOpen {
		return e.resumeRecordedDecision(ctx, workflow, gate, event)
	}
	if !gate.Authorizes(event.UserID) {
		return e.rejectUnauthorizedGateCommand(ctx, workflow, gate, event, "user is not in this Gate's reviewer allowlist")
	}
	active, err := e.gitlab.IsActiveProjectMember(ctx, event.ProjectID, event.UserID)
	if err != nil {
		return err
	}
	if !active {
		return e.rejectUnauthorizedGateCommand(ctx, workflow, gate, event, "user is not an active project member")
	}
	if err := e.store.DecideGate(ctx, gate, event.Command.Action, event.UserID, event.Username, event.Command.Feedback); err != nil {
		return err
	}
	if event.Command.Action == domain.ActionApprove && e.v3.RAG {
		artifact, err := e.store.ArtifactByID(ctx, gate.ArtifactID)
		if err != nil {
			return err
		}
		_, _, err = e.store.IngestKnowledge(ctx, store.KnowledgeSource{ProjectID: workflow.GitLabProjectID, SourceType: "APPROVED_ARTIFACT",
			SourceKey: artifact.ID, SourceVersion: fmt.Sprintf("%d", artifact.Version), Title: string(artifact.Type), AuthorityLevel: 90,
			AccessScope: map[string]any{"gitlab_project_id": workflow.GitLabProjectID}, Content: artifact.Markdown + "\n" + string(artifact.Content), ParentPath: string(artifact.Type)})
		if err != nil {
			return fmt.Errorf("index approved artifact: %w", err)
		}
	}
	ack := fmt.Sprintf("Gate `%s` recorded `%s` from @%s.", gate.ID, event.Command.Action, event.Username)
	if err := e.queueStatusNote(ctx, workflow, "gate-decision:"+gate.ID, "<!-- ai-factory:decision:"+gate.ID+" -->", ack); err != nil {
		return err
	}
	if event.Command.Action == domain.ActionApprove {
		return e.advanceApprovedGate(ctx, workflow, gate)
	}
	return e.reworkGate(ctx, workflow, gate, event.Command.Feedback)
}

func (e *Engine) resumeRecordedDecision(ctx context.Context, workflow domain.Workflow, gate domain.Gate, event webhook.GateNote) error {
	expected := domain.GateApproved
	switch event.Command.Action {
	case domain.ActionRequestChanges:
		expected = domain.GateChangesRequested
	case domain.ActionReject:
		expected = domain.GateRejected
	case domain.ActionApprove:
	default:
		return fmt.Errorf("unsupported gate action %q", event.Command.Action)
	}
	if gate.Status != expected {
		// A stale or conflicting command cannot rewrite an already recorded Engineer decision.
		return nil
	}
	if event.Command.Action == domain.ActionApprove {
		switch gate.Type {
		case domain.GateRequirement:
			if workflow.State != domain.StateWaitingRequirementReview {
				return nil
			}
		case domain.GatePRD, domain.GateTest:
			if workflow.State != domain.StateWaitingPRDAndTestReview {
				return nil
			}
		case domain.GateArchitecture:
			if workflow.State != domain.StateWaitingArchitectureReview {
				return nil
			}
		case domain.GateCodeReview:
			if workflow.State != domain.StateExecutingWorkItems {
				return nil
			}
		case domain.GateRelease:
			if workflow.State != domain.StateWaitingReleaseApproval {
				return nil
			}
		}
		return e.advanceApprovedGate(ctx, workflow, gate)
	}
	switch gate.Type {
	case domain.GateRequirement:
		if workflow.State != domain.StateWaitingRequirementReview {
			return nil
		}
	case domain.GatePRD, domain.GateTest:
		if workflow.State != domain.StateWaitingPRDAndTestReview {
			return nil
		}
	case domain.GateArchitecture:
		if workflow.State != domain.StateWaitingArchitectureReview {
			return nil
		}
	case domain.GateCodeReview:
		if workflow.State != domain.StateExecutingWorkItems {
			return nil
		}
	case domain.GateRelease:
		if workflow.State != domain.StateWaitingReleaseApproval {
			return nil
		}
	}
	return e.reworkGate(ctx, workflow, gate, gate.Feedback)
}

func (e *Engine) rejectUnauthorizedGateCommand(ctx context.Context, workflow domain.Workflow, gate domain.Gate,
	event webhook.GateNote, reason string) error {
	_ = e.store.AddAudit(ctx, workflow.ID, "gate.unauthorized_attempt", event.UserID,
		map[string]any{"gate_id": gate.ID, "reason": reason, "username": event.Username})
	return e.queueStatusNote(ctx, workflow, fmt.Sprintf("unauthorized:%d:%s", event.NoteID, gate.ID),
		fmt.Sprintf("<!-- ai-factory:unauthorized:%d -->", event.NoteID),
		fmt.Sprintf("@%s cannot decide Gate `%s`: %s.", event.Username, gate.ID, reason))
}

func (e *Engine) advanceApprovedGate(ctx context.Context, workflow domain.Workflow, gate domain.Gate) error {
	switch gate.Type {
	case domain.GateRequirement:
		if err := e.store.Transition(ctx, workflow.ID, domain.StateMaterializingWorkItems,
			"requirement gate approved", map[string]any{"gate_id": gate.ID}); err != nil {
			return err
		}
		artifact, err := e.store.LatestArtifact(ctx, workflow.ID, domain.ArtifactRequirement)
		if err != nil {
			return err
		}
		var review agents.RequirementReview
		if err := json.Unmarshal(artifact.Content, &review); err != nil {
			return err
		}
		project := e.projects[workflow.GitLabProjectID]
		var acceptanceIDs []string
		for _, criterion := range review.AcceptanceCriteria {
			acceptanceIDs = append(acceptanceIDs, criterion.ID)
		}
		var domainItems []domain.WorkItem
		dependencies := make(map[string][]string)
		defaultBranch := project.DefaultBranch
		if defaultBranch == "" {
			defaultBranch = "master"
		}
		targetBranch := defaultBranch
		if project.IntegrationBranch && len(review.WorkItems) > 1 {
			targetBranch = fmt.Sprintf("ai/workflow/%d", workflow.IssueIID)
		}
		for _, item := range review.WorkItems {
			domainItems = append(domainItems, domain.WorkItem{
				ID: uuid.NewString(), WorkflowID: workflow.ID, Key: item.Key, Title: item.Title,
				State: domain.WorkItemPlanned, OwnerRole: item.OwnerRole,
				AssigneeID: project.OwnerIDs[item.OwnerRole], TargetBranch: targetBranch,
				AcceptanceIDs: append([]string(nil), acceptanceIDs...), Revision: 1,
			})
			dependencies[item.Key] = append([]string(nil), item.Dependencies...)
		}
		if project.IntegrationBranch && len(review.WorkItems) > 1 {
			var childKeys []string
			for _, item := range review.WorkItems {
				childKeys = append(childKeys, item.Key)
			}
			integrationBranch := fmt.Sprintf("ai/workflow/%d", workflow.IssueIID)
			domainItems = append(domainItems, domain.WorkItem{
				ID: uuid.NewString(), WorkflowID: workflow.ID, Key: "__integration",
				GitLabIssueIID: workflow.IssueIID, Title: "[Tech][Integration] Assemble approved work items",
				State: domain.WorkItemPlanned, OwnerRole: "fullstack", AssigneeID: project.OwnerIDs["fullstack"],
				BranchName: integrationBranch, TargetBranch: defaultBranch,
				AcceptanceIDs: append([]string(nil), acceptanceIDs...), Revision: 1,
			})
			dependencies["__integration"] = childKeys
		}
		if err := e.store.SaveWorkItems(ctx, workflow.ID, domainItems, dependencies); err != nil {
			return err
		}
		savedItems, err := e.store.ListWorkItems(ctx, workflow.ID)
		if err != nil {
			return err
		}
		savedByKey := make(map[string]domain.WorkItem, len(savedItems))
		for _, item := range savedItems {
			savedByKey[item.Key] = item
		}
		for _, item := range review.WorkItems {
			saved := savedByKey[item.Key]
			marker := fmt.Sprintf("<!-- ai-factory:work-item:%s:%s -->", workflow.ID, item.Key)
			description := renderChildIssue(workflow, item, artifact)
			payload := CreateIssuePayload{
				ProjectID: workflow.GitLabProjectID, ParentIID: workflow.IssueIID,
				Title: item.Title, Description: description, Marker: marker,
				Labels:     []string{"automation::human-required", "stage::planning"},
				AssigneeID: project.OwnerIDs[item.OwnerRole],
				WorkItemID: saved.ID, BranchSlug: item.Key,
			}
			if err := e.store.EnqueueOutbox(ctx, "work-item:"+workflow.ID+":"+item.Key, MessageCreateIssue, payload); err != nil {
				return err
			}
		}
		return e.store.EnqueueEvent(ctx, "generate-plans:"+gate.ID, "workflow.generate_plans",
			GeneratePlansEvent{WorkflowID: workflow.ID, GateID: gate.ID}, time.Now().UTC().Add(2*time.Second))
	case domain.GatePRD, domain.GateTest:
		open, err := e.store.OpenGates(ctx, workflow.ID)
		if err != nil {
			return err
		}
		if len(open) != 0 {
			return nil
		}
		if err := e.store.Transition(ctx, workflow.ID, domain.StateReadyForArchitecture,
			"PRD and test gates approved", map[string]any{"last_gate_id": gate.ID}); err != nil {
			return err
		}
		project := e.projects[workflow.GitLabProjectID]
		if !project.FullLifecycle {
			return e.queueStatusNote(ctx, workflow, "ready:"+workflow.ID,
				"<!-- ai-factory:ready-for-architecture -->",
				"## Ready for architecture\n\nThe Requirement, PRD, and Test Gates are approved. Full lifecycle automation is disabled for this project.")
		}
		return e.store.EnqueueEvent(ctx, "generate-architecture:"+workflow.ID+":"+fmt.Sprint(workflow.Revision),
			"workflow.generate_architecture", GenerateArchitectureEvent{WorkflowID: workflow.ID}, time.Now().UTC())
	case domain.GateArchitecture:
		if err := e.store.Transition(ctx, workflow.ID, domain.StatePlanning,
			"architecture gate approved", map[string]any{"gate_id": gate.ID}); err != nil {
			return err
		}
		project := e.projects[workflow.GitLabProjectID]
		if project.IntegrationBranch {
			ref := project.DefaultBranch
			if ref == "" {
				ref = "master"
			}
			branch := fmt.Sprintf("ai/workflow/%d", workflow.IssueIID)
			if err := e.store.EnqueueOutbox(ctx, "ensure-integration-branch:"+workflow.ID,
				MessageEnsureBranch, EnsureBranchPayload{
					ProjectID: workflow.GitLabProjectID, Branch: branch, Ref: ref,
				}); err != nil {
				return err
			}
		}
		if err := e.store.ActivateWorkItems(ctx, workflow.ID); err != nil {
			return err
		}
		items, err := e.store.ListWorkItems(ctx, workflow.ID)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Key == "__integration" {
				continue
			}
			if item.GitLabIssueIID == 0 {
				return fmt.Errorf("work item %s has not been materialized", item.ID)
			}
			if item.State == domain.WorkItemReadyForCodex {
				if err := e.store.EnqueueOutbox(ctx, "ready-work-item:"+item.ID, MessageUpdateIssue, UpdateIssuePayload{
					ProjectID: workflow.GitLabProjectID, IssueIID: item.GitLabIssueIID,
					AddLabels: []string{"ai::ready-for-codex"}, RemoveLabels: []string{"stage::planning"},
				}); err != nil {
					return err
				}
				body := fmt.Sprintf("## Ready for Codex\n\nWork item `%s` is assigned to GitLab user `%d` and all dependencies are satisfied.\n\n"+
					"- Branch: `%s`\n- MR target: `%s`\n\n"+
					"Start it from the assigned engineer's visible Dispatcher task with:\n\n`/start-codex task:%s client:<client-id>`\n\n"+
					"The Factory records the dispatch but never runs code headlessly.",
					item.Key, item.AssigneeID, item.BranchName, item.TargetBranch, item.ID)
				if err := e.store.EnqueueOutbox(ctx, "ready-note:"+item.ID, MessageUpsertNote, UpsertNotePayload{
					ProjectID: workflow.GitLabProjectID, IssueIID: item.GitLabIssueIID,
					Marker: "<!-- ai-factory:ready:" + item.ID + " -->", Body: body,
				}); err != nil {
					return err
				}
			}
		}
		if err := e.store.Transition(ctx, workflow.ID, domain.StateExecutingWorkItems,
			"implementation plan activated", map[string]any{"work_item_count": len(items)}); err != nil {
			return err
		}
		return e.queueStatusNote(ctx, workflow, "execution:"+workflow.ID,
			"<!-- ai-factory:execution -->",
			"## Implementation execution started\n\nReady work items are discoverable by each engineer's Codex Dispatcher. Engineer Gates remain authoritative.")
	case domain.GateCodeReview:
		return e.approveCodeReview(ctx, workflow, gate)
	case domain.GateRelease:
		project := e.projects[workflow.GitLabProjectID]
		if project.ProductionEnabled {
			if err := e.store.Transition(ctx, workflow.ID, domain.StateProductionDeploying,
				"release gate approved for production", map[string]any{"gate_id": gate.ID}); err != nil {
				return err
			}
			_, _, sha, _, err := e.store.LatestReleaseCandidate(ctx, workflow.ID)
			if err != nil {
				return err
			}
			if err := e.queueDeliveryRequest(ctx, workflow, "production_deploy", sha, "production"); err != nil {
				return err
			}
			return e.queueStatusNote(ctx, workflow, "production-deploy:"+workflow.ID,
				"<!-- ai-factory:production-deploy -->",
				"## Production deployment authorized\n\nThe production adapter may proceed for the approved release candidate.")
		}
		if err := e.store.Transition(ctx, workflow.ID, domain.StateObserving,
			"release gate approved; production adapter is locked", map[string]any{"gate_id": gate.ID}); err != nil {
			return err
		}
		if err := e.store.StartObservation(ctx, workflow.ID, "", 30*time.Minute,
			map[string]any{"mode": "test-staging", "production_enabled": false}); err != nil {
			return err
		}
		return e.queueStatusNote(ctx, workflow, "observe:"+workflow.ID,
			"<!-- ai-factory:observing -->",
			"## Observation window started\n\nProduction credentials are disabled. V2 test mode observes the approved staging deployment.")
	case domain.GateProductionMigration:
		artifact, err := e.store.GetArtifact(ctx, gate.ArtifactID)
		if err != nil {
			return err
		}
		var callback webhook.ExternalCallback
		if err := json.Unmarshal(artifact.Content, &callback); err != nil {
			return fmt.Errorf("decode production migration evidence: %w", err)
		}
		return e.openReleaseGate(ctx, workflow, callback)
	case domain.GateIncident:
		return e.queueStatusNote(ctx, workflow, "special-gate-approved:"+gate.ID,
			"<!-- ai-factory:special-gate:"+gate.ID+" -->",
			"Incident remediation was authorized. Factory records the decision, but the visible remediation task must still produce reviewed code and delivery evidence.")
	default:
		return fmt.Errorf("unsupported gate type %q", gate.Type)
	}
}

func renderChildIssue(workflow domain.Workflow, item agents.WorkItem, artifact domain.Artifact) string {
	return fmt.Sprintf("## Goal\n\nDeliver the independently reviewable boundary approved in parent Issue #%d.\n\n"+
		"## Delivery boundary\n\n%s\n\n## Rationale\n\n%s\n\n## Dependencies\n\n%s\n\n"+
		"## Traceability\n\n- Parent Issue: #%d\n- Factory workflow: `%s`\n- Requirement artifact: `%s` v%d\n- Source hash: `%s`\n\n"+
		"## Authorization\n\nThis Issue records an approved delivery boundary. Coding still requires the later architecture/plan Gates.\n",
		workflow.IssueIID, item.IndependentBoundary, item.Rationale,
		strings.Join(item.Dependencies, "\n- "), workflow.IssueIID, workflow.ID,
		artifact.Type, artifact.Version, artifact.SourceHash)
}

func (e *Engine) reworkGate(ctx context.Context, workflow domain.Workflow, gate domain.Gate, feedback string) error {
	project := e.projects[workflow.GitLabProjectID]
	switch gate.Type {
	case domain.GateRequirement:
		if err := e.store.Transition(ctx, workflow.ID, domain.StateRequirementAnalysis,
			"requirement gate returned for rework", map[string]any{"gate_id": gate.ID}); err != nil {
			return err
		}
		workflow.State = domain.StateRequirementAnalysis
		snapshots, err := e.store.LatestSnapshots(ctx, workflow.ID)
		if err != nil {
			return err
		}
		return e.publishRequirementGate(ctx, workflow, project, snapshots, feedback)
	case domain.GatePRD, domain.GateTest:
		if err := e.store.Transition(ctx, workflow.ID, domain.StatePRDGenerating,
			"planning artifact returned for rework", map[string]any{"gate_id": gate.ID, "gate_type": gate.Type}); err != nil {
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
		var prdFeedback, testFeedback string
		if gate.Type == domain.GatePRD {
			prdFeedback = feedback
		} else {
			testFeedback = feedback
		}
		// Rebuild both artifacts so their shared requirement baseline and traceability stay consistent.
		return e.publishPlanningGates(ctx, workflow, project, snapshots, requirement, prdFeedback, testFeedback)
	case domain.GateArchitecture:
		if err := e.store.Transition(ctx, workflow.ID, domain.StateArchitectureGenerating,
			"architecture gate returned for rework", map[string]any{"gate_id": gate.ID}); err != nil {
			return err
		}
		return e.store.EnqueueEvent(ctx, "regenerate-architecture:"+gate.ID, "workflow.generate_architecture",
			GenerateArchitectureEvent{WorkflowID: workflow.ID, Feedback: feedback}, time.Now().UTC())
	case domain.GateCodeReview:
		artifact, err := e.store.GetArtifact(ctx, gate.ArtifactID)
		if err != nil {
			return err
		}
		var report qualityArtifact
		if err := json.Unmarshal(artifact.Content, &report); err != nil {
			return err
		}
		if err := e.store.SetWorkItemState(ctx, report.WorkItemID, domain.WorkItemRework,
			map[string]any{"gate_id": gate.ID, "feedback": feedback}); err != nil {
			return err
		}
		return e.queueWorkItemNote(ctx, workflow, report.WorkItemID, "code-review-rework:"+gate.ID,
			"<!-- ai-factory:code-review-rework:"+gate.ID+" -->",
			"## Code Review changes requested\n\n"+feedback+"\n\nThe Coding task must update the MR; a new commit invalidates the previous approval.")
	case domain.GateRelease:
		if err := e.store.Transition(ctx, workflow.ID, domain.StateAssemblingRelease,
			"release gate returned for rework", map[string]any{"gate_id": gate.ID, "feedback": feedback}); err != nil {
			return err
		}
		return e.queueStatusNote(ctx, workflow, "release-rework:"+gate.ID,
			"<!-- ai-factory:release-rework:"+gate.ID+" -->", "## Release changes requested\n\n"+feedback)
	case domain.GateProductionMigration:
		artifact, err := e.store.GetArtifact(ctx, gate.ArtifactID)
		if err != nil {
			return err
		}
		var callback webhook.ExternalCallback
		if err := json.Unmarshal(artifact.Content, &callback); err != nil {
			return fmt.Errorf("decode production migration evidence: %w", err)
		}
		return e.openProductionMigrationGate(ctx, workflow, callback, feedback)
	case domain.GateIncident:
		artifact, err := e.store.GetArtifact(ctx, gate.ArtifactID)
		if err != nil {
			return err
		}
		var callback webhook.ExternalCallback
		if err := json.Unmarshal(artifact.Content, &callback); err != nil {
			return fmt.Errorf("decode incident evidence: %w", err)
		}
		return e.openIncidentGate(ctx, workflow, callback, feedback)
	default:
		return fmt.Errorf("unsupported gate type %q", gate.Type)
	}
}
