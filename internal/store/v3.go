package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type ContextEntryInput struct {
	SourceType     string
	SourceID       string
	AuthorityLevel int
	TokenCount     int
	ContentHash    string
	Citation       any
	Required       bool
}

// CreateContextManifest persists the exact ordered source set supplied to an
// agent. The manifest is append-only and stores hashes and citations rather
// than copying potentially sensitive source bodies.
func (s *Store) CreateContextManifest(ctx context.Context, workflowID, purpose, policyVersion string, entries []ContextEntryInput) (string, error) {
	hash := sha256.New()
	totalTokens := 0
	for index, entry := range entries {
		fmt.Fprintf(hash, "%d:%s:%s:%d:%s\n", index, entry.SourceType, entry.SourceID, entry.AuthorityLevel, entry.ContentHash)
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
		citation, err := json.Marshal(entry.Citation)
		if err != nil {
			return "", err
		}
		var sourceID any
		if entry.SourceID != "" {
			sourceID = entry.SourceID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO context_entries
			(id,context_manifest_id,ordinal,source_type,source_id,authority_level,token_count,content_hash,citation_json,required)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, uuid.NewString(), manifestID, index, entry.SourceType,
			sourceID, entry.AuthorityLevel, entry.TokenCount, entry.ContentHash, string(citation), entry.Required); err != nil {
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
