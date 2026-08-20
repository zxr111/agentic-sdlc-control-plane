package engine

import (
	"strings"
	"testing"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/agents"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
)

func TestRepositoryKnowledgePathAllowlist(t *testing.T) {
	allowed := []string{"README.md", "docs/design.md", "service/docs/runbook.adoc", "openapi.yaml", "db/migrations/001.sql"}
	for _, path := range allowed {
		if !isRepositoryKnowledgePath(path) {
			t.Fatalf("expected %s to be indexed", path)
		}
	}
	for _, path := range []string{"cmd/server/main.go", ".env", "secrets.txt", "assets/logo.png"} {
		if isRepositoryKnowledgePath(path) {
			t.Fatalf("unexpected repository knowledge path %s", path)
		}
	}
	unique := uniqueKnowledgePaths([]string{"README.md", "README.md", "docs/a.md", "docs/b.md"}, 2)
	if len(unique) != 2 || unique[0] != "README.md" || unique[1] != "docs/a.md" {
		t.Fatalf("unexpected bounded paths %#v", unique)
	}
}

func TestCombinedHashChangesWithImage(t *testing.T) {
	first := []domain.Snapshot{{
		ConfluencePageID: "1", Version: 2, ContentHash: strings.Repeat("a", 64),
		Images: []domain.Image{{AttachmentID: "x", Version: 1, ContentHash: strings.Repeat("b", 64)}},
	}}
	second := []domain.Snapshot{{
		ConfluencePageID: "1", Version: 2, ContentHash: strings.Repeat("a", 64),
		Images: []domain.Image{{AttachmentID: "x", Version: 2, ContentHash: strings.Repeat("c", 64)}},
	}}
	if combinedHash(first) == combinedHash(second) {
		t.Fatal("visual change must invalidate source hash")
	}
}

func TestChildIssueIncludesTraceability(t *testing.T) {
	got := renderChildIssue(domain.Workflow{ID: "wf", IssueIID: 9}, agents.WorkItem{
		IndependentBoundary: "Independent API", Rationale: "Separate lifecycle",
		Dependencies: []string{"contract"},
	}, domain.Artifact{Type: domain.ArtifactRequirement, Version: 2, SourceHash: "hash"})
	for _, expected := range []string{"#9", "`wf`", "`REQUIREMENT_REVIEW` v2", "`hash`"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("missing %s in %s", expected, got)
		}
	}
}
