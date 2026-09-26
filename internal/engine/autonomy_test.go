package engine

import (
	"encoding/json"
	"testing"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
)

func TestAssessArtifactRiskAllowsBoundedReadyChange(t *testing.T) {
	artifact := domain.Artifact{Content: json.RawMessage(`{"decision":"ready_for_human_approval","open_questions":[]}`)}
	risk, eligible, _ := assessArtifactRisk(artifact)
	if risk != domain.RiskL1 || !eligible {
		t.Fatalf("risk=%s eligible=%t", risk, eligible)
	}
}

func TestAssessArtifactRiskEscalatesSensitiveAndIncompleteChange(t *testing.T) {
	artifact := domain.Artifact{Markdown: "Production payment authorization", Content: json.RawMessage(`{"decision":"changes_requested","blockers":["owner missing"]}`)}
	risk, eligible, reasons := assessArtifactRisk(artifact)
	if risk != domain.RiskL4 || eligible || len(reasons) < 2 {
		t.Fatalf("risk=%s eligible=%t reasons=%v", risk, eligible, reasons)
	}
}
