package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/codingagent"
	"github.com/google/uuid"
)

const codingFlowPolicyVersion = "coding-multi-agent-v1"

func (s *Store) EnsureCodingSession(ctx context.Context, dispatchID string) (codingagent.CodingSession, error) {
	manifest, err := s.CodingTaskManifest(ctx, dispatchID)
	if err != nil {
		return codingagent.CodingSession{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return codingagent.CodingSession{}, err
	}
	defer tx.Rollback()
	session, err := scanCodingSession(tx.QueryRowContext(ctx, `SELECT id,dispatch_id,manifest_hash,state,head_commit,
		repair_attempts,max_repairs,revision,created_at,updated_at FROM coding_sessions WHERE dispatch_id=$1 FOR UPDATE`, dispatchID))
	if err == nil {
		if session.ManifestHash != manifest.Hash {
			return session, errors.New("coding session is bound to a different manifest")
		}
		return session, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return session, err
	}
	now := time.Now().UTC()
	session = codingagent.CodingSession{ID: uuid.NewString(), DispatchID: dispatchID, ManifestHash: manifest.Hash,
		State: codingagent.StatePlanning, MaxRepairs: 3, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO coding_sessions
		(id,dispatch_id,manifest_hash,state,max_repairs,revision,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7)`,
		session.ID, session.DispatchID, session.ManifestHash, session.State, session.MaxRepairs, session.Revision, now); err != nil {
		return session, err
	}
	if _, err := createCodingRun(ctx, tx, session, codingagent.RolePlanner); err != nil {
		return session, err
	}
	if err := recordCodingTransition(ctx, tx, session, "", codingagent.StatePlanning, "", "coding session created"); err != nil {
		return session, err
	}
	return session, tx.Commit()
}

func (s *Store) GetCodingSession(ctx context.Context, sessionID string) (codingagent.CodingSession, error) {
	return scanCodingSession(s.db.QueryRowContext(ctx, `SELECT id,dispatch_id,manifest_hash,state,head_commit,
		repair_attempts,max_repairs,revision,created_at,updated_at FROM coding_sessions WHERE id=$1`, sessionID))
}

func scanCodingSession(row rowScanner) (codingagent.CodingSession, error) {
	var value codingagent.CodingSession
	if err := row.Scan(&value.ID, &value.DispatchID, &value.ManifestHash, &value.State, &value.HeadCommit,
		&value.RepairAttempts, &value.MaxRepairs, &value.Revision, &value.CreatedAt, &value.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return value, ErrNotFound
		}
		return value, err
	}
	return value, nil
}

func (s *Store) PendingCodingRuns(ctx context.Context, sessionID string) ([]codingagent.AgentRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,role,status,attempt,manifest_hash,base_commit,head_commit,
		provider_session_id,input_json,input_hash,created_at FROM coding_agent_runs
		WHERE session_id=$1 AND status='PENDING' ORDER BY created_at,role`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []codingagent.AgentRun{}
	for rows.Next() {
		var value codingagent.AgentRun
		if err := rows.Scan(&value.ID, &value.SessionID, &value.Role, &value.Status, &value.Attempt,
			&value.ManifestHash, &value.BaseCommit, &value.HeadCommit, &value.ProviderSessionID,
			&value.Input, &value.InputHash, &value.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) StartCodingAgentRun(ctx context.Context, runID, providerSessionID string) (codingagent.AgentRun, error) {
	if providerSessionID == "" {
		return codingagent.AgentRun{}, errors.New("provider session id is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return codingagent.AgentRun{}, err
	}
	defer tx.Rollback()
	run, err := scanCodingRun(tx.QueryRowContext(ctx, codingRunSelect+` WHERE id=$1 FOR UPDATE`, runID))
	if err != nil {
		return run, err
	}
	if run.Status == codingagent.RunRunning && run.ProviderSessionID == providerSessionID {
		return run, tx.Commit()
	}
	if run.Status != codingagent.RunPending {
		return run, fmt.Errorf("coding Agent Run is %s, not PENDING", run.Status)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE coding_agent_runs SET status='RUNNING',provider_session_id=$2,
		started_at=CURRENT_TIMESTAMP WHERE id=$1`, runID, providerSessionID); err != nil {
		return run, err
	}
	run.Status, run.ProviderSessionID = codingagent.RunRunning, providerSessionID
	return run, tx.Commit()
}

const codingRunSelect = `SELECT id,session_id,role,status,attempt,manifest_hash,base_commit,head_commit,
	provider_session_id,input_json,input_hash,created_at FROM coding_agent_runs`

func scanCodingRun(row rowScanner) (codingagent.AgentRun, error) {
	var value codingagent.AgentRun
	if err := row.Scan(&value.ID, &value.SessionID, &value.Role, &value.Status, &value.Attempt,
		&value.ManifestHash, &value.BaseCommit, &value.HeadCommit, &value.ProviderSessionID,
		&value.Input, &value.InputHash, &value.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return value, ErrNotFound
		}
		return value, err
	}
	return value, nil
}

func (s *Store) CompleteCodingAgentRun(ctx context.Context, runID string, completion codingagent.RunCompletion) (codingagent.CodingSession, error) {
	if !json.Valid(completion.Output) || completion.ArtifactType == "" {
		return codingagent.CodingSession{}, errors.New("a valid structured output artifact is required")
	}
	var outputObject map[string]any
	if err := json.Unmarshal(completion.Output, &outputObject); err != nil || outputObject == nil {
		return codingagent.CodingSession{}, errors.New("output artifact must be a JSON object")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return codingagent.CodingSession{}, err
	}
	defer tx.Rollback()
	run, err := scanCodingRun(tx.QueryRowContext(ctx, codingRunSelect+` WHERE id=$1 FOR UPDATE`, runID))
	if err != nil {
		return codingagent.CodingSession{}, err
	}
	session, err := scanCodingSession(tx.QueryRowContext(ctx, `SELECT id,dispatch_id,manifest_hash,state,head_commit,
		repair_attempts,max_repairs,revision,created_at,updated_at FROM coding_sessions WHERE id=$1 FOR UPDATE`, run.SessionID))
	if err != nil {
		return session, err
	}
	if run.Status == codingagent.RunCompleted {
		return session, tx.Commit()
	}
	if run.Status != codingagent.RunRunning {
		return session, fmt.Errorf("coding Agent Run is %s, not RUNNING", run.Status)
	}
	if completion.ProviderSessionID != run.ProviderSessionID || completion.ManifestHash != run.ManifestHash || run.ManifestHash != session.ManifestHash {
		return session, errors.New("completion is not bound to the active run and manifest")
	}
	if (run.Role == codingagent.RoleReviewer || run.Role == codingagent.RoleSecurity || run.Role == codingagent.RoleJudge || run.Role == codingagent.RoleQuality) &&
		completion.HeadCommit != session.HeadCommit {
		if _, updateErr := tx.ExecContext(ctx, `UPDATE coding_agent_runs SET status='STALE',finished_at=CURRENT_TIMESTAMP,
			error_summary='result commit does not match current session head' WHERE id=$1`, run.ID); updateErr != nil {
			return session, updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return session, commitErr
		}
		return session, errors.New("review result is stale because its commit does not match the session head")
	}
	if err := validateCodingCompletion(run, session, completion); err != nil {
		return session, err
	}
	outputHash := contentHash(completion.Output)
	artifactID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO coding_artifacts
		(id,session_id,producer_run_id,artifact_type,base_commit,head_commit,content_json,content_hash)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, artifactID, session.ID, run.ID, completion.ArtifactType,
		completion.BaseCommit, completion.HeadCommit, completion.Output, outputHash); err != nil {
		return session, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE coding_agent_runs SET status='COMPLETED',base_commit=$2,head_commit=$3,
		output_hash=$4,decision=$5,blocking_findings=$6,high_risk=$7,checks_passed=$8,finished_at=CURRENT_TIMESTAMP WHERE id=$1`,
		run.ID, completion.BaseCommit, completion.HeadCommit, outputHash, completion.Decision,
		completion.BlockingFindings, completion.HighRisk, completion.ChecksPassed); err != nil {
		return session, err
	}
	previousHead := session.HeadCommit
	if run.Role == codingagent.RoleCoder || run.Role == codingagent.RoleRepair {
		session.HeadCommit = completion.HeadCommit
	}
	if run.Role == codingagent.RoleRepair && previousHead != session.HeadCommit {
		if _, err := tx.ExecContext(ctx, `UPDATE coding_agent_runs SET status='STALE',
			error_summary='superseded by repaired commit' WHERE session_id=$1 AND head_commit=$2
			AND status='COMPLETED' AND role IN ('REVIEWER','SECURITY_RELIABILITY','JUDGE','QUALITY')`,
			session.ID, previousHead); err != nil {
			return session, err
		}
	}
	if run.Role == codingagent.RoleJudge {
		var reviewBlockers int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(blocking_findings),0) FROM coding_agent_runs
			WHERE session_id=$1 AND head_commit=$2 AND attempt=$3 AND status='COMPLETED'
			AND role IN ('REVIEWER','SECURITY_RELIABILITY')`, session.ID, session.HeadCommit, run.Attempt).Scan(&reviewBlockers); err != nil {
			return session, err
		}
		if reviewBlockers > completion.BlockingFindings {
			completion.BlockingFindings = reviewBlockers
		}
	}
	plan, err := codingagent.NextTransition(session.State, run.Role, completion, session.RepairAttempts, session.MaxRepairs)
	if err != nil {
		return session, err
	}
	if (run.Role == codingagent.RoleReviewer || run.Role == codingagent.RoleSecurity) && plan.ToState != codingagent.StateWaitingHuman {
		joined, err := codingReviewJoined(ctx, tx, session.ID, session.HeadCommit, run.Attempt)
		if err != nil {
			return session, err
		}
		if !joined {
			return session, tx.Commit()
		}
		if _, err := createCodingRun(ctx, tx, session, codingagent.RoleJudge); err != nil {
			return session, err
		}
		if err := recordCodingTransition(ctx, tx, session, session.State, session.State, run.ID, "independent reviews joined"); err != nil {
			return session, err
		}
		return session, tx.Commit()
	}
	from := session.State
	if plan.IncrementRepair {
		session.RepairAttempts++
	}
	session.State = plan.ToState
	session.Revision++
	session.UpdatedAt = time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE coding_sessions SET state=$2,head_commit=$3,repair_attempts=$4,
		revision=$5,updated_at=$6 WHERE id=$1`, session.ID, session.State, session.HeadCommit,
		session.RepairAttempts, session.Revision, session.UpdatedAt); err != nil {
		return session, err
	}
	if session.State == codingagent.StateWaitingHuman {
		if _, err := tx.ExecContext(ctx, `UPDATE coding_agent_runs SET status='CANCELLED',finished_at=CURRENT_TIMESTAMP,
			error_summary='session escalated to human review' WHERE session_id=$1 AND id<>$2
			AND status IN ('PENDING','RUNNING')`, session.ID, run.ID); err != nil {
			return session, err
		}
	}
	for _, role := range plan.NextRoles {
		if _, err := createCodingRun(ctx, tx, session, role); err != nil {
			return session, err
		}
	}
	if err := recordCodingTransition(ctx, tx, session, from, session.State, run.ID, plan.Reason); err != nil {
		return session, err
	}
	return session, tx.Commit()
}

func (s *Store) FailCodingAgentRun(ctx context.Context, runID string, failure codingagent.RunFailure) (codingagent.CodingSession, error) {
	if err := failure.Validate(); err != nil {
		return codingagent.CodingSession{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return codingagent.CodingSession{}, err
	}
	defer tx.Rollback()
	run, err := scanCodingRun(tx.QueryRowContext(ctx, codingRunSelect+` WHERE id=$1 FOR UPDATE`, runID))
	if err != nil {
		return codingagent.CodingSession{}, err
	}
	session, err := scanCodingSession(tx.QueryRowContext(ctx, `SELECT id,dispatch_id,manifest_hash,state,head_commit,
		repair_attempts,max_repairs,revision,created_at,updated_at FROM coding_sessions WHERE id=$1 FOR UPDATE`, run.SessionID))
	if err != nil {
		return session, err
	}
	if run.Status != codingagent.RunRunning || failure.ProviderSessionID != run.ProviderSessionID ||
		failure.ManifestHash != session.ManifestHash || run.ManifestHash != session.ManifestHash {
		return session, errors.New("failure is not bound to the active run and manifest")
	}
	runStatus, target := codingagent.RunFailed, codingagent.StateFailed
	if failure.Code == "CANCELLED" {
		runStatus, target = codingagent.RunCancelled, codingagent.StateCancelled
	}
	if _, err := tx.ExecContext(ctx, `UPDATE coding_agent_runs SET status=$2,error_summary=$3,
		finished_at=CURRENT_TIMESTAMP WHERE id=$1`, run.ID, runStatus, failure.Code); err != nil {
		return session, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE coding_agent_runs SET status='CANCELLED',error_summary='peer run failed',
		finished_at=CURRENT_TIMESTAMP WHERE session_id=$1 AND id<>$2 AND status IN ('PENDING','RUNNING')`, session.ID, run.ID); err != nil {
		return session, err
	}
	from := session.State
	session.State, session.Revision, session.UpdatedAt = target, session.Revision+1, time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE coding_sessions SET state=$2,revision=$3,updated_at=$4 WHERE id=$1`,
		session.ID, session.State, session.Revision, session.UpdatedAt); err != nil {
		return session, err
	}
	if err := recordCodingTransition(ctx, tx, session, from, target, run.ID, "agent run "+failure.Code); err != nil {
		return session, err
	}
	return session, tx.Commit()
}

func validateCodingCompletion(run codingagent.AgentRun, session codingagent.CodingSession, value codingagent.RunCompletion) error {
	expected := map[codingagent.Role]string{codingagent.RolePlanner: "IMPLEMENTATION_PLAN", codingagent.RoleTest: "TEST_PLAN",
		codingagent.RoleCoder: "CANDIDATE_COMMIT", codingagent.RoleReviewer: "REVIEW_REPORT",
		codingagent.RoleSecurity: "SECURITY_REPORT", codingagent.RoleJudge: "REVIEW_DECISION",
		codingagent.RoleRepair: "REPAIR_RESULT", codingagent.RoleQuality: "QUALITY_REPORT"}
	if value.ArtifactType != expected[run.Role] {
		return fmt.Errorf("role %s must produce %s", run.Role, expected[run.Role])
	}
	if run.Role == codingagent.RoleCoder && (!codingagent.ValidCommitSHA(value.BaseCommit) || !codingagent.ValidCommitSHA(value.HeadCommit) || value.BaseCommit == value.HeadCommit) {
		return errors.New("Coder must bind a new exact head commit to an exact base commit")
	}
	if run.Role == codingagent.RoleRepair && (!codingagent.ValidCommitSHA(value.HeadCommit) || value.BaseCommit != session.HeadCommit || value.HeadCommit == value.BaseCommit) {
		return errors.New("Repair must produce a new commit from the current exact head")
	}
	if run.Role == codingagent.RoleReviewer || run.Role == codingagent.RoleSecurity || run.Role == codingagent.RoleJudge || run.Role == codingagent.RoleQuality {
		if session.HeadCommit == "" || value.HeadCommit != session.HeadCommit {
			return errors.New("review result is stale because its commit does not match the session head")
		}
	}
	return nil
}

func codingReviewJoined(ctx context.Context, tx *sql.Tx, sessionID, head string, attempt int) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT role) FROM coding_agent_runs
		WHERE session_id=$1 AND head_commit=$2 AND attempt=$3 AND status='COMPLETED'
		AND role IN ('REVIEWER','SECURITY_RELIABILITY')`, sessionID, head, attempt).Scan(&count)
	return count == 2, err
}

func createCodingRun(ctx context.Context, tx *sql.Tx, session codingagent.CodingSession, role codingagent.Role) (codingagent.AgentRun, error) {
	if err := codingagent.ValidateRole(role); err != nil {
		return codingagent.AgentRun{}, err
	}
	attempt := 1
	if role == codingagent.RoleReviewer || role == codingagent.RoleSecurity || role == codingagent.RoleJudge || role == codingagent.RoleQuality {
		attempt = session.RepairAttempts + 1
	} else if role == codingagent.RoleRepair {
		attempt = session.RepairAttempts
	}
	type artifactRef struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Hash       string `json:"hash"`
		HeadCommit string `json:"head_commit,omitempty"`
	}
	refs := []artifactRef{}
	rows, err := tx.QueryContext(ctx, `SELECT id,artifact_type,content_hash,head_commit FROM coding_artifacts
		WHERE session_id=$1 AND (head_commit='' OR head_commit=$2) ORDER BY created_at`, session.ID, session.HeadCommit)
	if err != nil {
		return codingagent.AgentRun{}, err
	}
	for rows.Next() {
		var ref artifactRef
		if err := rows.Scan(&ref.ID, &ref.Type, &ref.Hash, &ref.HeadCommit); err != nil {
			rows.Close()
			return codingagent.AgentRun{}, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Close(); err != nil {
		return codingagent.AgentRun{}, err
	}
	input, _ := json.Marshal(map[string]any{"session_id": session.ID, "manifest_hash": session.ManifestHash,
		"role": role, "head_commit": session.HeadCommit, "artifacts": refs, "tool_policy": codingagent.PolicyForRole(role)})
	run := codingagent.AgentRun{ID: uuid.NewString(), SessionID: session.ID, Role: role, Status: codingagent.RunPending,
		Attempt: attempt, ManifestHash: session.ManifestHash, HeadCommit: session.HeadCommit, Input: input,
		InputHash: contentHash(input), CreatedAt: time.Now().UTC()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO coding_agent_runs
		(id,session_id,role,status,attempt,manifest_hash,head_commit,input_json,input_hash,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, run.ID, run.SessionID, run.Role, run.Status, run.Attempt,
		run.ManifestHash, run.HeadCommit, run.Input, run.InputHash, run.CreatedAt); err != nil {
		return run, err
	}
	for _, ref := range refs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO coding_run_inputs(consumer_run_id,artifact_id) VALUES($1,$2)`, run.ID, ref.ID); err != nil {
			return run, err
		}
	}
	return run, nil
}

func recordCodingTransition(ctx context.Context, tx *sql.Tx, session codingagent.CodingSession,
	from, to codingagent.CodingState, triggerRunID, reason string) error {
	var trigger any
	if triggerRunID != "" {
		trigger = triggerRunID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO coding_stage_transitions
		(id,session_id,from_state,to_state,trigger_run_id,manifest_hash,head_commit,reason,policy_version)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, uuid.NewString(), session.ID, from, to, trigger,
		session.ManifestHash, session.HeadCommit, reason, codingFlowPolicyVersion)
	return err
}

func contentHash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
