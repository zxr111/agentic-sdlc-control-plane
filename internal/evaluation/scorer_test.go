package evaluation

import (
	"encoding/json"
	"testing"
)

func TestDeterministicScores(t *testing.T) {
	expectations := Expectations{RequiredFields: []string{"decision", "facts"},
		RequiredCitations: []string{"page-42@v7"}, ForbiddenStrings: []string{"deploy production"},
		MinimumItems: map[string]int{"facts": 1}}
	scores := DeterministicScores(json.RawMessage(`{"decision":"ready","facts":["page-42@v7"]}`), expectations)
	if got := Overall(scores); got != 1 {
		t.Fatalf("expected perfect score, got %v: %#v", got, scores)
	}
	failing := DeterministicScores(json.RawMessage(`{"decision":"ready","facts":[],"note":"deploy production"}`), expectations)
	if got := Overall(failing); got >= 1 {
		t.Fatalf("expected contract failures, got %v: %#v", got, failing)
	}
}

func TestInvalidJSONStopsScoring(t *testing.T) {
	scores := DeterministicScores(json.RawMessage(`{"broken"`), Expectations{})
	if len(scores) != 1 || scores[0].Dimension != "json_valid" || scores[0].Value != 0 {
		t.Fatalf("unexpected scores %#v", scores)
	}
}
