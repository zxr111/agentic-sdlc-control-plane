package toolgateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
)

var ErrDenied = errors.New("tool call denied by policy")

type Gateway struct {
	Store *store.Store
	MCP   *MCPClient
}

type Request struct {
	AgentRunID     string
	ToolKey        string
	ProjectID      int64
	AgentType      string
	WorkflowState  string
	Input          json.RawMessage
	Shadow         bool
	ProductionLock bool
	GateID         string
}

type knowledgeSearchInput struct {
	Query            string `json:"query"`
	MinimumAuthority int    `json:"minimum_authority"`
	Limit            int    `json:"limit"`
}

func (g Gateway) Execute(ctx context.Context, request Request) (json.RawMessage, error) {
	if len(request.Input) == 0 || len(request.Input) > 32*1024 {
		return nil, errors.New("tool input size is invalid")
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.Input, &decoded); err != nil {
		return nil, errors.New("tool input must be valid JSON")
	}
	authorization, err := g.Store.AuthorizeToolCall(ctx, store.ToolAuthorizationRequest{
		AgentRunID: request.AgentRunID, ToolKey: request.ToolKey, ProjectID: request.ProjectID,
		AgentType: request.AgentType, WorkflowState: request.WorkflowState, Input: decoded,
		RedactedInput: decoded, Shadow: request.Shadow, ProductionLock: request.ProductionLock, GateID: request.GateID,
	})
	if err != nil {
		return nil, err
	}
	if authorization.Decision.Action != "EXECUTE" {
		return nil, ErrDenied
	}
	var output json.RawMessage
	switch request.ToolKey {
	case "knowledge.search":
		var input knowledgeSearchInput
		if err := json.Unmarshal(request.Input, &input); err != nil {
			return nil, g.fail(ctx, authorization.CallID, err)
		}
		input.Query = strings.TrimSpace(input.Query)
		if input.Query == "" || len(input.Query) > 1000 {
			return nil, g.fail(ctx, authorization.CallID, errors.New("query is required and must not exceed 1000 bytes"))
		}
		hits, err := g.Store.SearchKnowledge(ctx, request.ProjectID, input.Query, input.MinimumAuthority, input.Limit)
		if err != nil {
			return nil, g.fail(ctx, authorization.CallID, err)
		}
		output, err = json.Marshal(hits)
		if err != nil {
			return nil, g.fail(ctx, authorization.CallID, err)
		}
	default:
		if authorization.AdapterType != "mcp" || g.MCP == nil {
			return nil, g.fail(ctx, authorization.CallID, errors.New("tool adapter is not executable in-process"))
		}
		var config MCPAdapterConfig
		if err := json.Unmarshal(authorization.AdapterConfig, &config); err != nil {
			return nil, g.fail(ctx, authorization.CallID, errors.New("invalid MCP adapter configuration"))
		}
		var err error
		output, err = g.MCP.Call(ctx, config, request.ToolKey, request.Input)
		if err != nil {
			return nil, g.fail(ctx, authorization.CallID, err)
		}
	}
	digest := sha256.Sum256(output)
	if err := g.Store.FinishToolCall(ctx, authorization.CallID, "COMPLETED", hex.EncodeToString(digest[:]), nil); err != nil {
		return nil, err
	}
	return output, nil
}

func (g Gateway) fail(ctx context.Context, callID string, err error) error {
	_ = g.Store.FinishToolCall(ctx, callID, "FAILED", "", err)
	return err
}
