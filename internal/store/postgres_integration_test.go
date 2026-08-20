//go:build integration

package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"github.com/google/uuid"
)

func integrationStore(t *testing.T) *Store {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_TEST_URL is not configured")
	}
	repository, err := Open(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repository.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return repository
}

func TestQueueClaimsAreExclusive(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	if _, err := repository.db.ExecContext(ctx, `DELETE FROM event_queue`); err != nil {
		t.Fatal(err)
	}
	key := "integration:" + uuid.NewString()
	if err := repository.EnqueueEvent(ctx, key, "test", map[string]string{"id": key}, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	wait.Add(2)
	type claimResult struct {
		event *domain.QueueEvent
		err   error
	}
	results := make(chan claimResult, 2)
	for _, worker := range []string{"one", "two"} {
		go func(worker string) {
			defer wait.Done()
			event, err := repository.ClaimEvent(ctx, worker, time.Minute)
			results <- claimResult{event: event, err: err}
		}(worker)
	}
	wait.Wait()
	close(results)
	var claims, empty int
	for result := range results {
		switch {
		case result.err == nil:
			claims++
			if err := repository.CompleteEvent(ctx, result.event.ID); err != nil {
				t.Fatal(err)
			}
		case errors.Is(result.err, ErrNotFound):
			empty++
		default:
			t.Fatal(result.err)
		}
	}
	if claims != 1 || empty != 1 {
		t.Fatalf("claims=%d empty=%d", claims, empty)
	}
}

func TestWorkflowIssueIsIdempotent(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	projectID := time.Now().UnixNano()
	first := domain.NewWorkflow(projectID, 7, "First")
	got, err := repository.GetOrCreateWorkflow(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	second := domain.NewWorkflow(projectID, 7, "Updated")
	again, err := repository.GetOrCreateWorkflow(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != again.ID || again.IssueTitle != "Updated" {
		t.Fatalf("first=%#v again=%#v", got, again)
	}
}

func TestPostgresWorkflowArtifactsGateAndOutboxRoundTrip(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	if _, err := repository.db.ExecContext(ctx, `DELETE FROM outbox_messages`); err != nil {
		t.Fatal(err)
	}
	workflow, err := repository.GetOrCreateWorkflow(ctx, domain.NewWorkflow(time.Now().UnixNano(), 11, "Round trip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Transition(ctx, workflow.ID, domain.StateIngesting, "test", nil); err != nil {
		t.Fatal(err)
	}
	if err := repository.Transition(ctx, workflow.ID, domain.StateRequirementAnalysis, "test", nil); err != nil {
		t.Fatal(err)
	}
	workflow, err = repository.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}

	snapshot := domain.Snapshot{
		ID: uuid.NewString(), WorkflowID: workflow.ID, ConfluencePageID: "635077069",
		Version: 3, Title: "PID", URL: "https://example.atlassian.net/wiki/pages/635077069",
		UpdatedAt: "2026-07-27T00:00:00Z", ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		NormalizedText: "normalized", RawStorage: "<p>raw</p>",
		Images: []domain.Image{{
			AttachmentID: "42", Version: 1, Filename: "flow.png",
			MediaType: "image/png", ContentHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Markdown: "![flow](/uploads/flow.png)", Order: 1,
		}},
	}
	if err := repository.SaveSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	snapshots, err := repository.LatestSnapshots(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || len(snapshots[0].Images) != 1 || snapshots[0].Images[0].Filename != "flow.png" {
		t.Fatalf("unexpected snapshots: %#v", snapshots)
	}

	content := json.RawMessage(`{"decision":"ready_for_human_approval"}`)
	artifact := domain.Artifact{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactRequirement,
		Version: 1, SourceHash: snapshot.ContentHash, Content: content, Markdown: "## Review",
		Model: "test-model", Prompt: "requirement-v1", GeneratedAt: time.Now().UTC(),
	}
	gate := domain.NewGate(workflow.ID, domain.GateRequirement, artifact.ID, 1, []int64{995})
	outboxPayload, err := json.Marshal(map[string]any{"workflow_id": workflow.ID})
	if err != nil {
		t.Fatal(err)
	}
	note := domain.OutboxMessage{
		DedupeKey: "integration:gate:" + gate.ID, Type: "gitlab.upsert_note", Payload: outboxPayload,
	}
	if err := repository.PublishGate(ctx, workflow, artifact, gate, domain.StateWaitingRequirementReview, note); err != nil {
		t.Fatal(err)
	}

	gotArtifact, err := repository.LatestArtifact(ctx, workflow.ID, domain.ArtifactRequirement)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(gotArtifact.Content, &decoded); err != nil {
		t.Fatal(err)
	}
	if gotArtifact.ID != artifact.ID || decoded["decision"] != "ready_for_human_approval" {
		t.Fatalf("unexpected artifact: %#v", gotArtifact)
	}
	gotGate, err := repository.GetGate(ctx, gate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotGate.Status != domain.GateOpen || !gotGate.Authorizes(995) {
		t.Fatalf("unexpected gate: %#v", gotGate)
	}
	pending, err := repository.PendingOutboxPrefix(ctx, note.DedupeKey)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("expected one pending outbox message, got %d", pending)
	}
	message, err := repository.ClaimOutbox(ctx, "integration-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if message.DedupeKey != note.DedupeKey {
		t.Fatalf("claimed wrong outbox message: %#v", message)
	}
	if err := repository.CompleteOutbox(ctx, message.ID, "123"); err != nil {
		t.Fatal(err)
	}

	if err := repository.DecideGate(ctx, gotGate, domain.ActionApprove, 995, "brucegong", "approved"); err != nil {
		t.Fatal(err)
	}
	gotGate, err = repository.GetGate(ctx, gate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotGate.Status != domain.GateApproved || gotGate.DecidedAt == nil || gotGate.Feedback != "approved" {
		t.Fatalf("decision was not persisted: %#v", gotGate)
	}
	if err := repository.Transition(ctx, workflow.ID, domain.StateMaterializingWorkItems, "approved", nil); err != nil {
		t.Fatal(err)
	}
	if err := repository.Transition(ctx, workflow.ID, domain.StatePRDGenerating, "materialized", nil); err != nil {
		t.Fatal(err)
	}
	workflow, err = repository.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	prdArtifact := domain.Artifact{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactPRD, Version: 1,
		SourceHash: snapshot.ContentHash, Content: json.RawMessage(`{"problem":"test"}`),
		Markdown: "## PRD", Model: "test-model", Prompt: "prd-v1", GeneratedAt: time.Now().UTC(),
	}
	testArtifact := domain.Artifact{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Type: domain.ArtifactTestPlan, Version: 1,
		SourceHash: snapshot.ContentHash, Content: json.RawMessage(`{"decision":"ready_for_human_approval"}`),
		Markdown: "## Tests", Model: "test-model", Prompt: "test-v1", GeneratedAt: time.Now().UTC(),
	}
	prdGate := domain.NewGate(workflow.ID, domain.GatePRD, prdArtifact.ID, 1, []int64{995})
	testGate := domain.NewGate(workflow.ID, domain.GateTest, testArtifact.ID, 1, []int64{995})
	prdPayload, _ := json.Marshal(map[string]string{"artifact": "prd"})
	testPayload, _ := json.Marshal(map[string]string{"artifact": "test"})
	prdNote := domain.OutboxMessage{DedupeKey: "integration:prd:" + prdGate.ID, Type: "gitlab.upsert_note", Payload: prdPayload}
	testNote := domain.OutboxMessage{DedupeKey: "integration:test:" + testGate.ID, Type: "gitlab.upsert_note", Payload: testPayload}
	if err := repository.PublishPlanningGates(ctx, workflow,
		[]domain.Artifact{prdArtifact, testArtifact}, []domain.Gate{prdGate, testGate},
		[]domain.OutboxMessage{prdNote, testNote}); err != nil {
		t.Fatal(err)
	}
	openGates, err := repository.OpenGates(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(openGates) != 2 {
		t.Fatalf("expected two planning gates, got %#v", openGates)
	}
	nextPRDVersion, err := repository.NextArtifactVersion(ctx, workflow.ID, domain.ArtifactPRD)
	if err != nil {
		t.Fatal(err)
	}
	if nextPRDVersion != 2 {
		t.Fatalf("expected next PRD version 2, got %d", nextPRDVersion)
	}
	if err := repository.AddAudit(ctx, workflow.ID, "integration.checked", 995, map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	reconcilable, err := repository.ListReconcilableWorkflows(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range reconcilable {
		if candidate.ID == workflow.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("waiting workflow was not reconcilable")
	}

	dashboard, err := repository.Dashboard(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var dashboardWorkflow *DashboardWorkflow
	for index := range dashboard.Workflows {
		if dashboard.Workflows[index].ID == workflow.ID {
			dashboardWorkflow = &dashboard.Workflows[index]
			break
		}
	}
	if dashboardWorkflow == nil {
		t.Fatal("workflow was not returned by the dashboard query")
	}
	if len(dashboardWorkflow.Gates) < 3 || len(dashboardWorkflow.Artifacts) != 3 ||
		len(dashboardWorkflow.Sources) != 1 || len(dashboardWorkflow.Activity) == 0 {
		t.Fatalf("incomplete dashboard workflow: %#v", dashboardWorkflow)
	}
}

func TestExpiredQueueLeaseIsRecovered(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	if _, err := repository.db.ExecContext(ctx, `DELETE FROM event_queue`); err != nil {
		t.Fatal(err)
	}
	key := "integration:lease:" + uuid.NewString()
	if err := repository.EnqueueEvent(ctx, key, "test", map[string]string{"id": key}, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	first, err := repository.ClaimEvent(ctx, "crashed-worker", -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.ClaimEvent(ctx, "recovery-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Attempts != 2 {
		t.Fatalf("lease was not recovered: first=%#v second=%#v", first, second)
	}
	if err := repository.CompleteEvent(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCodexDispatchIsIdempotentAndHasNoLease(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	workflow, err := repository.GetOrCreateWorkflow(ctx, domain.NewWorkflow(time.Now().UnixNano(), 77, "Dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	item := domain.WorkItem{
		ID: uuid.NewString(), WorkflowID: workflow.ID, Key: "backend-api", Title: "[Feature][API] Build endpoint",
		State: domain.WorkItemReadyForCodex, OwnerRole: "backend", AssigneeID: 1272,
		TargetBranch: "master", AcceptanceIDs: []string{"AC-1"}, Revision: 1,
	}
	if err := repository.SaveWorkItems(ctx, workflow.ID, []domain.WorkItem{item}, nil); err != nil {
		t.Fatal(err)
	}
	first, created, err := repository.StartCodex(ctx, item.ID, "engineer-mac", 1272)
	if err != nil || !created {
		t.Fatalf("first dispatch: created=%t err=%v", created, err)
	}
	second, created, err := repository.StartCodex(ctx, item.ID, "engineer-mac", 1272)
	if err != nil || created || first.ID != second.ID {
		t.Fatalf("duplicate dispatch: first=%#v second=%#v created=%t err=%v", first, second, created, err)
	}
	if _, _, err := repository.StartCodex(ctx, item.ID, "other-mac", 995); err == nil {
		t.Fatal("unassigned engineer must not receive an existing dispatch")
	}
}

func TestV3ContextManifestAndAgentTraceRoundTrip(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	if err := repository.BootstrapRegistry(ctx, "test-model", []RegistryDefinition{{
		AgentType: "REQUIREMENT", PromptKey: "requirement-review", DisplayName: "Requirement",
		Instructions: "review safely", OutputSchema: json.RawMessage(`{"type":"object"}`),
	}}); err != nil {
		t.Fatal(err)
	}
	baseline, err := repository.ActivePromptVersion(ctx, "requirement-review")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := repository.CreatePromptVersion(ctx, "requirement-review", "review more safely",
		json.RawMessage(`{"type":"object"}`), "integration-test")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := repository.CreatePromptVersion(ctx, "requirement-review", "review more safely",
		json.RawMessage(`{"type":"object"}`), "integration-test")
	if err != nil || duplicate.ID != candidate.ID {
		t.Fatalf("prompt candidate was not idempotent candidate=%#v duplicate=%#v err=%v", candidate, duplicate, err)
	}
	if err := repository.ActivatePromptVersion(ctx, "requirement-review", candidate.ID, "integration-reviewer"); err != nil {
		t.Fatal(err)
	}
	active, err := repository.ActivePromptVersion(ctx, "requirement-review")
	if err != nil || active.ID != candidate.ID {
		t.Fatalf("candidate was not activated active=%#v err=%v", active, err)
	}
	if err := repository.ActivatePromptVersion(ctx, "requirement-review", baseline.ID, "integration-reviewer"); err != nil {
		t.Fatal(err)
	}
	workflow, err := repository.GetOrCreateWorkflow(ctx, domain.NewWorkflow(time.Now().UnixNano(), 81, "V3 trace"))
	if err != nil {
		t.Fatal(err)
	}
	manifestID, err := repository.CreateContextManifest(ctx, workflow.ID, "REQUIREMENT", "v1", []ContextEntryInput{{
		SourceType: "CONFLUENCE_SNAPSHOT", AuthorityLevel: 100, TokenCount: 12,
		ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Citation:    map[string]any{"url": "https://example.test/wiki/1", "version": 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := repository.StartAgentRunWithContext(ctx, workflow.ID, "", "REQUIREMENT", "test-model",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", manifestID)
	if err != nil {
		t.Fatal(err)
	}
	trace := AgentRunTrace{ProviderResponseID: "resp_test", InputTokens: 120, CachedTokens: 20,
		OutputTokens: 40, ReasoningTokens: 15, LatencyMS: 321, FinishReason: "completed"}
	if err := repository.FinishAgentRunWithTrace(ctx, runID, "COMPLETED", "", trace, nil); err != nil {
		t.Fatal(err)
	}
	var gotManifest, responseID, finishReason, profileVersionID, promptVersionID, modelVersionID string
	var inputTokens, cachedTokens, outputTokens, reasoningTokens, latencyMS int64
	if err := repository.db.QueryRowContext(ctx, `SELECT context_manifest_id,provider_response_id,input_tokens,
		cached_tokens,output_tokens,reasoning_tokens,latency_ms,finish_reason,agent_profile_version_id,prompt_version_id,model_version_id
		FROM agent_runs WHERE id=$1`, runID).Scan(&gotManifest, &responseID, &inputTokens, &cachedTokens, &outputTokens,
		&reasoningTokens, &latencyMS, &finishReason, &profileVersionID, &promptVersionID, &modelVersionID); err != nil {
		t.Fatal(err)
	}
	if gotManifest != manifestID || responseID != "resp_test" || inputTokens != 120 || cachedTokens != 20 ||
		outputTokens != 40 || reasoningTokens != 15 || latencyMS != 321 || finishReason != "completed" {
		t.Fatalf("unexpected trace manifest=%s response=%s tokens=%d/%d/%d/%d latency=%d finish=%s",
			gotManifest, responseID, inputTokens, cachedTokens, outputTokens, reasoningTokens, latencyMS, finishReason)
	}
	if profileVersionID == "" || promptVersionID == "" || modelVersionID == "" {
		t.Fatalf("registry versions were not bound profile=%s prompt=%s model=%s", profileVersionID, promptVersionID, modelVersionID)
	}
}

func TestV3KnowledgeAndProjectMemoryLifecycle(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	projectID := time.Now().UnixNano()
	source := KnowledgeSource{ProjectID: projectID, SourceType: "CONFLUENCE", SourceKey: "page-42",
		SourceVersion: "7", Title: "Payment retry", AuthorityLevel: 100,
		AccessScope: map[string]any{"project_id": projectID},
		Content:     "payment retry must preserve idempotency key and reject duplicate settlement", ParentPath: "Payment/Retry"}
	versionID, created, err := repository.IngestKnowledge(ctx, source)
	if err != nil || !created || versionID == "" {
		t.Fatalf("ingest failed id=%s created=%t err=%v", versionID, created, err)
	}
	duplicateID, created, err := repository.IngestKnowledge(ctx, source)
	if err != nil || created || duplicateID != versionID {
		t.Fatalf("ingest was not idempotent id=%s created=%t err=%v", duplicateID, created, err)
	}
	hits, err := repository.SearchKnowledge(ctx, projectID, "idempotency", 50, 10)
	if err != nil || len(hits) != 1 || hits[0].SourceVersion != "7" || hits[0].AuthorityLevel != 100 {
		t.Fatalf("unexpected search hits=%#v err=%v", hits, err)
	}
	memoryID, err := repository.ProposeProjectMemory(ctx, ProjectMemory{ProjectID: projectID,
		Key: "payment-retry", Content: "Retries preserve the idempotency key", SourceDocumentID: hits[0].DocumentID},
		[]map[string]string{{"chunk_id": hits[0].ChunkID, "hash": hits[0].ContentHash}})
	if err != nil || memoryID == "" {
		t.Fatalf("memory proposal failed id=%s err=%v", memoryID, err)
	}
	memories, err := repository.ActiveProjectMemories(ctx, projectID)
	if err != nil || len(memories) != 0 {
		t.Fatalf("candidate memory entered active context %#v err=%v", memories, err)
	}
	if err := repository.ReviewProjectMemory(ctx, projectID, "payment-retry", "APPROVE", "engineer", nil); err != nil {
		t.Fatal(err)
	}
	memories, err = repository.ActiveProjectMemories(ctx, projectID)
	if err != nil || len(memories) != 1 {
		t.Fatalf("approved memory missing %#v err=%v", memories, err)
	}
	if err := repository.ReviewProjectMemory(ctx, projectID, "payment-retry", "REVOKE", "engineer", nil); err != nil {
		t.Fatal(err)
	}
	memories, err = repository.ActiveProjectMemories(ctx, projectID)
	if err != nil || len(memories) != 0 {
		t.Fatalf("revoked memory remained active %#v err=%v", memories, err)
	}
	if err := repository.RevokeKnowledgeSource(ctx, projectID, "CONFLUENCE", "page-42"); err != nil {
		t.Fatal(err)
	}
	hits, err = repository.SearchKnowledge(ctx, projectID, "idempotency", 0, 10)
	if err != nil || len(hits) != 0 {
		t.Fatalf("revoked source remained searchable %#v err=%v", hits, err)
	}
}
