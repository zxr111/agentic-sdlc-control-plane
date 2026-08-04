package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	url   string
	token string
	http  *http.Client
}

func New(url, token string) *Client {
	return &Client{
		url: strings.TrimSpace(url), token: strings.TrimSpace(token),
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Configured() bool {
	return c != nil && c.url != "" && c.token != ""
}

func (c *Client) Trigger(ctx context.Context, idempotencyKey string, payload any) (string, error) {
	if !c.Configured() {
		return "", errors.New("delivery trigger is not configured")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("delivery trigger failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return "", fmt.Errorf("delivery trigger returned HTTP %d: %s",
			response.StatusCode, strings.TrimSpace(string(detail)))
	}
	externalID := response.Header.Get("X-External-ID")
	if externalID == "" {
		externalID = response.Header.Get("Location")
	}
	if externalID == "" {
		externalID = idempotencyKey
	}
	return externalID, nil
}
