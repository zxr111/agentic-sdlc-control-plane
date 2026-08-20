package tooling

import "strings"

type Request struct {
	ProjectID      int64
	AgentType      string
	WorkflowState  string
	RiskLevel      string
	ConfiguredRule string
	Shadow         bool
	ProductionLock bool
	HasGate        bool
}

type Decision struct {
	Action       string `json:"action"`
	Reason       string `json:"reason"`
	RequiresGate bool   `json:"requires_gate"`
}

func Decide(request Request) Decision {
	risk := strings.ToUpper(request.RiskLevel)
	rule := strings.ToUpper(request.ConfiguredRule)
	if request.Shadow && risk != "L0" && risk != "L1" {
		return Decision{Action: "DENY", Reason: "shadow evaluation cannot use write tools"}
	}
	if risk == "L4" && !request.ProductionLock {
		return Decision{Action: "DENY", Reason: "production lock is disabled", RequiresGate: true}
	}
	if rule == "" || rule == "DENY" {
		return Decision{Action: "DENY", Reason: "no matching allow policy"}
	}
	if risk == "L3" || risk == "L4" {
		if !request.HasGate {
			return Decision{Action: "REQUIRE_GATE", Reason: "high-risk tool requires an Engineer Gate", RequiresGate: true}
		}
		return Decision{Action: "OUTBOX", Reason: "approved high-risk operation must use transactional outbox", RequiresGate: true}
	}
	if risk == "L2" {
		return Decision{Action: "OUTBOX", Reason: "write operation must use transactional outbox"}
	}
	return Decision{Action: "EXECUTE", Reason: "read-only or local tool allowed by policy"}
}
