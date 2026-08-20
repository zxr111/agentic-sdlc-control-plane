package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	runID := uuid.NewString()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO retrieval_runs
		(id,workflow_id,query_text,filters_json,strategy) VALUES ($1,$2,$3,$4,'LEXICAL_AUTHORITY_V1')`,
		runID, workflowID, query, `{"project_scoped":true}`); err != nil {
		return nil, err
	}
	hits, err := s.SearchKnowledge(ctx, projectID, query, minimumAuthority, limit)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for index, hit := range hits {
		hits[index].RerankScore = hit.LexicalScore + float64(hit.AuthorityLevel)/1000
		if _, err := tx.ExecContext(ctx, `INSERT INTO retrieval_results
			(id,retrieval_run_id,knowledge_chunk_id,rank,lexical_score,vector_score,rerank_score,selected)
			VALUES ($1,$2,$3,$4,$5,$6,$7,TRUE)`, uuid.NewString(), runID, hit.ChunkID, index+1,
			hit.LexicalScore, hit.VectorScore, hits[index].RerankScore); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE retrieval_runs SET finished_at=CURRENT_TIMESTAMP WHERE id=$1`, runID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return hits, nil
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
	if memory.ID == "" {
		memory.ID = uuid.NewString()
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	source := any(memory.SourceDocumentID)
	_, err = s.db.ExecContext(ctx, `INSERT INTO project_memories
		(id,project_id,memory_key,content,status,source_document_id,evidence_json)
		VALUES ($1,$2,$3,$4,'CANDIDATE',$5,$6)
		ON CONFLICT (project_id,memory_key) DO UPDATE SET content=EXCLUDED.content,status='CANDIDATE',
		source_document_id=EXCLUDED.source_document_id,evidence_json=EXCLUDED.evidence_json,
		approved_by='',approved_at=NULL,updated_at=CURRENT_TIMESTAMP`, memory.ID, memory.ProjectID, memory.Key,
		memory.Content, source, string(raw))
	return memory.ID, err
}

func (s *Store) ReviewProjectMemory(ctx context.Context, projectID int64, key, decision, actor string, expiresAt *time.Time) error {
	status := "REVOKED"
	if decision == "APPROVE" {
		status = "ACTIVE"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE project_memories SET status=$1,approved_by=$2,
		approved_at=CURRENT_TIMESTAMP,expires_at=$3,updated_at=CURRENT_TIMESTAMP
		WHERE project_id=$4 AND memory_key=$5 AND source_document_id IS NOT NULL
		AND status IN ('CANDIDATE','REVIEW_REQUIRED','ACTIVE')`,
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
