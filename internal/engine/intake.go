package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/connectors/confluence"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
	"github.com/google/uuid"
)

// intakeModule owns external requirement intake and authoritative source refresh.
type intakeModule struct{ engine *Engine }

func (e *Engine) intake() intakeModule { return intakeModule{engine: e} }

func (m intakeModule) issueChanged(ctx context.Context, event webhook.IssueChanged) error {
	return m.engine.handleIssueChanged(ctx, event)
}

func (e *Engine) handleIssueChanged(ctx context.Context, event webhook.IssueChanged) error {
	project, ok := e.projects[event.ProjectID]
	if !ok {
		return fmt.Errorf("project %d is not configured", event.ProjectID)
	}
	issue, err := e.gitlab.GetIssue(ctx, event.ProjectID, event.IssueIID)
	if err != nil {
		return err
	}
	if issue.State != "opened" || !issue.HasLabel(project.EnabledLabel) {
		return nil
	}
	workflow, err := e.store.GetOrCreateWorkflow(ctx, domain.NewWorkflow(event.ProjectID, event.IssueIID, issue.Title))
	if err != nil {
		return err
	}
	if e.v3.RAG {
		_, _, err := e.store.IngestKnowledge(ctx, store.KnowledgeSource{ProjectID: event.ProjectID, SourceType: "GITLAB_ISSUE",
			SourceKey: fmt.Sprintf("%d/%d", event.ProjectID, event.IssueIID), SourceVersion: event.EventID, Title: issue.Title,
			AuthorityLevel: 60, AccessScope: map[string]any{"gitlab_project_id": event.ProjectID},
			Content: issue.Title + "\n\n" + issue.Description, ParentPath: "GitLab Issue"})
		if err != nil {
			return fmt.Errorf("index GitLab issue: %w", err)
		}
	}
	switch workflow.State {
	case domain.StateNew:
		if err := e.store.Transition(ctx, workflow.ID, domain.StateIngesting, "eligible intake issue received", nil); err != nil {
			return err
		}
		workflow.State = domain.StateIngesting
	case domain.StateWaitingRequirementReview, domain.StateWaitingPRDAndTestReview, domain.StateReadyForArchitecture:
	case domain.StateIngesting:
	case domain.StateRequirementAnalysis, domain.StateMaterializingWorkItems, domain.StatePRDGenerating:
		return nil
	case domain.StateArchitectureGenerating, domain.StateWaitingArchitectureReview, domain.StatePlanning,
		domain.StateExecutingWorkItems, domain.StateAssemblingRelease, domain.StateReleaseCIRunning,
		domain.StateStagingDeploying, domain.StateStagingVerifying, domain.StateWaitingReleaseApproval,
		domain.StateProductionDeploying, domain.StateObserving, domain.StateCompleted,
		domain.StatePaused, domain.StateCancelled:
		_ = e.store.AddAudit(ctx, workflow.ID, "intake.change_after_architecture", 0,
			map[string]any{"state": workflow.State, "issue_event": event.EventID})
		return nil
	default:
		return fmt.Errorf("workflow %s has unsupported state %s", workflow.ID, workflow.State)
	}

	pageIDs := confluence.ExtractPageReferences(issue.Description)
	if len(pageIDs) == 0 {
		payload := UpsertNotePayload{
			ProjectID: issue.ProjectID, IssueIID: issue.IID,
			Marker: "<!-- ai-factory:intake-error -->",
			Body:   "## AI Factory intake blocked\n\nNo Confluence page URL was found in this Issue. Add at least one authoritative page URL and update the Issue.\n",
		}
		_ = e.store.AddAudit(ctx, workflow.ID, "intake.blocked", 0, map[string]any{"reason": "missing_confluence_reference"})
		return e.store.EnqueueOutbox(ctx, "intake-error:"+workflow.ID, MessageUpsertNote, payload)
	}

	snapshots, sourceHash, err := e.ingestSnapshots(ctx, workflow, pageIDs)
	if err != nil {
		return err
	}
	if workflow.SourceHash == sourceHash && workflow.Revision > 1 {
		return nil
	}
	if workflow.State != domain.StateIngesting {
		if err := e.store.Transition(ctx, workflow.ID, domain.StateIngesting, "intake issue or authoritative source changed",
			map[string]any{"source_hash": sourceHash}); err != nil {
			return err
		}
		workflow.State = domain.StateIngesting
	}
	if err := e.store.Transition(ctx, workflow.ID, domain.StateRequirementAnalysis, "authoritative source snapshot captured",
		map[string]any{"source_hash": sourceHash, "page_count": len(snapshots)}); err != nil {
		return err
	}
	workflow.State = domain.StateRequirementAnalysis
	workflow.SourceHash = sourceHash
	if e.v3.Registry {
		return e.store.EnqueueEvent(ctx, "analyze-requirement:"+workflow.ID+":"+sourceHash, "workflow.analyze_requirement",
			AnalyzeRequirementEvent{WorkflowID: workflow.ID}, time.Now().UTC())
	}
	return e.publishRequirementGate(ctx, workflow, project, snapshots, "")
}

func (e *Engine) ingestSnapshots(ctx context.Context, workflow domain.Workflow, pageIDs []string) ([]domain.Snapshot, string, error) {
	var snapshots []domain.Snapshot
	for _, pageID := range pageIDs {
		page, err := e.confluence.FetchPage(ctx, pageID)
		if err != nil {
			return nil, "", err
		}
		for index := range page.Images {
			image := &page.Images[index]
			content, err := e.confluence.Download(ctx, image.DownloadURL)
			if err != nil {
				return nil, "", err
			}
			image.ContentHash = confluence.ContentHash(content)
			url, markdown, err := e.gitlab.Upload(ctx, workflow.GitLabProjectID,
				confluence.SafeFilename(image.Filename), image.MediaType, content)
			if err != nil {
				return nil, "", err
			}
			image.GitLabURL = url
			image.Markdown = markdown
		}
		snapshot := domain.Snapshot{
			ID: uuid.NewString(), WorkflowID: workflow.ID, ConfluencePageID: page.ID,
			Version: page.Version, Title: page.Title, URL: page.URL, UpdatedAt: page.UpdatedAt,
			ContentHash: page.ContentHash, NormalizedText: page.NormalizedText,
			RawStorage: page.Storage, Images: page.Images,
		}
		snapshots = append(snapshots, snapshot)
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].ConfluencePageID < snapshots[j].ConfluencePageID })
	hash := combinedHash(snapshots)
	for _, snapshot := range snapshots {
		if err := e.store.SaveSnapshot(ctx, snapshot); err != nil {
			return nil, "", err
		}
		if e.v3.RAG {
			_, _, err := e.store.IngestKnowledge(ctx, store.KnowledgeSource{
				ProjectID: workflow.GitLabProjectID, SourceType: "CONFLUENCE", SourceKey: snapshot.ConfluencePageID,
				SourceVersion: fmt.Sprintf("%d", snapshot.Version), Title: snapshot.Title, AuthorityLevel: 100,
				AccessScope: map[string]any{"gitlab_project_id": workflow.GitLabProjectID},
				Content:     snapshot.NormalizedText, ParentPath: snapshot.Title,
			})
			if err != nil {
				return nil, "", fmt.Errorf("index Confluence snapshot %s: %w", snapshot.ConfluencePageID, err)
			}
		}
	}
	return snapshots, hash, nil
}
