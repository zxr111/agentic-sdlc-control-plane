package knowledge

import "strings"

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
