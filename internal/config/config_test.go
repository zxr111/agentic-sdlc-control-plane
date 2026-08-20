package config

import (
	"os"
	"testing"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
)

func TestLoadProjects(t *testing.T) {
	t.Setenv("FACTORY_PROJECTS_JSON", `[{
		"gitlab_project_id": 10,
		"path": "argus/argus-server",
		"reviewer_ids": {
			"REQUIREMENT": [1],
			"PRD": [2],
			"TEST": [3]
		}
	}]`)
	got, err := loadProjects()
	if err != nil {
		t.Fatal(err)
	}
	if got[10].EnabledLabel != "automation::enabled" {
		t.Fatalf("default label missing: %#v", got[10])
	}
}

func TestComponentValidationUsesLeastPrivilegeCredentials(t *testing.T) {
	project := domain.ProjectConfig{GitLabProjectID: 10, Path: "argus/test", ReviewerIDs: map[domain.GateType][]int64{
		domain.GateRequirement: {1}, domain.GatePRD: {1}, domain.GateTest: {1},
	}}
	base := Config{DatabaseURL: "postgres://test", Projects: map[int64]domain.ProjectConfig{10: project}}
	agent := base
	agent.ComponentMode = "agent-runtime"
	agent.OpenAIAPIKey = "model-key"
	if err := agent.Validate(); err != nil {
		t.Fatalf("agent runtime required unrelated external credentials: %v", err)
	}
	api := base
	api.ComponentMode = "api"
	api.GitLabWebhookSecret = "webhook"
	api.CallbackSharedSecret = "callback"
	if err := api.Validate(); err != nil {
		t.Fatalf("API required unrelated worker credentials: %v", err)
	}
	worker := base
	worker.ComponentMode = "worker"
	worker.GitLabToken = "gitlab"
	worker.ConfluenceEmail = "service@example.test"
	worker.ConfluenceToken = "confluence"
	if err := worker.Validate(); err != nil {
		t.Fatalf("worker required unrelated model credentials: %v", err)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
