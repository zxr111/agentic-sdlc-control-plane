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
