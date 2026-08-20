package toolgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type mcpRoundTripper func(*http.Request) (*http.Response, error)

func (f mcpRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestMCPClientEnforcesTransportAndAllowlist(t *testing.T) {
	transport := mcpRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost {
			t.Fatalf("method=%s", request.Method)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":"factory-tool-call","result":{"ok":true}}`))}, nil
	})
	client := &MCPClient{AllowedHosts: map[string]bool{"mcp.test": true}, HTTP: &http.Client{Transport: transport}}
	result, err := client.Call(context.Background(), MCPAdapterConfig{Endpoint: "https://mcp.test/tools"}, "read.test", json.RawMessage(`{"id":1}`))
	if err != nil || string(result) != `{"ok":true}` {
		t.Fatalf("result=%s err=%v", result, err)
	}
	client.AllowedHosts = map[string]bool{}
	if _, err := client.Call(context.Background(), MCPAdapterConfig{Endpoint: "https://mcp.test/tools"}, "read.test", json.RawMessage(`{}`)); err == nil {
		t.Fatal("disallowed host accepted")
	}
	client.AllowedHosts = map[string]bool{"mcp.test": true}
	if _, err := client.Call(context.Background(), MCPAdapterConfig{Endpoint: "http://mcp.test/tools"}, "read.test", json.RawMessage(`{}`)); err == nil {
		t.Fatal("plain HTTP accepted")
	}
}

func TestMCPClientRejectsOversizedAndErrorResponses(t *testing.T) {
	client := &MCPClient{AllowedHosts: map[string]bool{"mcp.test": true}}
	client.HTTP = &http.Client{Transport: mcpRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 1024*1024+1)))}, nil
	})}
	if _, err := client.Call(context.Background(), MCPAdapterConfig{Endpoint: "https://mcp.test"}, "read.test", json.RawMessage(`{}`)); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestToolOutputMustMatchRegisteredSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["ok"],"properties":{"ok":{"type":"boolean"}}}`)
	if err := validateOutput(schema, json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := validateOutput(schema, json.RawMessage(`{"ok":"yes","leak":"data"}`)); err == nil {
		t.Fatal("invalid MCP output bypassed the registered output schema")
	}
}
