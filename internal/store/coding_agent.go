package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/codingagent"
	"github.com/google/uuid"
)

func (s *Store) CodingTaskManifest(ctx context.Context, dispatchID string) (codingagent.TaskManifest, error) {
	var value codingagent.TaskManifest
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT manifest_json FROM coding_task_manifests WHERE dispatch_id=$1`, dispatchID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return value, ErrNotFound
	}
	if err != nil {
		return value, err
	}
	return value, json.Unmarshal(raw, &value)
}

// EnsureCodingTaskManifest freezes the approved SDD boundary for one visible
// dispatch. Repeated reads return exactly the same immutable manifest.
func (s *Store) EnsureCodingTaskManifest(ctx context.Context, dispatchID, provider, fallbackRepository string) (codingagent.TaskManifest, error) {
	if existing, err := s.CodingTaskManifest(ctx, dispatchID); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return codingagent.TaskManifest{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return codingagent.TaskManifest{}, err
	}
	defer tx.Rollback()
	var manifest codingagent.TaskManifest
	err = tx.QueryRowContext(ctx, `SELECT d.id,wi.id,wi.workflow_id,wi.title,wi.target_branch,wi.branch_name
		FROM codex_dispatches d JOIN work_items wi ON wi.id=d.work_item_id
		WHERE d.id=$1 FOR UPDATE OF d`, dispatchID).
		Scan(&manifest.DispatchID, &manifest.WorkItemID, &manifest.WorkflowID, &manifest.Title, &manifest.BaseRef, &manifest.Branch)
	if errors.Is(err, sql.ErrNoRows) {
		return codingagent.TaskManifest{}, ErrNotFound
	}
	if err != nil {
		return codingagent.TaskManifest{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(sc.repository,$2),pi.sdd_artifact_id,a.source_hash,
		pi.likely_paths,pi.acceptance_ids,pi.verification FROM planned_impacts pi
		JOIN artifacts a ON a.id=pi.sdd_artifact_id LEFT JOIN service_catalog sc ON sc.id=pi.service_id
		WHERE pi.work_item_id=$1 AND pi.status='ACTIVE' ORDER BY pi.created_at`, manifest.WorkItemID, fallbackRepository)
	if err != nil {
		return manifest, err
	}
	defer rows.Close()
	var repository, sourceID, sourceHash string
	count := 0
	for rows.Next() {
		var currentRepository, currentSourceID, currentSourceHash string
		var pathsRaw, acceptanceRaw, checksRaw []byte
		if err := rows.Scan(&currentRepository, &currentSourceID, &currentSourceHash, &pathsRaw, &acceptanceRaw, &checksRaw); err != nil {
			return manifest, err
		}
		if count > 0 && (repository != currentRepository || sourceID != currentSourceID || sourceHash != currentSourceHash) {
			return manifest, errors.New("active SDD impacts disagree on repository or source artifact")
		}
		repository, sourceID, sourceHash = currentRepository, currentSourceID, currentSourceHash
		for _, pair := range []struct {
			raw    []byte
			target *[]string
		}{{pathsRaw, &manifest.AllowedPaths}, {acceptanceRaw, &manifest.AcceptanceIDs}, {checksRaw, &manifest.RequiredChecks}} {
			var values []string
			if err := json.Unmarshal(pair.raw, &values); err != nil {
				return manifest, err
			}
			*pair.target = append(*pair.target, values...)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return manifest, err
	}
	if count == 0 {
		return manifest, errors.New("dispatch has no active approved SDD impact")
	}
	manifest.ID = uuid.NewString()
	manifest.Version = 1
	manifest.Provider = strings.ToLower(strings.TrimSpace(provider))
	manifest.Repository = repository
	manifest.SourceArtifactID = sourceID
	manifest.SourceArtifactHash = sourceHash
	manifest.CreatedAt = time.Now().UTC()
	manifest.Permissions = codingagent.PermissionSet{WriteFiles: true, RunCommands: true, CreateCommit: true, CreateDraftMR: true}
	manifest.AllowedPaths = uniqueSorted(manifest.AllowedPaths)
	manifest.AcceptanceIDs = uniqueSorted(manifest.AcceptanceIDs)
	manifest.RequiredChecks = uniqueSorted(manifest.RequiredChecks)
	if err := manifest.Seal(); err != nil {
		return manifest, err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return manifest, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO coding_task_manifests
		(id,dispatch_id,work_item_id,workflow_id,manifest_version,provider,manifest_hash,repository,base_ref,
		 branch_name,source_artifact_id,source_artifact_hash,manifest_json)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(dispatch_id) DO NOTHING`,
		manifest.ID, manifest.DispatchID, manifest.WorkItemID, manifest.WorkflowID, manifest.Version, manifest.Provider,
		manifest.Hash, manifest.Repository, manifest.BaseRef, manifest.Branch, manifest.SourceArtifactID,
		manifest.SourceArtifactHash, raw)
	if err != nil {
		return manifest, err
	}
	if err := tx.Commit(); err != nil {
		return manifest, err
	}
	return s.CodingTaskManifest(ctx, dispatchID)
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func (s *Store) SaveCodingAgentEvidence(ctx context.Context, evidence codingagent.Evidence) error {
	manifest, err := s.CodingTaskManifest(ctx, evidence.DispatchID)
	if err != nil {
		return err
	}
	if err := evidence.Validate(manifest); err != nil {
		return err
	}
	if evidence.ID == "" {
		evidence.ID = uuid.NewString()
	}
	if evidence.CreatedAt.IsZero() {
		evidence.CreatedAt = time.Now().UTC()
	}
	payload := evidence.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if !json.Valid(payload) {
		return fmt.Errorf("evidence payload is not valid JSON")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO coding_agent_evidence
		(id,dispatch_id,manifest_hash,provider,session_id,evidence_kind,commit_sha,command_text,exit_code,payload_json,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, evidence.ID, evidence.DispatchID,
		evidence.ManifestHash, evidence.Provider, evidence.SessionID, evidence.Kind, evidence.CommitSHA,
		evidence.Command, evidence.ExitCode, payload, evidence.CreatedAt)
	return err
}
