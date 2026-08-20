package evaluation

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Expectations struct {
	RequiredFields    []string       `json:"required_fields"`
	RequiredCitations []string       `json:"required_citations"`
	ForbiddenStrings  []string       `json:"forbidden_strings"`
	MinimumItems      map[string]int `json:"minimum_items"`
}

type Score struct {
	ScorerKey     string         `json:"scorer_key"`
	ScorerVersion string         `json:"scorer_version"`
	Dimension     string         `json:"dimension"`
	Value         float64        `json:"value"`
	Evidence      map[string]any `json:"evidence"`
}

func DeterministicScores(output json.RawMessage, expectations Expectations) []Score {
	const scorer = "deterministic-contract"
	const version = "v1"
	var decoded map[string]any
	if err := json.Unmarshal(output, &decoded); err != nil {
		return []Score{{ScorerKey: scorer, ScorerVersion: version, Dimension: "json_valid", Value: 0,
			Evidence: map[string]any{"error": err.Error()}}}
	}
	scores := []Score{{ScorerKey: scorer, ScorerVersion: version, Dimension: "json_valid", Value: 1,
		Evidence: map[string]any{"valid": true}}}
	missing := []string{}
	for _, field := range expectations.RequiredFields {
		if value, ok := decoded[field]; !ok || value == nil || value == "" {
			missing = append(missing, field)
		}
	}
	scores = append(scores, binaryScore(scorer, version, "required_fields", len(missing) == 0,
		map[string]any{"missing": missing}))
	raw := strings.ToLower(string(output))
	missingCitations := []string{}
	for _, citation := range expectations.RequiredCitations {
		if !strings.Contains(raw, strings.ToLower(citation)) {
			missingCitations = append(missingCitations, citation)
		}
	}
	scores = append(scores, binaryScore(scorer, version, "citations", len(missingCitations) == 0,
		map[string]any{"missing": missingCitations}))
	foundForbidden := []string{}
	for _, forbidden := range expectations.ForbiddenStrings {
		if forbidden != "" && strings.Contains(raw, strings.ToLower(forbidden)) {
			foundForbidden = append(foundForbidden, forbidden)
		}
	}
	scores = append(scores, binaryScore(scorer, version, "policy_safety", len(foundForbidden) == 0,
		map[string]any{"found": foundForbidden}))
	violations := map[string]string{}
	for field, minimum := range expectations.MinimumItems {
		items, ok := decoded[field].([]any)
		if !ok || len(items) < minimum {
			violations[field] = fmt.Sprintf("expected at least %d items", minimum)
		}
	}
	scores = append(scores, binaryScore(scorer, version, "minimum_items", len(violations) == 0,
		map[string]any{"violations": violations}))
	return scores
}

func binaryScore(scorer, version, dimension string, passed bool, evidence map[string]any) Score {
	value := float64(0)
	if passed {
		value = 1
	}
	return Score{ScorerKey: scorer, ScorerVersion: version, Dimension: dimension, Value: value, Evidence: evidence}
}

func Overall(scores []Score) float64 {
	if len(scores) == 0 {
		return 0
	}
	total := float64(0)
	for _, score := range scores {
		total += score.Value
	}
	return total / float64(len(scores))
}
