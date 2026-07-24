package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

type State string

const (
	StateNew                      State = "NEW"
	StateIngesting                State = "INGESTING"
	StateRequirementAnalysis      State = "REQUIREMENT_ANALYSIS"
	StateWaitingRequirementReview State = "WAITING_REQUIREMENT_REVIEW"
	StateMaterializingWorkItems   State = "MATERIALIZING_WORK_ITEMS"
	StatePRDGenerating            State = "PRD_GENERATING"
	StateWaitingPRDAndTestReview  State = "WAITING_PRD_AND_TEST_REVIEW"
	StateReadyForArchitecture     State = "READY_FOR_ARCHITECTURE"
)

var transitions = map[State][]State{
	StateNew:                      {StateIngesting},
	StateIngesting:                {StateRequirementAnalysis},
	StateRequirementAnalysis:      {StateWaitingRequirementReview},
	StateWaitingRequirementReview: {StateIngesting, StateRequirementAnalysis, StateMaterializingWorkItems},
	StateMaterializingWorkItems:   {StatePRDGenerating},
	StatePRDGenerating:            {StateWaitingPRDAndTestReview},
	StateWaitingPRDAndTestReview:  {StateIngesting, StatePRDGenerating, StateReadyForArchitecture},
	StateReadyForArchitecture:     {StateIngesting},
}

func (s State) CanTransition(to State) bool {
	return slices.Contains(transitions[s], to)
}

func ValidateTransition(from, to State) error {
	if !from.CanTransition(to) {
		return fmt.Errorf("invalid workflow transition %s -> %s", from, to)
	}
	return nil
}

type ArtifactType string

const (
	ArtifactRequirement ArtifactType = "REQUIREMENT_REVIEW"
	ArtifactPRD         ArtifactType = "PRD"
	ArtifactTestPlan    ArtifactType = "TEST_PLAN"
)

type GateType string

const (
	GateRequirement GateType = "REQUIREMENT"
	GatePRD         GateType = "PRD"
	GateTest        GateType = "TEST"
)

type GateStatus string

const (
	GateOpen             GateStatus = "OPEN"
	GateApproved         GateStatus = "APPROVED"
	GateChangesRequested GateStatus = "CHANGES_REQUESTED"
	GateRejected         GateStatus = "REJECTED"
	GateSuperseded       GateStatus = "SUPERSEDED"
)

type GateAction string

const (
	ActionApprove        GateAction = "approve"
	ActionRequestChanges GateAction = "request-changes"
	ActionReject         GateAction = "reject"
)

type Workflow struct {
	ID              string
	GitLabProjectID int64
	IssueIID        int64
	IssueTitle      string
	State           State
	SourceHash      string
	Revision        int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func NewWorkflow(projectID, issueIID int64, title string) Workflow {
	return Workflow{
		ID:              uuid.NewString(),
		GitLabProjectID: projectID,
		IssueIID:        issueIID,
		IssueTitle:      title,
		State:           StateNew,
		Revision:        1,
	}
}

type Snapshot struct {
	ID               string
	WorkflowID       string
	ConfluencePageID string
	Version          int
	Title            string
	URL              string
	UpdatedAt        string
	ContentHash      string
	NormalizedText   string
	RawStorage       string
	Images           []Image
	CreatedAt        time.Time
}

type Image struct {
	AttachmentID string `json:"attachment_id"`
	Version      int    `json:"version"`
	Filename     string `json:"filename"`
	MediaType    string `json:"media_type"`
	DownloadURL  string `json:"download_url"`
	ContentHash  string `json:"content_hash,omitempty"`
	GitLabURL    string `json:"gitlab_url,omitempty"`
	Markdown     string `json:"markdown,omitempty"`
	Order        int    `json:"order"`
}

type Artifact struct {
	ID          string
	WorkflowID  string
	Type        ArtifactType
	Version     int
	SourceHash  string
	Content     json.RawMessage
	Markdown    string
	Model       string
	Prompt      string
	GeneratedAt time.Time
}

type Gate struct {
	ID            string
	WorkflowID    string
	Type          GateType
	Status        GateStatus
	ArtifactID    string
	Revision      int
	ReviewerIDs   []int64
	OpenedAt      time.Time
	DecidedAt     *time.Time
	DecisionActor int64
	Feedback      string
}

func NewGate(workflowID string, gateType GateType, artifactID string, revision int, reviewers []int64) Gate {
	return Gate{
		ID:          uuid.NewString(),
		WorkflowID:  workflowID,
		Type:        gateType,
		Status:      GateOpen,
		ArtifactID:  artifactID,
		Revision:    revision,
		ReviewerIDs: append([]int64(nil), reviewers...),
		OpenedAt:    time.Now().UTC(),
	}
}

func (g Gate) Authorizes(userID int64) bool {
	return slices.Contains(g.ReviewerIDs, userID)
}

type QueueEvent struct {
	ID          int64
	DedupeKey   string
	Type        string
	Payload     json.RawMessage
	Attempts    int
	AvailableAt time.Time
	LeaseUntil  *time.Time
}

type OutboxMessage struct {
	ID          int64
	DedupeKey   string
	Type        string
	Payload     json.RawMessage
	Attempts    int
	AvailableAt time.Time
	LeaseUntil  *time.Time
}

type ProjectConfig struct {
	GitLabProjectID  int64                 `json:"gitlab_project_id"`
	Path             string                `json:"path"`
	EnabledLabel     string                `json:"enabled_label"`
	ReviewerIDs      map[GateType][]int64  `json:"reviewer_ids"`
	ReviewerMentions map[GateType][]string `json:"reviewer_mentions,omitempty"`
	OwnerIDs         map[string]int64      `json:"owner_ids,omitempty"`
	Module           string                `json:"module,omitempty"`
}

func (p ProjectConfig) Validate() error {
	if p.GitLabProjectID <= 0 || strings.TrimSpace(p.Path) == "" {
		return errors.New("project requires gitlab_project_id and path")
	}
	if p.EnabledLabel == "" {
		p.EnabledLabel = "automation::enabled"
	}
	for _, gate := range []GateType{GateRequirement, GatePRD, GateTest} {
		if len(p.ReviewerIDs[gate]) == 0 {
			return fmt.Errorf("project %s has no reviewers for gate %s", p.Path, gate)
		}
	}
	return nil
}

type AuditEvent struct {
	WorkflowID string
	Type       string
	ActorID    int64
	Details    json.RawMessage
	CreatedAt  time.Time
}
