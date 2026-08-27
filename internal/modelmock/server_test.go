package modelmock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStructuredResponseConformsToRequiredShape(t *testing.T) {
	body := `{"model":"local-demo","text":{"format":{"name":"hello","schema":{"type":"object","properties":{"decision":{"type":"string","enum":["ready"]},"items":{"type":"array","items":{"type":"string"}}},"required":["decision","items"]}}}}`
	request := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(body))
	response := httptest.NewRecorder()
	new(Server).Routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Factory-Demo-Only") != "true" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(envelope.Output[0].Content[0].Text, `"decision":"ready"`) {
		t.Fatalf("output=%s", envelope.Output[0].Content[0].Text)
	}
}

func TestDemoRequirementIncludesRunnableWorkItem(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"decision": map[string]any{"type": "string", "enum": []any{"changes_requested", "ready_for_human_approval"}},
		"work_items": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
			"key": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"},
		}}},
	}}
	result := synthesize(schema, "root").(map[string]any)
	if result["decision"] != "ready_for_human_approval" || len(result["work_items"].([]any)) != 1 {
		t.Fatalf("result=%#v", result)
	}
}
