package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPEmbedderUsesCompatibleEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/embeddings" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request path=%s", request.URL.Path)
		}
		var payload struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		data := make([]map[string]any, len(payload.Input))
		for index := range payload.Input {
			data[index] = map[string]any{"index": index, "embedding": make([]float64, EmbeddingDimensions)}
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	embedder := HTTPEmbedder{BaseURL: server.URL + "/v1", Token: "secret", Model: "approved-v1", Client: server.Client()}
	vectors, err := embedder.EmbedDocuments(context.Background(), []string{"one", "two"})
	if err != nil || len(vectors) != 2 || len(vectors[0]) != EmbeddingDimensions {
		t.Fatalf("vectors=%v err=%v", len(vectors), err)
	}
}
