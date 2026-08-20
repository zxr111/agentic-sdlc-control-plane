package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

func (s *Store) SubmitRegistryChangeApproval(ctx context.Context, registryType, candidateID, actor string) error {
	if actor == "" {
		return errors.New("registry approval requires actor")
	}
	var exists bool
	var query string
	switch registryType {
	case "SKILL":
		query = `SELECT EXISTS(SELECT 1 FROM skill_versions WHERE id=$1 AND status='DRAFT')`
	case "TOOL_VERSION":
		query = `SELECT EXISTS(SELECT 1 FROM tool_versions WHERE id=$1 AND status='DRAFT')`
	case "TOOL_POLICY":
		query = `SELECT EXISTS(SELECT 1 FROM tool_policies WHERE id=$1 AND status='CANDIDATE')`
	default:
		return errors.New("unsupported registry change type")
	}
	if err := s.db.QueryRowContext(ctx, query, candidateID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO registry_change_approvals(id,registry_type,candidate_id,actor)
		VALUES($1,$2,$3,$4) ON CONFLICT(registry_type,candidate_id,actor) DO NOTHING`, uuid.NewString(), registryType, candidateID, actor)
	return err
}

func requireRegistryApprovals(ctx context.Context, tx *sql.Tx, registryType, candidateID, activationActor string) error {
	var approvals int
	var activationActorApproved bool
	if err := tx.QueryRowContext(ctx, `SELECT count(DISTINCT actor),COALESCE(bool_or(actor=$3),false) FROM registry_change_approvals
		WHERE registry_type=$1 AND candidate_id=$2`, registryType, candidateID, activationActor).Scan(&approvals, &activationActorApproved); err != nil {
		return err
	}
	if approvals < 2 || activationActorApproved {
		return ErrGovernanceRequired
	}
	return nil
}

func (s *Store) PromoteSkillVersion(ctx context.Context, skillKey, candidateID, actor string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var definitionID string
	if err := tx.QueryRowContext(ctx, `SELECT sd.id FROM skill_definitions sd JOIN skill_versions sv ON sv.skill_definition_id=sd.id
		WHERE sd.skill_key=$1 AND sv.id=$2 AND sv.status='DRAFT' FOR UPDATE`, skillKey, candidateID).Scan(&definitionID); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	if err := requireRegistryApprovals(ctx, tx, "SKILL", candidateID, actor); err != nil {
		return err
	}
	var previous any
	_ = tx.QueryRowContext(ctx, `SELECT id FROM skill_versions WHERE skill_definition_id=$1 AND status='ACTIVE' LIMIT 1`, definitionID).Scan(&previous)
	if _, err := tx.ExecContext(ctx, `UPDATE skill_versions SET status='RETIRED' WHERE skill_definition_id=$1 AND status='ACTIVE'`, definitionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE skill_versions SET status='ACTIVE' WHERE id=$1 AND status='DRAFT'`, candidateID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO registry_activation_audits
		(id,registry_type,definition_key,previous_version_id,activated_version_id,action,actor)
		VALUES($1,'SKILL',$2,$3,$4,'PROMOTE',$5)`, uuid.NewString(), skillKey, previous, candidateID, actor); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PromoteToolVersion(ctx context.Context, toolKey, candidateID, actor string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var definitionID string
	if err := tx.QueryRowContext(ctx, `SELECT td.id FROM tool_definitions td JOIN tool_versions tv ON tv.tool_definition_id=td.id
		WHERE td.tool_key=$1 AND tv.id=$2 AND tv.status='DRAFT' FOR UPDATE`, toolKey, candidateID).Scan(&definitionID); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	if err := requireRegistryApprovals(ctx, tx, "TOOL_VERSION", candidateID, actor); err != nil {
		return err
	}
	var previous any
	_ = tx.QueryRowContext(ctx, `SELECT id FROM tool_versions WHERE tool_definition_id=$1 AND status='ACTIVE' LIMIT 1`, definitionID).Scan(&previous)
	if _, err := tx.ExecContext(ctx, `UPDATE tool_versions SET status='RETIRED' WHERE tool_definition_id=$1 AND status='ACTIVE'`, definitionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_versions SET status='ACTIVE' WHERE id=$1 AND status='DRAFT'`, candidateID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO registry_activation_audits
		(id,registry_type,definition_key,previous_version_id,activated_version_id,action,actor)
		VALUES($1,'TOOL_VERSION',$2,$3,$4,'PROMOTE',$5)`, uuid.NewString(), toolKey, previous, candidateID, actor); err != nil {
		return err
	}
	return tx.Commit()
}

type ToolPolicyCandidateInput struct {
	ToolKey, AgentType, WorkflowState, Decision string
	ProjectID                                   *int64
	RequiresGate                                bool
	Conditions                                  any
}

func (s *Store) CreateToolPolicyCandidate(ctx context.Context, input ToolPolicyCandidateInput) (string, error) {
	if input.AgentType == "" {
		input.AgentType = "*"
	}
	if input.WorkflowState == "" {
		input.WorkflowState = "*"
	}
	if input.Decision != "ALLOW" && input.Decision != "DENY" {
		return "", errors.New("tool policy decision must be ALLOW or DENY")
	}
	conditions, err := json.Marshal(input.Conditions)
	if err != nil {
		return "", err
	}
	if input.Conditions == nil {
		conditions = []byte(`{}`)
	}
	id := uuid.NewString()
	err = s.db.QueryRowContext(ctx, `INSERT INTO tool_policies
		(id,tool_version_id,project_id,agent_type,workflow_state,decision,requires_gate,conditions_json,version,status)
		SELECT $1,tv.id,$2,$3,$4,$5,$6,$7,COALESCE((SELECT max(p.version)+1 FROM tool_policies p WHERE p.tool_version_id=tv.id),1),'CANDIDATE'
		FROM tool_versions tv JOIN tool_definitions td ON td.id=tv.tool_definition_id
		WHERE td.tool_key=$8 AND tv.status='ACTIVE' RETURNING id`, id, input.ProjectID, input.AgentType, input.WorkflowState,
		input.Decision, input.RequiresGate, string(conditions), input.ToolKey).Scan(&id)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	return id, err
}

func (s *Store) PromoteToolPolicyCandidate(ctx context.Context, candidateID, actor string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var toolVersionID, toolKey, agentType, workflowState string
	var projectID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT tp.tool_version_id,td.tool_key,tp.project_id,tp.agent_type,tp.workflow_state
		FROM tool_policies tp JOIN tool_versions tv ON tv.id=tp.tool_version_id JOIN tool_definitions td ON td.id=tv.tool_definition_id
		WHERE tp.id=$1 AND tp.status='CANDIDATE' FOR UPDATE`, candidateID).Scan(&toolVersionID, &toolKey, &projectID, &agentType, &workflowState); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	if err := requireRegistryApprovals(ctx, tx, "TOOL_POLICY", candidateID, actor); err != nil {
		return err
	}
	var projectValue any
	if projectID.Valid {
		projectValue = projectID.Int64
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_policies SET status='RETIRED' WHERE tool_version_id=$1 AND status='ACTIVE'
		AND project_id IS NOT DISTINCT FROM $2 AND agent_type=$3 AND workflow_state=$4`, toolVersionID, projectValue, agentType, workflowState); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_policies SET status='ACTIVE',approved_by=$1,approved_at=CURRENT_TIMESTAMP WHERE id=$2`, actor, candidateID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO registry_activation_audits
		(id,registry_type,definition_key,activated_version_id,action,actor)
		VALUES($1,'TOOL_POLICY',$2,$3,'PROMOTE',$4)`, uuid.NewString(), toolKey, candidateID, actor); err != nil {
		return err
	}
	return tx.Commit()
}
