package codingagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Provider is implemented by engineer-visible coding clients. The Factory
// persists manifests and evidence, but deliberately does not invoke providers.
type Provider interface {
	Start(context.Context, SessionRequest) (Session, error)
	Cancel(context.Context, string) error
}

type PermissionSet struct {
	WriteFiles    bool `json:"write_files"`
	RunCommands   bool `json:"run_commands"`
	CreateCommit  bool `json:"create_commit"`
	CreateDraftMR bool `json:"create_draft_mr"`
	Merge         bool `json:"merge"`
	Deploy        bool `json:"deploy"`
}

type TaskManifest struct {
	ID                 string        `json:"id"`
	Version            int           `json:"version"`
	DispatchID         string        `json:"dispatch_id"`
	WorkItemID         string        `json:"work_item_id"`
	WorkflowID         string        `json:"workflow_id"`
	Provider           string        `json:"provider"`
	Repository         string        `json:"repository"`
	BaseRef            string        `json:"base_ref"`
	Branch             string        `json:"branch"`
	Title              string        `json:"title"`
	AllowedPaths       []string      `json:"allowed_paths"`
	AcceptanceIDs      []string      `json:"acceptance_ids"`
	RequiredChecks     []string      `json:"required_checks"`
	SourceArtifactID   string        `json:"source_artifact_id"`
	SourceArtifactHash string        `json:"source_artifact_hash"`
	Permissions        PermissionSet `json:"permissions"`
	CreatedAt          time.Time     `json:"created_at"`
	Hash               string        `json:"hash"`
}

type SessionRequest struct {
	Manifest TaskManifest `json:"manifest"`
	Prompt   string       `json:"prompt"`
}

type Session struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
}

type Evidence struct {
	ID           string          `json:"id"`
	DispatchID   string          `json:"dispatch_id"`
	ManifestHash string          `json:"manifest_hash"`
	Provider     string          `json:"provider"`
	SessionID    string          `json:"session_id"`
	Kind         string          `json:"kind"`
	CommitSHA    string          `json:"commit_sha,omitempty"`
	Command      string          `json:"command,omitempty"`
	ExitCode     *int            `json:"exit_code,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

func (m *TaskManifest) Seal() error {
	if err := m.Validate(); err != nil {
		return err
	}
	m.Hash = ""
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	m.Hash = hex.EncodeToString(sum[:])
	return nil
}

func (m TaskManifest) Verify() error {
	expected := m.Hash
	if expected == "" {
		return errors.New("manifest hash is required")
	}
	if err := m.Seal(); err != nil {
		return err
	}
	if m.Hash != expected {
		return errors.New("manifest content does not match its hash")
	}
	return nil
}

func (m TaskManifest) Validate() error {
	if m.ID == "" || m.DispatchID == "" || m.WorkItemID == "" || m.WorkflowID == "" {
		return errors.New("manifest identity fields are required")
	}
	if m.Version < 1 || strings.TrimSpace(m.Provider) == "" || strings.TrimSpace(m.Repository) == "" {
		return errors.New("manifest version, provider, and repository are required")
	}
	if m.BaseRef == "" || m.Branch == "" || m.SourceArtifactID == "" || m.SourceArtifactHash == "" {
		return errors.New("manifest must be bound to source design and git refs")
	}
	if len(m.AllowedPaths) == 0 {
		return errors.New("manifest requires at least one allowed path")
	}
	if m.Permissions.Merge || m.Permissions.Deploy {
		return errors.New("coding agents cannot receive merge or deploy permission")
	}
	return nil
}

func (e Evidence) Validate(manifest TaskManifest) error {
	if e.DispatchID != manifest.DispatchID || e.ManifestHash != manifest.Hash {
		return errors.New("evidence is not bound to the active dispatch manifest")
	}
	if e.Provider != manifest.Provider || e.SessionID == "" {
		return errors.New("evidence provider and session are required")
	}
	switch e.Kind {
	case "SESSION_STARTED", "TOOL_CALL", "FILE_CHANGE", "CHECK_RESULT", "COMMIT", "DRAFT_MR", "SESSION_COMPLETED", "SESSION_FAILED":
	default:
		return fmt.Errorf("unsupported evidence kind %q", e.Kind)
	}
	if e.Kind == "CHECK_RESULT" && (e.Command == "" || e.ExitCode == nil) {
		return errors.New("check evidence requires command and exit code")
	}
	if (e.Kind == "COMMIT" || e.Kind == "DRAFT_MR") && e.CommitSHA == "" {
		return errors.New("commit-bound evidence requires commit_sha")
	}
	return nil
}
