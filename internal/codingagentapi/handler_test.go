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

func (f *fakeRepository) EnsureCodingSession(context.Context, string) (codingagent.CodingSession, error) {
	return codingagent.CodingSession{ID: "session", State: codingagent.StatePlanning}, nil
}
func (f *fakeRepository) GetCodingSession(context.Context, string) (codingagent.CodingSession, error) {
	return codingagent.CodingSession{ID: "session", State: codingagent.StatePlanning}, nil
}
func (f *fakeRepository) PendingCodingRuns(context.Context, string) ([]codingagent.AgentRun, error) {
	return []codingagent.AgentRun{{ID: "run", Role: codingagent.RolePlanner}}, nil
}
func (f *fakeRepository) StartCodingAgentRun(context.Context, string, string) (codingagent.AgentRun, error) {
	return codingagent.AgentRun{ID: "run", Status: codingagent.RunRunning}, nil
}
func (f *fakeRepository) CompleteCodingAgentRun(context.Context, string, codingagent.RunCompletion) (codingagent.CodingSession, error) {
	return codingagent.CodingSession{ID: "session", State: codingagent.StateTestDesign}, nil
}
func (f *fakeRepository) FailCodingAgentRun(context.Context, string, codingagent.RunFailure) (codingagent.CodingSession, error) {
	return codingagent.CodingSession{ID: "session", State: codingagent.StateFailed}, nil
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
