package config

import (
	"os"
	"strings"
	"testing"
)

func TestAgentRuntimeAndDispatcherCredentialBoundary(t *testing.T) {
	runtime := readDeployment(t, "../../deploy/base/agent-runtime.yaml")
	for _, forbidden := range []string{"DATABASE_URL", "GITLAB_API_TOKEN", "CONFLUENCE_API_TOKEN", "CALLBACK_SHARED_SECRET"} {
		if strings.Contains(runtime, forbidden) {
			t.Fatalf("Agent Runtime manifest contains forbidden credential %s", forbidden)
		}
	}
	for _, required := range []string{"factory-agent-runtime", "OPENAI_API_KEY", "AGENT_RUNTIME_SHARED_SECRET", "containerPort: 8090"} {
		if !strings.Contains(runtime, required) {
			t.Fatalf("Agent Runtime manifest missing %s", required)
		}
	}
	dispatcher := readDeployment(t, "../../deploy/base/agent-dispatcher.yaml")
	for _, forbidden := range []string{"OPENAI_API_KEY", "GITLAB_API_TOKEN", "CONFLUENCE_API_TOKEN"} {
		if strings.Contains(dispatcher, forbidden) {
			t.Fatalf("Agent Dispatcher manifest contains forbidden credential %s", forbidden)
		}
	}
	for _, required := range []string{"AGENT_DATABASE_URL", "AGENT_RUNTIME_URL", "AGENT_RUNTIME_SHARED_SECRET"} {
		if !strings.Contains(dispatcher, required) {
			t.Fatalf("Agent Dispatcher manifest missing %s", required)
		}
	}
	evaluation := readDeployment(t, "../../deploy/base/evaluation-worker.yaml")
	if strings.Contains(evaluation, "OPENAI_API_KEY") || !strings.Contains(evaluation, "AGENT_RUNTIME_URL") {
		t.Fatal("Evaluation Worker bypasses the isolated Agent Runtime")
	}
}

func TestV3CIAndDatabaseRoleProvisioning(t *testing.T) {
	ci := readDeployment(t, "../../.gitlab-ci.yml")
	for _, required := range []string{"pgvector/pgvector:pg16", "AGENT_DATABASE_URL", "EVALUATION_DATABASE_URL",
		"AGENT_RUNTIME_SHARED_SECRET", "./internal/toolgateway", "ai-sdlc-factory-agent-runtime", "ai-sdlc-factory-knowledge-indexer"} {
		if !strings.Contains(ci, required) {
			t.Fatalf("V3 CI is missing %s", required)
		}
	}
	grants := readDeployment(t, "../../deploy/postgres/v3-role-grants.sql")
	for _, required := range []string{"ai_factory_agent", "ai_factory_evaluation", "REVOKE CREATE ON SCHEMA", "REVOKE ALL ON schema_migrations"} {
		if !strings.Contains(grants, required) {
			t.Fatalf("V3 database role grants are missing %s", required)
		}
	}
}

func readDeployment(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
