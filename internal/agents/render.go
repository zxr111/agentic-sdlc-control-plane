package agents

import (
	"fmt"
	"strings"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
)

func RenderRequirement(review RequirementReview, snapshots []domain.Snapshot) string {
	var out strings.Builder
	out.WriteString("## Key visuals\n\n")
	visuals := 0
	for _, snapshot := range snapshots {
		for _, image := range snapshot.Images {
			if image.Markdown == "" {
				continue
			}
			out.WriteString(image.Markdown)
			out.WriteString("\n\n*")
			out.WriteString(escape(image.Filename))
			out.WriteString(" — visual evidence from Confluence; written requirements remain authoritative.*\n\n")
			visuals++
		}
	}
	if visuals == 0 {
		out.WriteString("_No source visuals were embedded in the authoritative Confluence pages._\n\n")
	}
	fmt.Fprintf(&out, "## Goal\n\n%s\n\n## AI requirement review\n\n**Decision:** `%s`\n\n%s\n\n",
		review.Goal, review.Decision, review.Summary)
	renderList(&out, "Source facts", review.Facts)
	renderList(&out, "AI inferences", review.Inferences)
	renderQuestions(&out, review.Questions)
	renderList(&out, "In scope", review.InScope)
	renderList(&out, "Out of scope", review.OutOfScope)
	renderList(&out, "Constraints", review.Constraints)
	renderList(&out, "Failure modes", review.FailureModes)
	out.WriteString("## Risks\n\n")
	for _, risk := range review.Risks {
		fmt.Fprintf(&out, "- **%s · %s** — Evidence: %s Impact: %s Mitigation: %s\n",
			risk.ID, risk.Severity, risk.Evidence, risk.Impact, risk.Mitigation)
	}
	out.WriteString("\n## Acceptance criteria\n\n")
	for _, criterion := range review.AcceptanceCriteria {
		fmt.Fprintf(&out, "- **%s:** %s\n  - Evidence: %s\n", criterion.ID, criterion.Behavior, criterion.Evidence)
	}
	out.WriteString("\n## Candidate independently deliverable work items\n\n")
	for _, item := range review.WorkItems {
		fmt.Fprintf(&out, "- **%s** `%s` (%s)\n  - Boundary: %s\n  - Rationale: %s\n",
			item.Title, item.Key, item.OwnerRole, item.IndependentBoundary, item.Rationale)
		if len(item.Dependencies) > 0 {
			fmt.Fprintf(&out, "  - Dependencies: %s\n", strings.Join(item.Dependencies, ", "))
		}
	}
	return out.String()
}

func RenderPRD(prd PRD) string {
	var out strings.Builder
	fmt.Fprintf(&out, "## Problem\n\n%s\n\n## Product goal\n\n%s\n\n", prd.Problem, prd.Goal)
	renderList(&out, "Personas", prd.Personas)
	renderList(&out, "User journeys", prd.UserJourneys)
	renderRequirements(&out, "Functional requirements", prd.FunctionalRequirements)
	renderRequirements(&out, "Non-functional requirements", prd.NonFunctional)
	renderList(&out, "Data contracts", prd.DataContracts)
	renderList(&out, "Dependencies", prd.Dependencies)
	renderList(&out, "Out of scope", prd.OutOfScope)
	renderList(&out, "Rollout", prd.Rollout)
	renderList(&out, "Rollback", prd.Rollback)
	renderList(&out, "Observability", prd.Observability)
	renderQuestions(&out, prd.OpenQuestions)
	return out.String()
}

func RenderTestPlan(plan TestPlan) string {
	var out strings.Builder
	fmt.Fprintf(&out, "## Test review\n\n**Decision:** `%s`\n\n%s\n\n", plan.Decision, plan.CoverageSummary)
	renderList(&out, "Blockers", plan.Blockers)
	renderList(&out, "Residual risks", plan.ResidualRisks)
	out.WriteString("## Test cases\n\n")
	for _, test := range plan.TestCases {
		fmt.Fprintf(&out, "### %s: %s\n\n", test.ID, test.Name)
		fmt.Fprintf(&out, "- Acceptance criteria: %s\n- Layer: %s\n- Execution: %s\n- Priority: %s\n",
			strings.Join(test.AcceptanceCriteria, ", "), test.Layer, test.Execution, test.Priority)
		fmt.Fprintf(&out, "- Preconditions: %s\n- Test data: %s\n- Steps:\n",
			strings.Join(test.Preconditions, "; "), strings.Join(test.TestData, "; "))
		for i, step := range test.Steps {
			fmt.Fprintf(&out, "  %d. %s\n", i+1, step)
		}
		fmt.Fprintf(&out, "- Expected result: %s\n- Cleanup: %s\n- Coverage: %s\n\n",
			test.ExpectedResult, strings.Join(test.Cleanup, "; "), strings.Join(test.CoverageDimensions, ", "))
	}
	out.WriteString("## Coverage matrix\n\n| Acceptance criterion | Tests | Dimensions | Gaps |\n|---|---|---|---|\n")
	for _, entry := range plan.CoverageMatrix {
		fmt.Fprintf(&out, "| %s | %s | %s | %s |\n", escape(entry.AcceptanceCriterion),
			escape(strings.Join(entry.TestCases, ", ")), escape(strings.Join(entry.Dimensions, ", ")),
			escape(strings.Join(entry.Gaps, "; ")))
	}
	return out.String()
}

func renderList(out *strings.Builder, title string, values []string) {
	fmt.Fprintf(out, "## %s\n\n", title)
	if len(values) == 0 {
		out.WriteString("_None recorded._\n\n")
		return
	}
	for _, value := range values {
		fmt.Fprintf(out, "- %s\n", value)
	}
	out.WriteByte('\n')
}

func renderQuestions(out *strings.Builder, questions []Question) {
	out.WriteString("## Questions for engineers\n\n")
	if len(questions) == 0 {
		out.WriteString("_No unresolved questions._\n\n")
		return
	}
	for _, question := range questions {
		fmt.Fprintf(out, "- **%s → %s** %s\n  - Blocking: `%t`\n  - Why: %s\n  - Evidence needed: %s\n",
			question.ID, question.TargetRole, question.Question, question.Blocking,
			question.WhyBlocking, question.EvidenceNeeded)
	}
	out.WriteByte('\n')
}

func renderRequirements(out *strings.Builder, title string, values []ProductRequirement) {
	fmt.Fprintf(out, "## %s\n\n", title)
	for _, value := range values {
		fmt.Fprintf(out, "- **%s:** %s\n  - Source: %s\n", value.ID, value.Description, strings.Join(value.SourceAC, ", "))
	}
	out.WriteByte('\n')
}

func escape(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}
