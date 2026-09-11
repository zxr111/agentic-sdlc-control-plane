package store

import (
	"context"
	"encoding/json"
	"errors"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/knowledge"
	"github.com/google/uuid"
)

type RAGEvaluationCase struct {
	ID               string
	ProjectID        int64
	Key              string
	DatasetVersion   string
	Query            string
	ExpectedChunkIDs []string
	ExpectedAnswer   string
	Metadata         any
}

type RAGEvaluationResult struct {
	ID      string
	CaseID  string
	Metrics knowledge.RetrievalMetrics
	Passed  bool
}

func (s *Store) UpsertRAGEvaluationCase(ctx context.Context, value RAGEvaluationCase) (string, error) {
	if value.ProjectID <= 0 || value.Key == "" || value.DatasetVersion == "" || value.Query == "" {
		return "", errors.New("RAG evaluation case requires project, key, dataset version, and query")
	}
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	expected, _ := json.Marshal(value.ExpectedChunkIDs)
	metadata, _ := json.Marshal(value.Metadata)
	err := s.db.QueryRowContext(ctx, `INSERT INTO rag_evaluation_cases
		(id,project_id,case_key,dataset_version,query_text,expected_chunk_ids,expected_answer,metadata_json,status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'ACTIVE')
		ON CONFLICT (project_id,dataset_version,case_key) DO UPDATE SET query_text=EXCLUDED.query_text,
		expected_chunk_ids=EXCLUDED.expected_chunk_ids,expected_answer=EXCLUDED.expected_answer,
		metadata_json=EXCLUDED.metadata_json,status='ACTIVE' RETURNING id`, value.ID, value.ProjectID, value.Key,
		value.DatasetVersion, value.Query, string(expected), value.ExpectedAnswer, string(metadata)).Scan(&value.ID)
	return value.ID, err
}

// RunRAGEvaluation executes the production retrieval path against a versioned
// case and persists replayable ranking metrics. Activation policy can require
// Passed before an embedding or reranker version is promoted.
func (s *Store) RunRAGEvaluation(ctx context.Context, caseID string, k int) (RAGEvaluationResult, error) {
	var value RAGEvaluationCase
	var expectedRaw []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,case_key,dataset_version,query_text,expected_chunk_ids,expected_answer
		FROM rag_evaluation_cases WHERE id=$1 AND status='ACTIVE'`, caseID).Scan(&value.ID, &value.ProjectID,
		&value.Key, &value.DatasetVersion, &value.Query, &expectedRaw, &value.ExpectedAnswer)
	if err != nil {
		return RAGEvaluationResult{}, err
	}
	if err := json.Unmarshal(expectedRaw, &value.ExpectedChunkIDs); err != nil {
		return RAGEvaluationResult{}, err
	}
	if k <= 0 {
		k = 10
	}
	hits, err := s.SearchKnowledge(ctx, value.ProjectID, value.Query, 0, k)
	if err != nil {
		return RAGEvaluationResult{}, err
	}
	resultIDs := make([]string, len(hits))
	for index, hit := range hits {
		resultIDs[index] = hit.ChunkID
	}
	relevant := map[string]bool{}
	for _, id := range value.ExpectedChunkIDs {
		relevant[id] = true
	}
	metrics := knowledge.EvaluateRanking(resultIDs, relevant, k)
	result := RAGEvaluationResult{ID: uuid.NewString(), CaseID: caseID, Metrics: metrics,
		Passed: metrics.RecallAtK == 1 && metrics.MRR > 0 && metrics.NDCGAtK > 0}
	configuration, _ := json.Marshal(map[string]any{"embedder": s.knowledgeEmbedder.ModelVersion(),
		"reranker": s.knowledgeReranker.ModelVersion(), "k": k})
	_, err = s.db.ExecContext(ctx, `INSERT INTO rag_evaluation_results
		(id,evaluation_case_id,configuration_json,recall_at_k,mrr,ndcg_at_k,passed,details_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, result.ID, result.CaseID, string(configuration),
		metrics.RecallAtK, metrics.MRR, metrics.NDCGAtK, result.Passed, `{"stage":"retrieval"}`)
	return result, err
}
