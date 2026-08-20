package agents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/routing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestGenerateReturnsProviderTrace(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/responses" {
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		if request.Header.Get("Idempotency-Key") != "workflow:trace_test" {
			t.Fatalf("idempotency key=%q", request.Header.Get("Idempotency-Key"))
		}
		body := `{
			"id":"resp_test","status":"completed",
			"usage":{"input_tokens":120,"output_tokens":40,
				"input_tokens_details":{"cached_tokens":20},
				"output_tokens_details":{"reasoning_tokens":15}},
			"output":[{"type":"message","content":[{"type":"output_text","text":"{\"ok\":true}"}]}]
		}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	client := New("https://api.example.test", "test-key", "test-model")
	client.http.Transport = transport
	var output map[string]bool
	trace, err := client.generate(context.Background(), "workflow", "trace_test", "instructions", "input",
		json.RawMessage(`{"type":"object"}`), &output)
	if err != nil {
		t.Fatal(err)
	}
	if !output["ok"] {
		t.Fatalf("unexpected output %#v", output)
	}
	if trace.ProviderResponseID != "resp_test" || trace.InputTokens != 120 || trace.CachedTokens != 20 ||
		trace.OutputTokens != 40 || trace.ReasoningTokens != 15 || trace.FinishReason != "completed" {
		t.Fatalf("unexpected trace %#v", trace)
	}
	if trace.Latency <= 0 {
		t.Fatalf("expected positive latency, got %v", trace.Latency)
	}
}

func TestGenerateUsesControlledModelRoute(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body["model"] != "fallback" {
			t.Fatalf("unexpected routed model %v", body["model"])
		}
		response := `{"id":"resp_route","status":"completed","usage":{"input_tokens":100,"output_tokens":20,
			"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":5}},
			"output":[{"type":"message","content":[{"type":"output_text","text":"{\"ok\":true}"}]}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	client := New("https://api.example.test", "key", "preferred")
	client.http.Transport = transport
	client.ConfigureRouting([]routing.Model{
		{ID: "preferred", Key: "preferred", Active: true, Healthy: false, Quality: 100, Capabilities: map[string]bool{"structured_output": true}},
		{ID: "fallback", Key: "fallback", Active: true, Healthy: true, Quality: 90, InputCost: 100, OutputCost: 200, Capabilities: map[string]bool{"structured_output": true}},
	}, true, 10000)
	var output map[string]bool
	trace, err := client.generate(context.Background(), "workflow", "requirement_review_v1", "instructions", "input", json.RawMessage(`{"type":"object"}`), &output)
	if err != nil {
		t.Fatal(err)
	}
	if !trace.Fallback || trace.SelectedModelKey != "fallback" || trace.RiskLevel != "HIGH" || trace.RouteReason == "" {
		t.Fatalf("route trace missing %#v", trace)
	}
}
