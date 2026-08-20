package toolgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type MCPAdapterConfig struct {
	Endpoint   string `json:"endpoint"`
	ServerName string `json:"server_name"`
}

type MCPClient struct {
	AllowedHosts      map[string]bool
	HTTP              *http.Client
	AllowHTTPForTests bool
}

func (c *MCPClient) Call(ctx context.Context, config MCPAdapterConfig, tool string, arguments json.RawMessage) (json.RawMessage, error) {
	endpoint, err := url.Parse(strings.TrimSpace(config.Endpoint))
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil {
		return nil, errors.New("invalid MCP endpoint")
	}
	if endpoint.Scheme != "https" && !(c.AllowHTTPForTests && endpoint.Scheme == "http") {
		return nil, errors.New("MCP endpoint must use HTTPS")
	}
	if !c.AllowedHosts[strings.ToLower(endpoint.Hostname())] {
		return nil, errors.New("MCP host is not allowed")
	}
	requestBody, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "factory-tool-call", "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": json.RawMessage(arguments)}})
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint.String(), bytes.NewReader(requestBody))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("MCP returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 1024*1024 {
		return nil, errors.New("MCP response exceeds 1 MiB")
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("MCP returned invalid JSON-RPC")
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		return nil, errors.New("MCP tool returned an error")
	}
	if len(envelope.Result) == 0 {
		return nil, errors.New("MCP response has no result")
	}
	return envelope.Result, nil
}
