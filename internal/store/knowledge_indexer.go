package store

import "context"

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
