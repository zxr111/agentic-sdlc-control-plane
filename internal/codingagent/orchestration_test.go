package codingagent

import "testing"

func TestCodingFlowRoutesReviewToRepair(t *testing.T) {
	plan, err := NextTransition(StateReviewing, RoleJudge, RunCompletion{Decision: "CHANGES_REQUESTED", BlockingFindings: 1}, 0, 3)
	if err != nil || plan.ToState != StateRepairing || len(plan.NextRoles) != 1 || plan.NextRoles[0] != RoleRepair || !plan.IncrementRepair {
		t.Fatalf("unexpected plan: %#v, %v", plan, err)
	}
}

func TestCodingFlowEscalatesAfterRepairLimit(t *testing.T) {
	plan, err := NextTransition(StateReviewing, RoleJudge, RunCompletion{Decision: "CHANGES_REQUESTED"}, 3, 3)
	if err != nil || plan.ToState != StateWaitingHuman {
		t.Fatalf("unexpected plan: %#v, %v", plan, err)
	}
}

func TestQualityRequiresChecks(t *testing.T) {
	plan, err := NextTransition(StateQuality, RoleQuality, RunCompletion{Decision: "READY", ChecksPassed: false}, 0, 3)
	if err != nil || plan.ToState != StateRepairing {
		t.Fatalf("unexpected plan: %#v, %v", plan, err)
	}
}

func TestCommitSHAValidation(t *testing.T) {
	if !ValidCommitSHA("0123456789012345678901234567890123456789") || ValidCommitSHA("main") {
		t.Fatal("commit SHA validation is not exact")
	}
}
