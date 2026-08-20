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
	"github.com/google/uuid"
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
	return s.ClaimEventTypes(ctx, workerID, lease, nil)
}

func (s *Store) ClaimEventTypes(ctx context.Context, workerID string, lease time.Duration, eventTypes []string) (*domain.QueueEvent, error) {
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
		AND (COALESCE(cardinality($1::text[]),0)=0 OR event_type=ANY($1::text[]))
		ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`, eventTypes)
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

func (s *Store) PublishOperationalGate(ctx context.Context, artifact domain.Artifact, gate domain.Gate, note domain.OutboxMessage) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if gate.Type != domain.GateCodeReview {
		if _, err := tx.ExecContext(ctx, `UPDATE gates SET status='SUPERSEDED'
			WHERE workflow_id=$1 AND gate_type=$2 AND status='OPEN'`, gate.WorkflowID, gate.Type); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts
		(id,workflow_id,artifact_type,artifact_version,source_hash,content_json,markdown,model,prompt_version,generated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, artifact.ID, artifact.WorkflowID, artifact.Type,
		artifact.Version, artifact.SourceHash, string(artifact.Content), artifact.Markdown, artifact.Model,
		artifact.Prompt, artifact.GeneratedAt); err != nil {
		return err
	}
	reviewers, _ := json.Marshal(gate.ReviewerIDs)
	if _, err := tx.ExecContext(ctx, `INSERT INTO gates
		(id,workflow_id,gate_type,status,artifact_id,revision,reviewer_ids,opened_at,feedback)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'')`, gate.ID, gate.WorkflowID, gate.Type, gate.Status,
		gate.ArtifactID, gate.Revision, string(reviewers), gate.OpenedAt); err != nil {
		return err
	}
	audit, _ := json.Marshal(map[string]any{"gate_id": gate.ID, "gate_type": gate.Type, "artifact_id": artifact.ID})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(workflow_id,event_type,details_json)
		VALUES ($1,'gate.opened',$2)`, gate.WorkflowID, string(audit)); err != nil {
		return err
	}
	if err := enqueueOutboxTx(ctx, tx, note); err != nil {
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

func (s *Store) ArtifactByID(ctx context.Context, artifactID string) (domain.Artifact, error) {
	var value domain.Artifact
	err := s.db.QueryRowContext(ctx, `SELECT id,workflow_id,artifact_type,artifact_version,source_hash,
		content_json,markdown,model,prompt_version,generated_at FROM artifacts WHERE id=$1`, artifactID).
		Scan(&value.ID, &value.WorkflowID, &value.Type, &value.Version, &value.SourceHash, &value.Content, &value.Markdown, &value.Model, &value.Prompt, &value.GeneratedAt)
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
		FROM workflows WHERE state NOT IN ('COMPLETED','CANCELLED')
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

func (s *Store) SaveWorkItems(ctx context.Context, workflowID string, items []domain.WorkItem, dependencies map[string][]string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ids := make(map[string]string, len(items))
	for _, item := range items {
		acceptance, marshalErr := json.Marshal(item.AcceptanceIDs)
		if marshalErr != nil {
			return marshalErr
		}
		var storedID string
		if err := tx.QueryRowContext(ctx, `INSERT INTO work_items
			(id,workflow_id,work_item_key,gitlab_issue_iid,title,state,owner_role,assignee_id,branch_name,target_branch,acceptance_ids,revision)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (workflow_id,work_item_key) DO UPDATE SET
			title=EXCLUDED.title,owner_role=EXCLUDED.owner_role,assignee_id=EXCLUDED.assignee_id,
			branch_name=EXCLUDED.branch_name,target_branch=EXCLUDED.target_branch,
			acceptance_ids=EXCLUDED.acceptance_ids,updated_at=CURRENT_TIMESTAMP
			RETURNING id`,
			item.ID, workflowID, item.Key, item.GitLabIssueIID, item.Title, item.State, item.OwnerRole,
			item.AssigneeID, item.BranchName, item.TargetBranch, string(acceptance), item.Revision).Scan(&storedID); err != nil {
			return err
		}
		ids[item.Key] = storedID
	}
	for key, dependencyKeys := range dependencies {
		for _, dependencyKey := range dependencyKeys {
			fromID, fromOK := ids[key]
			toID, toOK := ids[dependencyKey]
			if !fromOK || !toOK {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO work_item_dependencies(work_item_id,depends_on_id)
				VALUES ($1,$2) ON CONFLICT DO NOTHING`, fromID, toID); err != nil {
				return err
			}
		}
	}
	raw, _ := json.Marshal(map[string]any{"work_item_count": len(items)})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(workflow_id,event_type,details_json)
		VALUES ($1,'planning.work_items_saved',$2)`, workflowID, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MaterializeWorkItem(ctx context.Context, workItemID string, issueIID int64, branchName string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE work_items SET gitlab_issue_iid=$1,branch_name=$2,updated_at=CURRENT_TIMESTAMP
		WHERE id=$3 AND (gitlab_issue_iid=0 OR gitlab_issue_iid=$1)`, issueIID, branchName, workItemID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("work item is already linked to a different Issue")
	}
	return nil
}

func (s *Store) ListWorkItems(ctx context.Context, workflowID string) ([]domain.WorkItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,workflow_id,work_item_key,gitlab_issue_iid,title,state,
		owner_role,assignee_id,branch_name,target_branch,acceptance_ids,revision,created_at,updated_at
		FROM work_items WHERE workflow_id=$1 ORDER BY created_at,id`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.WorkItem
	for rows.Next() {
		item, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) ActivateWorkItems(ctx context.Context, workflowID string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE work_items wi SET state=CASE
			WHEN EXISTS (
				SELECT 1 FROM work_item_dependencies d JOIN work_items dependency ON dependency.id=d.depends_on_id
				WHERE d.work_item_id=wi.id AND dependency.state NOT IN ('MERGED','COMPLETED')
			) THEN 'WAITING_DEPENDENCY' ELSE 'READY_FOR_CODEX' END,
			revision=revision+1,updated_at=CURRENT_TIMESTAMP
		WHERE wi.workflow_id=$1 AND wi.state IN ('PLANNED','WAITING_DEPENDENCY')`, workflowID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetWorkItem(ctx context.Context, id string) (domain.WorkItem, error) {
	return scanWorkItem(s.db.QueryRowContext(ctx, `SELECT id,workflow_id,work_item_key,gitlab_issue_iid,title,state,
		owner_role,assignee_id,branch_name,target_branch,acceptance_ids,revision,created_at,updated_at
		FROM work_items WHERE id=$1`, id))
}

func (s *Store) GetWorkItemByIssue(ctx context.Context, projectID, issueIID int64) (domain.WorkItem, error) {
	return scanWorkItem(s.db.QueryRowContext(ctx, `SELECT wi.id,wi.workflow_id,wi.work_item_key,wi.gitlab_issue_iid,
		wi.title,wi.state,wi.owner_role,wi.assignee_id,wi.branch_name,wi.target_branch,wi.acceptance_ids,
		wi.revision,wi.created_at,wi.updated_at
		FROM work_items wi JOIN workflows w ON w.id=wi.workflow_id
		WHERE w.gitlab_project_id=$1 AND wi.gitlab_issue_iid=$2`, projectID, issueIID))
}

func (s *Store) GetWorkItemByBranch(ctx context.Context, projectID int64, branch string) (domain.WorkItem, error) {
	return scanWorkItem(s.db.QueryRowContext(ctx, `SELECT wi.id,wi.workflow_id,wi.work_item_key,wi.gitlab_issue_iid,
		wi.title,wi.state,wi.owner_role,wi.assignee_id,wi.branch_name,wi.target_branch,wi.acceptance_ids,
		wi.revision,wi.created_at,wi.updated_at
		FROM work_items wi JOIN workflows w ON w.id=wi.workflow_id
		WHERE w.gitlab_project_id=$1 AND wi.branch_name=$2`, projectID, branch))
}

func scanWorkItem(row rowScanner) (domain.WorkItem, error) {
	var item domain.WorkItem
	var acceptance []byte
	if err := row.Scan(&item.ID, &item.WorkflowID, &item.Key, &item.GitLabIssueIID, &item.Title, &item.State,
		&item.OwnerRole, &item.AssigneeID, &item.BranchName, &item.TargetBranch, &acceptance, &item.Revision,
		&item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.WorkItem{}, ErrNotFound
		}
		return domain.WorkItem{}, err
	}
	if err := json.Unmarshal(acceptance, &item.AcceptanceIDs); err != nil {
		return domain.WorkItem{}, err
	}
	return item, nil
}

func (s *Store) StartCodex(ctx context.Context, workItemID, clientID string, engineerID int64) (domain.CodexDispatch, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return domain.CodexDispatch{}, false, err
	}
	defer tx.Rollback()
	item, err := scanWorkItem(tx.QueryRowContext(ctx, `SELECT id,workflow_id,work_item_key,gitlab_issue_iid,title,state,
		owner_role,assignee_id,branch_name,target_branch,acceptance_ids,revision,created_at,updated_at
		FROM work_items WHERE id=$1 FOR UPDATE`, workItemID))
	if err != nil {
		return domain.CodexDispatch{}, false, err
	}
	if item.AssigneeID != engineerID {
		return domain.CodexDispatch{}, false, errors.New("command actor is not the assigned engineer")
	}
	var existing domain.CodexDispatch
	var completed sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT id,work_item_id,client_id,engineer_id,coding_thread_id,quality_thread_id,
		status,started_at,completed_at FROM codex_dispatches WHERE work_item_id=$1`, workItemID).
		Scan(&existing.ID, &existing.WorkItemID, &existing.ClientID, &existing.EngineerID,
			&existing.CodingThreadID, &existing.QualityThreadID, &existing.Status, &existing.StartedAt, &completed)
	if err == nil {
		if completed.Valid {
			existing.CompletedAt = &completed.Time
		}
		return existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.CodexDispatch{}, false, err
	}
	if item.State != domain.WorkItemReadyForCodex {
		return domain.CodexDispatch{}, false, fmt.Errorf("work item is %s, not READY_FOR_CODEX", item.State)
	}
	var unsatisfied int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_dependencies d
		JOIN work_items dependency ON dependency.id=d.depends_on_id
		WHERE d.work_item_id=$1 AND dependency.state NOT IN ('MERGED','COMPLETED')`, workItemID).Scan(&unsatisfied); err != nil {
		return domain.CodexDispatch{}, false, err
	}
	if unsatisfied != 0 {
		return domain.CodexDispatch{}, false, fmt.Errorf("%d dependencies are not complete", unsatisfied)
	}
	dispatch := domain.CodexDispatch{
		ID: uuid.NewString(), WorkItemID: workItemID, ClientID: clientID, EngineerID: engineerID,
		Status: "STARTED", StartedAt: time.Now().UTC(),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO codex_dispatches
		(id,work_item_id,client_id,engineer_id,status,started_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		dispatch.ID, dispatch.WorkItemID, dispatch.ClientID, dispatch.EngineerID, dispatch.Status, dispatch.StartedAt); err != nil {
		return domain.CodexDispatch{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET state='CODING',revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, workItemID); err != nil {
		return domain.CodexDispatch{}, false, err
	}
	raw, _ := json.Marshal(map[string]any{"work_item_id": workItemID, "client_id": clientID, "dispatch_id": dispatch.ID})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(workflow_id,event_type,actor_id,details_json)
		VALUES ($1,'codex.started',$2,$3)`, item.WorkflowID, engineerID, string(raw)); err != nil {
		return domain.CodexDispatch{}, false, err
	}
	return dispatch, true, tx.Commit()
}

func (s *Store) ResetCodex(ctx context.Context, workItemID, reason string, actorID int64) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	item, err := scanWorkItem(tx.QueryRowContext(ctx, `SELECT id,workflow_id,work_item_key,gitlab_issue_iid,title,state,
		owner_role,assignee_id,branch_name,target_branch,acceptance_ids,revision,created_at,updated_at
		FROM work_items WHERE id=$1 FOR UPDATE`, workItemID))
	if err != nil {
		return err
	}
	if item.AssigneeID != actorID {
		return errors.New("command actor is not the assigned engineer")
	}
	switch item.State {
	case domain.WorkItemCoding, domain.WorkItemDraftMR, domain.WorkItemAIQualityChecks,
		domain.WorkItemRework, domain.WorkItemCIRunning, domain.WorkItemBlocked:
	default:
		return fmt.Errorf("work item cannot reset Codex from %s", item.State)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM codex_dispatches WHERE work_item_id=$1`, workItemID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET state='READY_FOR_CODEX',revision=revision+1,
		updated_at=CURRENT_TIMESTAMP WHERE id=$1`, workItemID); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"work_item_id": workItemID, "reason": reason})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(workflow_id,event_type,actor_id,details_json)
		VALUES ($1,'codex.reset',$2,$3)`, item.WorkflowID, actorID, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetWorkItemState(ctx context.Context, id string, state domain.WorkItemState, details any) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var workflowID string
	var current domain.WorkItemState
	if err := tx.QueryRowContext(ctx, `SELECT workflow_id,state FROM work_items WHERE id=$1 FOR UPDATE`, id).
		Scan(&workflowID, &current); err != nil {
		return err
	}
	if err := domain.ValidateWorkItemTransition(current, state); err != nil {
		return err
	}
	if current == state {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET state=$1,revision=revision+1,
		updated_at=CURRENT_TIMESTAMP WHERE id=$2`, state, id); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"work_item_id": id, "state": state, "details": details})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(workflow_id,event_type,details_json)
		VALUES ($1,'work_item.transition',$2)`, workflowID, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SuspendWorkflow(ctx context.Context, workflowID string, action domain.ControlAction, reason string, actorID int64) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state domain.State
	var suspended domain.State
	if err := tx.QueryRowContext(ctx, `SELECT state,suspended_state FROM workflows WHERE id=$1 FOR UPDATE`, workflowID).
		Scan(&state, &suspended); err != nil {
		return err
	}
	next := state
	switch action {
	case domain.ControlPause:
		if state == domain.StatePaused || state == domain.StateCancelled || state == domain.StateCompleted {
			return fmt.Errorf("workflow cannot be paused from %s", state)
		}
		suspended = state
		next = domain.StatePaused
	case domain.ControlResume:
		if state != domain.StatePaused || suspended == "" {
			return errors.New("workflow is not paused")
		}
		next = suspended
		suspended = ""
	case domain.ControlCancel:
		if state == domain.StateCompleted {
			return errors.New("completed workflow cannot be cancelled")
		}
		next = domain.StateCancelled
	default:
		return fmt.Errorf("unsupported workflow control action %q", action)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflows SET state=$1,suspended_state=$2,revision=revision+1,
		updated_at=CURRENT_TIMESTAMP WHERE id=$3`, next, suspended, workflowID); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"from": state, "to": next, "action": action, "reason": reason})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(workflow_id,event_type,actor_id,details_json)
		VALUES ($1,'workflow.controlled',$2,$3)`, workflowID, actorID, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

type MergeRequestRecord struct {
	ID           string
	WorkItemID   string
	GitLabMRIID  int64
	SourceBranch string
	TargetBranch string
	HeadSHA      string
	State        string
	Draft        bool
	WebURL       string
}

func (s *Store) UpsertMergeRequest(ctx context.Context, value MergeRequestRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO merge_requests
		(id,work_item_id,gitlab_mr_iid,source_branch,target_branch,head_sha,state,draft,web_url)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (work_item_id) DO UPDATE SET gitlab_mr_iid=EXCLUDED.gitlab_mr_iid,
		source_branch=EXCLUDED.source_branch,target_branch=EXCLUDED.target_branch,head_sha=EXCLUDED.head_sha,
		state=EXCLUDED.state,draft=EXCLUDED.draft,web_url=EXCLUDED.web_url,updated_at=CURRENT_TIMESTAMP`,
		value.ID, value.WorkItemID, value.GitLabMRIID, value.SourceBranch, value.TargetBranch,
		value.HeadSHA, value.State, value.Draft, value.WebURL)
	return err
}

func (s *Store) GetMergeRequestByWorkItem(ctx context.Context, workItemID string) (MergeRequestRecord, error) {
	var value MergeRequestRecord
	err := s.db.QueryRowContext(ctx, `SELECT id,work_item_id,gitlab_mr_iid,source_branch,target_branch,
		head_sha,state,draft,web_url FROM merge_requests WHERE work_item_id=$1`, workItemID).
		Scan(&value.ID, &value.WorkItemID, &value.GitLabMRIID, &value.SourceBranch, &value.TargetBranch,
			&value.HeadSHA, &value.State, &value.Draft, &value.WebURL)
	if errors.Is(err, sql.ErrNoRows) {
		return MergeRequestRecord{}, ErrNotFound
	}
	return value, err
}

func (s *Store) RecordQualityRun(ctx context.Context, workItemID, headSHA, status string,
	verdict domain.QualityVerdict, artifactID string) (int, error) {
	mr, err := s.GetMergeRequestByWorkItem(ctx, workItemID)
	if err != nil {
		return 0, err
	}
	var attempt int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt),0)+1 FROM quality_runs
		WHERE merge_request_id=$1 AND head_sha=$2`, mr.ID, headSHA).Scan(&attempt); err != nil {
		return 0, err
	}
	var artifact any
	if artifactID != "" {
		artifact = artifactID
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO quality_runs
		(id,merge_request_id,head_sha,attempt,status,acceptance_coverage,test_evidence_coverage,
		required_ci_passed,p0_findings,p1_findings,high_security_findings,critical_security_findings,
		architecture_deviations,out_of_scope_changes,blockers,migration_validated,rollback_validated,
		report_artifact_id,finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,CURRENT_TIMESTAMP)`,
		uuid.NewString(), mr.ID, headSHA, attempt, status, verdict.AcceptanceCoverage,
		verdict.TestEvidenceCoverage, verdict.RequiredCIPassed, verdict.P0Findings, verdict.P1Findings,
		verdict.HighSecurityFindings, verdict.CriticalSecurityFindings, verdict.ArchitectureDeviations,
		verdict.OutOfScopeChanges, verdict.Blockers, verdict.MigrationValidated, verdict.RollbackValidated, artifact)
	return attempt, err
}

func (s *Store) GetArtifact(ctx context.Context, id string) (domain.Artifact, error) {
	var value domain.Artifact
	err := s.db.QueryRowContext(ctx, `SELECT id,workflow_id,artifact_type,artifact_version,source_hash,
		content_json,markdown,model,prompt_version,generated_at FROM artifacts WHERE id=$1`, id).
		Scan(&value.ID, &value.WorkflowID, &value.Type, &value.Version, &value.SourceHash,
			&value.Content, &value.Markdown, &value.Model, &value.Prompt, &value.GeneratedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Artifact{}, ErrNotFound
	}
	return value, err
}

func (s *Store) CountIncompleteWorkItems(ctx context.Context, workflowID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items
		WHERE workflow_id=$1 AND state NOT IN ('MERGED','COMPLETED','CANCELLED')`, workflowID).Scan(&count)
	return count, err
}

func (s *Store) UnblockDependents(ctx context.Context, completedWorkItemID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE work_items candidate SET state='READY_FOR_CODEX',
		revision=revision+1,updated_at=CURRENT_TIMESTAMP
		WHERE candidate.id IN (SELECT work_item_id FROM work_item_dependencies WHERE depends_on_id=$1)
		AND candidate.state='WAITING_DEPENDENCY'
		AND NOT EXISTS (
			SELECT 1 FROM work_item_dependencies remaining
			JOIN work_items dependency ON dependency.id=remaining.depends_on_id
			WHERE remaining.work_item_id=candidate.id AND dependency.state NOT IN ('MERGED','COMPLETED')
		)`, completedWorkItemID)
	return err
}

func (s *Store) RecordIncident(ctx context.Context, callbackID, source, workflowID, severity, title, status string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var workflow any
	if workflowID != "" {
		workflow = workflowID
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO incidents
		(id,workflow_id,source,external_id,severity,title,status,payload_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (source,external_id) DO UPDATE SET severity=EXCLUDED.severity,title=EXCLUDED.title,
		status=EXCLUDED.status,payload_json=EXCLUDED.payload_json,updated_at=CURRENT_TIMESTAMP`,
		uuid.NewString(), workflow, source, callbackID, severity, title, status, string(raw))
	return err
}

func (s *Store) RecordEmailRelay(ctx context.Context, messageIDHash string, engineerID, noteID int64, commandText, status string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO email_relays
		(id,message_id_hash,engineer_id,command_text,gitlab_note_id,status,relayed_at)
		VALUES ($1,$2,$3,$4,$5,$6,CURRENT_TIMESTAMP)
		ON CONFLICT (message_id_hash) DO UPDATE SET gitlab_note_id=EXCLUDED.gitlab_note_id,
		status=EXCLUDED.status,relayed_at=CURRENT_TIMESTAMP`,
		uuid.NewString(), messageIDHash, engineerID, commandText, noteID, status)
	return err
}

func (s *Store) UpsertPipelineRun(ctx context.Context, workflowID, workItemID string, pipelineID int64,
	ref, sha, status, webURL string) error {
	var item any
	if workItemID != "" {
		item = workItemID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO pipeline_runs
		(id,workflow_id,work_item_id,gitlab_pipeline_id,ref,sha,status,web_url)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (workflow_id,gitlab_pipeline_id) DO UPDATE SET ref=EXCLUDED.ref,sha=EXCLUDED.sha,
		status=EXCLUDED.status,web_url=EXCLUDED.web_url,updated_at=CURRENT_TIMESTAMP,
		finished_at=CASE WHEN EXCLUDED.status IN ('success','failed','canceled','skipped') THEN CURRENT_TIMESTAMP
		ELSE pipeline_runs.finished_at END`,
		uuid.NewString(), workflowID, item, pipelineID, ref, sha, status, webURL)
	return err
}

func (s *Store) SaveReleaseCandidate(ctx context.Context, workflowID, version, sha, status string) (string, error) {
	id := uuid.NewString()
	err := s.db.QueryRowContext(ctx, `INSERT INTO release_candidates
		(id,workflow_id,version,commit_sha,status) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (workflow_id,version) DO UPDATE SET commit_sha=EXCLUDED.commit_sha,
		status=EXCLUDED.status,updated_at=CURRENT_TIMESTAMP RETURNING id`,
		id, workflowID, version, sha, status).Scan(&id)
	return id, err
}

func (s *Store) LatestReleaseCandidate(ctx context.Context, workflowID string) (id, version, sha, status string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT id,version,commit_sha,status FROM release_candidates
		WHERE workflow_id=$1 ORDER BY created_at DESC LIMIT 1`, workflowID).Scan(&id, &version, &sha, &status)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

func (s *Store) SaveDeployment(ctx context.Context, workflowID, releaseCandidateID, environment, externalID, status string,
	productionEnabled bool) (string, error) {
	id := uuid.NewString()
	err := s.db.QueryRowContext(ctx, `INSERT INTO deployments
		(id,workflow_id,release_candidate_id,environment,external_deployment_id,status,production_enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (environment,external_deployment_id) DO UPDATE SET status=EXCLUDED.status,
		finished_at=CASE WHEN EXCLUDED.status IN ('success','failed','cancelled') THEN CURRENT_TIMESTAMP
		ELSE deployments.finished_at END RETURNING id`,
		id, workflowID, releaseCandidateID, environment, externalID, status, productionEnabled).Scan(&id)
	return id, err
}

func (s *Store) StartObservation(ctx context.Context, workflowID, deploymentID string, duration time.Duration, criteria any) error {
	raw, err := json.Marshal(criteria)
	if err != nil {
		return err
	}
	var deployment any
	if deploymentID != "" {
		deployment = deploymentID
	}
	start := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO observation_windows
		(id,workflow_id,deployment_id,status,starts_at,ends_at,success_criteria,result_json)
		VALUES ($1,$2,$3,'RUNNING',$4,$5,$6,'{}')`,
		uuid.NewString(), workflowID, deployment, start, start.Add(duration), string(raw))
	return err
}

func (s *Store) CompleteObservation(ctx context.Context, workflowID string, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE observation_windows SET status='PASSED',result_json=$1
		WHERE id=(SELECT id FROM observation_windows WHERE workflow_id=$2 ORDER BY starts_at DESC LIMIT 1)`,
		string(raw), workflowID)
	return err
}

func (s *Store) StartAgentRun(ctx context.Context, workflowID, workItemID, agentType, model, inputHash string) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		id, err := s.startAgentRunOnce(ctx, workflowID, workItemID, agentType, model, inputHash)
		if err == nil || !serializationFailure(err) {
			return id, err
		}
	}
	return "", errors.New("could not allocate Agent Run number after serialization retries")
}

func (s *Store) startAgentRunOnce(ctx context.Context, workflowID, workItemID, agentType, model, inputHash string) (string, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var workItem any
	if workItemID != "" {
		workItem = workItemID
	}
	var runNumber int
	if workItemID == "" {
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(run_number),0)+1 FROM agent_runs
			WHERE workflow_id=$1 AND work_item_id IS NULL AND agent_type=$2`, workflowID, agentType).Scan(&runNumber)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(run_number),0)+1 FROM agent_runs
			WHERE workflow_id=$1 AND work_item_id=$2 AND agent_type=$3`, workflowID, workItemID, agentType).Scan(&runNumber)
	}
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_runs
		(id,workflow_id,work_item_id,agent_type,run_number,status,model,input_hash,lifecycle_phase)
		VALUES ($1,$2,$3,$4,$5,'RUNNING',$6,$7,'RUNNING')`,
		id, workflowID, workItem, agentType, runNumber, model, inputHash); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func serializationFailure(err error) bool {
	type sqlStateError interface{ SQLState() string }
	var state sqlStateError
	return errors.As(err, &state) && state.SQLState() == "40001"
}

func (s *Store) FinishAgentRun(ctx context.Context, id, status, artifactID string, runError error) error {
	return s.FinishAgentRunWithTrace(ctx, id, status, artifactID, AgentRunTrace{}, runError)
}

func (s *Store) RequestAgentRunCancellation(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agent_runs SET cancel_requested_at=CURRENT_TIMESTAMP
		WHERE id=$1 AND status IN ('RUNNING','QUEUED') AND cancel_requested_at IS NULL`, id)
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
	return nil
}

func (s *Store) AgentRunCancellationRequested(ctx context.Context, id string) (bool, error) {
	var requested bool
	err := s.db.QueryRowContext(ctx, `SELECT cancel_requested_at IS NOT NULL FROM agent_runs WHERE id=$1`, id).Scan(&requested)
	if err == sql.ErrNoRows {
		return false, ErrNotFound
	}
	return requested, err
}

type AgentRunTrace struct {
	ProviderResponseID string
	InputTokens        int64
	CachedTokens       int64
	OutputTokens       int64
	ReasoningTokens    int64
	EstimatedCost      int64
	LatencyMS          int64
	FinishReason       string
	SelectedModelKey   string
	ProviderModelID    string
	Fallback           bool
	RouteReason        string
	RiskLevel          string
}

func (s *Store) FinishAgentRunWithTrace(ctx context.Context, id, status, artifactID string, trace AgentRunTrace, runError error) error {
	if status == "FAILED" && errors.Is(runError, context.Canceled) {
		status = "CANCELLED"
	}
	if status == "COMPLETED" {
		if err := s.beginAgentRunValidation(ctx, id); err != nil {
			return err
		}
		if err := s.validateAgentRunCompletion(ctx, id, trace); err != nil {
			_ = s.failAgentRunValidation(ctx, id, err)
			return err
		}
	}
	var artifact any
	if artifactID != "" {
		artifact = artifactID
	}
	errorSummary := ""
	if runError != nil {
		errorSummary = redactError(runError)
	}
	lifecyclePhase := "TERMINAL_FAILED"
	if status == "COMPLETED" {
		lifecyclePhase = "COMPLETED"
	} else if status == "CANCELLED" {
		lifecyclePhase = "CANCELLED"
	} else if status == "FAILED" && retryableAgentRunError(runError) {
		lifecyclePhase = "RETRYABLE_FAILED"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agent_runs SET status=$1,lifecycle_phase=$2,output_artifact_id=$3,
		error_summary=$4,provider_response_id=$5,input_tokens=$6,cached_tokens=$7,output_tokens=$8,
		reasoning_tokens=$9,estimated_cost_microunits=$10,latency_ms=$11,finish_reason=$12,provider_model_id=$13,
		model_version_id=COALESCE((SELECT id FROM model_versions WHERE model_key=$14 AND status='ACTIVE' ORDER BY created_at DESC LIMIT 1),model_version_id),
		finished_at=CURRENT_TIMESTAMP WHERE id=$15`,
		status, lifecyclePhase, artifact, errorSummary, trace.ProviderResponseID, trace.InputTokens, trace.CachedTokens,
		trace.OutputTokens, trace.ReasoningTokens, trace.EstimatedCost, trace.LatencyMS, trace.FinishReason, trace.ProviderModelID, trace.SelectedModelKey, id)
	if err != nil {
		return err
	}
	stepStatus := "FAILED"
	if status == "COMPLETED" {
		stepStatus = "COMPLETED"
	} else if status == "CANCELLED" {
		stepStatus = "CANCELLED"
	}
	if _, stepErr := s.db.ExecContext(ctx, `UPDATE agent_steps SET status=$1,finished_at=CURRENT_TIMESTAMP
		WHERE agent_run_id=$2 AND status='RUNNING'`, stepStatus, id); stepErr != nil {
		return stepErr
	}
	if trace.SelectedModelKey != "" {
		tx, txErr := s.db.BeginTx(ctx, nil)
		if txErr != nil {
			return txErr
		}
		defer tx.Rollback()
		_, err = tx.ExecContext(ctx, `INSERT INTO model_route_decisions
			(id,workflow_id,agent_run_id,requested_model_version_id,selected_model_version_id,risk_level,fallback,estimated_cost_microunits,reason)
			SELECT $1,ar.workflow_id,ar.id,requested.id,selected.id,$2,$3,$4,$5 FROM agent_runs ar
			JOIN model_versions selected ON selected.model_key=$6 AND selected.status='ACTIVE'
			LEFT JOIN model_versions requested ON requested.model_key=ar.model AND requested.status='ACTIVE'
			WHERE ar.id=$7 ORDER BY selected.created_at DESC LIMIT 1`, uuid.NewString(), trace.RiskLevel, trace.Fallback,
			trace.EstimatedCost, trace.RouteReason, trace.SelectedModelKey, id)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO model_health_events(id,model_version_id,healthy,latency_ms,error_summary)
			SELECT $1,id,$2,$3,$4 FROM model_versions WHERE model_key=$5 AND status='ACTIVE'
			ORDER BY created_at DESC LIMIT 1`, uuid.NewString(), runError == nil, trace.LatencyMS, errorSummary, trace.SelectedModelKey)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	return err
}

func retryableAgentRunError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"http 429", "http 500", "http 502", "http 503", "http 504", "timeout", "connection reset", "temporarily unavailable"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (s *Store) validateAgentRunCompletion(ctx context.Context, id string, trace AgentRunTrace) error {
	var governed bool
	var profileReady, promptReady, modelReady bool
	if err := s.db.QueryRowContext(ctx, `SELECT context_manifest_id IS NOT NULL,
		agent_profile_version_id IS NOT NULL,prompt_version_id IS NOT NULL,model_version_id IS NOT NULL
		FROM agent_runs WHERE id=$1 AND status='RUNNING'`, id).
		Scan(&governed, &profileReady, &promptReady, &modelReady); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	// Legacy V2 and engineer-visible callback runs have no governed V3 context.
	if !governed {
		return nil
	}
	if !profileReady || !promptReady || !modelReady {
		return errors.New("governed Agent Run is missing immutable registry bindings")
	}
	if strings.TrimSpace(trace.ProviderResponseID) == "" || strings.TrimSpace(trace.FinishReason) == "" {
		return errors.New("governed Agent Run is missing provider completion evidence")
	}
	if trace.InputTokens <= 0 || trace.OutputTokens <= 0 {
		return errors.New("governed Agent Run is missing provider usage evidence")
	}
	var unsettledTools, invalidContext, invalidSources int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_calls
		WHERE agent_run_id=$1 AND status NOT IN ('COMPLETED','FAILED','DENY','REQUIRE_GATE','QUEUED')`, id).Scan(&unsettledTools); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM context_entries ce JOIN agent_runs ar ON ar.context_manifest_id=ce.context_manifest_id
		WHERE ar.id=$1 AND (ce.content_hash='' OR ce.citation_json IS NULL OR ce.citation_json='{}'::jsonb)`, id).Scan(&invalidContext); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM context_entries ce JOIN agent_runs ar ON ar.context_manifest_id=ce.context_manifest_id
		WHERE ar.id=$1 AND CASE ce.source_type
			WHEN 'CONFLUENCE_SNAPSHOT' THEN NOT EXISTS(SELECT 1 FROM source_snapshots ss WHERE ss.id=ce.source_id AND ss.workflow_id=ar.workflow_id)
			WHEN 'KNOWLEDGE_CHUNK' THEN NOT EXISTS(SELECT 1 FROM knowledge_chunks kc JOIN knowledge_versions kv ON kv.id=kc.knowledge_version_id
				JOIN knowledge_documents kd ON kd.id=kv.document_id WHERE kc.id=ce.source_id AND kc.content_hash=ce.content_hash AND kv.status='ACTIVE' AND kd.status='ACTIVE')
			WHEN 'PROJECT_MEMORY' THEN NOT EXISTS(SELECT 1 FROM project_memories pm JOIN knowledge_documents kd ON kd.id=pm.source_document_id
				WHERE pm.id=ce.source_id AND pm.status='ACTIVE' AND kd.status='ACTIVE' AND (pm.expires_at IS NULL OR pm.expires_at>CURRENT_TIMESTAMP))
			WHEN 'SKILL_VERSION' THEN NOT EXISTS(SELECT 1 FROM skill_versions sv WHERE sv.id=ce.source_id AND sv.status='ACTIVE' AND sv.content_hash=ce.content_hash)
			ELSE true END`, id).Scan(&invalidSources); err != nil {
		return err
	}
	if unsettledTools != 0 {
		return errors.New("governed Agent Run has unsettled tool calls")
	}
	if invalidContext != 0 {
		return errors.New("governed Agent Run has context entries without hash or citation evidence")
	}
	if invalidSources != 0 {
		return errors.New("governed Agent Run has context entries whose source evidence is no longer verifiable")
	}
	return nil
}

func (s *Store) beginAgentRunValidation(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET lifecycle_phase='VALIDATING'
		WHERE id=$1 AND status='RUNNING' AND lifecycle_phase='RUNNING'`, id)
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
		WHERE agent_run_id=$1 AND step_type='MODEL_RESPONSE' AND status='RUNNING'`, id); err != nil {
		return err
	}
	if err := insertAgentStep(ctx, tx, id, "VALIDATION", "RUNNING", nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) failAgentRunValidation(ctx context.Context, id string, validationErr error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='FAILED',lifecycle_phase='TERMINAL_FAILED',
		error_summary=$1,finished_at=CURRENT_TIMESTAMP WHERE id=$2 AND status='RUNNING' AND lifecycle_phase='VALIDATING'`,
		redactError(validationErr), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_steps SET status='FAILED',finished_at=CURRENT_TIMESTAMP,
		metadata_json=jsonb_set(metadata_json,'{error_summary}',to_jsonb($1::text))
		WHERE agent_run_id=$2 AND status='RUNNING'`, redactError(validationErr), id); err != nil {
		return err
	}
	return tx.Commit()
}
