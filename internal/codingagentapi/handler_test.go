package codingagentapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/codingagent"
)

type fakeRepository struct {
	manifest codingagent.TaskManifest
	evidence codingagent.Evidence
}

func (f *fakeRepository) EnsureCodingTaskManifest(context.Context, string, string, string) (codingagent.TaskManifest, error) {
	return f.manifest, nil
}

func (f *fakeRepository) SaveCodingAgentEvidence(_ context.Context, evidence codingagent.Evidence) error {
	f.evidence = evidence
	return nil
}

func TestManifestRequiresAuthentication(t *testing.T) {
	repository := &fakeRepository{}
	mux := http.NewServeMux()
	New("secret", repository).Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/coding-agent/dispatches/d/manifest", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}
