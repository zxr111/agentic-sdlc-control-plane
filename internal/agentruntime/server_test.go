package agentruntime

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestRuntimeEnforcesContractAndReplacesProviderCredential(t *testing.T) {
	runtime, err := New("https://provider.example/v1", "provider-secret", "internal-secret", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	var providerCalls int32
	runtime.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		atomic.AddInt32(&providerCalls, 1)
		if got := request.Header.Get("Authorization"); got != "Bearer provider-secret" {
			t.Fatalf("provider credential=%q", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"id":"response-1","status":"completed"}`))}, nil
	})}
	handler := runtime.Routes()
	valid := `{"model":"test","store":false,"max_output_tokens":100,"safety_identifier":"safe","text":{"format":{"type":"json_schema","name":"result","strict":true,"schema":{"type":"object"}}}}`
	request := httptest.NewRequest(http.MethodPost, "http://runtime/responses", strings.NewReader(valid))
	request.Header.Set("Authorization", "Bearer internal-secret")
	request.Header.Set("Idempotency-Key", "run-1:result")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	replayRequest := httptest.NewRequest(http.MethodPost, "http://runtime/responses", strings.NewReader(valid))
	replayRequest.Header.Set("Authorization", "Bearer internal-secret")
	replayRequest.Header.Set("Idempotency-Key", "run-1:result")
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusOK || replay.Header().Get("X-Agent-Runtime-Replay") != "true" || atomic.LoadInt32(&providerCalls) != 1 {
		t.Fatalf("replay status=%d calls=%d", replay.Code, providerCalls)
	}
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "http://runtime/responses", strings.NewReader(valid)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	invalidRequest := httptest.NewRequest(http.MethodPost, "http://runtime/responses", strings.NewReader(strings.Replace(valid, `"store":false`, `"store":true`, 1)))
	invalidRequest.Header.Set("Authorization", "Bearer internal-secret")
	invalidRequest.Header.Set("Idempotency-Key", "run-2:result")
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid status=%d", invalid.Code)
	}
}
