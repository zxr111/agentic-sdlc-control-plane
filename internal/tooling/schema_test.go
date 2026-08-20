package tooling

import (
	"encoding/json"
	"testing"
)

func TestValidateJSONFailsClosed(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["commit_sha"],"properties":{"commit_sha":{"type":"string","pattern":"^[a-f0-9]{40}$"}}}`)
	if err := ValidateJSON(schema, map[string]any{"commit_sha": "1234567890abcdef1234567890abcdef12345678"}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []any{map[string]any{}, map[string]any{"commit_sha": "main"}, map[string]any{"commit_sha": "1234567890abcdef1234567890abcdef12345678", "extra": true}} {
		if err := ValidateJSON(schema, invalid); err == nil {
			t.Fatalf("invalid input accepted: %#v", invalid)
		}
	}
	if err := ValidateJSON(json.RawMessage(`{"type":"object","oneOf":[]}`), map[string]any{}); err == nil {
		t.Fatal("unsupported schema keyword was ignored")
	}
}
