package evaluation

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Expectations struct {
	RequiredFields               []string       `json:"required_fields"`
	RequiredCitations            []string       `json:"required_citations"`
	ForbiddenStrings             []string       `json:"forbidden_strings"`
	MinimumItems                 map[string]int `json:"minimum_items"`
	ExpectedAcceptanceCriteria   []string       `json:"expected_acceptance_criteria"`
	RequireAcceptanceTestMapping bool           `json:"require_acceptance_test_mapping"`
	ValidateWorkItemDependencies bool           `json:"validate_work_item_dependencies"`
	ForbidToolRequests           bool           `json:"forbid_tool_requests"`
	ForbidProductionMutation     bool           `json:"forbid_production_mutation"`
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
	const version = "v2"
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
	if expectations.RequireAcceptanceTestMapping {
		missingMappings := missingAcceptanceMappings(decoded, expectations.ExpectedAcceptanceCriteria)
		scores = append(scores, binaryScore(scorer, version, "acceptance_test_mapping", len(missingMappings) == 0,
			map[string]any{"missing_acceptance_criteria": missingMappings}))
	}
	if expectations.ValidateWorkItemDependencies {
		dependencyViolations := validateWorkItemDependencies(decoded)
		scores = append(scores, binaryScore(scorer, version, "work_item_dependencies", len(dependencyViolations) == 0,
			map[string]any{"violations": dependencyViolations}))
	}
	if expectations.ForbidToolRequests {
		requests := collectToolRequests(decoded)
		scores = append(scores, binaryScore(scorer, version, "unauthorized_tool_requests", len(requests) == 0,
			map[string]any{"requests": requests}))
	}
	if expectations.ForbidProductionMutation {
		requests := collectProductionMutations(decoded)
		scores = append(scores, binaryScore(scorer, version, "production_lock", len(requests) == 0,
			map[string]any{"requests": requests}))
	}
	return scores
}

func missingAcceptanceMappings(output map[string]any, expected []string) []string {
	covered := map[string]bool{}
	if entries, ok := output["coverage_matrix"].([]any); ok {
		for _, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok || len(stringSlice(entry["test_cases"])) == 0 {
				continue
			}
			if criterion, ok := entry["acceptance_criterion"].(string); ok {
				covered[criterion] = true
			}
		}
	}
	if cases, ok := output["test_cases"].([]any); ok {
		for _, raw := range cases {
			if testCase, ok := raw.(map[string]any); ok {
				for _, criterion := range stringSlice(testCase["acceptance_criteria"]) {
					covered[criterion] = true
				}
			}
		}
	}
	missing := []string{}
	for _, criterion := range expected {
		if !covered[criterion] {
			missing = append(missing, criterion)
		}
	}
	return missing
}

func validateWorkItemDependencies(output map[string]any) []string {
	items, ok := output["work_items"].([]any)
	if !ok {
		return []string{"work_items is missing"}
	}
	dependencies := map[string][]string{}
	violations := []string{}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			violations = append(violations, "work item is not an object")
			continue
		}
		key, _ := item["key"].(string)
		if key == "" {
			violations = append(violations, "work item key is empty")
			continue
		}
		if _, exists := dependencies[key]; exists {
			violations = append(violations, "duplicate work item: "+key)
		}
		dependencies[key] = stringSlice(item["dependencies"])
	}
	for key, values := range dependencies {
		for _, dependency := range values {
			if dependency == key {
				violations = append(violations, "self dependency: "+key)
			} else if _, exists := dependencies[dependency]; !exists {
				violations = append(violations, "unknown dependency: "+key+" -> "+dependency)
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(key string) bool {
		if visiting[key] {
			return true
		}
		if visited[key] {
			return false
		}
		visiting[key] = true
		for _, dependency := range dependencies[key] {
			if _, exists := dependencies[dependency]; exists && visit(dependency) {
				return true
			}
		}
		visiting[key] = false
		visited[key] = true
		return false
	}
	for key := range dependencies {
		if visit(key) {
			violations = append(violations, "dependency cycle includes: "+key)
			break
		}
	}
	return violations
}

func collectToolRequests(value any) []string {
	result := []string{}
	object, ok := value.(map[string]any)
	if !ok {
		return result
	}
	for key, nested := range object {
		lower := strings.ToLower(key)
		if lower == "tool_request" || lower == "tool_requests" || lower == "tool_call" || lower == "tool_calls" {
			if raw, err := json.Marshal(nested); err == nil && string(raw) != "null" && string(raw) != "[]" && string(raw) != "{}" {
				result = append(result, string(raw))
			}
		}
		switch typed := nested.(type) {
		case map[string]any:
			result = append(result, collectToolRequests(typed)...)
		case []any:
			for _, entry := range typed {
				result = append(result, collectToolRequests(entry)...)
			}
		}
	}
	return result
}

func collectProductionMutations(value any) []string {
	result := []string{}
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			lowerKey := strings.ToLower(key)
			if text, ok := nested.(string); ok {
				lower := strings.ToLower(text)
				if (lowerKey == "environment" && lower == "production") ||
					(strings.Contains(lowerKey, "tool") && strings.Contains(lower, "production")) ||
					(lowerKey == "action" && strings.Contains(lower, "production")) {
					result = append(result, key+"="+text)
				}
			}
			result = append(result, collectProductionMutations(nested)...)
		}
	case []any:
		for _, nested := range typed {
			result = append(result, collectProductionMutations(nested)...)
		}
	}
	return result
}

func stringSlice(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
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
