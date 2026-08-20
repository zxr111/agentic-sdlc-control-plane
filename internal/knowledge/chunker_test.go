package knowledge

import (
	"strings"
	"testing"
)

func TestChunkTextIsDeterministicAndOverlapping(t *testing.T) {
	text := strings.Join([]string{"one", "two", "three", "four", "five", "six", "seven"}, " ")
	first := ChunkText(text, "page/section", 4, 1)
	second := ChunkText(text, "page/section", 4, 1)
	if len(first) != 2 || first[0] != second[0] || first[1] != second[1] {
		t.Fatalf("unexpected chunks %#v %#v", first, second)
	}
	if first[0].Content != "one two three four" || first[1].Content != "four five six seven" {
		t.Fatalf("overlap was not preserved %#v", first)
	}
}
