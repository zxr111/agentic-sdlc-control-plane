package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/toolgateway"
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
	MessageProposeMemory   = "factory.propose_memory"
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

type ProposeMemoryPayload struct {
	ProjectID        int64  `json:"project_id"`
	Key              string `json:"key"`
	Content          string `json:"content"`
	SourceDocumentID string `json:"source_document_id"`
	ToolCallID       string `json:"tool_call_id"`
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
	case MessageProposeMemory:
		var payload ProposeMemoryPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return "", err
		}
		return e.store.ProposeProjectMemory(ctx, store.ProjectMemory{ProjectID: payload.ProjectID,
			Key: payload.Key, Content: payload.Content, SourceDocumentID: payload.SourceDocumentID},
			map[string]any{"tool_call_id": payload.ToolCallID, "delivery": "transactional-outbox"})
	default:
		return "", fmt.Errorf("unsupported outbox message type %q", message.Type)
	}
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

func (e *Engine) startAgentRun(ctx context.Context, workflow domain.Workflow, agentType, inputHash string, snapshots []domain.Snapshot) (resultRunID, resultContext string, resultErr error) {
	profileKey := strings.ToLower(agentType)
	for _, role := range []string{"PRIMARY", "CRITIC", "SECURITY_RELIABILITY", "JUDGE"} {
		if strings.HasSuffix(agentType, "_"+role) {
			profileKey = "multiagent_" + strings.ToLower(role)
			break
		}
	}
	runID, err := e.store.StartAgentRunWithProfile(ctx, workflow.ID, "", agentType, profileKey, e.agents.Model(), inputHash, "")
	if err != nil {
		return "", "", err
	}
	if err := e.store.BeginAgentRunContext(ctx, runID); err != nil {
		return "", "", err
	}
	defer func() {
		if resultErr != nil {
			_ = e.store.FinishAgentRun(ctx, runID, "FAILED", "", resultErr)
		}
	}()
	contextTokenLimit, err := e.store.AgentRunContextTokenLimit(ctx, runID)
	if err != nil {
		return "", "", err
	}
	contextText, transmittedSnapshots := boundedSourceText(snapshots, contextTokenLimit)
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
			var hits []store.KnowledgeHit
			if e.v3.ToolGateway {
				input, marshalErr := json.Marshal(map[string]any{"query": workflow.IssueTitle, "minimum_authority": 0, "limit": 8})
				if marshalErr != nil {
					return "", "", marshalErr
				}
				output, gatewayErr := (toolgateway.Gateway{Store: e.store}).Execute(ctx, toolgateway.Request{
					AgentRunID: runID, ToolKey: "knowledge.search", ProjectID: workflow.GitLabProjectID,
					AgentType: agentType, WorkflowState: string(workflow.State), Input: input,
					ProductionLock: true, Actor: "context-builder", AgenticRetrieval: true,
				})
				if gatewayErr != nil {
					return "", "", fmt.Errorf("governed knowledge tool: %w", gatewayErr)
				}
				if err := json.Unmarshal(output, &hits); err != nil {
					return "", "", fmt.Errorf("decode governed knowledge tool output: %w", err)
				}
			} else {
				hits, err = e.store.RetrieveKnowledge(ctx, workflow.ID, workflow.GitLabProjectID, workflow.IssueTitle, 0, 8)
			}
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
		if err := e.store.BindAgentRunContext(ctx, runID, manifestID); err != nil {
			return "", "", err
		}
	} else if err := e.store.BeginAgentRunExecution(ctx, runID); err != nil {
		return "", "", err
	}
	return runID, contextText, nil
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

func (e *Engine) runtimePromptForRun(ctx context.Context, runID, fallbackLabel string) (agents.RuntimePrompt, string, error) {
	if !e.v3.Registry {
		return agents.RuntimePrompt{}, fallbackLabel, nil
	}
	prompt, err := e.store.AgentRunPromptRuntime(ctx, runID)
	if err != nil {
		return agents.RuntimePrompt{}, "", fmt.Errorf("resolve Agent Run prompt: %w", err)
	}
	return agents.RuntimePrompt{Instructions: prompt.Content, OutputSchema: prompt.OutputSchema},
		fmt.Sprintf("%s-v%d", prompt.PromptKey, prompt.Version), nil
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
	if e.v3.Evaluation {
		for projectID := range e.projects {
			if _, err := e.store.ProposeOperationalImprovements(ctx, projectID, ""); err != nil {
				return fmt.Errorf("cluster V3 operational improvements for project %d: %w", projectID, err)
			}
		}
	}
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
