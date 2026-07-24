package store

import "testing"

func TestEmbeddedMigrations(t *testing.T) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one migration")
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	if got := retryDelay(100).Seconds(); got != 300 {
		t.Fatalf("expected 300 seconds, got %v", got)
	}
}
