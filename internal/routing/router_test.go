package routing

import "testing"

func TestHighRiskDoesNotSilentlyFallback(t *testing.T) {
	models := []Model{{ID: "preferred", Active: true, Healthy: false, Quality: 10, Capabilities: map[string]bool{"structured": true}},
		{ID: "fallback", Active: true, Healthy: true, Quality: 8, Capabilities: map[string]bool{"structured": true}}}
	if _, err := Route(models, Request{PreferredModelID: "preferred", Risk: "HIGH", RequiredCapabilities: []string{"structured"}, AllowFallback: false}); err == nil {
		t.Fatal("high-risk request silently fell back")
	}
	decision, err := Route(models, Request{PreferredModelID: "preferred", Risk: "HIGH", RequiredCapabilities: []string{"structured"}, AllowFallback: true})
	if err != nil || !decision.Fallback || decision.Model.ID != "fallback" {
		t.Fatalf("controlled fallback failed %#v err=%v", decision, err)
	}
}

func TestLowRiskRouterHonorsBudgetAndCost(t *testing.T) {
	models := []Model{{ID: "a", Active: true, Healthy: true, Quality: 9, InputCost: 1000, OutputCost: 2000, Capabilities: map[string]bool{}},
		{ID: "b", Active: true, Healthy: true, Quality: 7, InputCost: 100, OutputCost: 200, Capabilities: map[string]bool{}}}
	decision, err := Route(models, Request{PreferredModelID: "missing", Risk: "LOW", EstimatedInputTokens: 1_000_000, EstimatedOutputTokens: 1_000_000, BudgetMicrounits: 500, AllowFallback: true})
	if err != nil || decision.Model.ID != "b" {
		t.Fatalf("budget route failed %#v err=%v", decision, err)
	}
}
