package codingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// PiClient talks to a Pi SDK bridge running inside an engineer-visible task.
// It is intentionally not wired into factory-worker: the control plane may
// describe and record work, but it must not start a headless coding session.
type PiClient struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

func (c PiClient) Start(ctx context.Context, request SessionRequest) (Session, error) {
	if request.Manifest.Provider != "pi" {
		return Session{}, fmt.Errorf("Pi client cannot start provider %q", request.Manifest.Provider)
	}
	if err := request.Manifest.Verify(); err != nil {
		return Session{}, err
	}
	var session Session
	if err := c.call(ctx, http.MethodPost, "/v1/sessions", request, &session); err != nil {
		return Session{}, err
	}
	if session.ID == "" || session.Provider != "pi" {
		return Session{}, errors.New("Pi bridge returned an invalid session")
	}
	return session, nil
}

func (c PiClient) Cancel(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session id is required")
	}
	return c.call(ctx, http.MethodPost, "/v1/sessions/"+sessionID+"/cancel", struct{}{}, nil)
}

func (c PiClient) call(ctx context.Context, method, path string, input, output any) error {
	if strings.TrimSpace(c.BaseURL) == "" || c.Client == nil {
		return errors.New("Pi bridge URL and HTTP client are required")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	response, err := c.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Pi bridge returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
}

var _ Provider = PiClient{}
