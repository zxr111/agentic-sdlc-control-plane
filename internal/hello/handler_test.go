package hello

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHelloWorld(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)

	request := httptest.NewRequest(http.MethodGet, "/hello", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type=%q", got)
	}
	var body response
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Message != "Hello, World!" || body.Service != "ai-sdlc-factory" {
		t.Fatalf("unexpected response %#v", body)
	}
}

func TestHelloWorldRejectsMutationMethods(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)

	request := httptest.NewRequest(http.MethodPost, "/hello", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want=%d", recorder.Code, http.StatusMethodNotAllowed)
	}
	if got := recorder.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("allow=%q want=%q", got, http.MethodGet)
	}
}
