package routing

import (
	"errors"
	"sort"
)

type Model struct {
	ID           string
	Key          string
	Healthy      bool
	Active       bool
	Quality      int
	InputCost    int64
	OutputCost   int64
	Capabilities map[string]bool
}

type Request struct {
	PreferredModelID      string
	Risk                  string
	RequiredCapabilities  []string
	EstimatedInputTokens  int64
	EstimatedOutputTokens int64
	BudgetMicrounits      int64
	AllowFallback         bool
}

type Decision struct {
	Model         Model
	Fallback      bool
	EstimatedCost int64
	Reason        string
}

func Route(models []Model, request Request) (Decision, error) {
	eligible := make([]Model, 0, len(models))
	for _, model := range models {
		if !model.Active || !model.Healthy || !supports(model, request.RequiredCapabilities) {
			continue
		}
		cost := estimate(model, request)
		if request.BudgetMicrounits > 0 && cost > request.BudgetMicrounits {
			continue
		}
		eligible = append(eligible, model)
	}
	if len(eligible) == 0 {
		return Decision{}, errors.New("no healthy model satisfies capability and budget policy")
	}
	for _, model := range eligible {
		if model.ID == request.PreferredModelID {
			return Decision{Model: model, EstimatedCost: estimate(model, request), Reason: "preferred model is healthy and policy-compliant"}, nil
		}
	}
	if !request.AllowFallback {
		return Decision{}, errors.New("preferred model unavailable and fallback is forbidden")
	}
	if request.Risk == "HIGH" || request.Risk == "CRITICAL" {
		sort.SliceStable(eligible, func(i, j int) bool { return eligible[i].Quality > eligible[j].Quality })
	} else {
		sort.SliceStable(eligible, func(i, j int) bool { return estimate(eligible[i], request) < estimate(eligible[j], request) })
	}
	chosen := eligible[0]
	return Decision{Model: chosen, Fallback: true, EstimatedCost: estimate(chosen, request), Reason: "controlled fallback selected by risk and budget policy"}, nil
}

func supports(model Model, required []string) bool {
	for _, capability := range required {
		if !model.Capabilities[capability] {
			return false
		}
	}
	return true
}
func estimate(model Model, request Request) int64 {
	return request.EstimatedInputTokens*model.InputCost/1_000_000 + request.EstimatedOutputTokens*model.OutputCost/1_000_000
}
