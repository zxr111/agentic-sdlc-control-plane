// Package modelmock provides a deterministic structured-output provider for
// local test demonstrations. It must never be used as production evidence.
package modelmock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
)

type Server struct{ sequence atomic.Int64 }

type request struct {
	Model string `json:"model"`
	Text  struct {
		Format struct {
			Name   string         `json:"name"`
			Schema map[string]any `json:"schema"`
		} `json:"format"`
	} `json:"text"`
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","mode":"local-demo"}`))
	})
	mux.HandleFunc("POST /responses", s.responses)
	return mux
}

func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	var input request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if err := decoder.Decode(&input); err != nil || input.Model == "" || len(input.Text.Format.Schema) == 0 {
		http.Error(w, "invalid structured-output request", http.StatusBadRequest)
		return
	}
	result := synthesize(input.Text.Format.Schema, input.Text.Format.Name)
	text, err := json.Marshal(result)
	if err != nil {
		http.Error(w, "cannot synthesize schema", http.StatusUnprocessableEntity)
		return
	}
	id := s.sequence.Add(1)
	envelope := map[string]any{
		"id": fmt.Sprintf("local-demo-%d", id), "model": input.Model, "status": "completed",
		"usage":  map[string]any{"input_tokens": 64, "output_tokens": 32, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}},
		"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": string(text)}}}},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Factory-Demo-Only", "true")
	_ = json.NewEncoder(w).Encode(envelope)
}

func synthesize(schema map[string]any, field string) any {
	if values, ok := schema["enum"].([]any); ok && len(values) > 0 {
		if field == "decision" {
			for _, preferred := range []string{"ready_for_human_approval", "READY"} {
				for _, value := range values {
					if value == preferred {
						return value
					}
				}
			}
		}
		return values[0]
	}
	switch schema["type"] {
	case "object":
		result := map[string]any{}
		properties, _ := schema["properties"].(map[string]any)
		for key, raw := range properties {
			property, _ := raw.(map[string]any)
			result[key] = synthesize(property, key)
		}
		return result
	case "array":
		items, _ := schema["items"].(map[string]any)
		minimum, _ := schema["minItems"].(float64)
		localDemoRequired := map[string]bool{
			"acceptance_criteria": true, "work_items": true, "functional_requirements": true,
			"test_cases": true, "coverage_matrix": true, "implementation_units": true,
		}
		if minimum > 0 || localDemoRequired[field] {
			return []any{synthesize(items, field)}
		}
		return []any{}
	case "integer", "number":
		return 0
	case "boolean":
		return false
	case "null":
		return nil
	default:
		values := map[string]string{
			"id": "AC-1", "key": "hello-world", "title": "[Feature][HELLO] Add Hello World API",
			"behavior": "GET /hello 返回 HTTP 200 和 Hello World JSON", "evidence": "自动化 API 测试通过",
			"work_item_key": "hello-world", "repository": "argus/argus-server",
			"acceptance_criterion": "AC-1", "goal": "实现可验证的 Hello World API",
			"summary": "本地端到端演示需求已具备可测试验收标准。",
		}
		if value := values[field]; value != "" {
			return value
		}
		return "本地演示：" + field
	}
}
