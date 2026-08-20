package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

type RegistryDefinition struct {
	AgentType    string
	PromptKey    string
	DisplayName  string
	Instructions string
	OutputSchema json.RawMessage
}

func registryID(kind, key string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("ai-sdlc-factory:v3:"+kind+":"+key)).String()
}

// BootstrapRegistry installs the immutable built-in versions. Repeated calls
// are idempotent and never overwrite version content or activation decisions.
func (s *Store) BootstrapRegistry(ctx context.Context, model string, definitions []RegistryDefinition) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	providerID := registryID("provider", "openai")
	if _, err := tx.ExecContext(ctx, `INSERT INTO model_providers
		(id,provider_key,display_name,base_url,secret_reference,status)
		VALUES ($1,'openai','OpenAI','','env:OPENAI_API_KEY','ACTIVE') ON CONFLICT (provider_key) DO NOTHING`, providerID); err != nil {
		return err
	}
	modelID := registryID("model", "openai:"+model)
	if _, err := tx.ExecContext(ctx, `INSERT INTO model_versions
		(id,provider_id,model_key,capabilities,status) VALUES ($1,$2,$3,$4,'ACTIVE')
		ON CONFLICT (provider_id,model_key) DO NOTHING`, modelID, providerID, model,
		`{"structured_output":true,"reasoning":true,"tool_calling":true}`); err != nil {
		return err
	}
	policyID := registryID("model-policy", "default")
	if _, err := tx.ExecContext(ctx, `INSERT INTO model_policies
		(id,policy_key,rules_json,fallback_model_version_id,allow_fallback,status)
		VALUES ($1,'default',$2,$3,false,'ACTIVE') ON CONFLICT (policy_key) DO NOTHING`,
		policyID, `{"strategy":"fixed","high_risk_fallback":"deny"}`, modelID); err != nil {
		return err
	}
	for _, definition := range definitions {
		promptDefinitionID := registryID("prompt-definition", definition.PromptKey)
		if _, err := tx.ExecContext(ctx, `INSERT INTO prompt_definitions
			(id,prompt_key,display_name,description) VALUES ($1,$2,$3,$4)
			ON CONFLICT (prompt_key) DO NOTHING`, promptDefinitionID, definition.PromptKey,
			definition.DisplayName, "Factory 内置 Prompt"); err != nil {
			return err
		}
		schema := []byte(definition.OutputSchema)
		digest := sha256.Sum256(append(append([]byte(definition.Instructions), 0), schema...))
		contentHash := hex.EncodeToString(digest[:])
		promptVersionID := registryID("prompt-version", definition.PromptKey+":"+contentHash)
		if _, err := tx.ExecContext(ctx, `INSERT INTO prompt_versions
			(id,prompt_definition_id,version,status,content,output_schema,content_hash,created_by,approved_by,approved_at)
			VALUES ($1,$2,1,'ACTIVE',$3,$4,$5,'factory-bootstrap','factory-bootstrap',CURRENT_TIMESTAMP)
			ON CONFLICT (prompt_definition_id,content_hash) DO NOTHING`, promptVersionID, promptDefinitionID,
			definition.Instructions, string(schema), contentHash); err != nil {
			return err
		}
		profileKey := strings.ToLower(definition.AgentType)
		profileID := registryID("agent-profile", profileKey)
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_profiles
			(id,profile_key,display_name,description) VALUES ($1,$2,$3,$4)
			ON CONFLICT (profile_key) DO NOTHING`, profileID, profileKey, definition.DisplayName,
			"Factory 内置 Agent Profile"); err != nil {
			return err
		}
		profileVersionID := registryID("agent-profile-version", profileKey+":"+contentHash+":"+model)
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_profile_versions
			(id,agent_profile_id,version,status,prompt_version_id,model_policy_id,context_policy,tool_policy,budget_json)
			VALUES ($1,$2,1,'ACTIVE',$3,$4,$5,$6,$7) ON CONFLICT (agent_profile_id,version) DO NOTHING`,
			profileVersionID, profileID, promptVersionID, policyID,
			`{"authority_order":["confluence_snapshot","approved_artifact","project_memory"],"citation_required":true}`,
			`{"default":"deny"}`, `{"max_output_tokens":12000,"reasoning_effort":"medium"}`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type PromptVersionRecord struct {
	ID          string
	PromptKey   string
	Version     int
	Status      string
	ContentHash string
}

type PromptRuntime struct {
	PromptVersionRecord
	Content      string
	OutputSchema json.RawMessage
}

func (s *Store) PromptRuntime(ctx context.Context, versionID string) (PromptRuntime, error) {
	var result PromptRuntime
	var schema []byte
	err := s.db.QueryRowContext(ctx, `SELECT pv.id,pd.prompt_key,pv.version,pv.status,pv.content_hash,pv.content,pv.output_schema
		FROM prompt_versions pv JOIN prompt_definitions pd ON pd.id=pv.prompt_definition_id WHERE pv.id=$1`, versionID).
		Scan(&result.ID, &result.PromptKey, &result.Version, &result.Status, &result.ContentHash, &result.Content, &schema)
	if err == sql.ErrNoRows {
		return PromptRuntime{}, ErrNotFound
	}
	if err != nil {
		return PromptRuntime{}, err
	}
	result.OutputSchema = json.RawMessage(schema)
	return result, nil
}

func (s *Store) CreatePromptVersion(ctx context.Context, promptKey, content string, schema json.RawMessage, actor string) (PromptVersionRecord, error) {
	digest := sha256.Sum256(append(append([]byte(content), 0), []byte(schema)...))
	contentHash := hex.EncodeToString(digest[:])
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return PromptVersionRecord{}, err
	}
	defer tx.Rollback()
	var definitionID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM prompt_definitions WHERE prompt_key=$1 FOR UPDATE`, promptKey).Scan(&definitionID); err != nil {
		if err == sql.ErrNoRows {
			return PromptVersionRecord{}, ErrNotFound
		}
		return PromptVersionRecord{}, err
	}
	var existing PromptVersionRecord
	err = tx.QueryRowContext(ctx, `SELECT id,version,status,content_hash FROM prompt_versions
		WHERE prompt_definition_id=$1 AND content_hash=$2`, definitionID, contentHash).
		Scan(&existing.ID, &existing.Version, &existing.Status, &existing.ContentHash)
	if err == nil {
		existing.PromptKey = promptKey
		return existing, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return PromptVersionRecord{}, err
	}
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM prompt_versions WHERE prompt_definition_id=$1`, definitionID).Scan(&version); err != nil {
		return PromptVersionRecord{}, err
	}
	record := PromptVersionRecord{ID: uuid.NewString(), PromptKey: promptKey, Version: version, Status: "DRAFT", ContentHash: contentHash}
	if _, err := tx.ExecContext(ctx, `INSERT INTO prompt_versions
		(id,prompt_definition_id,version,status,content,output_schema,content_hash,created_by)
		VALUES ($1,$2,$3,'DRAFT',$4,$5,$6,$7)`, record.ID, definitionID, version, content, string(schema), contentHash, actor); err != nil {
		return PromptVersionRecord{}, err
	}
	return record, tx.Commit()
}

// ActivatePromptVersion is an explicit human action. It never activates a
// candidate implicitly as a result of evaluation or model output.
func (s *Store) ActivatePromptVersion(ctx context.Context, promptKey, versionID, actor string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var definitionID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM prompt_definitions WHERE prompt_key=$1 FOR UPDATE`, promptKey).Scan(&definitionID); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	var currentStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM prompt_versions WHERE id=$1 AND prompt_definition_id=$2`, versionID, definitionID).Scan(&currentStatus); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	if currentStatus != "RETIRED" {
		return ErrGovernanceRequired
	}
	var previous any
	_ = tx.QueryRowContext(ctx, `SELECT id FROM prompt_versions WHERE prompt_definition_id=$1 AND status='ACTIVE' LIMIT 1`, definitionID).Scan(&previous)
	result, err := tx.ExecContext(ctx, `UPDATE prompt_versions SET status='RETIRED'
		WHERE prompt_definition_id=$1 AND status='ACTIVE' AND id<>$2`, definitionID, versionID)
	if err != nil {
		return err
	}
	_ = result
	result, err = tx.ExecContext(ctx, `UPDATE prompt_versions SET status='ACTIVE',approved_by=$1,approved_at=CURRENT_TIMESTAMP
		WHERE id=$2 AND prompt_definition_id=$3`, actor, versionID, definitionID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO registry_activation_audits
		(id,registry_type,definition_key,previous_version_id,activated_version_id,action,actor)
		VALUES($1,'PROMPT',$2,$3,$4,'ROLLBACK',$5)`, uuid.NewString(), promptKey, previous, versionID, actor); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PromotePromptVersion(ctx context.Context, promptKey, versionID, evaluationRunID, blindReviewID, canaryID, actor string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var definitionID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM prompt_definitions WHERE prompt_key=$1 FOR UPDATE`, promptKey).Scan(&definitionID); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM prompt_versions pv
		JOIN evaluation_runs er ON er.prompt_version_id=pv.id AND er.id=$3 AND er.status='COMPLETED' AND er.shadow=true
		JOIN evaluation_blind_reviews br ON br.id=$4 AND br.candidate_run_id=er.id AND br.status='APPROVED'
		JOIN canary_releases cr ON cr.id=$5 AND cr.candidate_version_id=pv.id AND cr.evaluation_run_id=er.id
			AND cr.blind_review_id=br.id AND cr.status='APPROVED'
		WHERE pv.id=$1 AND pv.prompt_definition_id=$2 AND pv.status='DRAFT')`, versionID, definitionID, evaluationRunID, blindReviewID, canaryID).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return ErrGovernanceRequired
	}
	var previous any
	_ = tx.QueryRowContext(ctx, `SELECT id FROM prompt_versions WHERE prompt_definition_id=$1 AND status='ACTIVE' LIMIT 1`, definitionID).Scan(&previous)
	if _, err := tx.ExecContext(ctx, `UPDATE prompt_versions SET status='RETIRED' WHERE prompt_definition_id=$1 AND status='ACTIVE'`, definitionID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE prompt_versions SET status='ACTIVE',approved_by=$1,approved_at=CURRENT_TIMESTAMP WHERE id=$2 AND status='DRAFT'`, actor, versionID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO registry_activation_audits
		(id,registry_type,definition_key,previous_version_id,activated_version_id,evaluation_run_id,blind_review_id,canary_release_id,action,actor)
		VALUES($1,'PROMPT',$2,$3,$4,$5,$6,$7,'PROMOTE',$8)`, uuid.NewString(), promptKey, previous, versionID,
		evaluationRunID, blindReviewID, canaryID, actor); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ActivePromptVersion(ctx context.Context, promptKey string) (PromptVersionRecord, error) {
	var record PromptVersionRecord
	err := s.db.QueryRowContext(ctx, `SELECT pv.id,pd.prompt_key,pv.version,pv.status,pv.content_hash
		FROM prompt_versions pv JOIN prompt_definitions pd ON pd.id=pv.prompt_definition_id
		WHERE pd.prompt_key=$1 AND pv.status='ACTIVE' ORDER BY pv.version DESC LIMIT 1`, promptKey).
		Scan(&record.ID, &record.PromptKey, &record.Version, &record.Status, &record.ContentHash)
	if err == sql.ErrNoRows {
		return PromptVersionRecord{}, ErrNotFound
	}
	return record, err
}
