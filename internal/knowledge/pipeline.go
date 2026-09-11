package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
)

const (
	ParserVersion  = "plain-structural-v1"
	CleanerVersion = "deterministic-v1"
	ChunkerVersion = "structural-window-v1"
)

var markdownHeading = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

// NormalizeText performs deterministic, non-generative cleaning. Raw source
// content remains stored separately; this representation is safe to rebuild.
func NormalizeText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, value)
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(strings.Join(strings.Fields(line), " "))
		if line == "" {
			if blank || len(out) == 0 {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// ChunkDocument respects Markdown heading boundaries and falls back to
// deterministic overlapping windows for oversized sections.
func ChunkDocument(value, rootPath string, size, overlap int) []Chunk {
	value = NormalizeText(value)
	type section struct {
		path string
		text []string
	}
	sections := []section{{path: rootPath}}
	headings := make([]string, 6)
	for _, line := range strings.Split(value, "\n") {
		match := markdownHeading.FindStringSubmatch(line)
		if match == nil {
			sections[len(sections)-1].text = append(sections[len(sections)-1].text, line)
			continue
		}
		level := len(match[1])
		headings[level-1] = strings.TrimSpace(match[2])
		for index := level; index < len(headings); index++ {
			headings[index] = ""
		}
		parts := []string{}
		if rootPath != "" {
			parts = append(parts, rootPath)
		}
		for _, heading := range headings {
			if heading != "" {
				parts = append(parts, heading)
			}
		}
		sections = append(sections, section{path: strings.Join(parts, " / "), text: []string{line}})
	}
	result := []Chunk{}
	for _, current := range sections {
		text := strings.TrimSpace(strings.Join(current.text, "\n"))
		if text == "" {
			continue
		}
		for _, chunk := range ChunkText(text, current.path, size, overlap) {
			chunk.Index = len(result)
			result = append(result, chunk)
		}
	}
	return result
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// EstimateTokens is a conservative local budget estimate. Provider-reported
// token usage remains authoritative for billing and runtime evidence.
func EstimateTokens(value string) int {
	count := 0
	latinRun := false
	for _, char := range value {
		if unicode.In(char, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			count++
			latinRun = false
			continue
		}
		if unicode.IsLetter(char) || unicode.IsNumber(char) {
			if !latinRun {
				count++
			}
			latinRun = true
		} else {
			latinRun = false
		}
	}
	return count
}
