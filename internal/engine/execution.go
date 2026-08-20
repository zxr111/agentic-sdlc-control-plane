package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
	"github.com/google/uuid"
)

// executionModule owns engineer-visible coding dispatch and work-item lifecycle.
type executionModule struct{ engine *Engine }

func (e *Engine) execution() executionModule { return executionModule{engine: e} }

func (m executionModule) control(ctx context.Context, event webhook.ControlNote) error {
	return m.engine.handleControlCommand(ctx, event)
}

func (m executionModule) lifecycle(ctx context.Context, event webhook.LifecycleEvent) error {
	return m.engine.handleLifecycleEvent(ctx, event)
}

func (e *Engine) handleControlCommand(ctx context.Context, event webhook.ControlNote) error {
	if event.EmailRelayHash != "" {
		if err := e.store.RecordEmailRelay(ctx, event.EmailRelayHash, event.UserID, event.NoteID,
			string(event.Command.Action), "RECEIVED"); err != nil {
			return err
		}
	}
	active, err := e.gitlab.IsActiveProjectMember(ctx, event.ProjectID, event.UserID)
	if err != nil {
		return err
	}
	if !active {
		return errors.New("control command actor is not an active project member")
	}
	switch event.Command.Action {
	case domain.ControlStartCodex:
		item, err := e.store.GetWorkItem(ctx, event.Command.WorkItemID)
		if err != nil {
			return err
		}
		workflow, err := e.store.GetWorkflow(ctx, item.WorkflowID)
		if err != nil {
			return err
		}
		if workflow.GitLabProjectID != event.ProjectID || item.GitLabIssueIID != event.IssueIID {
			return errors.New("start command must be posted on the target work-item Issue")
		}
		dispatch, created, err := e.store.StartCodex(ctx, item.ID, event.Command.ClientID, event.UserID)
		if err != nil {
			return e.queueWorkItemNote(ctx, workflow, item.ID, fmt.Sprintf("start-rejected:%d", event.NoteID),
				fmt.Sprintf("<!-- ai-factory:start-rejected:%d -->", event.NoteID),
				fmt.Sprintf("## Codex start rejected\n\n@%s: %s", event.Username, err))
		}
		if created {
			if err := e.store.EnqueueOutbox(ctx, "coding-label:"+item.ID, MessageUpdateIssue, UpdateIssuePayload{
				ProjectID: event.ProjectID, IssueIID: event.IssueIID, AddLabels: []string{"ai::coding"},
				RemoveLabels: []string{"ai::ready-for-codex"},
			}); err != nil {
				return err
			}
		}
		return e.queueWorkItemNote(ctx, workflow, item.ID, "dispatch:"+dispatch.ID,
			"<!-- ai-factory:dispatch:"+dispatch.ID+" -->",
			fmt.Sprintf("## Codex dispatch recorded\n\n- Dispatch: `%s`\n- Client: `%s`\n- Engineer: @%s\n- Created now: `%t`\n\n"+
				"The engineer's visible Codex task may now create its worktree and begin coding. This record has no lease or timeout.",
				dispatch.ID, dispatch.ClientID, event.Username, created))
	case domain.ControlResetCodex:
		item, err := e.store.GetWorkItem(ctx, event.Command.WorkItemID)
		if err != nil {
			return err
		}
		workflow, err := e.store.GetWorkflow(ctx, item.WorkflowID)
		if err != nil {
			return err
		}
		if workflow.GitLabProjectID != event.ProjectID || item.GitLabIssueIID != event.IssueIID {
			return errors.New("reset command must be posted on the target work-item Issue")
		}
		if err := e.store.ResetCodex(ctx, item.ID, event.Command.Reason, event.UserID); err != nil {
			return err
		}
		return e.queueWorkItemNote(ctx, workflow, item.ID, fmt.Sprintf("dispatch-reset:%d", event.NoteID),
			fmt.Sprintf("<!-- ai-factory:dispatch-reset:%d -->", event.NoteID),
			"## Codex dispatch reset\n\nReason: "+event.Command.Reason+"\n\nThe assigned engineer may start a new visible Coding task.")
	case domain.ControlPause, domain.ControlResume, domain.ControlCancel:
		workflow, err := e.store.GetWorkflow(ctx, event.Command.WorkflowID)
		if err != nil {
			return err
		}
		if workflow.GitLabProjectID != event.ProjectID || workflow.IssueIID != event.IssueIID {
			return errors.New("workflow command must be posted on the parent Issue")
		}
		if !projectAuthorizesControl(e.projects[event.ProjectID], event.UserID) {
			return errors.New("workflow control actor is not in the configured Engineer allowlist")
		}
		if err := e.store.SuspendWorkflow(ctx, workflow.ID, event.Command.Action, event.Command.Reason, event.UserID); err != nil {
			return err
		}
		return e.queueStatusNote(ctx, workflow, fmt.Sprintf("workflow-control:%d", event.NoteID),
			fmt.Sprintf("<!-- ai-factory:workflow-control:%d -->", event.NoteID),
			fmt.Sprintf("Workflow `%s` recorded `%s` from @%s. Reason: %s",
				workflow.ID, event.Command.Action, event.Username, event.Command.Reason))
	default:
		return fmt.Errorf("unsupported control action %q", event.Command.Action)
	}
}

func projectAuthorizesControl(project domain.ProjectConfig, userID int64) bool {
	for _, ids := range project.ReviewerIDs {
		for _, id := range ids {
			if id == userID {
				return true
			}
		}
	}
	for _, id := range project.OwnerIDs {
		if id == userID {
			return true
		}
	}
	return false
}

func (e *Engine) handleLifecycleEvent(ctx context.Context, event webhook.LifecycleEvent) error {
	switch event.ObjectKind {
	case "push", "tag_push":
		if !e.v3.RAG {
			return nil
		}
		if _, configured := e.projects[event.ProjectID]; !configured {
			return nil
		}
		return e.ingestRepositoryKnowledge(ctx, event)
	case "merge_request":
		mr, err := e.gitlab.GetMergeRequest(ctx, event.ProjectID, event.MergeRequestIID)
		if err != nil {
			return err
		}
		item, err := e.store.GetWorkItemByBranch(ctx, event.ProjectID, mr.SourceBranch)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		previous, previousErr := e.store.GetMergeRequestByWorkItem(ctx, item.ID)
		if previousErr != nil && !errors.Is(previousErr, store.ErrNotFound) {
			return previousErr
		}
		headChanged := previousErr == nil && previous.HeadSHA != "" && previous.HeadSHA != mr.SHA
		if err := e.store.UpsertMergeRequest(ctx, store.MergeRequestRecord{
			ID: uuid.NewString(), WorkItemID: item.ID, GitLabMRIID: mr.IID,
			SourceBranch: mr.SourceBranch, TargetBranch: mr.TargetBranch, HeadSHA: mr.SHA,
			State: mr.State, Draft: mr.Draft, WebURL: mr.WebURL,
		}); err != nil {
			return err
		}
		workflow, err := e.store.GetWorkflow(ctx, item.WorkflowID)
		if err != nil {
			return err
		}
		if e.v3.RAG {
			content := fmt.Sprintf("Merge Request !%d\nTitle: %s\nState: %s\nSource: %s\nTarget: %s\nHead SHA: %s\nURL: %s",
				mr.IID, mr.Title, mr.State, mr.SourceBranch, mr.TargetBranch, mr.SHA, mr.WebURL)
			if _, _, err := e.store.IngestKnowledge(ctx, store.KnowledgeSource{ProjectID: event.ProjectID, SourceType: "GITLAB_MR",
				SourceKey: fmt.Sprintf("%d/%d", event.ProjectID, mr.IID), SourceVersion: mr.SHA + ":" + mr.State, Title: mr.Title,
				AuthorityLevel: 50, AccessScope: map[string]any{"gitlab_project_id": event.ProjectID}, Content: content, ParentPath: "GitLab MR"}); err != nil {
				return fmt.Errorf("index GitLab MR: %w", err)
			}
		}
		if mr.State == "merged" || event.Action == "merge" {
			if item.State == domain.WorkItemMerged || item.State == domain.WorkItemCompleted {
				return nil
			}
			if item.State != domain.WorkItemMergeQueued {
				if err := e.store.SetWorkItemState(ctx, item.ID, domain.WorkItemBlocked,
					map[string]any{"reason": "MR merged without an exact-SHA Code Review Gate", "mr_iid": mr.IID}); err != nil {
					return err
				}
				return e.queueWorkItemNote(ctx, workflow, item.ID, "unauthorized-merge:"+fmt.Sprint(mr.IID),
					"<!-- ai-factory:unauthorized-merge:"+fmt.Sprint(mr.IID)+" -->",
					"## Delivery blocked\n\nGitLab reports this MR as merged before Factory recorded an exact-SHA Engineer Code Review Gate. "+
						"Factory will not advance the release. An engineer must investigate and explicitly recover the workflow.")
			}
			if err := e.store.SetWorkItemState(ctx, item.ID, domain.WorkItemMerged,
				map[string]any{"mr_iid": mr.IID, "head_sha": mr.SHA}); err != nil {
				return err
			}
			if err := e.store.UnblockDependents(ctx, item.ID); err != nil {
				return err
			}
			if err := e.publishNewlyReadyWorkItems(ctx, workflow); err != nil {
				return err
			}
			incomplete, err := e.store.CountIncompleteWorkItems(ctx, workflow.ID)
			if err != nil {
				return err
			}
			if incomplete == 0 && workflow.State == domain.StateExecutingWorkItems {
				if err := e.store.Transition(ctx, workflow.ID, domain.StateAssemblingRelease,
					"all approved work items merged", nil); err != nil {
					return err
				}
				if err := e.store.Transition(ctx, workflow.ID, domain.StateReleaseCIRunning,
					"release candidate CI requested", nil); err != nil {
					return err
				}
				if err := e.queueDeliveryRequest(ctx, workflow, "release_ci", "", ""); err != nil {
					return err
				}
				return e.queueStatusNote(ctx, workflow, "release-ci:"+workflow.ID,
					"<!-- ai-factory:release-ci -->",
					"## Release CI requested\n\nAll work items are merged. Jenkins/GitLab CI must report the immutable release candidate before staging deployment.")
			}
			return nil
		}
		state := domain.WorkItemDraftMR
		if !mr.Draft {
			state = domain.WorkItemAIQualityChecks
		}
		if headChanged && (item.State == domain.WorkItemWaitingCodeReview || item.State == domain.WorkItemMergeQueued) {
			if err := e.store.SetWorkItemState(ctx, item.ID, domain.WorkItemRework,
				map[string]any{"reason": "MR head changed", "previous_sha": previous.HeadSHA, "head_sha": mr.SHA}); err != nil {
				return err
			}
			item.State = domain.WorkItemRework
		}
		if item.State == domain.WorkItemWaitingCodeReview || item.State == domain.WorkItemMergeQueued ||
			item.State == domain.WorkItemBlocked {
			return nil
		}
		if err := e.store.SetWorkItemState(ctx, item.ID, state,
			map[string]any{"mr_iid": mr.IID, "head_sha": mr.SHA, "draft": mr.Draft}); err != nil {
			return err
		}
		if !mr.Draft {
			return e.queueWorkItemNote(ctx, workflow, item.ID, "quality-request:"+mr.SHA,
				"<!-- ai-factory:quality-request:"+mr.SHA+" -->",
				fmt.Sprintf("## Independent AI Quality task required\n\nMR !%d at exact head `%s` needs a separate visible Quality task. "+
					"It must submit the structured quality callback; a new commit invalidates this result.", mr.IID, mr.SHA))
		}
		return nil
	case "pipeline", "build":
		if event.SourceBranch == "" {
			return nil
		}
		item, err := e.store.GetWorkItemByBranch(ctx, event.ProjectID, event.SourceBranch)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if event.ObjectID != 0 {
			if err := e.store.UpsertPipelineRun(ctx, item.WorkflowID, item.ID, event.ObjectID,
				event.SourceBranch, event.SHA, event.Status, event.URL); err != nil {
				return err
			}
		}
		if e.v3.RAG {
			content := fmt.Sprintf("Pipeline %d\nBranch: %s\nCommit SHA: %s\nStatus: %s\nURL: %s", event.ObjectID, event.SourceBranch, event.SHA, event.Status, event.URL)
			if _, _, err := e.store.IngestKnowledge(ctx, store.KnowledgeSource{ProjectID: event.ProjectID, SourceType: "GITLAB_PIPELINE",
				SourceKey: fmt.Sprintf("%d/%d", event.ProjectID, event.ObjectID), SourceVersion: event.SHA + ":" + event.Status, Title: fmt.Sprintf("Pipeline %d", event.ObjectID),
				AuthorityLevel: 50, AccessScope: map[string]any{"gitlab_project_id": event.ProjectID}, Content: content, ParentPath: "GitLab Pipeline"}); err != nil {
				return fmt.Errorf("index GitLab pipeline: %w", err)
			}
		}
		if event.Status == "failed" {
			if item.State == domain.WorkItemMerged || item.State == domain.WorkItemCompleted {
				return nil
			}
			return e.store.SetWorkItemState(ctx, item.ID, domain.WorkItemRework,
				map[string]any{"pipeline_status": event.Status, "sha": event.SHA})
		}
		if event.Status == "running" || event.Status == "pending" {
			if item.State == domain.WorkItemWaitingCodeReview || item.State == domain.WorkItemMergeQueued ||
				item.State == domain.WorkItemMerged || item.State == domain.WorkItemCompleted {
				return nil
			}
			return e.store.SetWorkItemState(ctx, item.ID, domain.WorkItemCIRunning,
				map[string]any{"pipeline_status": event.Status, "sha": event.SHA})
		}
		return nil
	default:
		return nil
	}
}

func (e *Engine) ingestRepositoryKnowledge(ctx context.Context, event webhook.LifecycleEvent) error {
	if strings.TrimSpace(event.SHA) == "" || strings.TrimSpace(event.SourceBranch) == "" {
		return errors.New("repository knowledge push requires ref and commit SHA")
	}
	for _, path := range uniqueKnowledgePaths(event.RemovedPaths, 100) {
		if isRepositoryKnowledgePath(path) {
			if err := e.store.RevokeKnowledgeSource(ctx, event.ProjectID, "REPOSITORY_DOC", path); err != nil {
				return err
			}
		}
	}
	changed := append(append([]string{}, event.AddedPaths...), event.ModifiedPaths...)
	for _, path := range uniqueKnowledgePaths(changed, 100) {
		if !isRepositoryKnowledgePath(path) {
			continue
		}
		content, err := e.gitlab.GetRepositoryFile(ctx, event.ProjectID, path, event.SHA)
		if err != nil {
			return fmt.Errorf("fetch repository knowledge %s: %w", path, err)
		}
		if _, _, err := e.store.IngestKnowledge(ctx, store.KnowledgeSource{
			ProjectID: event.ProjectID, SourceType: "REPOSITORY_DOC", SourceKey: path,
			SourceVersion: event.SHA, Title: path, AuthorityLevel: 65,
			AccessScope: map[string]any{"gitlab_project_id": event.ProjectID, "repository_ref": event.SourceBranch},
			Content:     string(content), ParentPath: path,
		}); err != nil {
			return fmt.Errorf("index repository knowledge %s: %w", path, err)
		}
	}
	return nil
}

func uniqueKnowledgePaths(paths []string, limit int) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		result = append(result, path)
		if len(result) >= limit {
			break
		}
	}
	return result
}

func isRepositoryKnowledgePath(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	base := lower
	if index := strings.LastIndex(base, "/"); index >= 0 {
		base = base[index+1:]
	}
	if strings.HasPrefix(base, "readme") || strings.HasPrefix(base, "openapi.") || strings.HasPrefix(base, "swagger.") {
		return true
	}
	if strings.HasPrefix(lower, "docs/") || strings.Contains(lower, "/docs/") ||
		strings.HasPrefix(lower, "migrations/") || strings.Contains(lower, "/migrations/") {
		for _, extension := range []string{".md", ".mdx", ".rst", ".txt", ".adoc", ".yaml", ".yml", ".json", ".sql"} {
			if strings.HasSuffix(lower, extension) {
				return true
			}
		}
	}
	return false
}

type qualityArtifact struct {
	WorkItemID      string                `json:"work_item_id"`
	MergeRequestIID int64                 `json:"merge_request_iid"`
	HeadSHA         string                `json:"head_sha"`
	Verdict         domain.QualityVerdict `json:"verdict"`
	Attempt         int                   `json:"attempt"`
}
