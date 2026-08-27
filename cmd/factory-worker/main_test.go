package main

import (
	"testing"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/routing"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
)

func TestApplyModelHealthToGovernedRegistryModels(t *testing.T) {
	models := []routing.Model{{ID: "version-1", Key: "gpt-test", Active: true, Healthy: false}}
	result := applyModelHealth(models, map[string]store.ModelHealthSnapshot{"gpt-test": {Healthy: true, LatencyMS: 5}})
	if !result[0].Healthy {
		t.Fatal("latest health evidence was not applied to governed model")
	}
	if models[0].Healthy {
		t.Fatal("input model registry was mutated")
	}
}
