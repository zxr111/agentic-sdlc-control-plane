//go:build integration

package engine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/agents"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/evaluation"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
)

func TestPromptEvaluationReplayIsShadowOnly(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_TEST_URL is not configured")
	}
	repository, err := store.Open(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	ctx := context.Background()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	definition := store.RegistryDefinition{AgentType: "REQUIREMENT", PromptKey: "eval-requirement", DisplayName: "Eval",
		Instructions: "Return the required JSON", OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"decision":{"type":"string"},"facts":{"type":"array","items":{"type":"string"}}},"required":["decision","facts"]}`)}
	definitions := []store.RegistryDefinition{definition}
	for _, builtin := range agents.BuiltinDefinitions() {
		if builtin.AgentType == "EVALUATION_JUDGE" {
			definitions = append(definitions, store.RegistryDefinition{AgentType: builtin.AgentType, PromptKey: builtin.PromptKey,
				DisplayName: builtin.DisplayName, Instructions: builtin.Instructions, OutputSchema: builtin.OutputSchema})
		}
	}
	if err := repository.BootstrapRegistry(ctx, "eval-model", definitions); err != nil {
		t.Fatal(err)
	}
	prompt, err := repository.ActivePromptVersion(ctx, "eval-requirement")
	if err != nil {
		t.Fatal(err)
	}
	suiteID, err := repository.EnsureEvaluationSuite(ctx, "eval-suite-"+prompt.ID, "REQUIREMENT", map[string]any{"minimum": 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.UpsertEvaluationCase(ctx, suiteID, store.EvaluationCaseInput{Key: "synthetic", Input: map[string]any{"source": "safe synthetic input"},
		Expected: evaluation.Expectations{RequiredFields: []string{"decision", "facts"}, MinimumItems: map[string]int{"facts": 1}}, DataSplit: "HOLDOUT"})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(response, `{"id":"resp_eval","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":1}},"output":[{"type":"message","content":[{"type":"output_text","text":"{\"decision\":\"ready\",\"facts\":[\"synthetic\"]}"}]}]}`)
			return
		}
		_, _ = io.WriteString(response, `{"id":"resp_judge","status":"completed","usage":{"input_tokens":8,"output_tokens":4},"output":[{"type":"message","content":[{"type":"output_text","text":"{\"dimensions\":[{\"name\":\"completeness\",\"score\":0.9,\"evidence\":\"all required fields\"}],\"summary\":\"anonymous review\"}"}]}]}`)
	}))
	defer server.Close()
	runner := New(repository, nil, nil, agents.New(server.URL, "test", "eval-model"), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runID, err := runner.RunPromptEvaluation(ctx, suiteID, prompt.ID)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := repository.EvaluationRunSummary(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "COMPLETED" || !summary.Shadow || summary.Outputs != 1 || summary.Scores != 6 || calls.Load() != 2 {
		t.Fatalf("unexpected replay summary %#v", summary)
	}
}
