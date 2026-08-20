package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type ContextEntryInput struct {
	SourceType        string
	SourceID          string
	AuthorityLevel    int
	TokenCount        int
	ContentHash       string
	Citation          any
	Required          bool
	CompressionMethod string
}

// CreateContextManifest persists the exact ordered source set supplied to an
// agent. The manifest is append-only and stores hashes and citations rather
// than copying potentially sensitive source bodies.
func (s *Store) CreateContextManifest(ctx context.Context, workflowID, purpose, policyVersion string, entries []ContextEntryInput) (string, error) {
	hash := sha256.New()
	totalTokens := 0
	for index, entry := range entries {
		if strings.TrimSpace(entry.SourceType) == "" || strings.TrimSpace(entry.SourceID) == "" || len(entry.ContentHash) != 64 || entry.Citation == nil {
			return "", fmt.Errorf("context entry %d is missing immutable source, hash, or citation evidence", index)
		}
		compression := entry.CompressionMethod
		if compression == "" {
			compression = "none"
		}
		fmt.Fprintf(hash, "%d:%s:%s:%d:%s:%s\n", index, entry.SourceType, entry.SourceID, entry.AuthorityLevel, entry.ContentHash, compression)
		totalTokens += entry.TokenCount
	}
	manifestID := uuid.NewString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO context_manifests
		(id,workflow_id,purpose,policy_version,total_tokens,content_hash) VALUES ($1,$2,$3,$4,$5,$6)`,
		manifestID, workflowID, purpose, policyVersion, totalTokens, hex.EncodeToString(hash.Sum(nil))); err != nil {
		return "", err
	}
	for index, entry := range entries {
		if entry.CompressionMethod == "" {
			entry.CompressionMethod = "none"
		}
		citation, err := json.Marshal(entry.Citation)
		if err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO context_entries
			(id,context_manifest_id,ordinal,source_type,source_id,authority_level,compression_method,token_count,content_hash,citation_json,required)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, uuid.NewString(), manifestID, index, entry.SourceType,
			entry.SourceID, entry.AuthorityLevel, entry.CompressionMethod, entry.TokenCount, entry.ContentHash, string(citation), entry.Required); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return manifestID, nil
}

func (s *Store) StartAgentRunWithContext(ctx context.Context, workflowID, workItemID, agentType, model, inputHash, contextManifestID string) (string, error) {
	return s.StartAgentRunWithProfile(ctx, workflowID, workItemID, agentType, strings.ToLower(agentType), model, inputHash, contextManifestID)
}

func (s *Store) StartAgentRunWithProfile(ctx context.Context, workflowID, workItemID, agentType, profileKey, model, inputHash, contextManifestID string) (string, error) {
	id, err := s.StartAgentRun(ctx, workflowID, workItemID, agentType, model, inputHash)
	if err != nil {
		return id, err
	}
	var manifest any
	if contextManifestID != "" {
		manifest = contextManifestID
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agent_runs SET context_manifest_id=$1 WHERE id=$2`, manifest, id); err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agent_runs SET
		agent_profile_version_id=(SELECT apv.id FROM agent_profile_versions apv JOIN agent_profiles ap ON ap.id=apv.agent_profile_id
			WHERE ap.profile_key=LOWER($2) AND apv.status='ACTIVE' ORDER BY apv.version DESC LIMIT 1),
		prompt_version_id=(SELECT apv.prompt_version_id FROM agent_profile_versions apv JOIN agent_profiles ap ON ap.id=apv.agent_profile_id
			WHERE ap.profile_key=LOWER($2) AND apv.status='ACTIVE' ORDER BY apv.version DESC LIMIT 1),
		model_version_id=(SELECT mv.id FROM model_versions mv WHERE mv.model_key=$3 AND mv.status='ACTIVE' ORDER BY mv.created_at DESC LIMIT 1)
		WHERE id=$1`, id, profileKey, model); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) BindAgentRunContext(ctx context.Context, agentRunID, manifestID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs ar SET context_manifest_id=$1,lifecycle_phase='RUNNING'
		WHERE ar.id=$2 AND ar.status='RUNNING' AND ar.lifecycle_phase='CONTEXT_BUILDING' AND ar.context_manifest_id IS NULL
		AND EXISTS(SELECT 1 FROM context_manifests cm WHERE cm.id=$1 AND cm.workflow_id=ar.workflow_id)`, manifestID, agentRunID)
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
	if _, err := tx.ExecContext(ctx, `UPDATE agent_steps SET status='COMPLETED',finished_at=CURRENT_TIMESTAMP
		WHERE agent_run_id=$1 AND step_type='CONTEXT_BUILDING' AND status='RUNNING'`, agentRunID); err != nil {
		return err
	}
	if err := insertAgentStep(ctx, tx, agentRunID, "MODEL_RESPONSE", "RUNNING", nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BeginAgentRunContext(ctx context.Context, agentRunID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET lifecycle_phase='CONTEXT_BUILDING'
		WHERE id=$1 AND status='RUNNING' AND lifecycle_phase='RUNNING'`, agentRunID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return err
		}
		return ErrNotFound
	}
	if err := insertAgentStep(ctx, tx, agentRunID, "CONTEXT_BUILDING", "RUNNING", nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AgentRunContextTokenLimit(ctx context.Context, agentRunID string) (int, error) {
	var limit int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((apv.context_policy->>'max_source_tokens')::int,8000)
		FROM agent_runs ar JOIN agent_profile_versions apv ON apv.id=ar.agent_profile_version_id WHERE ar.id=$1`, agentRunID).Scan(&limit)
	if err == sql.ErrNoRows {
		return 8000, nil
	}
	if err != nil {
		return 0, err
	}
	if limit <= 0 || limit > 100000 {
		return 0, errors.New("Agent Profile has an invalid context token limit")
	}
	return limit, nil
}

func (s *Store) BeginAgentRunExecution(ctx context.Context, agentRunID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET lifecycle_phase='RUNNING'
		WHERE id=$1 AND status='RUNNING' AND lifecycle_phase='CONTEXT_BUILDING'`, agentRunID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return err
		}
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_steps SET status='COMPLETED',finished_at=CURRENT_TIMESTAMP
		WHERE agent_run_id=$1 AND step_type='CONTEXT_BUILDING' AND status='RUNNING'`, agentRunID); err != nil {
		return err
	}
	if err := insertAgentStep(ctx, tx, agentRunID, "MODEL_RESPONSE", "RUNNING", nil); err != nil {
		return err
	}
	return tx.Commit()
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertAgentStep(ctx context.Context, executor sqlExecutor, agentRunID, stepType, status string, metadata any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = executor.ExecContext(ctx, `INSERT INTO agent_steps(id,agent_run_id,ordinal,step_type,status,metadata_json)
		SELECT $1,$2,COALESCE(MAX(ordinal),-1)+1,$3,$4,$5 FROM agent_steps WHERE agent_run_id=$2`,
		uuid.NewString(), agentRunID, stepType, status, string(raw))
	return err
}
