package knowledge

import (
	"context"
	"strings"
	"testing"
)

func TestNormalizeTextIsDeterministic(t *testing.T) {
	got := NormalizeText("  Title\r\n\r\n\r\n  value\t with   spaces \x00 ")
	if got != "Title\n\nvalue with spaces" {
		t.Fatalf("normalized=%q", got)
	}
}

func TestChunkDocumentPreservesHeadingPath(t *testing.T) {
	chunks := ChunkDocument("# Refund\nintro text\n## Retry\nretry three times", "PRD", 100, 10)
	if len(chunks) != 2 || chunks[0].ParentPath != "PRD / Refund" || chunks[1].ParentPath != "PRD / Refund / Retry" {
		t.Fatalf("chunks=%#v", chunks)
	}
}

func TestChineseEmbeddingContainsLocalSemanticSignal(t *testing.T) {
	refund := EmbedText("退款超时重试规则")
	retry := EmbedText("退款失败后的重试次数")
	unrelated := EmbedText("用户头像图片上传")
	if cosine(refund, retry) <= cosine(refund, unrelated) {
		t.Fatal("related Chinese text should be more similar")
	}
}

func TestChunkTextSplitsLongChineseSection(t *testing.T) {
	chunks := ChunkText(strings.Repeat("退款规则", 20), "规则", 20, 4)
	if len(chunks) < 2 || chunks[0].TokenCount > 20 {
		t.Fatalf("chunks=%#v", chunks)
	}
}

func TestDeterministicRerankerUsesQueryOverlap(t *testing.T) {
	plan := UnderstandQuery("refund retry policy")
	scores, err := (DeterministicReranker{}).Rerank(context.Background(), plan, []Candidate{
		{Content: "avatar upload policy", FusedScore: .01},
		{Content: "refund retry policy", FusedScore: .01},
	})
	if err != nil || scores[1] <= scores[0] {
		t.Fatalf("scores=%v err=%v", scores, err)
	}
}

func cosine(left, right []float64) float64 {
	var result float64
	for index := range left {
		result += left[index] * right[index]
	}
	return result
}

func TestQueryPlanIsBounded(t *testing.T) {
	plan := UnderstandQuery(strings.Repeat("payment retry ", 20))
	if len(plan.Queries) > 2 || len(plan.RequiredTerms) > 12 {
		t.Fatalf("plan=%#v", plan)
	}
}
