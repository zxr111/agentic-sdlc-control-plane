package knowledge

import (
	"strings"
	"testing"
)

func TestCompressTextIsBoundedAndPreservesEdges(t *testing.T) {
	input := "first " + strings.Repeat("middle ", 100) + "last"
	compressed, changed := CompressText(input, 40)
	if !changed || len(strings.Fields(compressed)) > 40 || !strings.HasPrefix(compressed, "first") || !strings.HasSuffix(compressed, "last") {
		t.Fatalf("invalid compression changed=%t words=%d value=%q", changed, len(strings.Fields(compressed)), compressed)
	}
}
