package delivery

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestTriggerUsesFixedURLAndIdempotencyKey(t *testing.T) {
	client := New("https://delivery.test/trigger", "secret")
	client.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://delivery.test/trigger" {
			t.Fatalf("unexpected URL %s", request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("missing bearer token")
		}
		if request.Header.Get("Idempotency-Key") != "workflow:release_ci" {
			t.Fatal("missing idempotency key")
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{"X-External-Id": []string{"jenkins-42"}},
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})
	externalID, err := client.Trigger(context.Background(), "workflow:release_ci", map[string]string{"action": "release_ci"})
	if err != nil {
		t.Fatal(err)
	}
	if externalID != "jenkins-42" {
		t.Fatalf("unexpected external ID %q", externalID)
	}
}
