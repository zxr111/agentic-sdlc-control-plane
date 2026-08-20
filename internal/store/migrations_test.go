package store

import (
	"strings"
	"testing"
)

func TestEmbeddedMigrations(t *testing.T) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one migration")
	}
}

func TestFullLifecycleMigrationContainsNoRunnerLeaseModel(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/002_full_lifecycle.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	for _, table := range []string{
		"work_items", "work_item_dependencies", "codex_dispatches", "agent_runs",
		"merge_requests", "quality_runs", "quality_findings", "pipeline_runs",
		"release_candidates", "deployments", "observation_windows", "incidents", "email_relays",
	} {
		if !strings.Contains(source, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("missing V2 table %s", table)
		}
	}
	for _, forbidden := range []string{"runner_leases", "codex_heartbeats"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("unexpected headless Runner model %s", forbidden)
		}
	}
}

func TestV3AgentPlatformMigrationContainsGovernedEntities(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/003_v3_agent_platform.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	for _, table := range []string{
		"prompt_definitions", "prompt_versions", "model_providers", "model_versions", "model_policies",
		"agent_profiles", "agent_profile_versions", "skill_definitions", "skill_versions",
		"context_manifests", "context_entries", "knowledge_documents", "knowledge_versions", "knowledge_chunks",
		"retrieval_runs", "retrieval_results", "project_memories", "tool_definitions", "tool_versions",
		"tool_policies", "tool_calls", "agent_steps", "agent_opinions", "evaluation_suites",
		"evaluation_cases", "evaluation_runs", "evaluation_outputs", "evaluation_scores", "evaluation_comparisons",
	} {
		if !strings.Contains(source, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("missing V3 table %s", table)
		}
	}
	for _, column := range []string{"agent_profile_version_id", "prompt_version_id", "model_version_id", "context_manifest_id", "input_tokens", "output_tokens", "latency_ms"} {
		if !strings.Contains(source, "agent_runs ADD COLUMN IF NOT EXISTS "+column) {
			t.Fatalf("missing agent run observability column %s", column)
		}
	}
}

func TestV3RoutingMigrationContainsHealthAndDecisionEvidence(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/004_v3_routing.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"model_health_events", "model_route_decisions"} {
		if !strings.Contains(string(content), "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("missing routing table %s", table)
		}
	}
}

func TestV3MemoryAndImprovementMigrationContainsGovernedEntities(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/008_v3_memory_and_improvement.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"project_memory_revisions", "improvement_candidates"} {
		if !strings.Contains(string(content), "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("missing memory and improvement table %s", table)
		}
	}
}

func TestV3ReplayabilityMigrationPreservesCaseHistoryAndParameters(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/009_v3_replayability.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	if !strings.Contains(source, "CREATE TABLE IF NOT EXISTS evaluation_case_revisions") ||
		!strings.Contains(source, "evaluation_runs ADD COLUMN IF NOT EXISTS parameters_json") {
		t.Fatal("replayability migration is incomplete")
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	if got := retryDelay(100).Seconds(); got != 300 {
		t.Fatalf("expected 300 seconds, got %v", got)
	}
}
