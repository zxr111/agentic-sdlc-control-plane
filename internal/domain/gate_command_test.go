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

func TestTransitions(t *testing.T) {
	if err := ValidateTransition(StateWaitingRequirementReview, StateMaterializingWorkItems); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTransition(StateWaitingRequirementReview, StateReadyForArchitecture); err == nil {
		t.Fatal("expected invalid transition")
	}
}
