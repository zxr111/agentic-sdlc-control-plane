package knowledge

import (
	"fmt"
	"regexp"
	"sort"
)

var citationPattern = regexp.MustCompile(`\[CITE:(K-[0-9]{3})\]`)
var evidencePattern = regexp.MustCompile(`<EVIDENCE id="(K-[0-9]{3})"`)

type CitationValidation struct {
	Referenced []string
	Missing    []string
	Unused     []string
}

func ValidateCitationReferences(citations []string, contextText string) error {
	available := map[string]bool{}
	for _, match := range evidencePattern.FindAllStringSubmatch(contextText, -1) {
		available[match[1]] = true
	}
	for _, citation := range citations {
		if !available[citation] {
			return fmt.Errorf("citation %s is absent from the exact model context", citation)
		}
	}
	return nil
}

func ExtractCitations(output string) []string {
	seen := map[string]bool{}
	var result []string
	for _, match := range citationPattern.FindAllStringSubmatch(output, -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			result = append(result, match[1])
		}
	}
	return result
}

// ValidateCitations proves that every model-produced citation was present in
// the exact context selection. Source status and hashes are validated by the
// store before context construction and should be checked again before use.
func ValidateCitations(output string, evidence []Evidence, requireCitation bool) (CitationValidation, error) {
	result := CitationValidation{Referenced: ExtractCitations(output)}
	available := map[string]bool{}
	for _, item := range evidence {
		available[item.ID] = true
	}
	used := map[string]bool{}
	for _, id := range result.Referenced {
		if !available[id] {
			result.Missing = append(result.Missing, id)
		} else {
			used[id] = true
		}
	}
	for _, item := range evidence {
		if !used[item.ID] {
			result.Unused = append(result.Unused, item.ID)
		}
	}
	sort.Strings(result.Missing)
	sort.Strings(result.Unused)
	if len(result.Missing) > 0 {
		return result, fmt.Errorf("output contains citations absent from context: %v", result.Missing)
	}
	if requireCitation && len(result.Referenced) == 0 {
		return result, fmt.Errorf("output requires at least one context citation")
	}
	return result, nil
}
