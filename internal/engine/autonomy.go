package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
)

const defaultAutoGatePolicyVersion = "risk-gate-v1"

func (e *Engine) evaluateAutoGate(ctx context.Context, gateID string) error {
	gate, err := e.store.GetGate(ctx, gateID)
	if err != nil {
		return err
	}
	workflow, err := e.store.GetWorkflow(ctx, gate.WorkflowID)
	if err != nil {
		return err
	}
	if gate.Status == domain.GateApproved {
		if autoGateMatchesState(gate.Type, workflow.State) {
			artifact, err := e.store.ArtifactByID(ctx, gate.ArtifactID)
			if err != nil {
				return err
			}
			if err := e.indexAutoApprovedArtifact(ctx, workflow, artifact); err != nil {
				return err
			}
			return e.advanceApprovedGate(ctx, workflow, gate)
		}
		return nil
	}
	if gate.Status != domain.GateOpen {
		return nil
	}
	project := e.projects[workflow.GitLabProjectID]
	policy, ok := project.GateAutomation[gate.Type]
	if !ok || policy.Mode == "HUMAN" || policy.Mode == "" {
		return nil
	}
	if !autoGateTypeAllowed(gate.Type) {
		return fmt.Errorf("gate %s cannot be decided by automation", gate.Type)
	}
	completed, err := e.store.ArtifactHasCompletedAgentRun(ctx, gate.ArtifactID)
	if err != nil {
		return err
	}
	if !completed {
		return fmt.Errorf("artifact %s is not bound to a completed Agent Run", gate.ArtifactID)
	}
	artifact, err := e.store.ArtifactByID(ctx, gate.ArtifactID)
	if err != nil {
		return err
	}
	version := policy.PolicyVersion
	if version == "" {
		version = defaultAutoGatePolicyVersion
	}
	risk, eligible, reasons := assessArtifactRisk(artifact)
	if !domain.RiskAtMost(risk, policy.MaximumAutomaticRisk) {
		eligible = false
		reasons = append(reasons, fmt.Sprintf("risk %s exceeds automatic threshold %s", risk, policy.MaximumAutomaticRisk))
	}
	evidenceHash := autoGateEvidenceHash(artifact, version, risk, reasons)
	assessmentID, err := e.store.SaveRiskAssessment(ctx, store.RiskAssessment{WorkflowID: workflow.ID, GateID: gate.ID,
		ArtifactID: artifact.ID, PolicyVersion: version, EvidenceHash: evidenceHash, RiskLevel: risk, Eligible: eligible, Reasons: reasons})
	if err != nil {
		return err
	}
	if !eligible {
		_ = e.store.AddAudit(ctx, workflow.ID, "gate.auto_escalated", 0, map[string]any{"gate_id": gate.ID,
			"risk_level": risk, "policy_version": version, "reasons": reasons})
		return e.queueStatusNote(ctx, workflow, "auto-gate-escalated:"+gate.ID,
			"<!-- ai-factory:auto-gate-escalated:"+gate.ID+" -->",
			fmt.Sprintf("## Human review required\n\nGate `%s` was not automatically approved. Risk: `%s`. Reasons: %s",
				gate.ID, risk, strings.Join(reasons, "; ")))
	}
	if err := e.store.DecideGateByPolicy(ctx, gate, assessmentID, version, evidenceHash); err != nil {
		return err
	}
	if err := e.indexAutoApprovedArtifact(ctx, workflow, artifact); err != nil {
		return err
	}
	if err := e.queueStatusNote(ctx, workflow, "auto-gate-approved:"+gate.ID,
		"<!-- ai-factory:auto-gate-approved:"+gate.ID+" -->",
		fmt.Sprintf("## Gate automatically approved\n\nGate `%s` passed policy `%s` at risk `%s`. Evidence: `%s`.",
			gate.ID, version, risk, evidenceHash)); err != nil {
		return err
	}
	return e.advanceApprovedGate(ctx, workflow, gate)
}

func (e *Engine) indexAutoApprovedArtifact(ctx context.Context, workflow domain.Workflow, artifact domain.Artifact) error {
	if !e.v3.RAG {
		return nil
	}
	_, _, err := e.store.IngestKnowledge(ctx, store.KnowledgeSource{ProjectID: workflow.GitLabProjectID,
		SourceType: "APPROVED_ARTIFACT", SourceKey: artifact.ID, SourceVersion: fmt.Sprintf("%d", artifact.Version),
		Title: string(artifact.Type), AuthorityLevel: 90, AccessScope: map[string]any{"gitlab_project_id": workflow.GitLabProjectID},
		Content: artifact.Markdown + "\n" + string(artifact.Content), ParentPath: string(artifact.Type)})
	return err
}

func assessArtifactRisk(artifact domain.Artifact) (domain.RiskLevel, bool, []string) {
	text := strings.ToLower(artifact.Markdown + "\n" + string(artifact.Content))
	risk := domain.RiskL1
	reasons := []string{"bounded application change"}
	if containsAny(text, "production", "生产", "credential", "凭证", "secret", "不可逆", "delete data", "drop table") {
		risk, reasons = domain.RiskL4, []string{"production, credential, secret, destructive, or irreversible change detected"}
	} else if containsAny(text, "payment", "支付", "authorization", "authentication", "权限", "安全", "database migration", "数据库迁移", "personal data", "隐私") {
		risk, reasons = domain.RiskL3, []string{"security, payment, identity, privacy, or database migration impact detected"}
	} else if containsAny(text, "api", "interface", "接口", "data_changes", "schema", "消息", "event") {
		risk, reasons = domain.RiskL2, []string{"API, interface, event, or data-contract impact detected"}
	}
	var value any
	if err := json.Unmarshal(artifact.Content, &value); err != nil {
		return risk, false, append(reasons, "artifact JSON cannot be evaluated")
	}
	blockers := automationBlockers(value, "")
	if len(blockers) > 0 {
		return risk, false, append(reasons, blockers...)
	}
	return risk, true, reasons
}

func automationBlockers(value any, key string) []string {
	var result []string
	switch typed := value.(type) {
	case map[string]any:
		if decision, ok := typed["decision"].(string); ok && decision != "ready_for_human_approval" {
			result = append(result, "artifact decision is not ready_for_human_approval")
		}
		for childKey, child := range typed {
			if (childKey == "blockers" || childKey == "open_questions") && collectionLength(child) > 0 {
				result = append(result, childKey+" are not empty")
			}
			if childKey == "blocking" && child == true {
				result = append(result, "blocking question remains")
			}
			result = append(result, automationBlockers(child, childKey)...)
		}
	case []any:
		for _, child := range typed {
			result = append(result, automationBlockers(child, key)...)
		}
	}
	return uniqueStrings(result)
}

func collectionLength(value any) int {
	if values, ok := value.([]any); ok {
		return len(values)
	}
	return 0
}
func containsAny(value string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}
func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
func autoGateEvidenceHash(artifact domain.Artifact, version string, risk domain.RiskLevel, reasons []string) string {
	sum := sha256.Sum256([]byte(artifact.ID + "\x00" + artifact.SourceHash + "\x00" + version + "\x00" + string(risk) + "\x00" + strings.Join(reasons, "\x00")))
	return hex.EncodeToString(sum[:])
}
func autoGateTypeAllowed(value domain.GateType) bool {
	return value == domain.GateRequirement || value == domain.GatePRD || value == domain.GateTest || value == domain.GateArchitecture || value == domain.GateSDD
}
func autoGateMatchesState(gate domain.GateType, state domain.State) bool {
	switch gate {
	case domain.GateRequirement:
		return state == domain.StateWaitingRequirementReview
	case domain.GatePRD, domain.GateTest:
		return state == domain.StateWaitingPRDAndTestReview
	case domain.GateArchitecture:
		return state == domain.StateWaitingArchitectureReview
	case domain.GateSDD:
		return state == domain.StateWaitingSDDReview
	}
	return false
}
