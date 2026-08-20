package gitlab

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

func TestGetRepositoryFileIsBoundedAndAuthenticated(t *testing.T) {
	client := New("https://gitlab.example.invalid/api/v4", "token")
	client.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("PRIVATE-TOKEN") != "token" || request.URL.Query().Get("ref") != "abc123" {
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("unauthorized")), Header: http.Header{}}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("# Design")), Header: http.Header{}}, nil
	})
	content, err := client.GetRepositoryFile(context.Background(), 7, "docs/design.md", "abc123")
	if err != nil || string(content) != "# Design" {
		t.Fatalf("content=%q err=%v", content, err)
	}
}

func TestGetRepositoryFileRejectsOversizeContent(t *testing.T) {
	client := New("https://gitlab.example.invalid/api/v4", "token")
	client.http.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (1<<20)+1))), Header: http.Header{}}, nil
	})
	if _, err := client.GetRepositoryFile(context.Background(), 7, "docs/large.md", "abc123"); err == nil {
		t.Fatal("oversize repository document was accepted")
	}
}
