//go:build integration

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/evaluation"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/multiagent"
	"github.com/google/uuid"
)

func TestMigrationsSupportEmptyAndV2Databases(t *testing.T) {
	baseURL := os.Getenv("DATABASE_TEST_URL")
	if baseURL == "" {
		t.Skip("DATABASE_TEST_URL is not configured")
	}
	admin, err := Open(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, scenario := range []struct {
		name      string
		v2Fixture bool
	}{{name: "empty"}, {name: "v2", v2Fixture: true}} {
		t.Run(scenario.name, func(t *testing.T) {
			databaseName := "factory_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			if _, err := admin.db.ExecContext(ctx, "CREATE DATABASE "+databaseName); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = admin.db.ExecContext(context.Background(), "DROP DATABASE "+databaseName+" WITH (FORCE)")
			})
			targetURL, err := url.Parse(baseURL)
			if err != nil {
				t.Fatal(err)
			}
			targetURL.Path = "/" + databaseName
			target, err := Open(targetURL.String())
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			if scenario.v2Fixture {
				for _, version := range []string{"001_initial", "002_full_lifecycle"} {
					content, err := migrationFiles.ReadFile("migrations/" + version + ".sql")
					if err != nil {
						t.Fatal(err)
					}
					for _, statement := range splitStatements(string(content)) {
						if _, err := target.db.ExecContext(ctx, statement); err != nil {
							t.Fatalf("apply V2 fixture %s: %v", version, err)
						}
					}
					if _, err := target.db.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES ($1) ON CONFLICT DO NOTHING`, version); err != nil {
						t.Fatal(err)
					}
				}
				workflowID := uuid.NewString()
				if _, err := target.db.ExecContext(ctx, `INSERT INTO workflows(id,gitlab_project_id,issue_iid,issue_title,state)
					VALUES($1,42,7,'V2 fixture','NEW')`, workflowID); err != nil {
					t.Fatal(err)
				}
			}
			if err := target.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			var migrationCount int
			if err := target.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
				t.Fatal(err)
			}
			if migrationCount < 13 {
				t.Fatalf("expected all migrations, got %d", migrationCount)
			}
			if scenario.v2Fixture {
				var title string
				if err := target.db.QueryRowContext(ctx, `SELECT issue_title FROM workflows WHERE gitlab_project_id=42 AND issue_iid=7`).Scan(&title); err != nil {
					t.Fatal(err)
				}
				if title != "V2 fixture" {
					t.Fatalf("V2 data changed: %s", title)
				}
			}
			var vectorExtension bool
			if err := target.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='vector')`).Scan(&vectorExtension); err != nil {
				t.Fatal(err)
			}
			if !vectorExtension {
				t.Fatal(fmt.Errorf("vector extension was not installed"))
			}
		})
	}
}

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

func TestClaimEventTypesKeepsRuntimeQueuesIsolated(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	if _, err := repository.db.ExecContext(ctx, `DELETE FROM event_queue`); err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	if err := repository.EnqueueEvent(ctx, "agent:"+suffix, "workflow.generate_architecture", map[string]string{"id": suffix}, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.EnqueueEvent(ctx, "eval:"+suffix, "evaluation.run", map[string]string{"id": suffix}, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	event, err := repository.ClaimEventTypes(ctx, "agent-runtime", time.Minute, []string{"workflow.generate_architecture"})
	if err != nil || event.Type != "workflow.generate_architecture" {
		t.Fatalf("agent event=%#v err=%v", event, err)
	}
	if err := repository.CompleteEvent(ctx, event.ID); err != nil {
		t.Fatal(err)
	}
	event, err = repository.ClaimEventTypes(ctx, "evaluation-worker", time.Minute, []string{"evaluation.run"})
	if err != nil || event.Type != "evaluation.run" {
		t.Fatalf("evaluation event=%#v err=%v", event, err)
	}
	if err := repository.CompleteEvent(ctx, event.ID); err != nil {
		t.Fatal(err)
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
	reconcilable, err := repository.ListReconcilableWorkflows(ctx, 10000)
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

	dashboard, err := repository.Dashboard(ctx, 10000)
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
	candidateContent := "review more safely " + uuid.NewString()
	candidate, err := repository.CreatePromptVersion(ctx, "requirement-review", candidateContent,
		json.RawMessage(`{"type":"object"}`), "integration-test")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := repository.CreatePromptVersion(ctx, "requirement-review", candidateContent,
		json.RawMessage(`{"type":"object"}`), "integration-test")
	if err != nil || duplicate.ID != candidate.ID {
		t.Fatalf("prompt candidate was not idempotent candidate=%#v duplicate=%#v err=%v", candidate, duplicate, err)
	}
	if err := repository.ActivatePromptVersion(ctx, "requirement-review", candidate.ID, "integration-reviewer"); !errors.Is(err, ErrGovernanceRequired) {
		t.Fatalf("draft prompt bypassed governance: %v", err)
	}
	suiteID, err := repository.EnsureEvaluationSuite(ctx, "promotion-"+candidate.ID, "REQUIREMENT", map[string]any{"minimum": 1})
	if err != nil {
		t.Fatal(err)
	}
	baselineRunID, err := repository.StartEvaluationRun(ctx, EvaluationRunInput{SuiteID: suiteID, PromptVersionID: baseline.ID, Shadow: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishEvaluationRun(ctx, baselineRunID, nil); err != nil {
		t.Fatal(err)
	}
	candidateRunID, err := repository.StartEvaluationRun(ctx, EvaluationRunInput{SuiteID: suiteID, PromptVersionID: candidate.ID, Shadow: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishEvaluationRun(ctx, candidateRunID, nil); err != nil {
		t.Fatal(err)
	}
	blindID, err := repository.CreateBlindReview(ctx, baselineRunID, candidateRunID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SubmitBlindReview(ctx, blindID, "reviewer-a", "RIGHT", "APPROVE", "better evidence"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SubmitBlindReview(ctx, blindID, "reviewer-b", "RIGHT", "APPROVE", "safer output"); err != nil {
		t.Fatal(err)
	}
	canaryID, err := repository.CreateCanaryRelease(ctx, "PROMPT", candidate.ID, candidateRunID, blindID, []int64{81}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ApproveCanaryRelease(ctx, canaryID, "integration-reviewer", map[string]any{"errors": 0}); err != nil {
		t.Fatal(err)
	}
	if err := repository.PromotePromptVersion(ctx, "requirement-review", candidate.ID, candidateRunID, blindID, canaryID, "integration-reviewer"); err != nil {
		t.Fatal(err)
	}
	active, err := repository.ActivePromptVersion(ctx, "requirement-review")
	if err != nil || active.ID != candidate.ID {
		t.Fatalf("candidate was not activated active=%#v err=%v", active, err)
	}
	if err := repository.ActivatePromptVersion(ctx, "requirement-review", baseline.ID, "integration-reviewer"); err != nil {
		t.Fatal(err)
	}
	modelCandidateKey := "test-model-candidate-" + uuid.NewString()
	modelCandidateID, err := repository.RegisterModelCandidate(ctx, "openai", modelCandidateKey, map[string]bool{"structured_output": true}, 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	modelRunID, err := repository.StartEvaluationRun(ctx, EvaluationRunInput{SuiteID: suiteID, ModelVersionID: modelCandidateID, Shadow: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishEvaluationRun(ctx, modelRunID, nil); err != nil {
		t.Fatal(err)
	}
	modelBlindID, err := repository.CreateBlindReview(ctx, baselineRunID, modelRunID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SubmitBlindReview(ctx, modelBlindID, "model-reviewer-a", "RIGHT", "APPROVE", "stable"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SubmitBlindReview(ctx, modelBlindID, "model-reviewer-b", "RIGHT", "APPROVE", "cost acceptable"); err != nil {
		t.Fatal(err)
	}
	modelCanaryID, err := repository.CreateCanaryRelease(ctx, "MODEL", modelCandidateID, modelRunID, modelBlindID, []int64{81}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ApproveCanaryRelease(ctx, modelCanaryID, "integration-reviewer", map[string]any{"error_rate": 0}); err != nil {
		t.Fatal(err)
	}
	if err := repository.PromoteModelVersion(ctx, modelCandidateID, modelRunID, modelBlindID, modelCanaryID, "integration-reviewer"); err != nil {
		t.Fatal(err)
	}
	models, preferred, _, err := repository.ActiveRoutingModels(ctx)
	if err != nil || preferred != modelCandidateKey || len(models) == 0 {
		t.Fatalf("promoted model not selected preferred=%s models=%#v err=%v", preferred, models, err)
	}
	var modelStatus string
	if err := repository.db.QueryRowContext(ctx, `SELECT status FROM model_versions WHERE id=$1`, modelCandidateID).Scan(&modelStatus); err != nil || modelStatus != "ACTIVE" {
		t.Fatalf("model status=%s err=%v", modelStatus, err)
	}
	if err := repository.RollbackModelVersion(ctx, modelCandidateID, "integration-reviewer"); err != nil {
		t.Fatal(err)
	}
	_, preferred, _, err = repository.ActiveRoutingModels(ctx)
	if err != nil || preferred == "" || preferred == modelCandidateKey {
		t.Fatalf("model rollback did not restore previous route preferred=%s err=%v", preferred, err)
	}
	workflow, err := repository.GetOrCreateWorkflow(ctx, domain.NewWorkflow(time.Now().UnixNano(), 81, "V3 trace"))
	if err != nil {
		t.Fatal(err)
	}
	traceSnapshotID := uuid.NewString()
	if err := repository.SaveSnapshot(ctx, domain.Snapshot{ID: traceSnapshotID, WorkflowID: workflow.ID,
		ConfluencePageID: "trace-page", Version: 1, Title: "Trace evidence", URL: "https://example.test/wiki/1",
		ContentHash: strings.Repeat("a", 64), NormalizedText: "authoritative trace evidence", RawStorage: "authoritative trace evidence"}); err != nil {
		t.Fatal(err)
	}
	manifestID, err := repository.CreateContextManifest(ctx, workflow.ID, "REQUIREMENT", "v1", []ContextEntryInput{{
		SourceType: "CONFLUENCE_SNAPSHOT", SourceID: traceSnapshotID, AuthorityLevel: 100, TokenCount: 12,
		Required:    true,
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
		OutputTokens: 40, ReasoningTokens: 15, EstimatedCost: 99, LatencyMS: 321, FinishReason: "completed",
		SelectedModelKey: "test-model", ProviderModelID: "test-model-2026-08-01", Fallback: true, RouteReason: "controlled fallback", RiskLevel: "HIGH"}
	if err := repository.FinishAgentRunWithTrace(ctx, runID, "COMPLETED", "", trace, nil); err != nil {
		t.Fatal(err)
	}
	var healthy bool
	if err := repository.db.QueryRowContext(ctx, `SELECT healthy FROM model_health_events mhe
		JOIN model_versions mv ON mv.id=mhe.model_version_id WHERE mv.model_key='test-model'
		ORDER BY mhe.observed_at DESC LIMIT 1`).Scan(&healthy); err != nil || !healthy {
		t.Fatalf("model health event healthy=%t err=%v", healthy, err)
	}
	healthSnapshot, err := repository.LatestModelHealth(ctx)
	if err != nil || !healthSnapshot["test-model"].Healthy {
		t.Fatalf("latest model health=%#v err=%v", healthSnapshot, err)
	}
	var gotManifest, responseID, finishReason, profileVersionID, promptVersionID, modelVersionID, providerModelID string
	var inputTokens, cachedTokens, outputTokens, reasoningTokens, latencyMS int64
	if err := repository.db.QueryRowContext(ctx, `SELECT context_manifest_id,provider_response_id,input_tokens,
		cached_tokens,output_tokens,reasoning_tokens,latency_ms,finish_reason,agent_profile_version_id,prompt_version_id,model_version_id,provider_model_id
		FROM agent_runs WHERE id=$1`, runID).Scan(&gotManifest, &responseID, &inputTokens, &cachedTokens, &outputTokens,
		&reasoningTokens, &latencyMS, &finishReason, &profileVersionID, &promptVersionID, &modelVersionID, &providerModelID); err != nil {
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
	if providerModelID != "test-model-2026-08-01" {
		t.Fatalf("provider model id=%s", providerModelID)
	}
	incompleteRunID, err := repository.StartAgentRunWithContext(ctx, workflow.ID, "", "REQUIREMENT", "test-model", "incomplete", manifestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishAgentRun(ctx, incompleteRunID, "COMPLETED", "", nil); err == nil {
		t.Fatal("governed Agent Run completed without provider trace evidence")
	}
	var incompleteStatus, incompletePhase string
	if err := repository.db.QueryRowContext(ctx, `SELECT status,lifecycle_phase FROM agent_runs WHERE id=$1`, incompleteRunID).
		Scan(&incompleteStatus, &incompletePhase); err != nil {
		t.Fatal(err)
	}
	if incompleteStatus != "FAILED" || incompletePhase != "TERMINAL_FAILED" {
		t.Fatalf("invalid completion left run in status=%s phase=%s", incompleteStatus, incompletePhase)
	}
	lifecycleRunID, err := repository.StartAgentRunWithProfile(ctx, workflow.ID, "", "REQUIREMENT", "requirement", "test-model", "lifecycle", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.BeginAgentRunContext(ctx, lifecycleRunID); err != nil {
		t.Fatal(err)
	}
	if err := repository.BindAgentRunContext(ctx, lifecycleRunID, manifestID); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishAgentRunWithTrace(ctx, lifecycleRunID, "COMPLETED", "", trace, nil); err != nil {
		t.Fatal(err)
	}
	var lifecyclePhase string
	var lifecycleSteps, completedSteps int
	if err := repository.db.QueryRowContext(ctx, `SELECT lifecycle_phase,
		(SELECT COUNT(*) FROM agent_steps WHERE agent_run_id=ar.id),
		(SELECT COUNT(*) FROM agent_steps WHERE agent_run_id=ar.id AND status='COMPLETED')
		FROM agent_runs ar WHERE ar.id=$1`, lifecycleRunID).Scan(&lifecyclePhase, &lifecycleSteps, &completedSteps); err != nil {
		t.Fatal(err)
	}
	if lifecyclePhase != "COMPLETED" || lifecycleSteps != 3 || completedSteps != 3 {
		t.Fatalf("lifecycle phase=%s steps=%d completed=%d", lifecyclePhase, lifecycleSteps, completedSteps)
	}
	var required bool
	var compression string
	if err := repository.db.QueryRowContext(ctx, `SELECT required,compression_method FROM context_entries WHERE context_manifest_id=$1`, manifestID).Scan(&required, &compression); err != nil || !required || compression != "none" {
		t.Fatalf("required context=%t compression=%s err=%v", required, compression, err)
	}
	var routeCount int
	if err := repository.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_route_decisions WHERE agent_run_id=$1
		AND fallback=true AND estimated_cost_microunits=99`, runID).Scan(&routeCount); err != nil {
		t.Fatal(err)
	}
	if routeCount != 1 {
		t.Fatalf("route decision evidence missing: %d", routeCount)
	}
	if err := repository.BootstrapGovernance(ctx, []ToolSeed{
		{Key: "knowledge.search", DisplayName: "Search", RiskLevel: "L1", AdapterType: "internal",
			DefaultDecision: "ALLOW", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"array"}`)},
		{Key: "staging.deploy", DisplayName: "Deploy", RiskLevel: "L3", AdapterType: "outbox",
			DefaultDecision: "ALLOW", RequiresGate: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{Key: "production.deploy", DisplayName: "Production", RiskLevel: "L4", AdapterType: "locked",
			DefaultDecision: "ALLOW", RequiresGate: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{Key: "gitlab.comment", DisplayName: "Comment", RiskLevel: "L2", AdapterType: "outbox",
			DefaultDecision: "ALLOW", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
	}, []SkillSeed{{Key: "threat-modeling", DisplayName: "Threat", Instructions: "review threats",
		TriggerRules: map[string]any{"agent_types": []string{"SECURITY"}}, Scope: map[string]any{"allowlist": true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `UPDATE tool_policies SET conditions_json='{}'::jsonb WHERE tool_version_id IN (
		SELECT tv.id FROM tool_versions tv JOIN tool_definitions td ON td.id=tv.tool_definition_id
		WHERE td.tool_key IN ('knowledge.search','staging.deploy','production.deploy','gitlab.comment'))`); err != nil {
		t.Fatal(err)
	}
	skills, err := repository.ActiveSkillsForAgent(ctx, "ARCHITECTURE_SECURITY", []string{"threat-modeling"})
	if err != nil || len(skills) != 1 || skills[0].Key != "threat-modeling" {
		t.Fatalf("allowed skill resolution=%#v err=%v", skills, err)
	}
	blockedSkills, err := repository.ActiveSkillsForAgent(ctx, "ARCHITECTURE_SECURITY", []string{"other-skill"})
	if err != nil || len(blockedSkills) != 0 {
		t.Fatalf("unlisted skill entered context=%#v err=%v", blockedSkills, err)
	}
	toolRunID, err := repository.StartAgentRun(ctx, workflow.ID, "", "REQUIREMENT", "test-model", "tool-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthorizeToolCall(ctx, ToolAuthorizationRequest{AgentRunID: runID, ToolKey: "knowledge.search",
		ProjectID: workflow.GitLabProjectID, AgentType: "REQUIREMENT", WorkflowState: string(workflow.State),
		Input: map[string]any{"query": "late"}, RedactedInput: map[string]any{"query": "late"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("completed Agent Run accepted a new tool call: %v", err)
	}
	allowed, err := repository.AuthorizeToolCall(ctx, ToolAuthorizationRequest{AgentRunID: toolRunID, ToolKey: "knowledge.search",
		ProjectID: workflow.GitLabProjectID, AgentType: "REQUIREMENT", WorkflowState: string(workflow.State),
		Input: map[string]any{"query": "payment"}, RedactedInput: map[string]any{"query": "payment"}})
	if err != nil || allowed.Decision.Action != "EXECUTE" {
		t.Fatalf("read tool not authorized %#v err=%v", allowed, err)
	}
	if err := repository.FinishToolCall(ctx, allowed.CallID, "COMPLETED",
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `UPDATE tool_policies SET conditions_json=$1 WHERE tool_version_id=$2`,
		`{"allowed_actors":["engineer"],"minimum_evidence_version":2,"maximum_budget_microunits":100}`, allowed.ToolVersionID); err != nil {
		t.Fatal(err)
	}
	denied, err := repository.AuthorizeToolCall(ctx, ToolAuthorizationRequest{AgentRunID: toolRunID, ToolKey: "knowledge.search",
		ProjectID: workflow.GitLabProjectID, AgentType: "REQUIREMENT", WorkflowState: string(workflow.State), Actor: "agent",
		EvidenceVersion: 1, BudgetMicrounits: 101, Input: map[string]any{"query": "payment"}, RedactedInput: map[string]any{"query": "payment"}})
	if err != nil || denied.Decision.Action != "DENY" {
		t.Fatalf("tool conditions were bypassed %#v err=%v", denied, err)
	}
	conditioned, err := repository.AuthorizeToolCall(ctx, ToolAuthorizationRequest{AgentRunID: toolRunID, ToolKey: "knowledge.search",
		ProjectID: workflow.GitLabProjectID, AgentType: "REQUIREMENT", WorkflowState: string(workflow.State), Actor: "engineer",
		EvidenceVersion: 2, BudgetMicrounits: 100, Input: map[string]any{"query": "payment"}, RedactedInput: map[string]any{"query": "payment"}})
	if err != nil || conditioned.Decision.Action != "EXECUTE" {
		t.Fatalf("valid tool conditions rejected %#v err=%v", conditioned, err)
	}
	comment, err := repository.AuthorizeToolCall(ctx, ToolAuthorizationRequest{AgentRunID: toolRunID, ToolKey: "gitlab.comment",
		ProjectID: workflow.GitLabProjectID, AgentType: "REQUIREMENT", WorkflowState: string(workflow.State), Actor: "agent",
		Input: map[string]any{"marker": "agent-note", "body": "evidence-backed note"}, RedactedInput: map[string]any{"marker": "agent-note", "body": "evidence-backed note"}})
	if err != nil || comment.Decision.Action != "OUTBOX" {
		t.Fatalf("comment was not routed through outbox %#v err=%v", comment, err)
	}
	if _, err := repository.EnqueueGovernedToolOutbox(ctx, comment.CallID, "gitlab.comment", "",
		map[string]any{"marker": "agent-note", "body": "evidence-backed note"}); err != nil {
		t.Fatal(err)
	}
	var commentQueued int
	if err := repository.db.QueryRowContext(ctx, `SELECT count(*) FROM outbox_messages WHERE dedupe_key=$1 AND message_type='gitlab.upsert_note'`,
		"tool-call:"+comment.CallID).Scan(&commentQueued); err != nil || commentQueued != 1 {
		t.Fatalf("comment outbox count=%d err=%v", commentQueued, err)
	}
	cancellableRunID, err := repository.StartAgentRun(ctx, workflow.ID, "", "CANCELLABLE", "test-model", "cancel-input")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RequestAgentRunCancellation(ctx, cancellableRunID); err != nil {
		t.Fatal(err)
	}
	if cancelled, err := repository.AgentRunCancellationRequested(ctx, cancellableRunID); err != nil || !cancelled {
		t.Fatalf("agent cancellation requested=%t err=%v", cancelled, err)
	}
	var toolStatus string
	if err := repository.db.QueryRowContext(ctx, `SELECT status FROM tool_calls WHERE id=$1`, allowed.CallID).Scan(&toolStatus); err != nil || toolStatus != "COMPLETED" {
		t.Fatalf("tool completion status=%s err=%v", toolStatus, err)
	}
	gateRequired, err := repository.AuthorizeToolCall(ctx, ToolAuthorizationRequest{AgentRunID: toolRunID, ToolKey: "staging.deploy",
		ProjectID: workflow.GitLabProjectID, AgentType: "REQUIREMENT", WorkflowState: string(workflow.State),
		Input: map[string]any{"sha": "abc"}, RedactedInput: map[string]any{"sha": "abc"}})
	if err != nil || gateRequired.Decision.Action != "DENY" {
		t.Fatalf("cross-stage staging tool was not denied %#v err=%v", gateRequired, err)
	}
	approvedSHA := "1234567890abcdef1234567890abcdef12345678"
	artifactID, approvedGateID := uuid.NewString(), uuid.NewString()
	if _, err := repository.db.ExecContext(ctx, `INSERT INTO artifacts(id,workflow_id,artifact_type,artifact_version,source_hash,content_json,markdown,model,prompt_version)
		VALUES($1,$2,'RELEASE_EVIDENCE',999,$3,'{}','test','test','test')`, artifactID, workflow.ID, approvedSHA); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `INSERT INTO gates(id,workflow_id,gate_type,status,artifact_id,revision,reviewer_ids,opened_at,decided_at,decision_actor,feedback)
		VALUES($1,$2,'RELEASE','APPROVED',$3,1,'[]',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,1,'approved')`, approvedGateID, workflow.ID, artifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `UPDATE workflows SET state='RELEASE_CI_RUNNING' WHERE id=$1`, workflow.ID); err != nil {
		t.Fatal(err)
	}
	releaseRunID, err := repository.StartAgentRun(ctx, workflow.ID, "", "RELEASE", "test-model", "release-tool-test")
	if err != nil {
		t.Fatal(err)
	}
	deploy, err := repository.AuthorizeToolCall(ctx, ToolAuthorizationRequest{AgentRunID: releaseRunID, ToolKey: "staging.deploy",
		ProjectID: workflow.GitLabProjectID, AgentType: "RELEASE", WorkflowState: "RELEASE_CI_RUNNING", GateID: approvedGateID, EvidenceVersion: 1,
		Input: map[string]any{"commit_sha": approvedSHA}, RedactedInput: map[string]any{"commit_sha": approvedSHA}})
	if err != nil || deploy.Decision.Action != "OUTBOX" {
		t.Fatalf("approved staging deploy not routed through outbox %#v err=%v", deploy, err)
	}
	if _, err := repository.EnqueueGovernedToolOutbox(ctx, deploy.CallID, "staging.deploy", approvedGateID,
		map[string]any{"commit_sha": approvedSHA}); err != nil {
		t.Fatal(err)
	}
	var deployQueued int
	if err := repository.db.QueryRowContext(ctx, `SELECT count(*) FROM outbox_messages WHERE dedupe_key=$1 AND message_type='delivery.trigger'`,
		"tool-call:"+deploy.CallID).Scan(&deployQueued); err != nil || deployQueued != 1 {
		t.Fatalf("deploy outbox count=%d err=%v", deployQueued, err)
	}
	production, err := repository.AuthorizeToolCall(ctx, ToolAuthorizationRequest{AgentRunID: releaseRunID, ToolKey: "production.deploy",
		ProjectID: workflow.GitLabProjectID, AgentType: "RELEASE", WorkflowState: "RELEASE_CI_RUNNING",
		Input: map[string]any{}, RedactedInput: map[string]any{}, ProductionLock: false})
	if err != nil || production.Decision.Action != "DENY" {
		t.Fatalf("production lock bypass %#v err=%v", production, err)
	}
	recorder := OpinionRecorder{Store: repository, AgentRunID: runID}
	if err := recorder.RecordOpinion(ctx, multiagent.Opinion{Role: "SECURITY", Decision: "CHANGES_REQUESTED", Confidence: .9,
		Summary: "risk", Findings: []string{"missing authorization"}, Evidence: []string{"source@v1"}}, true); err != nil {
		t.Fatal(err)
	}
	var opinions int
	if err := repository.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_opinions WHERE agent_run_id=$1 AND minority=true`, runID).Scan(&opinions); err != nil {
		t.Fatal(err)
	}
	if opinions != 1 {
		t.Fatalf("minority opinion not persisted: %d", opinions)
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
	selectedHits := append([]KnowledgeHit(nil), hits...)
	if _, err := repository.ProposeProjectMemory(ctx, ProjectMemory{ProjectID: projectID, Key: "missing-source", Content: "invalid"}, nil); err == nil {
		t.Fatal("memory without source was accepted")
	}
	workflow, err := repository.GetOrCreateWorkflow(ctx, domain.NewWorkflow(projectID, 82, "idempotency"))
	if err != nil {
		t.Fatal(err)
	}
	retrieved, err := repository.RetrieveKnowledge(ctx, workflow.ID, projectID, "idempotency", 50, 10)
	if err != nil || len(retrieved) != 1 {
		t.Fatalf("unexpected governed retrieval=%#v err=%v", retrieved, err)
	}
	var selected int
	if err := repository.db.QueryRowContext(ctx, `SELECT count(*) FROM retrieval_results rr
		JOIN retrieval_runs r ON r.id=rr.retrieval_run_id WHERE r.workflow_id=$1 AND rr.selected`, workflow.ID).Scan(&selected); err != nil {
		t.Fatal(err)
	}
	if selected != 1 {
		t.Fatalf("selected retrieval evidence=%d", selected)
	}
	if _, err := repository.RetrieveKnowledge(ctx, workflow.ID, projectID,
		"How do we preserve the payment retry idempotency key?", 50, 10); err != nil {
		t.Fatal(err)
	}
	var retrievalRounds, maxIteration, parentRounds, stoppedRounds int
	if err := repository.db.QueryRowContext(ctx, `SELECT count(*),max(iteration),count(*) FILTER(WHERE parent_run_id IS NOT NULL),
		count(*) FILTER(WHERE stop_reason<>'') FROM retrieval_runs WHERE workflow_id=$1 AND strategy='HYBRID_RRF_AGENTIC_V1'`, workflow.ID).
		Scan(&retrievalRounds, &maxIteration, &parentRounds, &stoppedRounds); err != nil {
		t.Fatal(err)
	}
	if retrievalRounds < 3 || maxIteration != 2 || parentRounds < 1 || stoppedRounds != retrievalRounds {
		t.Fatalf("retrieval audit rounds=%d max_iteration=%d parents=%d stopped=%d", retrievalRounds, maxIteration, parentRounds, stoppedRounds)
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
	if err := repository.ReviewProjectMemory(ctx, projectID, "payment-retry", "APPROVE", "engineer", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("candidate bypassed required review submission: %v", err)
	}
	if err := repository.SubmitProjectMemoryReview(ctx, projectID, "payment-retry", "review-requester"); err != nil {
		t.Fatal(err)
	}
	if err := repository.ReviewProjectMemory(ctx, projectID, "payment-retry", "APPROVE", "engineer", nil); err != nil {
		t.Fatal(err)
	}
	memories, err = repository.ActiveProjectMemories(ctx, projectID)
	if err != nil || len(memories) != 1 {
		t.Fatalf("approved memory missing %#v err=%v", memories, err)
	}
	if _, err := repository.ProposeProjectMemory(ctx, ProjectMemory{ProjectID: projectID, Key: "payment-retry",
		Content: "Updated retry memory", SourceDocumentID: hits[0].DocumentID}, []string{hits[0].ChunkID}); err != nil {
		t.Fatal(err)
	}
	var revisions int
	if err := repository.db.QueryRowContext(ctx, `SELECT count(*) FROM project_memory_revisions WHERE project_memory_id=$1`, memoryID).Scan(&revisions); err != nil || revisions < 2 {
		t.Fatalf("memory revisions=%d err=%v", revisions, err)
	}
	if err := repository.SubmitProjectMemoryReview(ctx, projectID, "payment-retry", "review-requester"); err != nil {
		t.Fatal(err)
	}
	if err := repository.ReviewProjectMemory(ctx, projectID, "payment-retry", "REVOKE", "engineer", nil); err != nil {
		t.Fatal(err)
	}
	memories, err = repository.ActiveProjectMemories(ctx, projectID)
	if err != nil || len(memories) != 0 {
		t.Fatalf("revoked memory remained active %#v err=%v", memories, err)
	}
	if _, err := repository.ProposeProjectMemory(ctx, ProjectMemory{ProjectID: projectID, Key: "source-revocation",
		Content: "must disappear with source", SourceDocumentID: hits[0].DocumentID}, []string{hits[0].ChunkID}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SubmitProjectMemoryReview(ctx, projectID, "source-revocation", "review-requester"); err != nil {
		t.Fatal(err)
	}
	if err := repository.ReviewProjectMemory(ctx, projectID, "source-revocation", "APPROVE", "engineer", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ProposeProjectMemory(ctx, ProjectMemory{ProjectID: projectID, Key: "expired-memory",
		Content: "temporary guidance", SourceDocumentID: hits[0].DocumentID}, []string{hits[0].ChunkID}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SubmitProjectMemoryReview(ctx, projectID, "expired-memory", "review-requester"); err != nil {
		t.Fatal(err)
	}
	expiredAt := time.Now().Add(-time.Minute)
	if err := repository.ReviewProjectMemory(ctx, projectID, "expired-memory", "APPROVE", "engineer", &expiredAt); err != nil {
		t.Fatal(err)
	}
	if expired, err := repository.ExpireProjectMemories(ctx); err != nil || expired < 1 {
		t.Fatalf("expired memories=%d err=%v", expired, err)
	}
	if err := repository.RevokeKnowledgeSource(ctx, projectID, "CONFLUENCE", "page-42"); err != nil {
		t.Fatal(err)
	}
	if err := repository.ValidateKnowledgeHits(ctx, selectedHits); err == nil {
		t.Fatal("revoked citation remained valid")
	}
	memories, err = repository.ActiveProjectMemories(ctx, projectID)
	if err != nil || len(memories) != 0 {
		t.Fatalf("memory from revoked source remained active %#v err=%v", memories, err)
	}
	hits, err = repository.SearchKnowledge(ctx, projectID, "idempotency", 0, 10)
	if err != nil || len(hits) != 0 {
		t.Fatalf("revoked source remained searchable %#v err=%v", hits, err)
	}
}

func TestV3SkillToolAndPolicyPromotionRequireIndependentApprovals(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	toolKey, skillKey := "review.tool."+suffix, "review-skill-"+suffix
	baseTool := ToolSeed{Key: toolKey, DisplayName: "Review Tool", RiskLevel: "L1", AdapterType: "internal",
		DefaultDecision: "ALLOW", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}
	baseSkill := SkillSeed{Key: skillKey, DisplayName: "Review Skill", Instructions: "version one",
		TriggerRules: map[string]any{"agent_types": []string{"REQUIREMENT"}}, Scope: map[string]any{"allowlist": true}}
	if err := repository.BootstrapGovernance(ctx, []ToolSeed{baseTool}, []SkillSeed{baseSkill}); err != nil {
		t.Fatal(err)
	}
	baseTool.InputSchema = json.RawMessage(`{"type":"object","required":["evidence"]}`)
	baseSkill.Instructions = "version two with evidence"
	if err := repository.BootstrapGovernance(ctx, []ToolSeed{baseTool}, []SkillSeed{baseSkill}); err != nil {
		t.Fatal(err)
	}
	var toolCandidateID, skillCandidateID string
	if err := repository.db.QueryRowContext(ctx, `SELECT tv.id FROM tool_versions tv JOIN tool_definitions td ON td.id=tv.tool_definition_id
		WHERE td.tool_key=$1 AND tv.status='DRAFT'`, toolKey).Scan(&toolCandidateID); err != nil {
		t.Fatal(err)
	}
	if err := repository.db.QueryRowContext(ctx, `SELECT sv.id FROM skill_versions sv JOIN skill_definitions sd ON sd.id=sv.skill_definition_id
		WHERE sd.skill_key=$1 AND sv.status='DRAFT'`, skillKey).Scan(&skillCandidateID); err != nil {
		t.Fatal(err)
	}
	if err := repository.PromoteSkillVersion(ctx, skillKey, skillCandidateID, "publisher"); !errors.Is(err, ErrGovernanceRequired) {
		t.Fatalf("skill bypassed approvals: %v", err)
	}
	for _, candidate := range []struct{ kind, id string }{{"SKILL", skillCandidateID}, {"TOOL_VERSION", toolCandidateID}} {
		if err := repository.SubmitRegistryChangeApproval(ctx, candidate.kind, candidate.id, "reviewer-a"); err != nil {
			t.Fatal(err)
		}
		if err := repository.SubmitRegistryChangeApproval(ctx, candidate.kind, candidate.id, "reviewer-b"); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.PromoteSkillVersion(ctx, skillKey, skillCandidateID, "publisher"); err != nil {
		t.Fatal(err)
	}
	if err := repository.PromoteToolVersion(ctx, toolKey, toolCandidateID, "publisher"); err != nil {
		t.Fatal(err)
	}
	policyID, err := repository.CreateToolPolicyCandidate(ctx, ToolPolicyCandidateInput{ToolKey: toolKey, AgentType: "REQUIREMENT",
		WorkflowState: "ANALYSIS", Decision: "ALLOW", Conditions: map[string]any{"allowed_actors": []string{"agent"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SubmitRegistryChangeApproval(ctx, "TOOL_POLICY", policyID, "reviewer-a"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SubmitRegistryChangeApproval(ctx, "TOOL_POLICY", policyID, "reviewer-b"); err != nil {
		t.Fatal(err)
	}
	if err := repository.PromoteToolPolicyCandidate(ctx, policyID, "publisher"); err != nil {
		t.Fatal(err)
	}
	var audits int
	if err := repository.db.QueryRowContext(ctx, `SELECT count(*) FROM registry_activation_audits
		WHERE activated_version_id IN ($1,$2,$3) AND actor='publisher'`, skillCandidateID, toolCandidateID, policyID).Scan(&audits); err != nil || audits != 3 {
		t.Fatalf("registry activation audits=%d err=%v", audits, err)
	}
}

func TestV3ImprovementCandidatesRequireEvidenceAndHumanReview(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	suiteID, err := repository.EnsureEvaluationSuite(ctx, "improvement-"+uuid.NewString(), "REQUIREMENT", map[string]any{"minimum": 0.8})
	if err != nil {
		t.Fatal(err)
	}
	caseID, err := repository.UpsertEvaluationCase(ctx, suiteID, EvaluationCaseInput{Key: "weak-case", Input: map[string]string{"requirement": "test"}, Expected: evaluation.Expectations{}, DataSplit: "TEST"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertEvaluationCase(ctx, suiteID, EvaluationCaseInput{Key: "weak-case", Input: map[string]string{"requirement": "updated"}, Expected: evaluation.Expectations{}, DataSplit: "TEST"}); err != nil {
		t.Fatal(err)
	}
	var caseRevisions int
	if err := repository.db.QueryRowContext(ctx, `SELECT count(*) FROM evaluation_case_revisions WHERE evaluation_case_id=$1`, caseID).Scan(&caseRevisions); err != nil || caseRevisions != 1 {
		t.Fatalf("case revisions=%d err=%v", caseRevisions, err)
	}
	runID, err := repository.StartEvaluationRun(ctx, EvaluationRunInput{SuiteID: suiteID, Shadow: true,
		Parameters: map[string]any{"temperature": 0, "seed": 42}})
	if err != nil {
		t.Fatal(err)
	}
	var parameters string
	if err := repository.db.QueryRowContext(ctx, `SELECT parameters_json::text FROM evaluation_runs WHERE id=$1`, runID).Scan(&parameters); err != nil || !strings.Contains(parameters, `"seed": 42`) {
		t.Fatalf("evaluation parameters=%s err=%v", parameters, err)
	}
	if err := repository.RecordEvaluationOutput(ctx, runID, caseID, json.RawMessage(`{"result":"weak"}`), "", time.Millisecond, nil,
		[]evaluation.Score{{ScorerKey: "deterministic", ScorerVersion: "1", Dimension: "completeness", Value: 0.4}}); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishEvaluationRun(ctx, runID, nil); err != nil {
		t.Fatal(err)
	}
	ids, err := repository.ProposeEvaluationImprovements(ctx, runID, 0.8)
	if err != nil || len(ids) != 1 {
		t.Fatalf("improvement ids=%v err=%v", ids, err)
	}
	if err := repository.ReviewImprovementCandidate(ctx, ids[0], "APPROVE", "reviewer"); err != nil {
		t.Fatal(err)
	}
	var status, reviewer string
	if err := repository.db.QueryRowContext(ctx, `SELECT status,reviewed_by FROM improvement_candidates WHERE id=$1`, ids[0]).Scan(&status, &reviewer); err != nil {
		t.Fatal(err)
	}
	if status != "APPROVED" || reviewer != "reviewer" {
		t.Fatalf("unexpected improvement review status=%s reviewer=%s", status, reviewer)
	}
	if _, err := repository.CreateImprovementCandidate(ctx, ImprovementCandidateInput{CandidateType: "MANUAL", TargetKey: "prompt", ExpectedImprovement: "better", RiskSummary: "risk"}); err == nil {
		t.Fatal("improvement without source evidence was accepted")
	}
}

func TestOperationalFailuresCreateReviewOnlyImprovementClusters(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	projectID := time.Now().UnixNano()
	workflow, err := repository.GetOrCreateWorkflow(ctx, domain.NewWorkflow(projectID, 33, "Operational learning"))
	if err != nil {
		t.Fatal(err)
	}
	artifactID, gateID := uuid.NewString(), uuid.NewString()
	if _, err := repository.db.ExecContext(ctx, `INSERT INTO artifacts
		(id,workflow_id,artifact_type,artifact_version,source_hash,content_json,markdown,model,prompt_version)
		VALUES($1,$2,'REQUIREMENT_REVIEW',1,$3,'{}','review','model','prompt')`, artifactID, workflow.ID, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `INSERT INTO gates
		(id,workflow_id,gate_type,status,artifact_id,revision,reviewer_ids,opened_at,feedback)
		VALUES($1,$2,'REQUIREMENT','CHANGES_REQUESTED',$3,1,'[7]',CURRENT_TIMESTAMP,'needs evidence')`, gateID, workflow.ID, artifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `INSERT INTO gate_decisions(gate_id,action,actor_id,actor_username,feedback)
		VALUES($1,'REQUEST_CHANGES',7,'reviewer','needs evidence')`, gateID); err != nil {
		t.Fatal(err)
	}
	workItemID, mergeRequestID, qualityRunID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := repository.db.ExecContext(ctx, `INSERT INTO work_items
		(id,workflow_id,work_item_key,title,state,owner_role) VALUES($1,$2,'API','API','REWORK','backend')`, workItemID, workflow.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `INSERT INTO merge_requests
		(id,work_item_id,gitlab_mr_iid,source_branch,target_branch,head_sha,state,draft)
		VALUES($1,$2,7,'feature','master',$3,'opened',false)`, mergeRequestID, workItemID, strings.Repeat("d", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `INSERT INTO quality_runs
		(id,merge_request_id,head_sha,attempt,status) VALUES($1,$2,$3,1,'FAILED')`, qualityRunID, mergeRequestID, strings.Repeat("d", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `INSERT INTO quality_findings
		(id,quality_run_id,category,severity,summary,evidence) VALUES($1,$2,'SECURITY','HIGH','unsafe input','test evidence')`, uuid.NewString(), qualityRunID); err != nil {
		t.Fatal(err)
	}
	if err := repository.UpsertPipelineRun(ctx, workflow.ID, workItemID, time.Now().UnixNano()%1000000,
		"feature", strings.Repeat("d", 40), "failed", "https://example.invalid/pipeline"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `INSERT INTO retrieval_runs
		(id,workflow_id,query_text,filters_json,strategy,stop_reason,finished_at)
		VALUES($1,$2,'missing architecture','{}','HYBRID_RRF_AGENTIC_V1','query_budget_exhausted',CURRENT_TIMESTAMP)`, uuid.NewString(), workflow.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordIncident(ctx, "incident-"+workflow.ID, "monitoring", workflow.ID, "high", "latency", "open", map[string]any{"safe": true}); err != nil {
		t.Fatal(err)
	}
	ids, err := repository.ProposeOperationalImprovements(ctx, projectID, "")
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, id := range ids {
		var candidateType, status string
		if err := repository.db.QueryRowContext(ctx, `SELECT candidate_type,status FROM improvement_candidates WHERE id=$1`, id).Scan(&candidateType, &status); err != nil {
			t.Fatal(err)
		}
		if status != "CANDIDATE" {
			t.Fatalf("operational candidate activated without review: %s", status)
		}
		types[candidateType] = true
	}
	for _, candidateType := range []string{"PROMPT_CHANGE", "SKILL_REVISION", "RETRIEVAL_POLICY", "EVALUATION_CASE", "PROJECT_MEMORY"} {
		if !types[candidateType] {
			t.Fatalf("missing operational improvement type %s: %#v", candidateType, types)
		}
	}
}

func TestKnowledgeIndexerFindsAndClearsSnapshotBacklog(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	projectID := time.Now().UnixNano()
	workflow, err := repository.GetOrCreateWorkflow(ctx, domain.NewWorkflow(projectID, 91, "Indexer"))
	if err != nil {
		t.Fatal(err)
	}
	pageID := "page-" + uuid.NewString()
	content := "recoverable indexed architecture evidence"
	digest := sha256.Sum256([]byte(content))
	if err := repository.SaveSnapshot(ctx, domain.Snapshot{ID: uuid.NewString(), WorkflowID: workflow.ID, ConfluencePageID: pageID,
		Version: 1, Title: "Recovery", URL: "https://example.test/" + pageID, UpdatedAt: time.Now().UTC().Format(time.RFC3339), ContentHash: hex.EncodeToString(digest[:]), NormalizedText: content}); err != nil {
		t.Fatal(err)
	}
	sources, err := repository.PendingKnowledgeSources(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	var selected *KnowledgeSource
	for index := range sources {
		if sources[index].ProjectID == projectID && sources[index].SourceKey == pageID {
			selected = &sources[index]
			break
		}
	}
	if selected == nil {
		t.Fatalf("snapshot source missing from backlog %#v", sources)
	}
	if _, created, err := repository.IngestKnowledge(ctx, *selected); err != nil || !created {
		t.Fatalf("index created=%t err=%v", created, err)
	}
	sources, err = repository.PendingKnowledgeSources(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		if source.ProjectID == projectID && source.SourceKey == pageID {
			t.Fatal("indexed snapshot remained in backlog")
		}
	}
}

func TestV3EvaluationRunIsIsolatedAndComparable(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	suiteKey := "requirement-" + uuid.NewString()
	suiteID, err := repository.EnsureEvaluationSuite(ctx, suiteKey, "REQUIREMENT", map[string]any{"minimum": 1})
	if err != nil {
		t.Fatal(err)
	}
	expectations := evaluation.Expectations{RequiredFields: []string{"decision", "facts"}, MinimumItems: map[string]int{"facts": 1}}
	caseID, err := repository.UpsertEvaluationCase(ctx, suiteID, EvaluationCaseInput{Key: "case-1",
		Input: map[string]any{"source": "synthetic"}, Expected: expectations,
		GoldenEvidence: []string{"synthetic"}, DataSplit: "HOLDOUT"})
	if err != nil {
		t.Fatal(err)
	}
	storedInput, storedExpectations, err := repository.EvaluationCase(ctx, caseID)
	if err != nil || !strings.Contains(string(storedInput), "synthetic") || len(storedExpectations.RequiredFields) != 2 {
		t.Fatalf("case round trip failed input=%s expected=%#v err=%v", storedInput, storedExpectations, err)
	}
	baselineID, err := repository.StartEvaluationRun(ctx, EvaluationRunInput{SuiteID: suiteID, Shadow: true})
	if err != nil {
		t.Fatal(err)
	}
	baselineOutput := json.RawMessage(`{"decision":"ready","facts":["fact"]}`)
	if err := repository.RecordEvaluationOutput(ctx, baselineID, caseID, baselineOutput, "", time.Millisecond,
		nil, evaluation.DeterministicScores(baselineOutput, expectations)); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishEvaluationRun(ctx, baselineID, nil); err != nil {
		t.Fatal(err)
	}
	candidateID, err := repository.StartEvaluationRun(ctx, EvaluationRunInput{SuiteID: suiteID, Shadow: true})
	if err != nil {
		t.Fatal(err)
	}
	candidateOutput := json.RawMessage(`{"decision":"ready","facts":[]}`)
	if err := repository.RecordEvaluationOutput(ctx, candidateID, caseID, candidateOutput, "", time.Millisecond,
		nil, evaluation.DeterministicScores(candidateOutput, expectations)); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishEvaluationRun(ctx, candidateID, nil); err != nil {
		t.Fatal(err)
	}
	comparison, err := repository.CompareEvaluationRuns(ctx, baselineID, candidateID)
	if err != nil || comparison.Decision != "REVIEW" {
		t.Fatalf("regression was not held for review comparison=%#v err=%v", comparison, err)
	}
	if comparison.Significance["minimum_items"].PairedSamples != 1 || comparison.Deltas["minimum_items"] >= 0 {
		t.Fatalf("paired comparison evidence missing: %#v", comparison)
	}
	var workflowWrites int
	if err := repository.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE event_type LIKE 'evaluation.%'`).Scan(&workflowWrites); err != nil {
		t.Fatal(err)
	}
	if workflowWrites != 0 {
		t.Fatalf("evaluation mutated workflow audit stream: %d", workflowWrites)
	}
	ids, err := repository.ProposeEvaluationImprovements(ctx, candidateID, 1)
	if err != nil || len(ids) != 0 {
		t.Fatalf("holdout findings leaked into automatic improvements ids=%v err=%v", ids, err)
	}
}

func TestSecurityEvaluationBootstrapIsIdempotentAndAdversarial(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	suiteID, err := repository.BootstrapSecurityEvaluationSuite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BootstrapSecurityEvaluationSuite(ctx); err != nil {
		t.Fatal(err)
	}
	cases, err := repository.EvaluationCases(ctx, suiteID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 2 {
		t.Fatalf("security evaluation cases=%d", len(cases))
	}
	for _, testCase := range cases {
		if !testCase.Expectations.ForbidToolRequests || !testCase.Expectations.ForbidProductionMutation || len(testCase.Expectations.ForbiddenStrings) == 0 {
			t.Fatalf("security case lacks exfiltration/tool constraints: %#v", testCase)
		}
	}
}

func TestCaptureHistoricalEvaluationCaseFreezesApprovedWorkflow(t *testing.T) {
	repository := integrationStore(t)
	ctx := context.Background()
	workflow, err := repository.GetOrCreateWorkflow(ctx, domain.NewWorkflow(time.Now().UnixNano(), 17, "Historical replay"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.Snapshot{ID: uuid.NewString(), WorkflowID: workflow.ID, ConfluencePageID: "history-page",
		Version: 3, Title: "Approved requirements", URL: "https://example.invalid/history-page",
		ContentHash: strings.Repeat("a", 64), NormalizedText: "Users can export an auditable report.", RawStorage: "<p>Users can export an auditable report.</p>"}
	if err := repository.SaveSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	artifactID := uuid.NewString()
	artifactContent := `{"decision":"ready","facts":["export required"],"acceptance_criteria":[{"id":"AC-1","behavior":"export","evidence":"report"}],"work_items":[{"key":"API","dependencies":[]}]}`
	if _, err := repository.db.ExecContext(ctx, `INSERT INTO artifacts
		(id,workflow_id,artifact_type,artifact_version,source_hash,content_json,markdown,model,prompt_version)
		VALUES($1,$2,'REQUIREMENT_REVIEW',1,$3,$4,'approved','model','prompt')`, artifactID, workflow.ID, strings.Repeat("b", 64), artifactContent); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `INSERT INTO gates
		(id,workflow_id,gate_type,status,artifact_id,revision,reviewer_ids,opened_at,decided_at,decision_actor,feedback)
		VALUES($1,$2,'REQUIREMENT','APPROVED',$3,1,'[7]',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,7,'')`, uuid.NewString(), workflow.ID, artifactID); err != nil {
		t.Fatal(err)
	}
	suiteID, err := repository.EnsureEvaluationSuite(ctx, "historical-"+workflow.ID, "REQUIREMENT", map[string]any{"minimum": 0.8})
	if err != nil {
		t.Fatal(err)
	}
	caseID, err := repository.CaptureHistoricalEvaluationCase(ctx, suiteID, workflow.ID, "", "VALIDATION")
	if err != nil {
		t.Fatal(err)
	}
	input, expectations, err := repository.EvaluationCase(ctx, caseID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(input), "history-page") || !strings.Contains(string(input), "auditable report") ||
		!expectations.ValidateWorkItemDependencies || !expectations.ForbidToolRequests || !expectations.ForbidProductionMutation {
		t.Fatalf("historical case was incomplete input=%s expectations=%#v", input, expectations)
	}
}
