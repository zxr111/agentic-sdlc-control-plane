//go:build integration

package store

import (
	"context"
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
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not configured")
	}
	repository, err := Open(dsn)
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
	key := "integration:" + uuid.NewString()
	if err := repository.EnqueueEvent(ctx, key, "test", map[string]string{"id": key}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	wait.Add(2)
	results := make(chan error, 2)
	for _, worker := range []string{"one", "two"} {
		go func(worker string) {
			defer wait.Done()
			_, err := repository.ClaimEvent(ctx, worker, time.Minute)
			results <- err
		}(worker)
	}
	wait.Wait()
	close(results)
	var claims, empty int
	for err := range results {
		switch {
		case err == nil:
			claims++
		case errors.Is(err, ErrNotFound):
			empty++
		default:
			t.Fatal(err)
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
