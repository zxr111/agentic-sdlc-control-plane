package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/agents"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"github.com/google/uuid"
)

type ontologyExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type ontologySource struct {
	Type, ID, Version, Hash, Authority string
	Level                              int
}

func ontologyKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func ontologyHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func upsertOntologyEntity(ctx context.Context, db ontologyExecutor, projectID int64, entityType, key, name string,
	attributes any, source ontologySource) (string, error) {
	key = ontologyKey(key)
	if key == "" || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("ontology %s entity requires key and name", entityType)
	}
	raw, err := json.Marshal(attributes)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	var revision int
	err = db.QueryRowContext(ctx, `INSERT INTO ontology_entities(id,project_id,entity_type,canonical_key,canonical_name,attributes)
		VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(project_id,entity_type,canonical_key) DO UPDATE SET
		canonical_name=EXCLUDED.canonical_name,attributes=EXCLUDED.attributes,status='ACTIVE',revision=ontology_entities.revision+1,
		updated_at=CURRENT_TIMESTAMP RETURNING id,revision`, id, projectID, entityType, key, name, string(raw)).Scan(&id, &revision)
	if err != nil {
		return "", err
	}
	if _, err := db.ExecContext(ctx, `UPDATE ontology_entity_revisions SET status='SUPERSEDED',valid_to=CURRENT_TIMESTAMP
		WHERE entity_id=$1 AND revision < $2 AND status='ACTIVE'`, id, revision); err != nil {
		return "", err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO ontology_entity_revisions
		(id,entity_id,revision,canonical_name,attributes,status,source_type,source_id,source_version,source_hash,authority_level)
		VALUES($1,$2,$3,$4,$5,'ACTIVE',$6,$7,$8,$9,$10) ON CONFLICT(entity_id,revision) DO NOTHING`,
		uuid.NewString(), id, revision, name, string(raw), source.Type, source.ID, source.Version, source.Hash, source.Level)
	return id, err
}

func upsertOntologyRelation(ctx context.Context, db ontologyExecutor, projectID int64, relationType, fromID, toID string,
	source ontologySource, attributes any) error {
	raw, err := json.Marshal(attributes)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO ontology_relations
		(id,project_id,relation_type,from_entity_id,to_entity_id,status,confidence,authority,source_type,source_id,source_version,source_hash,attributes)
		VALUES($1,$2,$3,$4,$5,'ACTIVE',1,$6,$7,$8,$9,$10,$11)
		ON CONFLICT(project_id,relation_type,from_entity_id,to_entity_id,source_type,source_id,source_version) DO UPDATE SET
		status='ACTIVE',confidence=1,authority=EXCLUDED.authority,source_hash=EXCLUDED.source_hash,attributes=EXCLUDED.attributes,valid_to=NULL`,
		uuid.NewString(), projectID, relationType, fromID, toID, source.Authority, source.Type, source.ID, source.Version, source.Hash, string(raw))
	return err
}

func (s *Store) ExpandOntologyQuery(ctx context.Context, projectID int64, query string, limit int) ([]string, error) {
	if limit <= 0 || limit > 24 {
		limit = 12
	}
	rows, err := s.db.QueryContext(ctx, `WITH matched AS (
		SELECT id FROM ontology_entities WHERE project_id=$1 AND status='ACTIVE'
		AND (search_vector @@ plainto_tsquery('simple',$2)
			OR strpos(lower($2),lower(canonical_name)) > 0 OR strpos(lower(canonical_name),lower($2)) > 0)
		AND EXISTS(SELECT 1 FROM ontology_relations r WHERE r.project_id=$1 AND r.status='ACTIVE'
			AND (r.from_entity_id=ontology_entities.id OR r.to_entity_id=ontology_entities.id)) LIMIT $3
	), related AS (
		SELECT from_entity_id id FROM ontology_relations WHERE project_id=$1 AND status='ACTIVE' AND to_entity_id IN (SELECT id FROM matched)
		UNION SELECT to_entity_id FROM ontology_relations WHERE project_id=$1 AND status='ACTIVE' AND from_entity_id IN (SELECT id FROM matched)
		UNION SELECT id FROM matched
	) SELECT DISTINCT canonical_name FROM ontology_entities WHERE status='ACTIVE' AND id IN (SELECT id FROM related) ORDER BY canonical_name LIMIT $3`,
		projectID, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func projectSDDOntology(ctx context.Context, db ontologyExecutor, workflow domain.Workflow, artifactID string,
	design agents.SoftwareDesign) error {
	source := ontologySource{Type: "SDD", ID: artifactID, Version: workflow.SourceHash,
		Hash: ontologyHash(artifactID, workflow.SourceHash), Authority: "APPROVED", Level: 90}
	for _, service := range design.Services {
		serviceID, err := upsertOntologyEntity(ctx, db, workflow.GitLabProjectID, "SERVICE", service.Service, service.Service,
			map[string]any{"change_type": service.ChangeType}, source)
		if err != nil {
			return err
		}
		repositoryID, err := upsertOntologyEntity(ctx, db, workflow.GitLabProjectID, "REPOSITORY", service.Repository, service.Repository, nil, source)
		if err != nil {
			return err
		}
		workItemID, err := upsertOntologyEntity(ctx, db, workflow.GitLabProjectID, "WORK_ITEM", workflow.ID+":"+service.WorkItemKey,
			service.WorkItemKey, map[string]any{"workflow_id": workflow.ID}, source)
		if err != nil {
			return err
		}
		if err := upsertOntologyRelation(ctx, db, workflow.GitLabProjectID, "STORED_IN", serviceID, repositoryID, source, nil); err != nil {
			return err
		}
		if err := upsertOntologyRelation(ctx, db, workflow.GitLabProjectID, "CHANGES", workItemID, serviceID, source, nil); err != nil {
			return err
		}
		features := make([]string, 0, len(service.FunctionalChanges))
		for _, name := range service.FunctionalChanges {
			id, err := upsertOntologyEntity(ctx, db, workflow.GitLabProjectID, "FEATURE", name, name, nil, source)
			if err != nil {
				return err
			}
			features = append(features, id)
			if err := upsertOntologyRelation(ctx, db, workflow.GitLabProjectID, "IMPLEMENTS_FEATURE", workItemID, id, source, nil); err != nil {
				return err
			}
		}
		for _, name := range service.Components {
			id, err := upsertOntologyEntity(ctx, db, workflow.GitLabProjectID, "COMPONENT", service.Service+":"+name, name, nil, source)
			if err != nil {
				return err
			}
			if err := upsertOntologyRelation(ctx, db, workflow.GitLabProjectID, "HAS_COMPONENT", serviceID, id, source, nil); err != nil {
				return err
			}
		}
		for _, name := range service.APIs {
			id, err := upsertOntologyEntity(ctx, db, workflow.GitLabProjectID, "API", service.Service+":"+name, name, nil, source)
			if err != nil {
				return err
			}
			if err := upsertOntologyRelation(ctx, db, workflow.GitLabProjectID, "EXPOSES", serviceID, id, source, nil); err != nil {
				return err
			}
		}
		for _, name := range service.DataChanges {
			id, err := upsertOntologyEntity(ctx, db, workflow.GitLabProjectID, "DATA_ENTITY", service.Service+":"+name, name, nil, source)
			if err != nil {
				return err
			}
			if err := upsertOntologyRelation(ctx, db, workflow.GitLabProjectID, "OWNS_DATA", serviceID, id, source, nil); err != nil {
				return err
			}
		}
		for _, name := range service.LikelyPaths {
			id, err := upsertOntologyEntity(ctx, db, workflow.GitLabProjectID, "CODE_PATH", name, name,
				map[string]any{"repository": service.Repository}, source)
			if err != nil {
				return err
			}
			if err := upsertOntologyRelation(ctx, db, workflow.GitLabProjectID, "LOCATED_AT", serviceID, id, source, nil); err != nil {
				return err
			}
		}
		for _, criterion := range service.AcceptanceIDs {
			criterionID, err := upsertOntologyEntity(ctx, db, workflow.GitLabProjectID, "ACCEPTANCE_CRITERION", workflow.ID+":"+criterion, criterion, nil, source)
			if err != nil {
				return err
			}
			for _, featureID := range features {
				if err := upsertOntologyRelation(ctx, db, workflow.GitLabProjectID, "VERIFIED_BY", featureID, criterionID, source, nil); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func projectActualOntology(ctx context.Context, db ontologyExecutor, projectID int64, workflowID, workItemKey,
	mrID string, mrIID int64, commitSHA string, changedPaths []string) error {
	source := ontologySource{Type: "MERGE_REQUEST", ID: mrID, Version: commitSHA, Hash: ontologyHash(mrID, commitSHA, strings.Join(changedPaths, "\n")), Authority: "VERIFIED", Level: 80}
	workID, err := upsertOntologyEntity(ctx, db, projectID, "WORK_ITEM", workflowID+":"+workItemKey, workItemKey, map[string]any{"workflow_id": workflowID}, source)
	if err != nil {
		return err
	}
	mrEntity, err := upsertOntologyEntity(ctx, db, projectID, "MERGE_REQUEST", fmt.Sprintf("%d", mrIID), fmt.Sprintf("MR !%d", mrIID), map[string]any{"record_id": mrID}, source)
	if err != nil {
		return err
	}
	commitID, err := upsertOntologyEntity(ctx, db, projectID, "COMMIT", commitSHA, commitSHA, nil, source)
	if err != nil {
		return err
	}
	if err := upsertOntologyRelation(ctx, db, projectID, "MR_IMPLEMENTS", mrEntity, workID, source, nil); err != nil {
		return err
	}
	if err := upsertOntologyRelation(ctx, db, projectID, "COMMIT_PART_OF", commitID, mrEntity, source, nil); err != nil {
		return err
	}
	for _, changed := range changedPaths {
		pathID, err := upsertOntologyEntity(ctx, db, projectID, "CODE_PATH", changed, changed, nil, source)
		if err != nil {
			return err
		}
		if err := upsertOntologyRelation(ctx, db, projectID, "MODIFIES", commitID, pathID, source, nil); err != nil {
			return err
		}
	}
	return nil
}
