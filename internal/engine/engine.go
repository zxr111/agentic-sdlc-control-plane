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
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/connectors/confluence"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/connectors/gitlab"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/webhook"
	"github.com/google/uuid"
)

const (
	MessageUpsertNote  = "gitlab.upsert_note"
	MessageCreateIssue = "gitlab.create_issue"
)

type Engine struct {
	store      *store.Store
	gitlab     *gitlab.Client
	confluence *confluence.Client
	agents     *agents.Client
	projects   map[int64]domain.ProjectConfig
	logger     *slog.Logger
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
}

type GeneratePlansEvent struct {
	WorkflowID string `json:"workflow_id"`
	GateID     string `json:"gate_id"`
}

func (e *Engine) HandleEvent(ctx context.Context, event domain.QueueEvent) error {
	switch event.Type {
	case "gitlab.issue.changed":
		var payload webhook.IssueChanged
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.handleIssueChanged(ctx, payload)
	case "gitlab.gate.command":
		var payload webhook.GateNote
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.handleGateCommand(ctx, payload)
	case "workflow.generate_plans":
		var payload GeneratePlansEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		return e.generatePlans(ctx, payload)
	case "system.reconcile":
		return e.reconcile(ctx)
	default:
		return fmt.Errorf("unsupported event type %q", event.Type)
	}
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
		return gitlab.ExternalID(issue.IID), nil
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
	var out strings.Builder
	for _, snapshot := range snapshots {
		fmt.Fprintf(&out, "\n--- Confluence Page %s v%d: %s ---\nURL: %s\nSHA-256: %s\n%s\n",
			snapshot.ConfluencePageID, snapshot.Version, snapshot.Title, snapshot.URL,
			snapshot.ContentHash, snapshot.NormalizedText)
	}
	return out.String()
}

func (e *Engine) publishRequirementGate(ctx context.Context, workflow domain.Workflow, project domain.ProjectConfig,
	snapshots []domain.Snapshot, feedback string) error {
	review, err := e.agents.ReviewRequirement(ctx, workflow.ID, sourceText(snapshots), feedback)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(review)
	if err != nil {
		return err
	}
	version, err := e.store.NextArtifactVersion(ctx, workflow.ID, domain.ArtifactRequirement)
	if err != nil {
		return err
	}
	artifact := domain.Artifact{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactRequirement,
		Version: version, SourceHash: combinedHash(snapshots), Content: raw,
		Markdown: agents.RenderRequirement(review, snapshots), Model: e.agents.Model(),
		Prompt: "requirement-review-v1", GeneratedAt: time.Now().UTC(),
	}
	gate := domain.NewGate(workflow.ID, domain.GateRequirement, artifact.ID, version, project.ReviewerIDs[domain.GateRequirement])
	body := artifactHeader(workflow, snapshots, artifact) + artifact.Markdown +
		gateInstructions(gate, project.ReviewerMentions[domain.GateRequirement])
	note := outboxNote(workflow, gate, body)
	return e.store.PublishGate(ctx, workflow, artifact, gate, domain.StateWaitingRequirementReview, note)
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
		for _, item := range review.WorkItems {
			marker := fmt.Sprintf("<!-- ai-factory:work-item:%s:%s -->", workflow.ID, item.Key)
			description := renderChildIssue(workflow, item, artifact)
			payload := CreateIssuePayload{
				ProjectID: workflow.GitLabProjectID, ParentIID: workflow.IssueIID,
				Title: item.Title, Description: description, Marker: marker,
				Labels:     []string{"automation::human-required", "stage::planning"},
				AssigneeID: project.OwnerIDs[item.OwnerRole],
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
		return e.queueStatusNote(ctx, workflow, "ready:"+workflow.ID,
			"<!-- ai-factory:ready-for-architecture -->",
			"## Ready for architecture\n\nThe Requirement, PRD, and Test Gates are approved. V1 stops at `READY_FOR_ARCHITECTURE`; no coding or deployment has been authorized.")
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
	source := sourceText(snapshots)
	var prd agents.PRD
	var tests agents.TestPlan
	var prdErr, testErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		prd, prdErr = e.agents.GeneratePRD(ctx, workflow.ID, source, string(reviewJSON), prdFeedback)
	}()
	go func() {
		defer wait.Done()
		tests, testErr = e.agents.GenerateTestPlan(ctx, workflow.ID, source, string(reviewJSON), testFeedback)
	}()
	wait.Wait()
	if prdErr != nil {
		return prdErr
	}
	if testErr != nil {
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
	return e.store.PublishPlanningGates(ctx, workflow,
		[]domain.Artifact{prdArtifact, testArtifact}, []domain.Gate{prdGate, testGate},
		[]domain.OutboxMessage{outboxNote(workflow, prdGate, prdBody), outboxNote(workflow, testGate, testBody)})
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
	default:
		return fmt.Errorf("unsupported gate type %q", gate.Type)
	}
}

func (e *Engine) queueStatusNote(ctx context.Context, workflow domain.Workflow, dedupeKey, marker, body string) error {
	return e.store.EnqueueOutbox(ctx, dedupeKey, MessageUpsertNote, UpsertNotePayload{
		ProjectID: workflow.GitLabProjectID, IssueIID: workflow.IssueIID, Marker: marker, Body: body,
	})
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
			if err := e.store.EnqueueEvent(ctx, fmt.Sprintf("gitlab:reconcile:%d", note.ID),
				"gitlab.gate.command", event, time.Now().UTC()); err != nil {
				return err
			}
		}
	}
	return nil
}
