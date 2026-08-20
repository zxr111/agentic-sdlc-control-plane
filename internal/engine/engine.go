package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/agents"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/config"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/connectors/confluence"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/connectors/delivery"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/connectors/gitlab"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/knowledge"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/multiagent"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
	"github.com/google/uuid"
)

const (
	MessageUpsertNote      = "gitlab.upsert_note"
	MessageCreateIssue     = "gitlab.create_issue"
	MessageUpdateIssue     = "gitlab.update_issue"
	MessageEnsureBranch    = "gitlab.ensure_branch"
	MessageCreateMR        = "gitlab.create_merge_request"
	MessageTriggerDelivery = "delivery.trigger"
)

type Engine struct {
	store      *store.Store
	gitlab     *gitlab.Client
	confluence *confluence.Client
	agents     *agents.Client
	projects   map[int64]domain.ProjectConfig
	logger     *slog.Logger
	delivery   *delivery.Client
	v3         config.V3Features
}

func (e *Engine) SetV3Features(features config.V3Features) { e.v3 = features }

func (e *Engine) SetDeliveryClient(client *delivery.Client) {
	e.delivery = client
}

func New(repository *store.Store, gitlabClient *gitlab.Client, confluenceClient *confluence.Client,
	agentClient *agents.Client, projects map[int64]domain.ProjectConfig, logger *slog.Logger) *Engine {
	return &Engine{
		store: repository, gitlab: gitlabClient, confluence: confluenceClient,
		agents: agentClient, projects: projects, logger: logger,
	}
}

type UpsertNotePayload struct {
	ProjectID int64  `json:"project_id"`
	IssueIID  int64  `json:"issue_iid"`
	Marker    string `json:"marker"`
	Body      string `json:"body"`
}

type CreateIssuePayload struct {
	ProjectID   int64    `json:"project_id"`
	ParentIID   int64    `json:"parent_iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
	AssigneeID  int64    `json:"assignee_id"`
	Marker      string   `json:"marker"`
	WorkItemID  string   `json:"work_item_id,omitempty"`
	BranchSlug  string   `json:"branch_slug,omitempty"`
}

type UpdateIssuePayload struct {
	ProjectID    int64    `json:"project_id"`
	IssueIID     int64    `json:"issue_iid"`
	AddLabels    []string `json:"add_labels,omitempty"`
	RemoveLabels []string `json:"remove_labels,omitempty"`
}

type EnsureBranchPayload struct {
	ProjectID int64  `json:"project_id"`
	Branch    string `json:"branch"`
	Ref       string `json:"ref"`
}

type CreateMergeRequestPayload struct {
	ProjectID   int64  `json:"project_id"`
	WorkItemID  string `json:"work_item_id"`
	Title       string `json:"title"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	Description string `json:"description"`
}

type DeliveryRequestPayload struct {
	RequestID   string `json:"request_id"`
	Action      string `json:"action"`
	WorkflowID  string `json:"workflow_id"`
	ProjectID   int64  `json:"project_id"`
	IssueIID    int64  `json:"issue_iid"`
	CommitSHA   string `json:"commit_sha,omitempty"`
	Environment string `json:"environment,omitempty"`
}

func (e *Engine) DeliverOutbox(ctx context.Context, message domain.OutboxMessage) (string, error) {
	switch message.Type {
	case MessageUpsertNote:
		var payload UpsertNotePayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return "", err
		}
		id, err := e.gitlab.UpsertNote(ctx, payload.ProjectID, payload.IssueIID, payload.Marker, payload.Body)
		return gitlab.ExternalID(id), err
	case MessageCreateIssue:
		var payload CreateIssuePayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return "", err
		}
		issue, err := e.gitlab.CreateIssueIdempotent(ctx, gitlab.CreateIssueInput{
			ProjectID: payload.ProjectID, Title: payload.Title, Description: payload.Description,
			Labels: payload.Labels, AssigneeID: payload.AssigneeID, Marker: payload.Marker,
		})
		if err != nil {
			return "", err
		}
		if payload.WorkItemID != "" {
			branch := fmt.Sprintf("ai/%d/%d-%s", payload.ParentIID, issue.IID, sanitizeSlug(payload.BranchSlug))
			if err := e.store.MaterializeWorkItem(ctx, payload.WorkItemID, issue.IID, branch); err != nil {
				return "", err
			}
		}
		return gitlab.ExternalID(issue.IID), nil
	case MessageUpdateIssue:
		var payload UpdateIssuePayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return "", err
		}
		issue, err := e.gitlab.UpdateIssueLabels(ctx, payload.ProjectID, payload.IssueIID, payload.AddLabels, payload.RemoveLabels)
		return gitlab.ExternalID(issue.IID), err
	case MessageEnsureBranch:
		var payload EnsureBranchPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return "", err
		}
		if err := e.gitlab.EnsureBranch(ctx, payload.ProjectID, payload.Branch, payload.Ref); err != nil {
			return "", err
		}
		return payload.Branch, nil
	case MessageCreateMR:
		var payload CreateMergeRequestPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return "", err
		}
		mr, err := e.gitlab.CreateMergeRequestIdempotent(ctx, payload.ProjectID, payload.Title,
			payload.Source, payload.Target, payload.Description)
		if err != nil {
			return "", err
		}
		if err := e.store.UpsertMergeRequest(ctx, store.MergeRequestRecord{
			ID: uuid.NewString(), WorkItemID: payload.WorkItemID, GitLabMRIID: mr.IID,
			SourceBranch: mr.SourceBranch, TargetBranch: mr.TargetBranch, HeadSHA: mr.SHA,
			State: mr.State, Draft: mr.Draft, WebURL: mr.WebURL,
		}); err != nil {
			return "", err
		}
		return gitlab.ExternalID(mr.IID), nil
	case MessageTriggerDelivery:
		var payload DeliveryRequestPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return "", err
		}
		if e.delivery == nil {
			return "", errors.New("delivery adapter is not configured")
		}
		return e.delivery.Trigger(ctx, payload.RequestID, payload)
	default:
		return "", fmt.Errorf("unsupported outbox message type %q", message.Type)
	}
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

func combinedHash(snapshots []domain.Snapshot) string {
	hash := sha256.New()
	for _, snapshot := range snapshots {
		fmt.Fprintf(hash, "%s:%d:%s\n", snapshot.ConfluencePageID, snapshot.Version, snapshot.ContentHash)
		for _, image := range snapshot.Images {
			fmt.Fprintf(hash, "%s:%d:%s\n", image.AttachmentID, image.Version, image.ContentHash)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func sourceText(snapshots []domain.Snapshot) string {
	text, _ := boundedSourceText(snapshots, 0)
	return text
}

func boundedSourceText(snapshots []domain.Snapshot, maxTokensPerSource int) (string, map[string]string) {
	var out strings.Builder
	transmitted := map[string]string{}
	for _, snapshot := range snapshots {
		content, compressed := knowledge.CompressText(snapshot.NormalizedText, maxTokensPerSource)
		method := "none"
		if compressed {
			method = "extractive-head-tail-v1"
		}
		transmitted[snapshot.ID] = content
		fmt.Fprintf(&out, "\n--- Confluence Page %s v%d: %s ---\nURL: %s\nSource SHA-256: %s\nCompression: %s\n%s\n",
			snapshot.ConfluencePageID, snapshot.Version, snapshot.Title, snapshot.URL,
			snapshot.ContentHash, method, content)
	}
	return out.String(), transmitted
}

func (e *Engine) startAgentRun(ctx context.Context, workflow domain.Workflow, agentType, inputHash string, snapshots []domain.Snapshot) (string, string, error) {
	contextText, transmittedSnapshots := boundedSourceText(snapshots, 8000)
	manifestID := ""
	if e.v3.ContextManifest {
		entries := make([]store.ContextEntryInput, 0, len(snapshots))
		for _, snapshot := range snapshots {
			transmitted := transmittedSnapshots[snapshot.ID]
			digest := sha256.Sum256([]byte(transmitted))
			transmittedHash := hex.EncodeToString(digest[:])
			compression := "none"
			if transmitted != snapshot.NormalizedText {
				compression = "extractive-head-tail-v1"
			}
			entries = append(entries, store.ContextEntryInput{
				SourceType: "CONFLUENCE_SNAPSHOT", SourceID: snapshot.ID, AuthorityLevel: 100,
				TokenCount: len(strings.Fields(transmitted)), ContentHash: transmittedHash, CompressionMethod: compression,
				Required: true, Citation: map[string]any{"url": snapshot.URL, "page_id": snapshot.ConfluencePageID,
					"version": snapshot.Version, "source_content_hash": snapshot.ContentHash},
			})
		}
		if e.v3.RAG {
			hits, err := e.store.RetrieveKnowledge(ctx, workflow.ID, workflow.GitLabProjectID, workflow.IssueTitle, 0, 8)
			if err != nil {
				return "", "", fmt.Errorf("retrieve governed context: %w", err)
			}
			if err := e.store.ValidateKnowledgeHits(ctx, hits); err != nil {
				return "", "", fmt.Errorf("validate governed citations: %w", err)
			}
			if len(hits) > 0 {
				var supplemental strings.Builder
				supplemental.WriteString("\n\n--- 受治理的补充知识（不覆盖上述权威需求）---\n")
				for _, hit := range hits {
					fmt.Fprintf(&supplemental, "\n[%s / %s v%s / SHA-256 %s]\n%s\n", hit.Title, hit.SourceKey, hit.SourceVersion, hit.ContentHash, hit.Content)
					entries = append(entries, store.ContextEntryInput{SourceType: "KNOWLEDGE_CHUNK", SourceID: hit.ChunkID,
						AuthorityLevel: hit.AuthorityLevel, TokenCount: len(strings.Fields(hit.Content)), ContentHash: hit.ContentHash,
						Citation: map[string]any{"document_id": hit.DocumentID, "source_type": hit.SourceType, "source_key": hit.SourceKey,
							"source_version": hit.SourceVersion, "title": hit.Title}})
				}
				contextText += supplemental.String()
			}
		}
		if e.v3.Memory {
			memories, err := e.store.ActiveProjectMemories(ctx, workflow.GitLabProjectID)
			if err != nil {
				return "", "", fmt.Errorf("load governed project memory: %w", err)
			}
			if len(memories) > 0 {
				var memoryText strings.Builder
				memoryText.WriteString("\n\n--- 经工程师批准的项目记忆（低于权威需求）---\n")
				for _, memory := range memories {
					digest := sha256.Sum256([]byte(memory.Content))
					hash := hex.EncodeToString(digest[:])
					fmt.Fprintf(&memoryText, "\n[%s / SHA-256 %s]\n%s\n", memory.Key, hash, memory.Content)
					entries = append(entries, store.ContextEntryInput{SourceType: "PROJECT_MEMORY", SourceID: memory.ID, AuthorityLevel: 70,
						TokenCount: len(strings.Fields(memory.Content)), ContentHash: hash, Citation: map[string]any{"memory_key": memory.Key, "source_document_id": memory.SourceDocumentID}})
				}
				contextText += memoryText.String()
			}
		}
		project := e.projects[workflow.GitLabProjectID]
		skills, err := e.store.ActiveSkillsForAgent(ctx, agentType, project.AllowedSkills)
		if err != nil {
			return "", "", fmt.Errorf("resolve governed skills: %w", err)
		}
		if len(skills) > 0 {
			var skillText strings.Builder
			skillText.WriteString("\n\n--- 项目允许的版本化 Agent Skills ---\n")
			for _, skill := range skills {
				fmt.Fprintf(&skillText, "\n[%s / SHA-256 %s]\n%s\n", skill.Key, skill.ContentHash, skill.Instructions)
				entries = append(entries, store.ContextEntryInput{SourceType: "SKILL_VERSION", SourceID: skill.VersionID,
					AuthorityLevel: 80, TokenCount: len(strings.Fields(skill.Instructions)), ContentHash: skill.ContentHash,
					Citation: map[string]any{"skill_key": skill.Key, "skill_version_id": skill.VersionID}})
			}
			contextText += skillText.String()
		}
		manifestID, err = e.store.CreateContextManifest(ctx, workflow.ID, agentType, "v1", entries)
		if err != nil {
			return "", "", err
		}
	}
	profileKey := strings.ToLower(agentType)
	for _, role := range []string{"PRIMARY", "CRITIC", "SECURITY_RELIABILITY", "JUDGE"} {
		if strings.HasSuffix(agentType, "_"+role) {
			profileKey = "multiagent_" + strings.ToLower(role)
			break
		}
	}
	runID, err := e.store.StartAgentRunWithProfile(ctx, workflow.ID, "", agentType, profileKey, e.agents.Model(), inputHash, manifestID)
	return runID, contextText, err
}

func storeTrace(trace agents.Trace) store.AgentRunTrace {
	return store.AgentRunTrace{
		ProviderResponseID: trace.ProviderResponseID,
		InputTokens:        trace.InputTokens, CachedTokens: trace.CachedTokens,
		OutputTokens: trace.OutputTokens, ReasoningTokens: trace.ReasoningTokens,
		LatencyMS: trace.Latency.Milliseconds(), FinishReason: trace.FinishReason,
		SelectedModelKey: trace.SelectedModelKey, Fallback: trace.Fallback, EstimatedCost: trace.EstimatedCost,
		ProviderModelID: trace.ProviderModelID,
		RouteReason:     trace.RouteReason, RiskLevel: trace.RiskLevel,
	}
}

// cancellableAgentContext turns the durable cancellation flag into context
// cancellation. Provider clients already honor context cancellation, so a
// cancelled Run cannot continue an expensive model request in the background.
func (e *Engine) cancellableAgentContext(parent context.Context, runID string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				requested, err := e.store.AgentRunCancellationRequested(ctx, runID)
				if err == nil && requested {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

type governedReviewObserver struct {
	mu        sync.Mutex
	engine    *Engine
	workflow  domain.Workflow
	stage     string
	snapshots []domain.Snapshot
	runIDs    map[string]string
}

func (o *governedReviewObserver) Start(ctx context.Context, role string) (string, error) {
	runID, _, err := o.engine.startAgentRun(ctx, o.workflow, o.stage+"_"+role, o.workflow.SourceHash, o.snapshots)
	if err == nil {
		o.mu.Lock()
		o.runIDs[role] = runID
		o.mu.Unlock()
	}
	return runID, err
}

func (o *governedReviewObserver) Finish(ctx context.Context, runID string, trace agents.Trace, runErr error) error {
	status := "COMPLETED"
	if runErr != nil {
		status = "FAILED"
	}
	return o.engine.store.FinishAgentRunWithTrace(ctx, runID, status, "", storeTrace(trace), runErr)
}

func (o *governedReviewObserver) RecordOpinion(ctx context.Context, opinion multiagent.Opinion, minority bool) error {
	o.mu.Lock()
	runID := o.runIDs[opinion.Role]
	o.mu.Unlock()
	if runID == "" {
		return fmt.Errorf("missing Agent Run for opinion role %s", opinion.Role)
	}
	return (store.OpinionRecorder{Store: o.engine.store, AgentRunID: runID}).RecordOpinion(ctx, opinion, minority)
}

func (e *Engine) runGovernedReview(ctx context.Context, workflow domain.Workflow, stage string, snapshots []domain.Snapshot,
	formalArtifact []byte) (multiagent.Synthesis, error) {
	observer := &governedReviewObserver{engine: e, workflow: workflow, stage: stage, snapshots: snapshots, runIDs: map[string]string{}}
	prompts := map[string]agents.RolePrompt{}
	for role, key := range map[string]string{multiagent.RolePrimary: "multiagent-primary", multiagent.RoleCritic: "multiagent-critic", multiagent.RoleSecurity: "multiagent-security-reliability", multiagent.RoleJudge: "multiagent-judge"} {
		active, err := e.store.ActivePromptVersion(ctx, key)
		if err != nil {
			return multiagent.Synthesis{}, fmt.Errorf("resolve %s prompt: %w", role, err)
		}
		runtime, err := e.store.PromptRuntime(ctx, active.ID)
		if err != nil {
			return multiagent.Synthesis{}, err
		}
		prompts[role] = agents.RolePrompt{Instructions: runtime.Content, Schema: runtime.OutputSchema}
	}
	runner := agents.NewGovernedMultiAgentRunnerWithPrompts(e.agents, observer, prompts)
	_, synthesis, err := multiagent.New(runner).Execute(ctx, multiagent.Input{WorkflowID: workflow.ID, AgentType: stage,
		AuthoritativeText: sourceText(snapshots), PrimaryArtifact: formalArtifact}, observer)
	return synthesis, err
}

func renderGovernedReview(synthesis multiagent.Synthesis) string {
	var out strings.Builder
	out.WriteString("\n\n## 多 Agent 独立审查\n\n")
	fmt.Fprintf(&out, "**Judge 决策：** `%s`\n\n%s\n\n", synthesis.Decision, synthesis.Summary)
	if len(synthesis.Consensus) > 0 {
		out.WriteString("### 共识\n\n")
		for _, item := range synthesis.Consensus {
			fmt.Fprintf(&out, "- %s\n", item)
		}
	}
	if len(synthesis.Disagreements) > 0 {
		out.WriteString("\n### 分歧与少数意见\n\n")
		for _, opinion := range synthesis.Disagreements {
			fmt.Fprintf(&out, "- **%s / %s：** %s\n", opinion.Role, opinion.Decision, opinion.Summary)
		}
	}
	if len(synthesis.UnresolvedRisks) > 0 {
		out.WriteString("\n### 未解决风险\n\n")
		for _, risk := range synthesis.UnresolvedRisks {
			fmt.Fprintf(&out, "- %s\n", risk)
		}
	}
	return out.String()
}

func (e *Engine) publishRequirementGate(ctx context.Context, workflow domain.Workflow, project domain.ProjectConfig,
	snapshots []domain.Snapshot, feedback string) error {
	runID, agentContext, err := e.startAgentRun(ctx, workflow, "REQUIREMENT", combinedHash(snapshots), snapshots)
	if err != nil {
		return err
	}
	runCtx, cancelRun := e.cancellableAgentContext(ctx, runID)
	defer cancelRun()
	review, trace, err := e.agents.ReviewRequirement(runCtx, runID, agentContext, feedback)
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
		Prompt: "requirement-review-v1", GeneratedAt: time.Now().UTC(),
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
		prd, prdTrace, prdErr = e.agents.GeneratePRD(prdCtx, prdRunID, source, string(reviewJSON), prdFeedback)
	}()
	go func() {
		defer wait.Done()
		tests, testTrace, testErr = e.agents.GenerateTestPlan(testCtx, testRunID, testSource, string(reviewJSON), testFeedback)
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
		Markdown: agents.RenderPRD(prd), Model: e.agents.Model(), Prompt: "prd-v1", GeneratedAt: now,
	}
	testArtifact := domain.Artifact{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactTestPlan,
		Version: testVersion, SourceHash: workflow.SourceHash, Content: testRaw,
		Markdown: agents.RenderTestPlan(tests), Model: e.agents.Model(), Prompt: "test-plan-v1", GeneratedAt: now,
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

type qualityArtifact struct {
	WorkItemID      string                `json:"work_item_id"`
	MergeRequestIID int64                 `json:"merge_request_iid"`
	HeadSHA         string                `json:"head_sha"`
	Verdict         domain.QualityVerdict `json:"verdict"`
	Attempt         int                   `json:"attempt"`
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

func (e *Engine) hasOpenGate(ctx context.Context, workflowID string, gateType domain.GateType) (bool, error) {
	gates, err := e.store.OpenGates(ctx, workflowID)
	if err != nil {
		return false, err
	}
	for _, gate := range gates {
		if gate.Type == gateType {
			return true, nil
		}
	}
	return false, nil
}

func sanitizeSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	lastDash := false
	for _, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
		if valid {
			out.WriteRune(char)
			lastDash = false
			continue
		}
		if !lastDash && out.Len() > 0 {
			out.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(out.String(), "-")
}

func (e *Engine) reconcile(ctx context.Context) error {
	workflows, err := e.store.ListReconcilableWorkflows(ctx, 100)
	if err != nil {
		return err
	}
	for _, workflow := range workflows {
		notes, err := e.gitlab.ListNotes(ctx, workflow.GitLabProjectID, workflow.IssueIID)
		if err != nil {
			e.logger.Warn("reconciliation could not list notes", "workflow_id", workflow.ID, "error", err)
			continue
		}
		for _, note := range notes {
			command, err := domain.ParseGateCommand(note.Body)
			if err != nil {
				continue
			}
			event := webhook.GateNote{
				ProjectID: workflow.GitLabProjectID, IssueIID: workflow.IssueIID,
				NoteID: note.ID, UserID: note.Author.ID, Username: note.Author.Username,
				Command: command, EventID: fmt.Sprintf("reconcile-note-%d", note.ID),
			}
			if err := e.store.EnqueueEvent(ctx, fmt.Sprintf("gitlab:reconcile:%d:%d:%d", workflow.GitLabProjectID, workflow.IssueIID, note.ID),
				"gitlab.gate.command", event, time.Now().UTC()); err != nil {
				return err
			}
		}
		items, err := e.store.ListWorkItems(ctx, workflow.ID)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.GitLabIssueIID == 0 || item.State == domain.WorkItemCompleted || item.State == domain.WorkItemCancelled {
				continue
			}
			itemNotes, err := e.gitlab.ListNotes(ctx, workflow.GitLabProjectID, item.GitLabIssueIID)
			if err != nil {
				e.logger.Warn("reconciliation could not list work item notes", "work_item_id", item.ID, "error", err)
				continue
			}
			for _, note := range itemNotes {
				command, err := domain.ParseControlCommand(note.Body)
				if err != nil {
					continue
				}
				event := webhook.ControlNote{
					ProjectID: workflow.GitLabProjectID, IssueIID: item.GitLabIssueIID,
					NoteID: note.ID, UserID: note.Author.ID, Username: note.Author.Username,
					Command: command, EventID: fmt.Sprintf("reconcile-note-%d", note.ID),
				}
				if err := e.store.EnqueueEvent(ctx, fmt.Sprintf("gitlab:reconcile:%d:%d:%d", workflow.GitLabProjectID, item.GitLabIssueIID, note.ID),
					"gitlab.control.command", event, time.Now().UTC()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
