package knowledge

import "strings"

// CompressText performs deterministic extractive compression. The original
// source remains immutable in the snapshot/index; the Context Manifest records
// that this bounded representation was transmitted.
func CompressText(text string, maxTokens int) (string, bool) {
	words := strings.Fields(text)
	if maxTokens <= 0 || len(words) <= maxTokens {
		return text, false
	}
	if maxTokens < 32 {
		maxTokens = 32
	}
	marker := []string{"[...compressed;see-cited-original...]"}
	head := (maxTokens - len(marker)) * 2 / 3
	tail := maxTokens - len(marker) - head
	result := append([]string{}, words[:head]...)
	result = append(result, marker...)
	result = append(result, words[len(words)-tail:]...)
	return strings.Join(result, " "), true
}
