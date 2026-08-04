package domain

import "testing"

func TestParseGateCommand(t *testing.T) {
	const gateID = "8c2c06b2-2ca8-42dd-82fa-0d483bd4c5af"
	got, err := ParseGateCommand("/request-changes gate:" + gateID + "\nPlease define timeout behavior.")
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionRequestChanges || got.GateID != gateID || got.Feedback != "Please define timeout behavior." {
		t.Fatalf("unexpected command: %#v", got)
	}
}

func TestParseGateCommandRequiresFeedback(t *testing.T) {
	_, err := ParseGateCommand("/reject gate:8c2c06b2-2ca8-42dd-82fa-0d483bd4c5af")
	if err == nil {
		t.Fatal("expected feedback validation error")
	}
}

func TestParseGateCommandMustBeFirstNonEmptyLine(t *testing.T) {
	_, err := ParseGateCommand("Looks good\n/approve gate:8c2c06b2-2ca8-42dd-82fa-0d483bd4c5af")
	if err == nil {
		t.Fatal("natural language must not be interpreted as approval")
	}
}

func TestTransitions(t *testing.T) {
	if err := ValidateTransition(StateWaitingRequirementReview, StateMaterializingWorkItems); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTransition(StateWaitingRequirementReview, StateReadyForArchitecture); err == nil {
		t.Fatal("expected invalid transition")
	}
	fullLifecycle := []State{
		StateReadyForArchitecture, StateArchitectureGenerating, StateWaitingArchitectureReview,
		StatePlanning, StateExecutingWorkItems, StateAssemblingRelease, StateReleaseCIRunning,
		StateStagingDeploying, StateStagingVerifying, StateWaitingReleaseApproval,
		StateProductionDeploying, StateObserving, StateCompleted,
	}
	for index := 0; index < len(fullLifecycle)-1; index++ {
		if err := ValidateTransition(fullLifecycle[index], fullLifecycle[index+1]); err != nil {
			t.Fatalf("full lifecycle transition %s -> %s: %v", fullLifecycle[index], fullLifecycle[index+1], err)
		}
	}
}

func TestQualityVerdictUsesHardBlockConditions(t *testing.T) {
	passing := QualityVerdict{
		AcceptanceCoverage: 100, TestEvidenceCoverage: 100, RequiredCIPassed: true,
		MigrationValidated: true, RollbackValidated: true,
	}
	if !passing.Passes() {
		t.Fatal("expected exact passing verdict")
	}
	passing.P1Findings = 1
	if passing.Passes() {
		t.Fatal("P1 finding must block")
	}
}

func TestWorkItemTransitionsCannotSkipCodeReview(t *testing.T) {
	if err := ValidateWorkItemTransition(WorkItemWaitingCodeReview, WorkItemMergeQueued); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkItemTransition(WorkItemAIQualityChecks, WorkItemMerged); err == nil {
		t.Fatal("quality checks must not skip Engineer Code Review")
	}
}

func TestProductionProjectRequiresEveryOperationalGateReviewer(t *testing.T) {
	project := ProjectConfig{
		GitLabProjectID:   1,
		Path:              "argus/argus-server",
		FullLifecycle:     true,
		ProductionEnabled: true,
		ReviewerIDs: map[GateType][]int64{
			GateRequirement:  {1},
			GatePRD:          {1},
			GateTest:         {1},
			GateArchitecture: {1},
			GateCodeReview:   {1},
			GateRelease:      {1},
			GateIncident:     {1},
		},
	}
	if err := project.Validate(); err == nil {
		t.Fatal("production project must configure the Production Migration Gate")
	}
	project.ReviewerIDs[GateProductionMigration] = []int64{1}
	if err := project.Validate(); err != nil {
		t.Fatal(err)
	}
}
