package agents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestGenerateReturnsProviderTrace(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/responses" {
			t.Fatalf("unexpected path %s", request.URL.Path)
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
