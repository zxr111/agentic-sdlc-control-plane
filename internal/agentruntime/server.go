package agentruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxRequestBytes  = 2 << 20
	maxResponseBytes = 8 << 20
)

type Server struct {
	providerURL string
	providerKey string
	sharedKey   string
	httpClient  *http.Client
	logger      *slog.Logger
	mu          sync.Mutex
	requests    map[string]*cachedResponse
}

type cachedResponse struct {
	hash      [32]byte
	status    int
	body      []byte
	createdAt time.Time
	done      chan struct{}
}

func New(providerURL, providerKey, sharedKey string, logger *slog.Logger) (*Server, error) {
	providerURL = strings.TrimRight(strings.TrimSpace(providerURL), "/")
	if providerURL == "" || strings.TrimSpace(providerKey) == "" || strings.TrimSpace(sharedKey) == "" {
		return nil, errors.New("agent runtime requires provider URL, provider key, and shared secret")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{providerURL: providerURL, providerKey: providerKey, sharedKey: sharedKey, logger: logger,
		requests: map[string]*cachedResponse{},
		httpClient: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("provider redirects are disabled")
		}}}, nil
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("POST /responses", s.responses)
	return mux
}

type governedRequest struct {
	Model            string `json:"model"`
	Store            bool   `json:"store"`
	MaxOutputTokens  int    `json:"max_output_tokens"`
	SafetyIdentifier string `json:"safety_identifier"`
	Text             struct {
		Format struct {
			Type   string          `json:"type"`
			Name   string          `json:"name"`
			Strict bool            `json:"strict"`
			Schema json.RawMessage `json:"schema"`
		} `json:"format"`
	} `json:"text"`
}

func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		http.Error(w, "request is too large", http.StatusRequestEntityTooLarge)
		return
	}
	var request governedRequest
	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	if request.Model == "" || request.Store || request.MaxOutputTokens <= 0 || request.MaxOutputTokens > 12000 ||
		request.SafetyIdentifier == "" || request.Text.Format.Type != "json_schema" || !request.Text.Format.Strict ||
		request.Text.Format.Name == "" || len(request.Text.Format.Schema) == 0 {
		http.Error(w, "request violates Agent Runtime policy", http.StatusUnprocessableEntity)
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 256 {
		http.Error(w, "valid Idempotency-Key is required", http.StatusBadRequest)
		return
	}
	digest := sha256.Sum256(body)
	entry, owner, conflict := s.reserve(idempotencyKey, digest)
	if conflict {
		http.Error(w, "idempotency key was reused with different content", http.StatusConflict)
		return
	}
	if !owner {
		select {
		case <-r.Context().Done():
			http.Error(w, "request cancelled", http.StatusRequestTimeout)
			return
		case <-entry.done:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Agent-Runtime-Replay", "true")
			w.WriteHeader(entry.status)
			_, _ = w.Write(entry.body)
			return
		}
	}
	providerRequest, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.providerURL+"/responses", bytes.NewReader(body))
	if err != nil {
		http.Error(w, "provider request failed", http.StatusBadGateway)
		return
	}
	providerRequest.Header.Set("Authorization", "Bearer "+s.providerKey)
	providerRequest.Header.Set("Content-Type", "application/json")
	providerRequest.Header.Set("Accept", "application/json")
	providerRequest.Header.Set("Idempotency-Key", idempotencyKey)
	started := time.Now()
	response, err := s.httpClient.Do(providerRequest)
	if err != nil {
		s.release(idempotencyKey)
		s.logger.Warn("model provider request failed", "duration_ms", time.Since(started).Milliseconds(), "error", err)
		http.Error(w, "model provider unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if readErr != nil || len(responseBody) > maxResponseBytes {
		s.release(idempotencyKey)
		http.Error(w, "model provider response is invalid", http.StatusBadGateway)
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.release(idempotencyKey)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		s.complete(idempotencyKey, response.StatusCode, responseBody)
	}
}

func (s *Server) reserve(key string, hash [32]byte) (*cachedResponse, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-time.Hour)
	for candidate, entry := range s.requests {
		if entry.createdAt.Before(cutoff) {
			delete(s.requests, candidate)
		}
	}
	if entry, exists := s.requests[key]; exists {
		return entry, false, entry.hash != hash
	}
	entry := &cachedResponse{hash: hash, createdAt: time.Now(), done: make(chan struct{})}
	s.requests[key] = entry
	return entry, true, false
}

func (s *Server) complete(key string, status int, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.requests[key]; entry != nil {
		entry.status = status
		entry.body = append([]byte(nil), body...)
		close(entry.done)
	}
}

func (s *Server) release(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.requests[key]; entry != nil {
		delete(s.requests, key)
		entry.status = http.StatusServiceUnavailable
		entry.body = []byte(`{"error":"request may be retried"}`)
		close(entry.done)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(provided) != len(s.sharedKey) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.sharedKey)) == 1
}

func Shutdown(ctx context.Context, server *http.Server) error { return server.Shutdown(ctx) }
