package tooling

import "testing"

func TestPolicyDefaultsToDenyAndKeepsProductionLocked(t *testing.T) {
	if got := Decide(Request{RiskLevel: "L1"}); got.Action != "DENY" {
		t.Fatalf("default was not deny: %#v", got)
	}
	if got := Decide(Request{RiskLevel: "L4", ConfiguredRule: "ALLOW", HasGate: true}); got.Action != "DENY" {
		t.Fatalf("production lock bypassed: %#v", got)
	}
	if got := Decide(Request{RiskLevel: "L3", ConfiguredRule: "ALLOW"}); got.Action != "REQUIRE_GATE" {
		t.Fatalf("gate not required: %#v", got)
	}
	if got := Decide(Request{RiskLevel: "L2", ConfiguredRule: "ALLOW"}); got.Action != "OUTBOX" {
		t.Fatalf("write did not use outbox: %#v", got)
	}
	if got := Decide(Request{RiskLevel: "L1", ConfiguredRule: "ALLOW"}); got.Action != "EXECUTE" {
		t.Fatalf("read was not allowed: %#v", got)
	}
}
