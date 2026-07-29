package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"sort"
	"strings"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

func Open(databaseURL string) (*Store, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) Migrate(ctx context.Context) error {
	const migrationLockKey int64 = 621957352640782655
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open migration lock connection: %w", err)
	}
	defer conn.Close()
	if err := acquireMigrationLock(ctx, conn, migrationLockKey, 30*time.Second); err != nil {
		return err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockKey)

	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version VARCHAR(128) COLLATE "C" PRIMARY KEY,
		applied_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return err
	}
	entries, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)
	for _, name := range entries {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=$1`, version).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			continue
		}
		content, err := migrationFiles.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, statement := range splitStatements(string(content)) {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %s: %w", version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func acquireMigrationLock(ctx context.Context, conn *sql.Conn, key int64, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		var locked bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if locked {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("migration lock was not acquired within 30 seconds")
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func splitStatements(source string) []string {
	var result []string
	for _, part := range strings.Split(source, ";") {
		value := strings.TrimSpace(part)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func (s *Store) EnqueueEvent(ctx context.Context, dedupeKey, eventType string, payload any, availableAt time.Time) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO event_queue
		(dedupe_key,event_type,payload_json,available_at,last_error)
		VALUES ($1,$2,$3,$4, '') ON CONFLICT (dedupe_key) DO NOTHING`,
		dedupeKey, eventType, string(raw), availableAt.UTC())
	return err
}

func (s *Store) ClaimEvent(ctx context.Context, workerID string, lease time.Duration) (*domain.QueueEvent, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE event_queue SET status='READY',locked_by='',lease_until=NULL
		WHERE status='PROCESSING' AND lease_until < CURRENT_TIMESTAMP`); err != nil {
		return nil, err
	}
	row := tx.QueryRowContext(ctx, `SELECT id,dedupe_key,event_type,payload_json,attempts,available_at,lease_until
		FROM event_queue WHERE status='READY' AND available_at <= CURRENT_TIMESTAMP
		ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`)
	var event domain.QueueEvent
	var leaseUntil sql.NullTime
	if err := row.Scan(&event.ID, &event.DedupeKey, &event.Type, &event.Payload, &event.Attempts, &event.AvailableAt, &leaseUntil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	nextLease := time.Now().UTC().Add(lease)
	if _, err := tx.ExecContext(ctx, `UPDATE event_queue SET status='PROCESSING',locked_by=$1,lease_until=$2,attempts=attempts+1 WHERE id=$3`,
		workerID, nextLease, event.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	event.Attempts++
	event.LeaseUntil = &nextLease
	return &event, nil
}

func (s *Store) CompleteEvent(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE event_queue SET status='DONE',processed_at=CURRENT_TIMESTAMP,locked_by='',lease_until=NULL WHERE id=$1`, id)
	return err
}

func (s *Store) RetryEvent(ctx context.Context, id int64, attempts, maxAttempts int, cause error) error {
	status := "READY"
	if attempts >= maxAttempts {
		status = "DEAD"
	}
	delay := retryDelay(attempts)
	_, err := s.db.ExecContext(ctx, `UPDATE event_queue SET status=$1,available_at=$2,locked_by='',lease_until=NULL,last_error=$3 WHERE id=$4`,
		status, time.Now().UTC().Add(delay), redactError(cause), id)
	return err
}

func (s *Store) EnqueueOutbox(ctx context.Context, dedupeKey, messageType string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO outbox_messages
		(dedupe_key,message_type,payload_json,last_error)
		VALUES ($1,$2,$3, '') ON CONFLICT (dedupe_key) DO NOTHING`,
		dedupeKey, messageType, string(raw))
	return err
}

func enqueueOutboxTx(ctx context.Context, tx *sql.Tx, message domain.OutboxMessage) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO outbox_messages
		(dedupe_key,message_type,payload_json,last_error)
		VALUES ($1,$2,$3, '') ON CONFLICT (dedupe_key) DO NOTHING`,
		message.DedupeKey, message.Type, string(message.Payload))
	return err
}

func (s *Store) ClaimOutbox(ctx context.Context, workerID string, lease time.Duration) (*domain.OutboxMessage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE outbox_messages SET status='READY',locked_by='',lease_until=NULL
		WHERE status='PROCESSING' AND lease_until < CURRENT_TIMESTAMP`); err != nil {
		return nil, err
	}
	row := tx.QueryRowContext(ctx, `SELECT id,dedupe_key,message_type,payload_json,attempts,available_at,lease_until
		FROM outbox_messages WHERE status='READY' AND available_at <= CURRENT_TIMESTAMP
		ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`)
	var message domain.OutboxMessage
	var leaseUntil sql.NullTime
	if err := row.Scan(&message.ID, &message.DedupeKey, &message.Type, &message.Payload, &message.Attempts, &message.AvailableAt, &leaseUntil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	nextLease := time.Now().UTC().Add(lease)
	if _, err := tx.ExecContext(ctx, `UPDATE outbox_messages SET status='PROCESSING',locked_by=$1,lease_until=$2,attempts=attempts+1 WHERE id=$3`,
		workerID, nextLease, message.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	message.Attempts++
	message.LeaseUntil = &nextLease
	return &message, nil
}

func (s *Store) CompleteOutbox(ctx context.Context, id int64, externalID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbox_messages SET status='DONE',delivered_at=CURRENT_TIMESTAMP,
		external_id=$1,locked_by='',lease_until=NULL WHERE id=$2`, externalID, id)
	return err
}

func (s *Store) RetryOutbox(ctx context.Context, id int64, attempts, maxAttempts int, cause error) error {
	status := "READY"
	if attempts >= maxAttempts {
		status = "DEAD"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE outbox_messages SET status=$1,available_at=$2,locked_by='',lease_until=NULL,last_error=$3 WHERE id=$4`,
		status, time.Now().UTC().Add(retryDelay(attempts)), redactError(cause), id)
	return err
}

func retryDelay(attempt int) time.Duration {
	seconds := math.Min(300, math.Pow(2, float64(max(attempt, 1))))
	return time.Duration(seconds) * time.Second
}

func redactError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 1500 {
		value = value[:1500]
	}
	return value
}

func (s *Store) GetOrCreateWorkflow(ctx context.Context, workflow domain.Workflow) (domain.Workflow, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return domain.Workflow{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO workflows
		(id,gitlab_project_id,issue_iid,issue_title,state,source_hash,revision)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (gitlab_project_id,issue_iid) DO UPDATE
		SET issue_title=EXCLUDED.issue_title,updated_at=CURRENT_TIMESTAMP`,
		workflow.ID, workflow.GitLabProjectID, workflow.IssueIID, workflow.IssueTitle,
		workflow.State, workflow.SourceHash, workflow.Revision)
	if err != nil {
		return domain.Workflow{}, err
	}
	got, err := scanWorkflow(tx.QueryRowContext(ctx, `SELECT id,gitlab_project_id,issue_iid,issue_title,state,source_hash,revision,created_at,updated_at
		FROM workflows WHERE gitlab_project_id=$1 AND issue_iid=$2 FOR UPDATE`, workflow.GitLabProjectID, workflow.IssueIID))
	if err != nil {
		return domain.Workflow{}, err
	}
	if got.ID == workflow.ID {
		details, _ := json.Marshal(map[string]any{"state": got.State})
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(workflow_id,event_type,actor_id,details_json)
			VALUES ($1, 'workflow.created', 0, $2)`, got.ID, string(details)); err != nil {
			return domain.Workflow{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Workflow{}, err
	}
	return got, nil
}

func (s *Store) GetWorkflowByIssue(ctx context.Context, projectID, issueIID int64) (domain.Workflow, error) {
	return scanWorkflow(s.db.QueryRowContext(ctx, `SELECT id,gitlab_project_id,issue_iid,issue_title,state,source_hash,revision,created_at,updated_at
		FROM workflows WHERE gitlab_project_id=$1 AND issue_iid=$2`, projectID, issueIID))
}

func (s *Store) GetWorkflow(ctx context.Context, id string) (domain.Workflow, error) {
	return scanWorkflow(s.db.QueryRowContext(ctx, `SELECT id,gitlab_project_id,issue_iid,issue_title,state,source_hash,revision,created_at,updated_at
		FROM workflows WHERE id=$1`, id))
}

type rowScanner interface{ Scan(...any) error }

func scanWorkflow(row rowScanner) (domain.Workflow, error) {
	var value domain.Workflow
	if err := row.Scan(&value.ID, &value.GitLabProjectID, &value.IssueIID, &value.IssueTitle,
		&value.State, &value.SourceHash, &value.Revision, &value.CreatedAt, &value.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Workflow{}, ErrNotFound
		}
		return domain.Workflow{}, err
	}
	return value, nil
}

func (s *Store) Transition(ctx context.Context, workflowID string, to domain.State, reason string, details any) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from domain.State
	if err := tx.QueryRowContext(ctx, `SELECT state FROM workflows WHERE id=$1 FOR UPDATE`, workflowID).Scan(&from); err != nil {
		return err
	}
	if err := domain.ValidateTransition(from, to); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflows SET state=$1,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=$2`, to, workflowID); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"from": from, "to": to, "reason": reason, "details": details})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(workflow_id,event_type,actor_id,details_json)
		VALUES ($1, 'workflow.transition', 0, $2)`, workflowID, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveSnapshot(ctx context.Context, snapshot domain.Snapshot) error {
	images, err := json.Marshal(snapshot.Images)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO source_snapshots
		(id,workflow_id,confluence_page_id,source_version,title,source_url,source_updated_at,
		 content_hash,normalized_text,raw_storage,images_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (workflow_id,confluence_page_id,source_version,content_hash) DO NOTHING`,
		snapshot.ID, snapshot.WorkflowID, snapshot.ConfluencePageID, snapshot.Version, snapshot.Title,
		snapshot.URL, snapshot.UpdatedAt, snapshot.ContentHash, snapshot.NormalizedText,
		snapshot.RawStorage, string(images))
	return err
}

func (s *Store) LatestSnapshots(ctx context.Context, workflowID string) ([]domain.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id,s.workflow_id,s.confluence_page_id,s.source_version,s.title,
		s.source_url,s.source_updated_at,s.content_hash,s.normalized_text,s.raw_storage,s.images_json,s.created_at
		FROM source_snapshots s
		JOIN (SELECT confluence_page_id,MAX(created_at) created_at FROM source_snapshots
			WHERE workflow_id=$1 GROUP BY confluence_page_id) latest
		ON latest.confluence_page_id=s.confluence_page_id AND latest.created_at=s.created_at
		WHERE s.workflow_id=$2 ORDER BY s.created_at`, workflowID, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Snapshot
	for rows.Next() {
		var value domain.Snapshot
		var images []byte
		if err := rows.Scan(&value.ID, &value.WorkflowID, &value.ConfluencePageID, &value.Version,
			&value.Title, &value.URL, &value.UpdatedAt, &value.ContentHash, &value.NormalizedText,
			&value.RawStorage, &images, &value.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(images, &value.Images); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) PublishGate(ctx context.Context, workflow domain.Workflow, artifact domain.Artifact, gate domain.Gate, state domain.State, note domain.OutboxMessage) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current domain.State
	if err := tx.QueryRowContext(ctx, `SELECT state FROM workflows WHERE id=$1 FOR UPDATE`, workflow.ID).Scan(&current); err != nil {
		return err
	}
	if err := domain.ValidateTransition(current, state); err != nil {
		return err
	}
	supersedeQuery := `UPDATE gates SET status='SUPERSEDED' WHERE workflow_id=$1 AND gate_type=$2 AND status='OPEN'`
	supersedeArgs := []any{workflow.ID, gate.Type}
	if gate.Type == domain.GateRequirement {
		// Any source/requirement re-review invalidates all downstream approvals and open Gates.
		supersedeQuery = `UPDATE gates SET status='SUPERSEDED' WHERE workflow_id=$1 AND status='OPEN'`
		supersedeArgs = []any{workflow.ID}
	}
	if _, err := tx.ExecContext(ctx, supersedeQuery, supersedeArgs...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts
		(id,workflow_id,artifact_type,artifact_version,source_hash,content_json,markdown,model,prompt_version,generated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, artifact.ID, artifact.WorkflowID, artifact.Type, artifact.Version,
		artifact.SourceHash, string(artifact.Content), artifact.Markdown, artifact.Model, artifact.Prompt, artifact.GeneratedAt); err != nil {
		return err
	}
	reviewers, _ := json.Marshal(gate.ReviewerIDs)
	if _, err := tx.ExecContext(ctx, `INSERT INTO gates
		(id,workflow_id,gate_type,status,artifact_id,revision,reviewer_ids,opened_at,feedback)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8, '')`, gate.ID, gate.WorkflowID, gate.Type, gate.Status,
		gate.ArtifactID, gate.Revision, string(reviewers), gate.OpenedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflows SET state=$1,source_hash=$2,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=$3`,
		state, artifact.SourceHash, workflow.ID); err != nil {
		return err
	}
	audit, _ := json.Marshal(map[string]any{"gate_id": gate.ID, "gate_type": gate.Type, "artifact_id": artifact.ID})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(workflow_id,event_type,actor_id,details_json)
		VALUES ($1, 'gate.opened', 0, $2)`, workflow.ID, string(audit)); err != nil {
		return err
	}
	if err := enqueueOutboxTx(ctx, tx, note); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PublishPlanningGates(ctx context.Context, workflow domain.Workflow, artifacts []domain.Artifact, gates []domain.Gate, notes []domain.OutboxMessage) error {
	if len(artifacts) != 2 || len(gates) != 2 || len(notes) != 2 {
		return errors.New("planning publication requires exactly PRD and TEST artifacts, gates, and notes")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current domain.State
	if err := tx.QueryRowContext(ctx, `SELECT state FROM workflows WHERE id=$1 FOR UPDATE`, workflow.ID).Scan(&current); err != nil {
		return err
	}
	if err := domain.ValidateTransition(current, domain.StateWaitingPRDAndTestReview); err != nil {
		return err
	}
	for index, artifact := range artifacts {
		gate := gates[index]
		if _, err := tx.ExecContext(ctx, `UPDATE gates SET status='SUPERSEDED' WHERE workflow_id=$1 AND gate_type=$2 AND status='OPEN'`,
			workflow.ID, gate.Type); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts
			(id,workflow_id,artifact_type,artifact_version,source_hash,content_json,markdown,model,prompt_version,generated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, artifact.ID, artifact.WorkflowID, artifact.Type, artifact.Version,
			artifact.SourceHash, string(artifact.Content), artifact.Markdown, artifact.Model, artifact.Prompt, artifact.GeneratedAt); err != nil {
			return err
		}
		reviewers, _ := json.Marshal(gate.ReviewerIDs)
		if _, err := tx.ExecContext(ctx, `INSERT INTO gates
			(id,workflow_id,gate_type,status,artifact_id,revision,reviewer_ids,opened_at,feedback)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8, '')`, gate.ID, gate.WorkflowID, gate.Type, gate.Status,
			gate.ArtifactID, gate.Revision, string(reviewers), gate.OpenedAt); err != nil {
			return err
		}
		audit, _ := json.Marshal(map[string]any{"gate_id": gate.ID, "gate_type": gate.Type, "artifact_id": artifact.ID})
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(workflow_id,event_type,actor_id,details_json)
			VALUES ($1, 'gate.opened', 0, $2)`, workflow.ID, string(audit)); err != nil {
			return err
		}
		if err := enqueueOutboxTx(ctx, tx, notes[index]); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflows SET state=$1,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=$2`,
		domain.StateWaitingPRDAndTestReview, workflow.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PendingOutboxPrefix(ctx context.Context, prefix string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_messages
		WHERE dedupe_key LIKE $1 || '%' AND status NOT IN ('DONE','DEAD')`, prefix).Scan(&count)
	return count, err
}

func (s *Store) GetGate(ctx context.Context, gateID string) (domain.Gate, error) {
	var gate domain.Gate
	var reviewers []byte
	var decided sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT id,workflow_id,gate_type,status,artifact_id,revision,reviewer_ids,
		opened_at,decided_at,decision_actor,feedback FROM gates WHERE id=$1`, gateID).
		Scan(&gate.ID, &gate.WorkflowID, &gate.Type, &gate.Status, &gate.ArtifactID, &gate.Revision,
			&reviewers, &gate.OpenedAt, &decided, &gate.DecisionActor, &gate.Feedback)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Gate{}, ErrNotFound
	}
	if err != nil {
		return domain.Gate{}, err
	}
	if decided.Valid {
		gate.DecidedAt = &decided.Time
	}
	if err := json.Unmarshal(reviewers, &gate.ReviewerIDs); err != nil {
		return domain.Gate{}, err
	}
	return gate, nil
}

func (s *Store) DecideGate(ctx context.Context, gate domain.Gate, action domain.GateAction, actorID int64, username, feedback string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status domain.GateStatus
	if err := tx.QueryRowContext(ctx, `SELECT status FROM gates WHERE id=$1 FOR UPDATE`, gate.ID).Scan(&status); err != nil {
		return err
	}
	if status != domain.GateOpen {
		return fmt.Errorf("gate is %s, not OPEN", status)
	}
	next := domain.GateApproved
	switch action {
	case domain.ActionRequestChanges:
		next = domain.GateChangesRequested
	case domain.ActionReject:
		next = domain.GateRejected
	case domain.ActionApprove:
	default:
		return fmt.Errorf("unsupported gate action %q", action)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gates SET status=$1,decided_at=CURRENT_TIMESTAMP,decision_actor=$2,feedback=$3 WHERE id=$4`,
		next, actorID, feedback, gate.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO gate_decisions(gate_id,action,actor_id,actor_username,feedback)
		VALUES ($1,$2,$3,$4,$5)`, gate.ID, action, actorID, username, feedback); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"gate_id": gate.ID, "gate_type": gate.Type, "action": action, "feedback": feedback})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(workflow_id,event_type,actor_id,details_json)
		VALUES ($1, 'gate.decided', $2, $3)`, gate.WorkflowID, actorID, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) OpenGates(ctx context.Context, workflowID string) ([]domain.Gate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM gates WHERE workflow_id=$1 AND status='OPEN' ORDER BY opened_at`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Gate
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		gate, err := s.GetGate(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, gate)
	}
	return result, rows.Err()
}

func (s *Store) LatestArtifact(ctx context.Context, workflowID string, artifactType domain.ArtifactType) (domain.Artifact, error) {
	var value domain.Artifact
	err := s.db.QueryRowContext(ctx, `SELECT id,workflow_id,artifact_type,artifact_version,source_hash,
		content_json,markdown,model,prompt_version,generated_at FROM artifacts
		WHERE workflow_id=$1 AND artifact_type=$2 ORDER BY artifact_version DESC LIMIT 1`, workflowID, artifactType).
		Scan(&value.ID, &value.WorkflowID, &value.Type, &value.Version, &value.SourceHash,
			&value.Content, &value.Markdown, &value.Model, &value.Prompt, &value.GeneratedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Artifact{}, ErrNotFound
	}
	return value, err
}

func (s *Store) NextArtifactVersion(ctx context.Context, workflowID string, artifactType domain.ArtifactType) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(artifact_version),0)+1 FROM artifacts
		WHERE workflow_id=$1 AND artifact_type=$2`, workflowID, artifactType).Scan(&version)
	return version, err
}

func (s *Store) AddAudit(ctx context.Context, workflowID, eventType string, actorID int64, details any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_events(workflow_id,event_type,actor_id,details_json)
		VALUES ($1,$2,$3,$4)`, workflowID, eventType, actorID, string(raw))
	return err
}

func (s *Store) ListReconcilableWorkflows(ctx context.Context, limit int) ([]domain.Workflow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,gitlab_project_id,issue_iid,issue_title,state,source_hash,revision,created_at,updated_at
		FROM workflows WHERE state IN ('WAITING_REQUIREMENT_REVIEW','WAITING_PRD_AND_TEST_REVIEW')
		ORDER BY updated_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Workflow
	for rows.Next() {
		value, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
