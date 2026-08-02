package webhook

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
)

type fakeStore struct {
	count   int
	kind    string
	payload any
}

func (f *fakeStore) EnqueueEvent(_ context.Context, _, kind string, payload any, _ time.Time) error {
	f.count++
	f.kind = kind
	f.payload = payload
	return nil
}
func (f *fakeStore) Ping(context.Context) error { return nil }

func TestIssueWebhookFiltersEnabledLabel(t *testing.T) {
	store := &fakeStore{}
	handler := New("secret", map[int64]domain.ProjectConfig{
		1: {GitLabProjectID: 1, EnabledLabel: "automation::enabled"},
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()
	body := `{"object_kind":"issue","project":{"id":1},"object_attributes":{"iid":4,"title":"x","action":"open"},"labels":[{"title":"automation::enabled"}]}`
	request := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", strings.NewReader(body))
	request.Header.Set("X-Gitlab-Token", "secret")
	request.Header.Set("X-Gitlab-Event", "Issue Hook")
	request.Header.Set("X-Gitlab-Event-UUID", "event-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || store.count != 1 || store.kind != "gitlab.issue.changed" {
		t.Fatalf("status=%d count=%d kind=%s", response.Code, store.count, store.kind)
	}
}

func TestWebhookRejectsWrongSecret(t *testing.T) {
	store := &fakeStore{}
	handler := New("secret", map[int64]domain.ProjectConfig{}, store, slog.Default()).Routes()
	request := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", strings.NewReader(`{}`))
	request.Header.Set("X-Gitlab-Token", "wrong")
	request.Header.Set("X-Gitlab-Event", "Issue Hook")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestNoteWebhookAcceptsExactControlCommand(t *testing.T) {
	store := &fakeStore{}
	handler := New("secret", map[int64]domain.ProjectConfig{
		1: {GitLabProjectID: 1},
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()
	body := `{"object_kind":"note","project":{"id":1},"user":{"id":9,"username":"engineer"},` +
		`"issue":{"iid":4},"object_attributes":{"id":22,"noteable_type":"Issue",` +
		`"note":"/start-codex task:51b9ca7e-a6f1-4bcf-a3d3-f113b9e44cb2 client:mac-1"}}`
	request := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", strings.NewReader(body))
	request.Header.Set("X-Gitlab-Token", "secret")
	request.Header.Set("X-Gitlab-Event", "Note Hook")
	request.Header.Set("X-Gitlab-Event-UUID", "event-control")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || store.kind != "gitlab.control.command" {
		t.Fatalf("status=%d kind=%s", response.Code, store.kind)
	}
}

func TestMonitoringCallbackRequiresSharedSecret(t *testing.T) {
	store := &fakeStore{}
	handler := New("secret", map[int64]domain.ProjectConfig{}, store, slog.Default()).Routes()
	request := httptest.NewRequest(http.MethodPost, "/callbacks/monitoring",
		strings.NewReader(`{"external_id":"sentry-1","severity":"high"}`))
	request.Header.Set("X-AI-Factory-Token", "wrong")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || store.count != 0 {
		t.Fatalf("status=%d count=%d", response.Code, store.count)
	}
}

func TestDeliveryCallbackCarriesProductionMigrationEvidence(t *testing.T) {
	store := &fakeStore{}
	handler := NewWithCallbackSecret("webhook", "callback", map[int64]domain.ProjectConfig{
		1: {GitLabProjectID: 1},
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()
	request := httptest.NewRequest(http.MethodPost, "/callbacks/jenkins", strings.NewReader(`{
		"external_id":"verify-1",
		"project_id":1,
		"workflow_id":"51b9ca7e-a6f1-4bcf-a3d3-f113b9e44cb2",
		"status":"staging_verified",
		"commit_sha":"0123456789012345678901234567890123456789",
		"requires_production_migration":true,
		"migration_plan":"run migration job",
		"rollback_plan":"restore snapshot",
		"change_window":"2026-08-01T01:00:00Z"
	}`))
	request.Header.Set("X-AI-Factory-Token", "callback")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	callback, ok := store.payload.(ExternalCallback)
	if response.Code != http.StatusAccepted || store.kind != "external.delivery" || !ok {
		t.Fatalf("status=%d kind=%s payload=%T", response.Code, store.kind, store.payload)
	}
	if !callback.RequiresProductionMigration || callback.MigrationPlan == "" || callback.RollbackPlan == "" {
		t.Fatalf("migration evidence was not preserved: %#v", callback)
	}
}
