package knowledge

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

// Embedder allows the local replayable implementation to be replaced by an
// approved in-network embedding runtime without changing retrieval semantics.
type Embedder interface {
	EmbedDocuments(context.Context, []string) ([][]float64, error)
	EmbedQuery(context.Context, string) ([]float64, error)
	ModelVersion() string
	Dimensions() int
}

type LocalEmbedder struct{}

func (LocalEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float64, error) {
	result := make([][]float64, len(texts))
	for index, value := range texts {
		result[index] = EmbedText(value)
	}
	return result, nil
}
func (LocalEmbedder) EmbedQuery(_ context.Context, value string) ([]float64, error) {
	return EmbedText(value), nil
}
func (LocalEmbedder) ModelVersion() string { return "feature-hash-cjk-v2" }
func (LocalEmbedder) Dimensions() int      { return EmbeddingDimensions }

// HTTPEmbedder calls an OpenAI-compatible /embeddings endpoint. Deployments
// should point it at an approved credential-isolating runtime; callers never
// include its token in logs or persisted evidence.
type HTTPEmbedder struct {
	BaseURL string
	Token   string
	Model   string
	Client  *http.Client
}

func (e HTTPEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	const batchSize = 64
	result := make([][]float64, 0, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		end := min(start+batchSize, len(texts))
		batch, err := e.embed(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
	}
	return result, nil
}
func (e HTTPEmbedder) EmbedQuery(ctx context.Context, text string) ([]float64, error) {
	vectors, err := e.embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, errors.New("embedding runtime returned an unexpected vector count")
	}
	return vectors[0], nil
}
func (e HTTPEmbedder) ModelVersion() string { return e.Model }
func (e HTTPEmbedder) Dimensions() int      { return EmbeddingDimensions }

func (e HTTPEmbedder) embed(ctx context.Context, texts []string) ([][]float64, error) {
	if strings.TrimSpace(e.BaseURL) == "" || strings.TrimSpace(e.Model) == "" {
		return nil, errors.New("embedding runtime URL and model are required")
	}
	body, err := json.Marshal(map[string]any{"model": e.Model, "input": texts, "dimensions": EmbeddingDimensions})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.BaseURL, "/")+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if e.Token != "" {
		request.Header.Set("Authorization", "Bearer "+e.Token)
	}
	client := e.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("embedding runtime request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return nil, fmt.Errorf("embedding runtime returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	var envelope struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&envelope); err != nil {
		return nil, err
	}
	result := make([][]float64, len(texts))
	for _, item := range envelope.Data {
		if item.Index < 0 || item.Index >= len(result) || len(item.Embedding) != EmbeddingDimensions {
			return nil, errors.New("embedding runtime returned an invalid vector")
		}
		result[item.Index] = item.Embedding
	}
	for _, vector := range result {
		if len(vector) != EmbeddingDimensions {
			return nil, errors.New("embedding runtime omitted a vector")
		}
	}
	return result, nil
}

// Reranker is deliberately provider-neutral. Implementations must not weaken
// project, authority, source-status, or access-scope filters.
type Reranker interface {
	Rerank(context.Context, QueryPlan, []Candidate) ([]float64, error)
	ModelVersion() string
}

type Candidate struct {
	Content        string
	ParentPath     string
	AuthorityLevel int
	LexicalScore   float64
	VectorScore    float64
	FusedScore     float64
}

type DeterministicReranker struct{}

func (DeterministicReranker) ModelVersion() string { return "deterministic-overlap-v1" }
func (DeterministicReranker) Rerank(_ context.Context, plan QueryPlan, candidates []Candidate) ([]float64, error) {
	result := make([]float64, len(candidates))
	queryTokens := uniqueTokens(plan.NormalizedQuery)
	for index, candidate := range candidates {
		contentTokens := uniqueTokens(candidate.ParentPath + " " + candidate.Content)
		matched := 0
		for token := range queryTokens {
			if contentTokens[token] {
				matched++
			}
		}
		overlap := 0.0
		if len(queryTokens) > 0 {
			overlap = float64(matched) / float64(len(queryTokens))
		}
		result[index] = candidate.FusedScore + 0.20*overlap + float64(candidate.AuthorityLevel)/1000
	}
	return result, nil
}
