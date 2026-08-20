package tooling

import "testing"

func TestPolicyDefaultsToDenyAndKeepsProductionLocked(t *testing.T) {
	valid := Request{ActorAllowed: true, EvidenceOK: true, BudgetOK: true}
	if got := Decide(valid); got.Action != "DENY" {
		t.Fatalf("default was not deny: %#v", got)
	}
	valid.RiskLevel, valid.ConfiguredRule, valid.HasGate = "L4", "ALLOW", true
	if got := Decide(valid); got.Action != "DENY" {
		t.Fatalf("production lock bypassed: %#v", got)
	}
	valid.RiskLevel, valid.HasGate = "L3", false
	if got := Decide(valid); got.Action != "REQUIRE_GATE" {
		t.Fatalf("gate not required: %#v", got)
	}
	valid.RiskLevel = "L2"
	if got := Decide(valid); got.Action != "OUTBOX" {
		t.Fatalf("write did not use outbox: %#v", got)
	}
	valid.RiskLevel = "L1"
	if got := Decide(valid); got.Action != "EXECUTE" {
		t.Fatalf("read was not allowed: %#v", got)
	}
	valid.ActorAllowed = false
	if got := Decide(valid); got.Action != "DENY" {
		t.Fatalf("actor condition was bypassed: %#v", got)
	}
}
