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

func TestRetryDelayIsBounded(t *testing.T) {
	if got := retryDelay(100).Seconds(); got != 300 {
		t.Fatalf("expected 300 seconds, got %v", got)
	}
}
