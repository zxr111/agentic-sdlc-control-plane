package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
)

type DashboardData struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Summary     DashboardSummary    `json:"summary"`
	Queues      DashboardQueues     `json:"queues"`
	Workflows   []DashboardWorkflow `json:"workflows"`
	Failures    []DashboardFailure  `json:"failures"`
}

type DashboardSummary struct {
	Total        int `json:"total"`
	InProgress   int `json:"in_progress"`
	WaitingGates int `json:"waiting_gates"`
	Ready        int `json:"ready"`
}

type DashboardQueues struct {
	EventsReady      int `json:"events_ready"`
	EventsProcessing int `json:"events_processing"`
	EventsDead       int `json:"events_dead"`
	OutboxReady      int `json:"outbox_ready"`
	OutboxProcessing int `json:"outbox_processing"`
	OutboxDead       int `json:"outbox_dead"`
}

type DashboardWorkflow struct {
	ID              string              `json:"id"`
	GitLabProjectID int64               `json:"gitlab_project_id"`
	ProjectPath     string              `json:"project_path,omitempty"`
	IssueIID        int64               `json:"issue_iid"`
	IssueTitle      string              `json:"issue_title"`
	IssueURL        string              `json:"issue_url,omitempty"`
	State           domain.State        `json:"state"`
	SourceHash      string              `json:"source_hash"`
	Revision        int                 `json:"revision"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
	Gates           []DashboardGate     `json:"gates"`
	Artifacts       []DashboardArtifact `json:"artifacts"`
	Sources         []DashboardSource   `json:"sources"`
	Activity        []DashboardActivity `json:"activity"`
}

type DashboardGate struct {
	ID            string            `json:"id"`
	Type          domain.GateType   `json:"type"`
	Status        domain.GateStatus `json:"status"`
	ArtifactID    string            `json:"artifact_id"`
	Revision      int               `json:"revision"`
	ReviewerIDs   []int64           `json:"reviewer_ids"`
	OpenedAt      time.Time         `json:"opened_at"`
	DecidedAt     *time.Time        `json:"decided_at,omitempty"`
	DecisionActor int64             `json:"decision_actor,omitempty"`
	Feedback      string            `json:"feedback,omitempty"`
}

type DashboardArtifact struct {
	ID          string              `json:"id"`
	Type        domain.ArtifactType `json:"type"`
	Version     int                 `json:"version"`
	Model       string              `json:"model"`
	Prompt      string              `json:"prompt"`
	GeneratedAt time.Time           `json:"generated_at"`
}

type DashboardSource struct {
	PageID      string    `json:"page_id"`
	Version     int       `json:"version"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	ContentHash string    `json:"content_hash"`
	ImageCount  int       `json:"image_count"`
	CapturedAt  time.Time `json:"captured_at"`
}

type DashboardActivity struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	ActorID   int64           `json:"actor_id"`
	Details   json.RawMessage `json:"details"`
	CreatedAt time.Time       `json:"created_at"`
}

type DashboardFailure struct {
	Kind        string    `json:"kind"`
	ID          int64     `json:"id"`
	Type        string    `json:"type"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	LastError   string    `json:"last_error"`
	AvailableAt time.Time `json:"available_at"`
}

func (s *Store) Dashboard(ctx context.Context, limit int) (DashboardData, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	result := DashboardData{
		GeneratedAt: time.Now().UTC(),
		Workflows:   []DashboardWorkflow{},
		Failures:    []DashboardFailure{},
	}
	if err := s.loadDashboardWorkflows(ctx, &result, limit); err != nil {
		return DashboardData{}, err
	}
	if err := s.loadDashboardGates(ctx, &result, limit); err != nil {
		return DashboardData{}, err
	}
	if err := s.loadDashboardArtifacts(ctx, &result, limit); err != nil {
		return DashboardData{}, err
	}
	if err := s.loadDashboardSources(ctx, &result, limit); err != nil {
		return DashboardData{}, err
	}
	if err := s.loadDashboardActivity(ctx, &result, limit); err != nil {
		return DashboardData{}, err
	}
	if err := s.loadDashboardQueues(ctx, &result); err != nil {
		return DashboardData{}, err
	}
	if err := s.loadDashboardFailures(ctx, &result); err != nil {
		return DashboardData{}, err
	}
	return result, nil
}

func (s *Store) loadDashboardWorkflows(ctx context.Context, result *DashboardData, limit int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,gitlab_project_id,issue_iid,issue_title,state,source_hash,
		revision,created_at,updated_at FROM workflows ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var workflow DashboardWorkflow
		if err := rows.Scan(&workflow.ID, &workflow.GitLabProjectID, &workflow.IssueIID,
			&workflow.IssueTitle, &workflow.State, &workflow.SourceHash, &workflow.Revision,
			&workflow.CreatedAt, &workflow.UpdatedAt); err != nil {
			return err
		}
		workflow.Gates = []DashboardGate{}
		workflow.Artifacts = []DashboardArtifact{}
		workflow.Sources = []DashboardSource{}
		workflow.Activity = []DashboardActivity{}
		result.Workflows = append(result.Workflows, workflow)
		result.Summary.Total++
		switch workflow.State {
		case domain.StateWaitingRequirementReview, domain.StateWaitingPRDAndTestReview:
			result.Summary.WaitingGates++
		case domain.StateReadyForArchitecture:
			result.Summary.Ready++
		default:
			result.Summary.InProgress++
		}
	}
	return rows.Err()
}

func (s *Store) loadDashboardGates(ctx context.Context, result *DashboardData, limit int) error {
	rows, err := s.db.QueryContext(ctx, `WITH recent AS (
			SELECT id FROM workflows ORDER BY updated_at DESC LIMIT $1
		)
		SELECT g.workflow_id,g.id,g.gate_type,g.status,g.artifact_id,g.revision,g.reviewer_ids,
			g.opened_at,g.decided_at,g.decision_actor,g.feedback
		FROM gates g JOIN recent r ON r.id=g.workflow_id
		ORDER BY g.opened_at DESC`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := workflowIndex(result.Workflows)
	for rows.Next() {
		var workflowID string
		var gate DashboardGate
		var reviewers []byte
		var decidedAt sql.NullTime
		if err := rows.Scan(&workflowID, &gate.ID, &gate.Type, &gate.Status, &gate.ArtifactID,
			&gate.Revision, &reviewers, &gate.OpenedAt, &decidedAt, &gate.DecisionActor,
			&gate.Feedback); err != nil {
			return err
		}
		if err := json.Unmarshal(reviewers, &gate.ReviewerIDs); err != nil {
			return fmt.Errorf("decode gate reviewers: %w", err)
		}
		if decidedAt.Valid {
			gate.DecidedAt = &decidedAt.Time
		}
		if position, ok := index[workflowID]; ok {
			result.Workflows[position].Gates = append(result.Workflows[position].Gates, gate)
		}
	}
	return rows.Err()
}

func (s *Store) loadDashboardArtifacts(ctx context.Context, result *DashboardData, limit int) error {
	rows, err := s.db.QueryContext(ctx, `WITH recent AS (
			SELECT id FROM workflows ORDER BY updated_at DESC LIMIT $1
		), latest AS (
			SELECT DISTINCT ON (a.workflow_id,a.artifact_type)
				a.workflow_id,a.id,a.artifact_type,a.artifact_version,a.model,a.prompt_version,a.generated_at
			FROM artifacts a JOIN recent r ON r.id=a.workflow_id
			ORDER BY a.workflow_id,a.artifact_type,a.artifact_version DESC
		)
		SELECT workflow_id,id,artifact_type,artifact_version,model,prompt_version,generated_at
		FROM latest ORDER BY generated_at DESC`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := workflowIndex(result.Workflows)
	for rows.Next() {
		var workflowID string
		var artifact DashboardArtifact
		if err := rows.Scan(&workflowID, &artifact.ID, &artifact.Type, &artifact.Version,
			&artifact.Model, &artifact.Prompt, &artifact.GeneratedAt); err != nil {
			return err
		}
		if position, ok := index[workflowID]; ok {
			result.Workflows[position].Artifacts = append(result.Workflows[position].Artifacts, artifact)
		}
	}
	return rows.Err()
}

func (s *Store) loadDashboardSources(ctx context.Context, result *DashboardData, limit int) error {
	rows, err := s.db.QueryContext(ctx, `WITH recent AS (
			SELECT id FROM workflows ORDER BY updated_at DESC LIMIT $1
		), latest AS (
			SELECT DISTINCT ON (s.workflow_id,s.confluence_page_id)
				s.workflow_id,s.confluence_page_id,s.source_version,s.title,s.source_url,
				s.content_hash,jsonb_array_length(s.images_json) AS image_count,s.created_at AS captured_at
			FROM source_snapshots s JOIN recent r ON r.id=s.workflow_id
			ORDER BY s.workflow_id,s.confluence_page_id,s.created_at DESC
		)
		SELECT workflow_id,confluence_page_id,source_version,title,source_url,content_hash,
			image_count,captured_at
		FROM latest
		ORDER BY captured_at DESC`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := workflowIndex(result.Workflows)
	for rows.Next() {
		var workflowID string
		var source DashboardSource
		if err := rows.Scan(&workflowID, &source.PageID, &source.Version, &source.Title,
			&source.URL, &source.ContentHash, &source.ImageCount, &source.CapturedAt); err != nil {
			return err
		}
		if position, ok := index[workflowID]; ok {
			result.Workflows[position].Sources = append(result.Workflows[position].Sources, source)
		}
	}
	return rows.Err()
}

func (s *Store) loadDashboardActivity(ctx context.Context, result *DashboardData, limit int) error {
	rows, err := s.db.QueryContext(ctx, `WITH recent AS (
			SELECT id FROM workflows ORDER BY updated_at DESC LIMIT $1
		), ranked AS (
			SELECT a.id,a.workflow_id,a.event_type,a.actor_id,a.details_json,a.created_at,
				ROW_NUMBER() OVER (PARTITION BY a.workflow_id ORDER BY a.created_at DESC) AS row_number
			FROM audit_events a JOIN recent r ON r.id=a.workflow_id
		)
		SELECT id,workflow_id,event_type,actor_id,details_json,created_at
		FROM ranked WHERE row_number <= 20 ORDER BY created_at DESC`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := workflowIndex(result.Workflows)
	for rows.Next() {
		var workflowID string
		var activity DashboardActivity
		if err := rows.Scan(&activity.ID, &workflowID, &activity.Type, &activity.ActorID,
			&activity.Details, &activity.CreatedAt); err != nil {
			return err
		}
		if position, ok := index[workflowID]; ok {
			result.Workflows[position].Activity = append(result.Workflows[position].Activity, activity)
		}
	}
	return rows.Err()
}

func (s *Store) loadDashboardQueues(ctx context.Context, result *DashboardData) error {
	rows, err := s.db.QueryContext(ctx, `SELECT kind,status,total FROM (
			SELECT 'event' AS kind,status,COUNT(*) AS total FROM event_queue GROUP BY status
			UNION ALL
			SELECT 'outbox' AS kind,status,COUNT(*) AS total FROM outbox_messages GROUP BY status
		) queue_counts`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, status string
		var total int
		if err := rows.Scan(&kind, &status, &total); err != nil {
			return err
		}
		switch kind + ":" + status {
		case "event:READY":
			result.Queues.EventsReady = total
		case "event:PROCESSING":
			result.Queues.EventsProcessing = total
		case "event:DEAD":
			result.Queues.EventsDead = total
		case "outbox:READY":
			result.Queues.OutboxReady = total
		case "outbox:PROCESSING":
			result.Queues.OutboxProcessing = total
		case "outbox:DEAD":
			result.Queues.OutboxDead = total
		}
	}
	return rows.Err()
}

func (s *Store) loadDashboardFailures(ctx context.Context, result *DashboardData) error {
	rows, err := s.db.QueryContext(ctx, `SELECT kind,id,item_type,status,attempts,last_error,available_at FROM (
			SELECT 'event' AS kind,id,event_type AS item_type,status,attempts,last_error,available_at
			FROM event_queue WHERE status='DEAD'
			UNION ALL
			SELECT 'outbox' AS kind,id,message_type AS item_type,status,attempts,last_error,available_at
			FROM outbox_messages WHERE status='DEAD'
		) failures ORDER BY available_at DESC LIMIT 20`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var failure DashboardFailure
		if err := rows.Scan(&failure.Kind, &failure.ID, &failure.Type, &failure.Status,
			&failure.Attempts, &failure.LastError, &failure.AvailableAt); err != nil {
			return err
		}
		result.Failures = append(result.Failures, failure)
	}
	return rows.Err()
}

func workflowIndex(workflows []DashboardWorkflow) map[string]int {
	result := make(map[string]int, len(workflows))
	for index, workflow := range workflows {
		result[workflow.ID] = index
	}
	return result
}
