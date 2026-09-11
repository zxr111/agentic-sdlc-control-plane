package knowledge

import (
	"strings"
	"testing"
)

func TestBuildContextAndValidateCitations(t *testing.T) {
	selection := BuildContext([]Evidence{
		{ID: "chunk-a", DocumentID: "doc-a", Content: "approved refund policy", ContentHash: "hash-a", SourceType: "PRD", SourceKey: "1", SourceVersion: "2", AuthorityLevel: 90, Score: .5},
		{ID: "chunk-b", DocumentID: "doc-b", Content: "old refund policy", ContentHash: "hash-b", SourceType: "ISSUE", SourceKey: "2", SourceVersion: "1", AuthorityLevel: 40, Score: .9},
	}, 100, 1)
	if len(selection.Evidence) != 2 || selection.Evidence[0].ID != "K-001" {
		t.Fatalf("selection=%#v", selection)
	}
	if _, err := ValidateCitations("Use the approved policy [CITE:K-001].", selection.Evidence, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateCitations("Invented [CITE:K-999].", selection.Evidence, true); err == nil {
		t.Fatal("expected invented citation rejection")
	}
}

func TestContextBudgetAndDeduplication(t *testing.T) {
	selection := BuildContext([]Evidence{
		{ID: "a", DocumentID: "a", Content: "退款规则", ContentHash: "same", AuthorityLevel: 100},
		{ID: "b", DocumentID: "b", Content: "退款规则", ContentHash: "same", AuthorityLevel: 90},
	}, 10, 2)
	if len(selection.Evidence) != 1 || selection.Excluded["b"] != "duplicate_content_hash" {
		t.Fatalf("selection=%#v", selection)
	}
}

func TestParseHTMLPreservesStructureAndDropsScripts(t *testing.T) {
	parsed, err := ParseDocument("CONFLUENCE_STORAGE", "text/html", []byte(`<h1>Refund</h1><p>Retry rule</p><script>steal()</script><ul><li>three times</li></ul>`))
	if err != nil || parsed.Content == "" {
		t.Fatalf("parsed=%#v err=%v", parsed, err)
	}
	if contains := ExtractCitations(parsed.Content); len(contains) != 0 {
		t.Fatalf("unexpected citation parse: %v", contains)
	}
	if strings.Contains(parsed.Content, "steal") {
		t.Fatal("script content must not be retained")
	}
}
