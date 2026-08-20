//go:build integration

package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
)

func TestAgenticKnowledgeToolUsesAuthoritativeScopeAndTrace(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_TEST_URL is not configured")
	}
	repository, err := store.Open(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repository.BootstrapGovernance(ctx, []store.ToolSeed{{Key: "knowledge.search", DisplayName: "Search",
		RiskLevel: "L1", AdapterType: "internal", DefaultDecision: "ALLOW",
		InputSchema:  json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"}}}`),
		OutputSchema: json.RawMessage(`{"type":"array"}`)}, {Key: "memory.propose", DisplayName: "Memory",
		RiskLevel: "L2", AdapterType: "outbox", DefaultDecision: "ALLOW",
		InputSchema:  json.RawMessage(`{"type":"object","required":["key","content"],"properties":{"key":{"type":"string"},"content":{"type":"string"}}}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`)}}, nil); err != nil {
		t.Fatal(err)
	}
	projectID := time.Now().UnixNano()
	workflow, err := repository.GetOrCreateWorkflow(ctx, domain.NewWorkflow(projectID, 1, "payment retry"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.IngestKnowledge(ctx, store.KnowledgeSource{ProjectID: projectID,
		SourceType: "CONFLUENCE", SourceKey: "page-1", SourceVersion: "1", Title: "Retry policy",
		AuthorityLevel: 100, AccessScope: map[string]any{"gitlab_project_id": projectID},
		Content: "Payment retry uses exponential backoff and stops after three attempts.", ParentPath: "Retry"}); err != nil {
		t.Fatal(err)
	}
	runID, err := repository.StartAgentRun(ctx, workflow.ID, "", "REQUIREMENT", "test-model", "tool-trace")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.BeginAgentRunContext(ctx, runID); err != nil {
		t.Fatal(err)
	}
	policyID, err := repository.CreateToolPolicyCandidate(ctx, store.ToolPolicyCandidateInput{ToolKey: "knowledge.search",
		ProjectID: &projectID, AgentType: "REQUIREMENT", WorkflowState: string(workflow.State), Decision: "ALLOW", Conditions: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SubmitRegistryChangeApproval(ctx, "TOOL_POLICY", policyID, "tool-reviewer-a"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SubmitRegistryChangeApproval(ctx, "TOOL_POLICY", policyID, "tool-reviewer-b"); err != nil {
		t.Fatal(err)
	}
	if err := repository.PromoteToolPolicyCandidate(ctx, policyID, "tool-publisher"); err != nil {
		t.Fatal(err)
	}
	gateway := Gateway{Store: repository}
	output, err := gateway.Execute(ctx, Request{AgentRunID: runID, ToolKey: "knowledge.search",
		ProjectID: projectID, AgentType: "REQUIREMENT", WorkflowState: string(workflow.State),
		Input:          json.RawMessage(`{"query":"payment retry","minimum_authority":0,"limit":5}`),
		ProductionLock: true, Actor: "context-builder", AgenticRetrieval: true})
	if err != nil || !json.Valid(output) {
		t.Fatalf("agentic tool output=%s err=%v", output, err)
	}
	var hits []store.KnowledgeHit
	if err := json.Unmarshal(output, &hits); err != nil || len(hits) == 0 {
		t.Fatalf("knowledge hits=%#v err=%v", hits, err)
	}
	manifestID, err := repository.CreateContextManifest(ctx, workflow.ID, "tool-test", "v3", []store.ContextEntryInput{{
		SourceType: "KNOWLEDGE_CHUNK", SourceID: hits[0].ChunkID, AuthorityLevel: hits[0].AuthorityLevel,
		TokenCount: len(hits[0].Content), ContentHash: hits[0].ContentHash, Required: true,
		Citation: map[string]any{"document_id": hits[0].DocumentID, "source_version": hits[0].SourceVersion},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.BindAgentRunContext(ctx, runID, manifestID); err != nil {
		t.Fatal(err)
	}
	memoryOutput, err := gateway.Execute(ctx, Request{AgentRunID: runID, ToolKey: "memory.propose",
		ProjectID: projectID, AgentType: "REQUIREMENT", WorkflowState: string(workflow.State),
		Input: json.RawMessage(`{"key":"payment-retry","content":"Retry payments at most three times."}`), ProductionLock: true})
	if err != nil || !json.Valid(memoryOutput) {
		t.Fatalf("memory tool output=%s err=%v", memoryOutput, err)
	}
	var queuedMemory map[string]any
	if err := json.Unmarshal(memoryOutput, &queuedMemory); err != nil || queuedMemory["status"] != "QUEUED" || queuedMemory["message_type"] != "factory.propose_memory" {
		t.Fatalf("memory proposal was not queued output=%s err=%v", memoryOutput, err)
	}
	toolCalls, retrievalRuns, err := repository.AgentRunToolEvidence(ctx, runID)
	if err != nil || toolCalls != 2 || retrievalRuns == 0 {
		t.Fatalf("tool evidence calls=%d retrievals=%d err=%v", toolCalls, retrievalRuns, err)
	}
	_, err = gateway.Execute(ctx, Request{AgentRunID: runID, ToolKey: "knowledge.search",
		ProjectID: projectID + 1, AgentType: "ARCHITECTURE", WorkflowState: "COMPLETED",
		Input: json.RawMessage(`{"query":"payment retry"}`), ProductionLock: true})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("spoofed tool scope was accepted: %v", err)
	}
	toolCalls, _, err = repository.AgentRunToolEvidence(ctx, runID)
	if err != nil || toolCalls != 3 {
		t.Fatalf("denied spoof was not traced calls=%d err=%v", toolCalls, err)
	}
}
