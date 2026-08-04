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
	StateNew                       State = "NEW"
	StateIngesting                 State = "INGESTING"
	StateRequirementAnalysis       State = "REQUIREMENT_ANALYSIS"
	StateWaitingRequirementReview  State = "WAITING_REQUIREMENT_REVIEW"
	StateMaterializingWorkItems    State = "MATERIALIZING_WORK_ITEMS"
	StatePRDGenerating             State = "PRD_GENERATING"
	StateWaitingPRDAndTestReview   State = "WAITING_PRD_AND_TEST_REVIEW"
	StateReadyForArchitecture      State = "READY_FOR_ARCHITECTURE"
	StateArchitectureGenerating    State = "ARCHITECTURE_GENERATING"
	StateWaitingArchitectureReview State = "WAITING_ARCHITECTURE_REVIEW"
	StatePlanning                  State = "PLANNING"
	StateExecutingWorkItems        State = "EXECUTING_WORK_ITEMS"
	StateAssemblingRelease         State = "ASSEMBLING_RELEASE"
	StateReleaseCIRunning          State = "RELEASE_CI_RUNNING"
	StateStagingDeploying          State = "STAGING_DEPLOYING"
	StateStagingVerifying          State = "STAGING_VERIFYING"
	StateWaitingReleaseApproval    State = "WAITING_RELEASE_APPROVAL"
	StateProductionDeploying       State = "PRODUCTION_DEPLOYING"
	StateObserving                 State = "OBSERVING"
	StateCompleted                 State = "COMPLETED"
	StatePaused                    State = "PAUSED"
	StateCancelled                 State = "CANCELLED"
)

var transitions = map[State][]State{
	StateNew:                       {StateIngesting},
	StateIngesting:                 {StateRequirementAnalysis},
	StateRequirementAnalysis:       {StateWaitingRequirementReview},
	StateWaitingRequirementReview:  {StateIngesting, StateRequirementAnalysis, StateMaterializingWorkItems},
	StateMaterializingWorkItems:    {StatePRDGenerating},
	StatePRDGenerating:             {StateWaitingPRDAndTestReview},
	StateWaitingPRDAndTestReview:   {StateIngesting, StatePRDGenerating, StateReadyForArchitecture},
	StateReadyForArchitecture:      {StateIngesting, StateArchitectureGenerating},
	StateArchitectureGenerating:    {StateWaitingArchitectureReview},
	StateWaitingArchitectureReview: {StateArchitectureGenerating, StatePlanning},
	StatePlanning:                  {StateExecutingWorkItems},
	StateExecutingWorkItems:        {StateAssemblingRelease},
	StateAssemblingRelease:         {StateReleaseCIRunning},
	StateReleaseCIRunning:          {StateAssemblingRelease, StateStagingDeploying},
	StateStagingDeploying:          {StateAssemblingRelease, StateStagingVerifying},
	StateStagingVerifying:          {StateAssemblingRelease, StateWaitingReleaseApproval},
	StateWaitingReleaseApproval:    {StateAssemblingRelease, StateProductionDeploying, StateObserving},
	StateProductionDeploying:       {StateAssemblingRelease, StateObserving},
	StateObserving:                 {StateAssemblingRelease, StateCompleted},
	StateCompleted:                 {},
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
	ArtifactRequirement         ArtifactType = "REQUIREMENT_REVIEW"
	ArtifactPRD                 ArtifactType = "PRD"
	ArtifactTestPlan            ArtifactType = "TEST_PLAN"
	ArtifactArchitecture        ArtifactType = "ARCHITECTURE"
	ArtifactImplementationPlan  ArtifactType = "IMPLEMENTATION_PLAN"
	ArtifactQualityReport       ArtifactType = "QUALITY_REPORT"
	ArtifactReleasePlan         ArtifactType = "RELEASE_PLAN"
	ArtifactIncidentReport      ArtifactType = "INCIDENT_REPORT"
	ArtifactProductionMigration ArtifactType = "PRODUCTION_MIGRATION_PLAN"
)

type GateType string

const (
	GateRequirement         GateType = "REQUIREMENT"
	GatePRD                 GateType = "PRD"
	GateTest                GateType = "TEST"
	GateArchitecture        GateType = "ARCHITECTURE"
	GateCodeReview          GateType = "CODE_REVIEW"
	GateRelease             GateType = "RELEASE"
	GateIncident            GateType = "INCIDENT"
	GateProductionMigration GateType = "PRODUCTION_MIGRATION"
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
	GitLabProjectID   int64                 `json:"gitlab_project_id"`
	Path              string                `json:"path"`
	EnabledLabel      string                `json:"enabled_label"`
	ReviewerIDs       map[GateType][]int64  `json:"reviewer_ids"`
	ReviewerMentions  map[GateType][]string `json:"reviewer_mentions,omitempty"`
	OwnerIDs          map[string]int64      `json:"owner_ids,omitempty"`
	Module            string                `json:"module,omitempty"`
	FullLifecycle     bool                  `json:"full_lifecycle,omitempty"`
	DefaultBranch     string                `json:"default_branch,omitempty"`
	IntegrationBranch bool                  `json:"integration_branch,omitempty"`
	ProductionEnabled bool                  `json:"production_enabled,omitempty"`
}

func (p ProjectConfig) Validate() error {
	if p.GitLabProjectID <= 0 || strings.TrimSpace(p.Path) == "" {
		return errors.New("project requires gitlab_project_id and path")
	}
	if p.EnabledLabel == "" {
		p.EnabledLabel = "automation::enabled"
	}
	if p.ProductionEnabled && !p.FullLifecycle {
		return fmt.Errorf("project %s cannot enable production without full_lifecycle", p.Path)
	}
	requiredGates := []GateType{GateRequirement, GatePRD, GateTest}
	if p.FullLifecycle {
		requiredGates = append(requiredGates, GateArchitecture, GateCodeReview, GateRelease, GateIncident)
	}
	if p.ProductionEnabled {
		requiredGates = append(requiredGates, GateProductionMigration)
	}
	for _, gate := range requiredGates {
		if len(p.ReviewerIDs[gate]) == 0 {
			return fmt.Errorf("project %s has no reviewers for gate %s", p.Path, gate)
		}
	}
	return nil
}

type WorkItemState string

const (
	WorkItemPlanned           WorkItemState = "PLANNED"
	WorkItemWaitingDependency WorkItemState = "WAITING_DEPENDENCY"
	WorkItemReadyForCodex     WorkItemState = "READY_FOR_CODEX"
	WorkItemCoding            WorkItemState = "CODING"
	WorkItemDraftMR           WorkItemState = "DRAFT_MR"
	WorkItemAIQualityChecks   WorkItemState = "AI_QUALITY_CHECKS"
	WorkItemRework            WorkItemState = "REWORK"
	WorkItemCIRunning         WorkItemState = "CI_RUNNING"
	WorkItemWaitingCodeReview WorkItemState = "WAITING_CODE_REVIEW"
	WorkItemMergeQueued       WorkItemState = "MERGE_QUEUED"
	WorkItemMerged            WorkItemState = "MERGED"
	WorkItemCompleted         WorkItemState = "COMPLETED"
	WorkItemBlocked           WorkItemState = "BLOCKED"
	WorkItemCancelled         WorkItemState = "CANCELLED"
)

type WorkItem struct {
	ID             string
	WorkflowID     string
	Key            string
	GitLabIssueIID int64
	Title          string
	State          WorkItemState
	OwnerRole      string
	AssigneeID     int64
	BranchName     string
	TargetBranch   string
	AcceptanceIDs  []string
	Revision       int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

var workItemTransitions = map[WorkItemState][]WorkItemState{
	WorkItemPlanned:           {WorkItemWaitingDependency, WorkItemReadyForCodex},
	WorkItemWaitingDependency: {WorkItemReadyForCodex, WorkItemCancelled},
	WorkItemReadyForCodex:     {WorkItemCoding, WorkItemDraftMR, WorkItemCancelled},
	WorkItemCoding:            {WorkItemDraftMR, WorkItemAIQualityChecks, WorkItemCIRunning, WorkItemRework, WorkItemBlocked, WorkItemCancelled},
	WorkItemDraftMR:           {WorkItemAIQualityChecks, WorkItemRework, WorkItemBlocked},
	WorkItemAIQualityChecks:   {WorkItemRework, WorkItemCIRunning, WorkItemWaitingCodeReview, WorkItemBlocked},
	WorkItemRework:            {WorkItemReadyForCodex, WorkItemCoding, WorkItemDraftMR, WorkItemAIQualityChecks, WorkItemBlocked},
	WorkItemCIRunning:         {WorkItemRework, WorkItemAIQualityChecks, WorkItemWaitingCodeReview, WorkItemBlocked},
	WorkItemWaitingCodeReview: {WorkItemRework, WorkItemMergeQueued, WorkItemBlocked},
	WorkItemMergeQueued:       {WorkItemRework, WorkItemMerged},
	WorkItemMerged:            {WorkItemCompleted},
	WorkItemCompleted:         {},
	WorkItemBlocked:           {WorkItemReadyForCodex, WorkItemRework, WorkItemCancelled},
	WorkItemCancelled:         {},
}

func ValidateWorkItemTransition(from, to WorkItemState) error {
	if from == to {
		return nil
	}
	if !slices.Contains(workItemTransitions[from], to) {
		return fmt.Errorf("invalid work item transition %s -> %s", from, to)
	}
	return nil
}

type CodexDispatch struct {
	ID              string
	WorkItemID      string
	ClientID        string
	EngineerID      int64
	CodingThreadID  string
	QualityThreadID string
	Status          string
	StartedAt       time.Time
	CompletedAt     *time.Time
}

type QualityVerdict struct {
	AcceptanceCoverage       float64 `json:"acceptance_coverage"`
	TestEvidenceCoverage     float64 `json:"test_evidence_coverage"`
	RequiredCIPassed         bool    `json:"required_ci_passed"`
	P0Findings               int     `json:"p0_findings"`
	P1Findings               int     `json:"p1_findings"`
	HighSecurityFindings     int     `json:"high_security_findings"`
	CriticalSecurityFindings int     `json:"critical_security_findings"`
	ArchitectureDeviations   int     `json:"architecture_deviations"`
	OutOfScopeChanges        int     `json:"out_of_scope_changes"`
	Blockers                 int     `json:"blockers"`
	MigrationValidated       bool    `json:"migration_validated"`
	RollbackValidated        bool    `json:"rollback_validated"`
}

func (v QualityVerdict) Passes() bool {
	return v.AcceptanceCoverage == 100 &&
		v.TestEvidenceCoverage == 100 &&
		v.RequiredCIPassed &&
		v.P0Findings == 0 &&
		v.P1Findings == 0 &&
		v.HighSecurityFindings == 0 &&
		v.CriticalSecurityFindings == 0 &&
		v.ArchitectureDeviations == 0 &&
		v.OutOfScopeChanges == 0 &&
		v.Blockers == 0 &&
		v.MigrationValidated &&
		v.RollbackValidated
}

type AuditEvent struct {
	WorkflowID string
	Type       string
	ActorID    int64
	Details    json.RawMessage
	CreatedAt  time.Time
}
