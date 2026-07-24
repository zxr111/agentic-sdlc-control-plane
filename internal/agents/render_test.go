package agents

import (
	"strings"
	"testing"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
)

func TestRenderRequirementPlacesVisualsFirst(t *testing.T) {
	got := RenderRequirement(RequirementReview{Goal: "Goal", Decision: "changes_requested"}, []domain.Snapshot{{
		Images: []domain.Image{{Filename: "workflow.png", Markdown: "![workflow](/uploads/a.png)"}},
	}})
	if strings.Index(got, "![workflow]") > strings.Index(got, "## Goal") {
		t.Fatalf("visual must appear before goal details:\n%s", got)
	}
}
