package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
)

type fakeReader struct {
	data store.DashboardData
	err  error
}

func (f fakeReader) Dashboard(context.Context, int) (store.DashboardData, error) {
	return f.data, f.err
}

func dashboardMux(reader Reader) *http.ServeMux {
	mux := http.NewServeMux()
	New(reader, map[int64]domain.ProjectConfig{
		3533: {GitLabProjectID: 3533, Path: "argus/argus-server"},
	}, "https://git.example.com/api/v4", slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux)
	return mux
}

func TestIndexAndAssetsAreEmbedded(t *testing.T) {
	mux := dashboardMux(fakeReader{})
	for _, path := range []string{"/dashboard/", "/dashboard/assets/app.css", "/dashboard/assets/app.js"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d", path, response.Code)
		}
		if response.Body.Len() == 0 {
			t.Fatalf("%s returned an empty body", path)
		}
	}
}

func TestDashboardAPIEnrichesGitLabIssueURL(t *testing.T) {
	now := time.Now().UTC()
	mux := dashboardMux(fakeReader{data: store.DashboardData{
		GeneratedAt: now,
		Workflows: []store.DashboardWorkflow{{
			ID: "workflow-1", GitLabProjectID: 3533, IssueIID: 478,
			IssueTitle: "PID", State: domain.StateWaitingRequirementReview,
		}},
	}})
	request := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var data store.DashboardData
	if err := json.NewDecoder(response.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	if len(data.Workflows) != 1 {
		t.Fatalf("workflows=%d", len(data.Workflows))
	}
	workflow := data.Workflows[0]
	if workflow.ProjectPath != "argus/argus-server" {
		t.Fatalf("project_path=%q", workflow.ProjectPath)
	}
	if !strings.HasSuffix(workflow.IssueURL, "/argus/argus-server/-/issues/478") {
		t.Fatalf("issue_url=%q", workflow.IssueURL)
	}
}
