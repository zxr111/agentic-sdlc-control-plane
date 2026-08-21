package agents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/multiagent"
)

type observerStub struct {
	mu         sync.Mutex
	runs       map[string]string
	finished   int
	recorded   []multiagent.Opinion
	minorities int
}

func (o *observerStub) Start(_ context.Context, role string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	id := "run-" + role
	o.runs[role] = id
	return id, nil
}
func (o *observerStub) Finish(_ context.Context, _ string, trace Trace, runErr error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if runErr == nil && trace.ProviderResponseID != "" {
		o.finished++
	}
	return nil
}
func (o *observerStub) RecordOpinion(_ context.Context, opinion multiagent.Opinion, minority bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.recorded = append(o.recorded, opinion)
	if minority {
		o.minorities++
	}
	return nil
}

func TestGovernedMultiAgentRunnerUsesIndependentCalls(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body["max_output_tokens"] != float64(2048) || body["reasoning"].(map[string]any)["effort"] != "high" {
			t.Fatalf("multi-agent profile budget was not applied: %#v", body)
		}
		format := body["text"].(map[string]any)["format"].(map[string]any)["name"].(string)
		payload := ""
		if format == "agent_synthesis_v1" {
			payload = `{"decision":"READY","consensus":["traceable"],"disagreements":[],"unresolved_risks":[],"summary":"judge"}`
		} else {
			decision := "READY"
			instructions := body["instructions"].(string)
			if strings.Contains(instructions, "SECURITY_RELIABILITY") {
				decision = "CHANGES_REQUESTED"
			}
			payload = `{"role":"member","decision":"` + decision + `","confidence":0.8,"summary":"review","findings":[],"evidence":["source@v1"],"metadata":{}}`
		}
		response := `{"id":"resp-` + format + `","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":1}},"output":[{"type":"message","content":[{"type":"output_text","text":` + mustJSON(payload) + `}]}]}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	client := New("https://api.example.test", "key", "model")
	client.http.Transport = transport
	observer := &observerStub{runs: map[string]string{}}
	runner := NewGovernedMultiAgentRunnerWithPrompts(client, observer, map[string]RolePrompt{
		multiagent.RolePrimary:  {Instructions: "PRIMARY", Schema: opinionSchema, MaxOutputTokens: 2048, ReasoningEffort: "high"},
		multiagent.RoleCritic:   {Instructions: "CRITIC", Schema: opinionSchema, MaxOutputTokens: 2048, ReasoningEffort: "high"},
		multiagent.RoleSecurity: {Instructions: "SECURITY_RELIABILITY", Schema: opinionSchema, MaxOutputTokens: 2048, ReasoningEffort: "high"},
		multiagent.RoleJudge:    {Instructions: "JUDGE", Schema: synthesisSchema, MaxOutputTokens: 2048, ReasoningEffort: "high"},
	})
	_, synthesis, err := multiagent.New(runner).Execute(context.Background(), multiagent.Input{WorkflowID: "w", AgentType: "REQUIREMENT", AuthoritativeText: "source", PrimaryArtifact: []byte(`{}`)}, observer)
	if err != nil {
		t.Fatal(err)
	}
	if len(observer.runs) != 4 || observer.finished != 4 {
		t.Fatalf("runs were not independent runs=%#v finished=%d", observer.runs, observer.finished)
	}
	if len(synthesis.Disagreements) != 1 || synthesis.Disagreements[0].Role != multiagent.RoleSecurity || observer.minorities != 1 {
		t.Fatalf("minority not preserved synthesis=%#v recorded=%#v", synthesis, observer.recorded)
	}
}

func mustJSON(value string) string { raw, _ := json.Marshal(value); return string(raw) }
