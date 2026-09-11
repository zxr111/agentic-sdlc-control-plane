package knowledge

import (
	"strings"
	"unicode"
)

var queryStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "do": true, "for": true, "how": true,
	"is": true, "of": true, "the": true, "to": true, "we": true, "what": true, "with": true,
}

// RewriteQuery provides a deterministic, bounded second retrieval query. It
// removes conversational filler but never invents terms absent from the
// authoritative request.
func RewriteQuery(query string) string {
	seen := map[string]bool{}
	result := make([]string, 0, 12)
	for _, token := range tokens(query) {
		if len(token) < 2 || queryStopWords[token] || seen[token] {
			continue
		}
		seen[token] = true
		result = append(result, token)
		if len(result) == 12 {
			break
		}
	}
	return strings.Join(result, " ")
}

type QueryPlan struct {
	OriginalQuery   string
	NormalizedQuery string
	Queries         []string
	RequiredTerms   []string
	Intent          string
	SourceTypes     []string
}

// UnderstandQuery creates a bounded, replayable plan. It never invents terms
// that were absent from the request and therefore remains safe as a fallback
// when no governed query-understanding model is configured.
func UnderstandQuery(query string) QueryPlan {
	original := strings.TrimSpace(query)
	normalized := strings.Join(tokens(original), " ")
	plan := QueryPlan{OriginalQuery: original, NormalizedQuery: normalized, Queries: []string{original}, Intent: inferIntent(normalized)}
	if rewritten := RewriteQuery(original); rewritten != "" && rewritten != strings.ToLower(original) {
		plan.Queries = append(plan.Queries, rewritten)
	}
	for _, token := range tokens(original) {
		if len([]rune(token)) >= 2 && !queryStopWords[token] {
			plan.RequiredTerms = appendUnique(plan.RequiredTerms, token)
		}
		if len(plan.RequiredTerms) == 12 {
			break
		}
	}
	return plan
}

func inferIntent(query string) string {
	switch {
	case strings.Contains(query, "test") || strings.Contains(query, "测试") || strings.Contains(query, "验证"):
		return "TEST_EVIDENCE"
	case strings.Contains(query, "deploy") || strings.Contains(query, "release") || strings.Contains(query, "部署") || strings.Contains(query, "发布"):
		return "DELIVERY_EVIDENCE"
	case strings.Contains(query, "code") || strings.Contains(query, "function") || strings.Contains(query, "代码") || strings.Contains(query, "实现"):
		return "IMPLEMENTATION"
	default:
		return "GENERAL_KNOWLEDGE"
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func uniqueTokens(value string) map[string]bool {
	result := map[string]bool{}
	for _, token := range semanticTokens(value) {
		result[token] = true
	}
	return result
}

// semanticTokens adds CJK unigrams and bigrams. PostgreSQL's simple text
// search and whitespace tokenization otherwise treat a Chinese paragraph as
// one token, which severely degrades both chunking estimates and retrieval.
func semanticTokens(value string) []string {
	result := tokens(value)
	var run []rune
	flush := func() {
		for index, char := range run {
			result = append(result, string(char))
			if index+1 < len(run) {
				result = append(result, string(run[index:index+2]))
			}
		}
		run = nil
	}
	for _, char := range []rune(strings.ToLower(value)) {
		if unicode.In(char, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			run = append(run, char)
		} else if len(run) > 0 {
			flush()
		}
	}
	if len(run) > 0 {
		flush()
	}
	return result
}
