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
	count int
	kind  string
}

func (f *fakeStore) EnqueueEvent(_ context.Context, _, kind string, _ any, _ time.Time) error {
	f.count++
	f.kind = kind
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
