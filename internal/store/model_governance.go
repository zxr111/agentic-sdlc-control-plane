package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/routing"
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

func (s *Store) EvaluationModel(ctx context.Context, modelVersionID string) (routing.Model, error) {
	var model routing.Model
	var capabilities []byte
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT mv.id::text,mv.model_key,mv.capabilities,
		mv.input_cost_microunits,mv.output_cost_microunits,mv.status,
		COALESCE((SELECT healthy FROM model_health_events mh WHERE mh.model_version_id=mv.id ORDER BY observed_at DESC LIMIT 1),true)
		FROM model_versions mv WHERE mv.id=$1`, modelVersionID).Scan(&model.ID, &model.Key, &capabilities,
		&model.InputCost, &model.OutputCost, &status, &model.Healthy)
	if err == sql.ErrNoRows {
		return routing.Model{}, ErrNotFound
	}
	if err != nil {
		return routing.Model{}, err
	}
	if status != "CANDIDATE" && status != "ACTIVE" {
		return routing.Model{}, ErrGovernanceRequired
	}
	if err := json.Unmarshal(capabilities, &model.Capabilities); err != nil {
		return routing.Model{}, err
	}
	if !model.Healthy || !model.Capabilities["structured_output"] {
		return routing.Model{}, ErrGovernanceRequired
	}
	model.Active = status == "ACTIVE"
	return model, nil
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
	var previous any
	_ = tx.QueryRowContext(ctx, `SELECT (rules_json->>'preferred_model_version_id')::uuid FROM model_policies
		WHERE policy_key='default' AND status='ACTIVE' AND rules_json ? 'preferred_model_version_id'`).Scan(&previous)
	if _, err := tx.ExecContext(ctx, `UPDATE model_versions SET status='ACTIVE' WHERE id=$1`, modelVersionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE model_policies SET rules_json=jsonb_set(rules_json,'{preferred_model_version_id}',to_jsonb($1::text),true)
		WHERE policy_key='default' AND status='ACTIVE'`, modelVersionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO registry_activation_audits(id,registry_type,definition_key,previous_version_id,activated_version_id,
		evaluation_run_id,blind_review_id,canary_release_id,action,actor) VALUES($1,'MODEL',$2,$3,$4,$5,$6,$7,'PROMOTE',$8)`,
		uuid.NewString(), modelKey, previous, modelVersionID, evaluationRunID, blindReviewID, canaryID, actor); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ActiveRoutingModels(ctx context.Context) ([]routing.Model, string, bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mv.id::text,mv.model_key,mv.capabilities,mv.input_cost_microunits,mv.output_cost_microunits,
		COALESCE((SELECT healthy FROM model_health_events mh WHERE mh.model_version_id=mv.id ORDER BY observed_at DESC LIMIT 1),true)
		FROM model_versions mv WHERE mv.status='ACTIVE' ORDER BY mv.created_at DESC`)
	if err != nil {
		return nil, "", false, err
	}
	defer rows.Close()
	var models []routing.Model
	for rows.Next() {
		var model routing.Model
		var capabilities []byte
		if err := rows.Scan(&model.ID, &model.Key, &capabilities, &model.InputCost, &model.OutputCost, &model.Healthy); err != nil {
			return nil, "", false, err
		}
		if err := json.Unmarshal(capabilities, &model.Capabilities); err != nil {
			return nil, "", false, err
		}
		model.Active = true
		model.Quality = 100
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, err
	}
	var preferred, fallback sql.NullString
	var allowFallback bool
	err = s.db.QueryRowContext(ctx, `SELECT preferred.model_key,fallback.model_key,mp.allow_fallback FROM model_policies mp
		LEFT JOIN model_versions preferred ON preferred.id=CASE WHEN mp.rules_json->>'preferred_model_version_id' ~* '^[0-9a-f-]{36}$'
			THEN (mp.rules_json->>'preferred_model_version_id')::uuid ELSE NULL END
		LEFT JOIN model_versions fallback ON fallback.id=mp.fallback_model_version_id
		WHERE mp.policy_key='default' AND mp.status='ACTIVE'`).Scan(&preferred, &fallback, &allowFallback)
	if err == sql.ErrNoRows {
		return models, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	if !preferred.Valid {
		preferred = fallback
	}
	return models, preferred.String, allowFallback, nil
}

func (s *Store) RollbackModelVersion(ctx context.Context, modelVersionID, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var key string
	if err := tx.QueryRowContext(ctx, `SELECT model_key FROM model_versions WHERE id=$1 AND status='ACTIVE' FOR UPDATE`, modelVersionID).Scan(&key); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	var previousID string
	err = tx.QueryRowContext(ctx, `SELECT previous_version_id::text FROM registry_activation_audits
		WHERE registry_type='MODEL' AND activated_version_id=$1 AND previous_version_id IS NOT NULL
		ORDER BY created_at DESC LIMIT 1`, modelVersionID).Scan(&previousID)
	if err == sql.ErrNoRows {
		err = tx.QueryRowContext(ctx, `SELECT id::text FROM model_versions WHERE id<>$1 AND status='ACTIVE' ORDER BY created_at DESC LIMIT 1`, modelVersionID).Scan(&previousID)
	}
	if err != nil {
		return ErrGovernanceRequired
	}
	if _, err := tx.ExecContext(ctx, `UPDATE model_versions SET status='RETIRED' WHERE id=$1`, modelVersionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE model_versions SET status='ACTIVE' WHERE id=$1`, previousID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE model_policies SET rules_json=jsonb_set(rules_json,'{preferred_model_version_id}',to_jsonb($1::text),true)
		WHERE policy_key='default' AND status='ACTIVE'`, previousID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO registry_activation_audits(id,registry_type,definition_key,previous_version_id,activated_version_id,action,actor)
		VALUES($1,'MODEL',$2,$3,$4,'ROLLBACK',$5)`, uuid.NewString(), key, modelVersionID, previousID, actor); err != nil {
		return err
	}
	return tx.Commit()
}
