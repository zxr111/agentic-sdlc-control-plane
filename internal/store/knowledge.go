package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/knowledge"
	"github.com/google/uuid"
)

type KnowledgeSource struct {
	ProjectID      int64
	SourceType     string
	SourceKey      string
	SourceVersion  string
	Title          string
	AuthorityLevel int
	AccessScope    any
	Content        string
	ParentPath     string
}

type KnowledgeHit struct {
	ChunkID        string  `json:"chunk_id"`
	DocumentID     string  `json:"document_id"`
	SourceType     string  `json:"source_type"`
	SourceKey      string  `json:"source_key"`
	SourceVersion  string  `json:"source_version"`
	Title          string  `json:"title"`
	ParentPath     string  `json:"parent_path"`
	Content        string  `json:"content"`
	ContentHash    string  `json:"content_hash"`
	AuthorityLevel int     `json:"authority_level"`
	LexicalScore   float64 `json:"lexical_score"`
	VectorScore    float64 `json:"vector_score"`
	RerankScore    float64 `json:"rerank_score"`
}

func (s *Store) ValidateKnowledgeHits(ctx context.Context, hits []KnowledgeHit) error {
	for _, hit := range hits {
		var valid bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_chunks kc
			JOIN knowledge_versions kv ON kv.id=kc.knowledge_version_id JOIN knowledge_documents kd ON kd.id=kv.document_id
			WHERE kc.id=$1 AND kc.content_hash=$2 AND kv.source_version=$3 AND kv.status='ACTIVE' AND kd.status='ACTIVE')`,
			hit.ChunkID, hit.ContentHash, hit.SourceVersion).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return errors.New("retrieved knowledge citation is no longer valid")
		}
	}
	return nil
}

// RetrieveKnowledge performs a bounded project-scoped retrieval and records
// every selected result so the exact RAG evidence can be replayed later.
func (s *Store) RetrieveKnowledge(ctx context.Context, workflowID string, projectID int64, query string, minimumAuthority, limit int) ([]KnowledgeHit, error) {
	return s.retrieveKnowledge(ctx, workflowID, "", projectID, query, minimumAuthority, limit)
}

func (s *Store) RetrieveKnowledgeForAgentRun(ctx context.Context, workflowID, agentRunID string, projectID int64,
	query string, minimumAuthority, limit int) ([]KnowledgeHit, error) {
	return s.retrieveKnowledge(ctx, workflowID, agentRunID, projectID, query, minimumAuthority, limit)
}

func (s *Store) retrieveKnowledge(ctx context.Context, workflowID, agentRunID string, projectID int64, query string, minimumAuthority, limit int) ([]KnowledgeHit, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	queries := []string{strings.TrimSpace(query)}
	if rewritten := knowledge.RewriteQuery(query); rewritten != "" && rewritten != strings.ToLower(strings.TrimSpace(query)) {
		queries = append(queries, rewritten)
	}
	type retrievalRound struct {
		id, query string
		hits      []KnowledgeHit
	}
	rounds := make([]retrievalRound, 0, len(queries))
	var parentID string
	filters, _ := json.Marshal(map[string]any{"project_scoped": true, "minimum_authority": minimumAuthority})
	for index, currentQuery := range queries {
		runID := uuid.NewString()
		var parent any
		if parentID != "" {
			parent = parentID
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO retrieval_runs
			(id,workflow_id,agent_run_id,query_text,filters_json,strategy,iteration,parent_run_id,rewritten_from)
			VALUES ($1,$2,$3,$4,$5,'HYBRID_RRF_AGENTIC_V1',$6,$7,$8)`, runID, workflowID,
			nullableUUID(agentRunID), currentQuery, string(filters), index+1, parent, query); err != nil {
			return nil, err
		}
		hits, err := s.SearchKnowledge(ctx, projectID, currentQuery, minimumAuthority, limit*2)
		if err != nil {
			return nil, err
		}
		for hitIndex := range hits {
			hits[hitIndex].RerankScore += float64(hits[hitIndex].AuthorityLevel) / 1000
		}
		rounds = append(rounds, retrievalRound{id: runID, query: currentQuery, hits: hits})
		parentID = runID
	}
	best := map[string]KnowledgeHit{}
	for _, round := range rounds {
		for _, hit := range round.hits {
			if existing, ok := best[hit.ChunkID]; !ok || hit.RerankScore > existing.RerankScore {
				best[hit.ChunkID] = hit
			}
		}
	}
	hits := make([]KnowledgeHit, 0, len(best))
	for _, hit := range best {
		hits = append(hits, hit)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].AuthorityLevel != hits[j].AuthorityLevel {
			return hits[i].AuthorityLevel > hits[j].AuthorityLevel
		}
		return hits[i].RerankScore > hits[j].RerankScore
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	selected := map[string]bool{}
	for _, hit := range hits {
		selected[hit.ChunkID] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, round := range rounds {
		for index, hit := range round.hits {
			exclusion := "lower fused rank"
			if selected[hit.ChunkID] {
				exclusion = ""
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO retrieval_results
			(id,retrieval_run_id,knowledge_chunk_id,rank,lexical_score,vector_score,rerank_score,selected)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, uuid.NewString(), round.id, hit.ChunkID, index+1,
				hit.LexicalScore, hit.VectorScore, hit.RerankScore, selected[hit.ChunkID]); err != nil {
				return nil, err
			}
			if exclusion != "" {
				if _, err := tx.ExecContext(ctx, `UPDATE retrieval_results SET exclusion_reason=$1
					WHERE retrieval_run_id=$2 AND knowledge_chunk_id=$3`, exclusion, round.id, hit.ChunkID); err != nil {
					return nil, err
				}
			}
		}
	}
	stopReason := "query_budget_exhausted"
	if len(hits) >= limit {
		stopReason = "sufficient_results"
	} else if len(rounds) == 1 {
		stopReason = "rewrite_not_applicable"
	}
	for _, round := range rounds {
		if _, err := tx.ExecContext(ctx, `UPDATE retrieval_runs SET finished_at=CURRENT_TIMESTAMP,
			selection_reason='authority then reciprocal-rank fusion',stop_reason=$1 WHERE id=$2`, stopReason, round.id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return hits, nil
}

func (s *Store) AgentRunToolEvidence(ctx context.Context, agentRunID string) (toolCalls, retrievalRuns int, err error) {
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM tool_calls WHERE agent_run_id=$1`, agentRunID).Scan(&toolCalls); err != nil {
		return 0, 0, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM retrieval_runs WHERE agent_run_id=$1`, agentRunID).Scan(&retrievalRuns)
	return toolCalls, retrievalRuns, err
}

func (s *Store) IngestKnowledge(ctx context.Context, source KnowledgeSource) (string, bool, error) {
	if err := knowledge.ValidateDocument(source.Content); err != nil {
		return "", false, err
	}
	digest := sha256.Sum256([]byte(source.Content))
	contentHash := hex.EncodeToString(digest[:])
	scope, err := json.Marshal(source.AccessScope)
	if err != nil {
		return "", false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	documentID := uuid.NewString()
	err = tx.QueryRowContext(ctx, `INSERT INTO knowledge_documents
		(id,project_id,source_type,source_key,title,authority_level,access_scope,status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'ACTIVE')
		ON CONFLICT (project_id,source_type,source_key) DO UPDATE SET title=EXCLUDED.title,
		authority_level=EXCLUDED.authority_level,access_scope=EXCLUDED.access_scope,status='ACTIVE',updated_at=CURRENT_TIMESTAMP
		RETURNING id`, documentID, source.ProjectID, source.SourceType, source.SourceKey, source.Title,
		source.AuthorityLevel, string(scope)).Scan(&documentID)
	if err != nil {
		return "", false, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id FROM knowledge_versions WHERE document_id=$1 AND source_version=$2`,
		documentID, source.SourceVersion).Scan(&existing)
	if err == nil {
		return existing, false, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return "", false, err
	}
	versionID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_versions SET status='SUPERSEDED' WHERE document_id=$1 AND status='ACTIVE'`, documentID); err != nil {
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_versions
		(id,document_id,source_version,content_hash,raw_content,status) VALUES ($1,$2,$3,$4,$5,'ACTIVE')`,
		versionID, documentID, source.SourceVersion, contentHash, source.Content); err != nil {
		return "", false, err
	}
	for _, chunk := range knowledge.ChunkText(source.Content, source.ParentPath, 400, 50) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_chunks
			(id,knowledge_version_id,chunk_index,parent_path,content,token_count,content_hash,embedding)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::vector)`, uuid.NewString(), versionID, chunk.Index, chunk.ParentPath,
			chunk.Content, chunk.TokenCount, chunk.Hash, knowledge.VectorLiteral(knowledge.EmbedText(chunk.Content))); err != nil {
			return "", false, err
		}
	}
	return versionID, true, tx.Commit()
}

func (s *Store) RevokeKnowledgeSource(ctx context.Context, projectID int64, sourceType, sourceKey string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE knowledge_documents SET status='REVOKED',updated_at=CURRENT_TIMESTAMP
		WHERE project_id=$1 AND source_type=$2 AND source_key=$3`, projectID, sourceType, sourceKey)
	return err
}

func (s *Store) SearchKnowledge(ctx context.Context, projectID int64, query string, minimumAuthority, limit int) ([]KnowledgeHit, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	embedding := knowledge.VectorLiteral(knowledge.EmbedText(query))
	rows, err := s.db.QueryContext(ctx, `WITH candidates AS (
		SELECT kc.*,kd.id document_id,kd.source_type,kd.source_key,kd.title,kd.authority_level,kv.source_version,kv.fetched_at
		FROM knowledge_chunks kc JOIN knowledge_versions kv ON kv.id=kc.knowledge_version_id
		JOIN knowledge_documents kd ON kd.id=kv.document_id
		WHERE kd.project_id=$1 AND kd.status='ACTIVE' AND kv.status='ACTIVE' AND kd.authority_level >= $3
	), lexical AS (
		SELECT id,ts_rank_cd(search_vector,plainto_tsquery('simple',$2)) score,
		row_number() OVER (ORDER BY ts_rank_cd(search_vector,plainto_tsquery('simple',$2)) DESC) rank
		FROM candidates WHERE search_vector @@ plainto_tsquery('simple',$2) LIMIT $4
	), semantic AS (
		SELECT id,1-(embedding <=> $5::vector) score,
		row_number() OVER (ORDER BY embedding <=> $5::vector) rank
		FROM candidates WHERE embedding IS NOT NULL LIMIT $4
	), fused AS (
		SELECT id,sum(rrf) score,max(lexical_score) lexical_score,max(vector_score) vector_score FROM (
			SELECT id,1.0/(60+rank) rrf,score lexical_score,0::double precision vector_score FROM lexical
			UNION ALL SELECT id,1.0/(60+rank),0,score FROM semantic
		) ranked GROUP BY id
	)
	SELECT c.id,c.document_id,c.source_type,c.source_key,c.source_version,c.title,c.parent_path,c.content,
		c.content_hash,c.authority_level,f.lexical_score,f.vector_score,f.score
	FROM fused f JOIN candidates c ON c.id=f.id
	ORDER BY c.authority_level DESC,f.score DESC,c.fetched_at DESC LIMIT $4`,
		projectID, query, minimumAuthority, limit, embedding)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []KnowledgeHit
	for rows.Next() {
		var hit KnowledgeHit
		if err := rows.Scan(&hit.ChunkID, &hit.DocumentID, &hit.SourceType, &hit.SourceKey, &hit.SourceVersion,
			&hit.Title, &hit.ParentPath, &hit.Content, &hit.ContentHash, &hit.AuthorityLevel, &hit.LexicalScore,
			&hit.VectorScore, &hit.RerankScore); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

type ProjectMemory struct {
	ID               string
	ProjectID        int64
	Key              string
	Content          string
	Status           string
	SourceDocumentID string
}

func (s *Store) ProposeProjectMemory(ctx context.Context, memory ProjectMemory, evidence any) (string, error) {
	if memory.SourceDocumentID == "" {
		return "", errors.New("project memory requires a source document")
	}
	if err := knowledge.ValidateDocument(memory.Content); err != nil {
		return "", err
	}
	if memory.ID == "" {
		memory.ID = uuid.NewString()
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	source := any(memory.SourceDocumentID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var existingID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM project_memories WHERE project_id=$1 AND memory_key=$2`, memory.ProjectID, memory.Key).Scan(&existingID); err == nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_memory_revisions(id,project_memory_id,revision,content,status,source_document_id,evidence_json,actor)
			SELECT $1,pm.id,COALESCE((SELECT max(r.revision)+1 FROM project_memory_revisions r WHERE r.project_memory_id=pm.id),1),pm.content,pm.status,pm.source_document_id,pm.evidence_json,'agent-revision'
			FROM project_memories pm WHERE pm.id=$2`, uuid.NewString(), existingID); err != nil {
			return "", err
		}
	} else if err != sql.ErrNoRows {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO project_memories
		(id,project_id,memory_key,content,status,source_document_id,evidence_json)
		VALUES ($1,$2,$3,$4,'CANDIDATE',$5,$6)
		ON CONFLICT (project_id,memory_key) DO UPDATE SET content=EXCLUDED.content,status='CANDIDATE',
		source_document_id=EXCLUDED.source_document_id,evidence_json=EXCLUDED.evidence_json,
		approved_by='',approved_at=NULL,updated_at=CURRENT_TIMESTAMP`, memory.ID, memory.ProjectID, memory.Key,
		memory.Content, source, string(raw))
	if err != nil {
		return "", err
	}
	if existingID != "" {
		memory.ID = existingID
	}
	return memory.ID, tx.Commit()
}

func (s *Store) SubmitProjectMemoryReview(ctx context.Context, projectID int64, key, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE project_memories pm SET status='REVIEW_REQUIRED',updated_at=CURRENT_TIMESTAMP
		FROM knowledge_documents kd WHERE pm.source_document_id=kd.id AND kd.status='ACTIVE' AND pm.project_id=$1
		AND pm.memory_key=$2 AND pm.status='CANDIDATE'`, projectID, key)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_memory_revisions(id,project_memory_id,revision,content,status,source_document_id,evidence_json,actor)
		SELECT $1,pm.id,COALESCE((SELECT max(r.revision)+1 FROM project_memory_revisions r WHERE r.project_memory_id=pm.id),1),pm.content,pm.status,pm.source_document_id,pm.evidence_json,$2
		FROM project_memories pm WHERE pm.project_id=$3 AND pm.memory_key=$4`, uuid.NewString(), actor, projectID, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReviewProjectMemory(ctx context.Context, projectID int64, key, decision, actor string, expiresAt *time.Time) error {
	status := "REVOKED"
	if decision == "APPROVE" {
		status = "ACTIVE"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE project_memories SET status=$1,approved_by=$2,
		approved_at=CURRENT_TIMESTAMP,expires_at=$3,updated_at=CURRENT_TIMESTAMP
		WHERE project_id=$4 AND memory_key=$5 AND source_document_id IS NOT NULL
		AND status='REVIEW_REQUIRED'`,
		status, actor, expiresAt, projectID, key)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ExpireProjectMemories(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE project_memories SET status='EXPIRED',updated_at=CURRENT_TIMESTAMP
		WHERE status='ACTIVE' AND expires_at IS NOT NULL AND expires_at<=CURRENT_TIMESTAMP`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ActiveProjectMemories(ctx context.Context, projectID int64) ([]ProjectMemory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pm.id,pm.project_id,pm.memory_key,pm.content,pm.status,pm.source_document_id::text
		FROM project_memories pm JOIN knowledge_documents kd ON kd.id=pm.source_document_id
		WHERE pm.project_id=$1 AND pm.status='ACTIVE' AND kd.status='ACTIVE' AND (pm.expires_at IS NULL OR pm.expires_at>CURRENT_TIMESTAMP)
		ORDER BY memory_key`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ProjectMemory
	for rows.Next() {
		var memory ProjectMemory
		if err := rows.Scan(&memory.ID, &memory.ProjectID, &memory.Key, &memory.Content, &memory.Status, &memory.SourceDocumentID); err != nil {
			return nil, err
		}
		result = append(result, memory)
	}
	return result, rows.Err()
}
