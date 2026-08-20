package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/google/uuid"
)

func (s *Store) RegisterModelCandidate(ctx context.Context, providerKey, modelKey string, capabilities any, inputCost, outputCost int64) (string, error) {
	raw, err := json.Marshal(capabilities)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	err = s.db.QueryRowContext(ctx, `INSERT INTO model_versions(id,provider_id,model_key,capabilities,input_cost_microunits,output_cost_microunits,status)
		SELECT $1,id,$2,$3,$4,$5,'CANDIDATE' FROM model_providers WHERE provider_key=$6
		ON CONFLICT(provider_id,model_key) DO UPDATE SET capabilities=EXCLUDED.capabilities,
		input_cost_microunits=EXCLUDED.input_cost_microunits,output_cost_microunits=EXCLUDED.output_cost_microunits
		RETURNING id`, id, modelKey, string(raw), inputCost, outputCost, providerKey).Scan(&id)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	return id, err
}

func (s *Store) PromoteModelVersion(ctx context.Context, modelVersionID, evaluationRunID, blindReviewID, canaryID, actor string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var modelKey string
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT mv.model_key,EXISTS(SELECT 1 FROM evaluation_runs er
		JOIN evaluation_blind_reviews br ON br.id=$3 AND br.candidate_run_id=er.id AND br.status='APPROVED'
		JOIN canary_releases cr ON cr.id=$4 AND cr.candidate_type='MODEL' AND cr.candidate_version_id=mv.id
			AND cr.evaluation_run_id=er.id AND cr.blind_review_id=br.id AND cr.status='APPROVED'
		WHERE er.id=$2 AND er.model_version_id=mv.id AND er.status='COMPLETED' AND er.shadow=true)
		FROM model_versions mv WHERE mv.id=$1 AND mv.status='CANDIDATE' FOR UPDATE`, modelVersionID, evaluationRunID, blindReviewID, canaryID).Scan(&modelKey, &eligible); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	if !eligible {
		return ErrGovernanceRequired
	}
	if _, err := tx.ExecContext(ctx, `UPDATE model_versions SET status='ACTIVE' WHERE id=$1`, modelVersionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO registry_activation_audits(id,registry_type,definition_key,activated_version_id,
		evaluation_run_id,blind_review_id,canary_release_id,action,actor) VALUES($1,'MODEL',$2,$3,$4,$5,$6,'PROMOTE',$7)`,
		uuid.NewString(), modelKey, modelVersionID, evaluationRunID, blindReviewID, canaryID, actor); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RollbackModelVersion(ctx context.Context, modelVersionID, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var key string
	if err := tx.QueryRowContext(ctx, `UPDATE model_versions SET status='RETIRED' WHERE id=$1 AND status='ACTIVE' RETURNING model_key`, modelVersionID).Scan(&key); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO registry_activation_audits(id,registry_type,definition_key,activated_version_id,action,actor)
		VALUES($1,'MODEL',$2,$3,'ROLLBACK',$4)`, uuid.NewString(), key, modelVersionID, actor); err != nil {
		return err
	}
	return tx.Commit()
}
