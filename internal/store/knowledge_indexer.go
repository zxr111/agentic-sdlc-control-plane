package store

import "context"

import (
	"crypto/sha256"
	"encoding/hex"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/knowledge"
)

func (s *Store) PendingKnowledgeSources(ctx context.Context, limit int) ([]KnowledgeSource, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	result := []KnowledgeSource{}
	rows, err := s.db.QueryContext(ctx, `SELECT w.gitlab_project_id,'CONFLUENCE',ss.confluence_page_id,
		ss.source_version::text,ss.title,100,ss.normalized_text,ss.title
		FROM source_snapshots ss JOIN workflows w ON w.id=ss.workflow_id
		WHERE NOT EXISTS(SELECT 1 FROM knowledge_documents kd JOIN knowledge_versions kv ON kv.document_id=kd.id
			WHERE kd.project_id=w.gitlab_project_id AND kd.source_type='CONFLUENCE' AND kd.source_key=ss.confluence_page_id
			AND kv.source_version=ss.source_version::text)
		ORDER BY ss.created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var source KnowledgeSource
		if err := rows.Scan(&source.ProjectID, &source.SourceType, &source.SourceKey, &source.SourceVersion,
			&source.Title, &source.AuthorityLevel, &source.Content, &source.ParentPath); err != nil {
			rows.Close()
			return nil, err
		}
		source.AccessScope = map[string]any{"gitlab_project_id": source.ProjectID}
		result = append(result, source)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	remaining := limit - len(result)
	if remaining <= 0 {
		return result, nil
	}
	rows, err = s.db.QueryContext(ctx, `SELECT w.gitlab_project_id,'APPROVED_ARTIFACT',a.id::text,a.artifact_version::text,
		a.artifact_type,a.markdown||E'\n'||a.content_json::text,a.artifact_type
		FROM artifacts a JOIN workflows w ON w.id=a.workflow_id JOIN gates g ON g.artifact_id=a.id AND g.status='APPROVED'
		WHERE NOT EXISTS(SELECT 1 FROM knowledge_documents kd JOIN knowledge_versions kv ON kv.document_id=kd.id
			WHERE kd.project_id=w.gitlab_project_id AND kd.source_type='APPROVED_ARTIFACT' AND kd.source_key=a.id::text
			AND kv.source_version=a.artifact_version::text)
		ORDER BY a.generated_at LIMIT $1`, remaining)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var source KnowledgeSource
		if err := rows.Scan(&source.ProjectID, &source.SourceType, &source.SourceKey, &source.SourceVersion,
			&source.Title, &source.Content, &source.ParentPath); err != nil {
			return nil, err
		}
		source.AuthorityLevel = 90
		source.AccessScope = map[string]any{"gitlab_project_id": source.ProjectID}
		result = append(result, source)
	}
	return result, rows.Err()
}

// BackfillKnowledgeEmbeddings builds the configured model's index alongside
// existing versions. Queries select one model_version, so vector spaces are
// never mixed during a governed rollout or rollback.
func (s *Store) BackfillKnowledgeEmbeddings(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kc.id,kd.title,kc.parent_path,kc.content
		FROM knowledge_chunks kc JOIN knowledge_versions kv ON kv.id=kc.knowledge_version_id
		JOIN knowledge_documents kd ON kd.id=kv.document_id
		WHERE kd.status='ACTIVE' AND kv.status='ACTIVE' AND NOT EXISTS (
			SELECT 1 FROM knowledge_chunk_embeddings ke WHERE ke.knowledge_chunk_id=kc.id AND ke.model_version=$1)
		ORDER BY kc.id LIMIT $2`, s.knowledgeEmbedder.ModelVersion(), limit)
	if err != nil {
		return 0, err
	}
	type pending struct{ id, input string }
	var values []pending
	for rows.Next() {
		var id, title, path, content string
		if err := rows.Scan(&id, &title, &path, &content); err != nil {
			rows.Close()
			return 0, err
		}
		values = append(values, pending{id: id, input: title + "\n" + path + "\n" + content})
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	inputs := make([]string, len(values))
	for index := range values {
		inputs[index] = values[index].input
	}
	vectors, err := s.knowledgeEmbedder.EmbedDocuments(ctx, inputs)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for index, value := range values {
		digest := sha256.Sum256([]byte(value.input))
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_chunk_embeddings
			(knowledge_chunk_id,model_version,dimensions,input_hash,embedding) VALUES ($1,$2,$3,$4,$5::vector)
			ON CONFLICT (knowledge_chunk_id,model_version) DO NOTHING`, value.id, s.knowledgeEmbedder.ModelVersion(),
			s.knowledgeEmbedder.Dimensions(), hex.EncodeToString(digest[:]), knowledge.VectorLiteral(vectors[index])); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(values), nil
}
