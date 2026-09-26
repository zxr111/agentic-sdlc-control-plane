package store

import (
	"context"
	"encoding/json"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"github.com/google/uuid"
)

type RiskAssessment struct {
	ID, WorkflowID, GateID, ArtifactID, PolicyVersion, EvidenceHash string
	RiskLevel                                                       domain.RiskLevel
	Eligible                                                        bool
	Reasons                                                         []string
}

func (s *Store) SaveRiskAssessment(ctx context.Context, value RiskAssessment) (string, error) {
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	reasons, err := json.Marshal(value.Reasons)
	if err != nil {
		return "", err
	}
	err = s.db.QueryRowContext(ctx, `INSERT INTO risk_assessments
		(id,workflow_id,gate_id,artifact_id,risk_level,eligible,reasons,policy_version,evidence_hash)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(gate_id,policy_version) DO UPDATE SET
		risk_level=EXCLUDED.risk_level,eligible=EXCLUDED.eligible,reasons=EXCLUDED.reasons,evidence_hash=EXCLUDED.evidence_hash
		RETURNING id`, value.ID, value.WorkflowID, value.GateID, value.ArtifactID, value.RiskLevel,
		value.Eligible, string(reasons), value.PolicyVersion, value.EvidenceHash).Scan(&value.ID)
	return value.ID, err
}

func (s *Store) DecideGateByPolicy(ctx context.Context, gate domain.Gate, assessmentID, policyVersion, evidenceHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status domain.GateStatus
	if err := tx.QueryRowContext(ctx, `SELECT status FROM gates WHERE id=$1 FOR UPDATE`, gate.ID).Scan(&status); err != nil {
		return err
	}
	if status == domain.GateApproved {
		return nil
	}
	if status != domain.GateOpen {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gates SET status='APPROVED',decided_at=CURRENT_TIMESTAMP,decision_actor=0,
		feedback='approved by governed automation policy' WHERE id=$1`, gate.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO gate_decisions
		(gate_id,action,actor_id,actor_username,feedback,decision_source,policy_version,risk_assessment_id,evidence_hash)
		VALUES($1,'approve',0,$2,'approved by governed automation policy','POLICY',$3,$4,$5)
		ON CONFLICT(gate_id,actor_id,action) DO NOTHING`, gate.ID, "policy:"+policyVersion, policyVersion, assessmentID, evidenceHash); err != nil {
		return err
	}
	details, _ := json.Marshal(map[string]any{"gate_id": gate.ID, "gate_type": gate.Type, "action": "approve",
		"decision_source": "POLICY", "policy_version": policyVersion, "risk_assessment_id": assessmentID, "evidence_hash": evidenceHash})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(workflow_id,event_type,actor_id,details_json)
		VALUES($1,'gate.auto_decided',0,$2)`, gate.WorkflowID, string(details)); err != nil {
		return err
	}
	return tx.Commit()
}
