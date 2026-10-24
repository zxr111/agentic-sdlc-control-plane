package codingagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var commitSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)

type Role string

const (
	RolePlanner  Role = "PLANNER"
	RoleTest     Role = "TEST_DESIGNER"
	RoleCoder    Role = "CODER"
	RoleReviewer Role = "REVIEWER"
	RoleSecurity Role = "SECURITY_RELIABILITY"
	RoleJudge    Role = "JUDGE"
	RoleRepair   Role = "REPAIR"
	RoleQuality  Role = "QUALITY"
)

type CodingState string

const (
	StatePlanning     CodingState = "PLANNING"
	StateTestDesign   CodingState = "TEST_DESIGN"
	StateCoding       CodingState = "CODING"
	StateReviewing    CodingState = "REVIEWING"
	StateRepairing    CodingState = "REPAIRING"
	StateQuality      CodingState = "QUALITY_CHECKING"
	StateReadyForMR   CodingState = "READY_FOR_MR"
	StateWaitingHuman CodingState = "WAITING_HUMAN"
	StateFailed       CodingState = "FAILED"
	StateCancelled    CodingState = "CANCELLED"
)

type RunStatus string

const (
	RunPending   RunStatus = "PENDING"
	RunRunning   RunStatus = "RUNNING"
	RunCompleted RunStatus = "COMPLETED"
	RunFailed    RunStatus = "FAILED"
	RunCancelled RunStatus = "CANCELLED"
	RunStale     RunStatus = "STALE"
)

type CodingSession struct {
	ID             string      `json:"id"`
	DispatchID     string      `json:"dispatch_id"`
	ManifestHash   string      `json:"manifest_hash"`
	State          CodingState `json:"state"`
	HeadCommit     string      `json:"head_commit,omitempty"`
	RepairAttempts int         `json:"repair_attempts"`
	MaxRepairs     int         `json:"max_repairs"`
	Revision       int         `json:"revision"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

type AgentRun struct {
	ID                string          `json:"id"`
	SessionID         string          `json:"session_id"`
	Role              Role            `json:"role"`
	Status            RunStatus       `json:"status"`
	Attempt           int             `json:"attempt"`
	ManifestHash      string          `json:"manifest_hash"`
	BaseCommit        string          `json:"base_commit,omitempty"`
	HeadCommit        string          `json:"head_commit,omitempty"`
	ProviderSessionID string          `json:"provider_session_id,omitempty"`
	Input             json.RawMessage `json:"input"`
	InputHash         string          `json:"input_hash"`
	CreatedAt         time.Time       `json:"created_at"`
}

type RunCompletion struct {
	ManifestHash      string          `json:"manifest_hash"`
	ProviderSessionID string          `json:"provider_session_id"`
	Decision          string          `json:"decision"`
	ArtifactType      string          `json:"artifact_type"`
	Output            json.RawMessage `json:"output"`
	BaseCommit        string          `json:"base_commit,omitempty"`
	HeadCommit        string          `json:"head_commit,omitempty"`
	BlockingFindings  int             `json:"blocking_findings"`
	HighRisk          bool            `json:"high_risk"`
	ChecksPassed      bool            `json:"checks_passed"`
}

type RunFailure struct {
	ManifestHash      string `json:"manifest_hash"`
	ProviderSessionID string `json:"provider_session_id"`
	Code              string `json:"code"`
}

func (f RunFailure) Validate() error {
	switch f.Code {
	case "MODEL_ERROR", "TOOL_DENIED", "INVALID_OUTPUT", "CHECK_FAILED", "CANCELLED":
		return nil
	default:
		return errors.New("unsupported coding-agent failure code")
	}
}

type TransitionPlan struct {
	ToState         CodingState
	NextRoles       []Role
	Reason          string
	IncrementRepair bool
}

func NextTransition(state CodingState, role Role, completion RunCompletion, repairAttempts, maxRepairs int) (TransitionPlan, error) {
	if completion.HighRisk {
		return TransitionPlan{ToState: StateWaitingHuman, Reason: "agent result requires human review"}, nil
	}
	switch state {
	case StatePlanning:
		if role != RolePlanner {
			break
		}
		return TransitionPlan{ToState: StateTestDesign, NextRoles: []Role{RoleTest}, Reason: "implementation plan validated"}, nil
	case StateTestDesign:
		if role != RoleTest {
			break
		}
		return TransitionPlan{ToState: StateCoding, NextRoles: []Role{RoleCoder}, Reason: "test design validated"}, nil
	case StateCoding:
		if role != RoleCoder || completion.HeadCommit == "" {
			break
		}
		return TransitionPlan{ToState: StateReviewing, NextRoles: []Role{RoleReviewer, RoleSecurity}, Reason: "candidate commit produced"}, nil
	case StateReviewing:
		if role == RoleReviewer || role == RoleSecurity {
			return TransitionPlan{ToState: StateReviewing, Reason: "independent review recorded; waiting for review join"}, nil
		}
		if role == RoleJudge {
			if completion.Decision == "READY" && completion.BlockingFindings == 0 {
				return TransitionPlan{ToState: StateQuality, NextRoles: []Role{RoleQuality}, Reason: "review judge accepted exact commit"}, nil
			}
			if repairAttempts >= maxRepairs {
				return TransitionPlan{ToState: StateWaitingHuman, Reason: "repair attempt limit reached"}, nil
			}
			return TransitionPlan{ToState: StateRepairing, NextRoles: []Role{RoleRepair}, Reason: "review requires repair", IncrementRepair: true}, nil
		}
	case StateRepairing:
		if role != RoleRepair || completion.HeadCommit == "" {
			break
		}
		return TransitionPlan{ToState: StateReviewing, NextRoles: []Role{RoleReviewer, RoleSecurity}, Reason: "repair produced a new candidate commit"}, nil
	case StateQuality:
		if role != RoleQuality {
			break
		}
		if completion.Decision == "READY" && completion.ChecksPassed && completion.BlockingFindings == 0 {
			return TransitionPlan{ToState: StateReadyForMR, Reason: "quality checks passed"}, nil
		}
		if repairAttempts >= maxRepairs {
			return TransitionPlan{ToState: StateWaitingHuman, Reason: "quality failed after repair limit"}, nil
		}
		return TransitionPlan{ToState: StateRepairing, NextRoles: []Role{RoleRepair}, Reason: "quality checks require repair", IncrementRepair: true}, nil
	}
	return TransitionPlan{}, fmt.Errorf("role %s cannot complete coding state %s", role, state)
}

func ValidateRole(role Role) error {
	switch role {
	case RolePlanner, RoleTest, RoleCoder, RoleReviewer, RoleSecurity, RoleJudge, RoleRepair, RoleQuality:
		return nil
	default:
		return errors.New("invalid coding-agent role")
	}
}

func ValidCommitSHA(value string) bool { return commitSHAPattern.MatchString(value) }
