package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/agents"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"github.com/google/uuid"
)

func (s *Store) SavePlannedImpacts(ctx context.Context, workflow domain.Workflow, artifactID string, design agents.SoftwareDesign) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	covered := make(map[string]bool, len(design.Services))
	for _, value := range design.Services {
		covered[value.WorkItemKey] = true
	}
	items, err := tx.QueryContext(ctx, `SELECT work_item_key FROM work_items WHERE workflow_id=$1 AND work_item_key <> '__integration'`, workflow.ID)
	if err != nil {
		return err
	}
	for items.Next() {
		var key string
		if err := items.Scan(&key); err != nil {
			items.Close()
			return err
		}
		if !covered[key] {
			items.Close()
			return fmt.Errorf("SDD does not cover work item %q", key)
		}
	}
	if err := items.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE planned_impacts SET status='SUPERSEDED' WHERE workflow_id=$1 AND status='ACTIVE'`, workflow.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ontology_relations SET status='SUPERSEDED',valid_to=CURRENT_TIMESTAMP
		WHERE source_type='SDD' AND source_id IN (SELECT id::text FROM artifacts WHERE workflow_id=$1) AND status='ACTIVE'`, workflow.ID); err != nil {
		return err
	}
	for _, value := range design.Services {
		if value.Service == "" || value.Repository == "" || value.WorkItemKey == "" {
			return fmt.Errorf("SDD service, repository, and work item key are required")
		}
		serviceID := uuid.NewString()
		if err := tx.QueryRowContext(ctx, `INSERT INTO service_catalog(id,project_id,service_key,repository,status)
			VALUES($1,$2,$3,$4,'ACTIVE') ON CONFLICT(project_id,service_key) DO UPDATE SET
			repository=EXCLUDED.repository,status='ACTIVE',updated_at=CURRENT_TIMESTAMP RETURNING id`, serviceID,
			workflow.GitLabProjectID, value.Service, value.Repository).Scan(&serviceID); err != nil {
			return err
		}
		var workItemID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM work_items WHERE workflow_id=$1 AND work_item_key=$2`, workflow.ID, value.WorkItemKey).Scan(&workItemID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("SDD references unknown work item %q", value.WorkItemKey)
			}
			return err
		}
		fields := [][]string{value.FunctionalChanges, value.Components, value.APIs, value.DataChanges,
			value.LikelyPaths, value.AcceptanceIDs, value.Verification, value.Risks}
		raw := make([]string, len(fields))
		for index := range fields {
			encoded, _ := json.Marshal(fields[index])
			raw[index] = string(encoded)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO planned_impacts
			(id,workflow_id,sdd_artifact_id,work_item_id,work_item_key,service_id,change_type,functional_changes,
			 components,apis,data_changes,likely_paths,acceptance_ids,verification,risks)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT(sdd_artifact_id,work_item_key,service_id) DO UPDATE SET status='ACTIVE',
			change_type=EXCLUDED.change_type,functional_changes=EXCLUDED.functional_changes,components=EXCLUDED.components,
			apis=EXCLUDED.apis,data_changes=EXCLUDED.data_changes,likely_paths=EXCLUDED.likely_paths,
			acceptance_ids=EXCLUDED.acceptance_ids,verification=EXCLUDED.verification,risks=EXCLUDED.risks`, uuid.NewString(), workflow.ID,
			artifactID, workItemID, value.WorkItemKey, serviceID, value.ChangeType, raw[0], raw[1], raw[2], raw[3],
			raw[4], raw[5], raw[6], raw[7]); err != nil {
			return err
		}
	}
	if err := projectSDDOntology(ctx, tx, workflow, artifactID, design); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecordActualImpact(ctx context.Context, workflowID, workItemID, commitSHA string, changedPaths []string) error {
	mr, err := s.GetMergeRequestByWorkItem(ctx, workItemID)
	if err != nil {
		return err
	}
	var projectID int64
	var workItemKey string
	if err := s.db.QueryRowContext(ctx, `SELECT w.gitlab_project_id,wi.work_item_key FROM workflows w JOIN work_items wi ON wi.workflow_id=w.id
		WHERE w.id=$1 AND wi.id=$2`, workflowID, workItemID).Scan(&projectID, &workItemKey); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changedRaw, _ := json.Marshal(changedPaths)
	rows, err := tx.QueryContext(ctx, `SELECT service_id,likely_paths,functional_changes,acceptance_ids FROM planned_impacts
		WHERE workflow_id=$1 AND work_item_id=$2 AND status='ACTIVE' ORDER BY created_at`, workflowID, workItemID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type plannedImpact struct {
		serviceID                    string
		paths, functions, acceptance []byte
	}
	var impacts []plannedImpact
	for rows.Next() {
		var impact plannedImpact
		if err := rows.Scan(&impact.serviceID, &impact.paths, &impact.functions, &impact.acceptance); err != nil {
			return err
		}
		impacts = append(impacts, impact)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(impacts) == 0 {
		return fmt.Errorf("no approved SDD impact for work item %s", workItemID)
	}
	for _, impact := range impacts {
		var planned []string
		if err := json.Unmarshal(impact.paths, &planned); err != nil {
			return err
		}
		deviations := unmatchedPaths(changedPaths, planned)
		deviationRaw, _ := json.Marshal(deviations)
		_, err = tx.ExecContext(ctx, `INSERT INTO actual_impacts
			(id,workflow_id,work_item_id,merge_request_id,service_id,commit_sha,changed_paths,planned_paths,implemented_functions,acceptance_ids,architecture_deviation,deviation_details)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(merge_request_id,commit_sha,service_id) DO UPDATE SET
			changed_paths=EXCLUDED.changed_paths,planned_paths=EXCLUDED.planned_paths,implemented_functions=EXCLUDED.implemented_functions,
			acceptance_ids=EXCLUDED.acceptance_ids,architecture_deviation=EXCLUDED.architecture_deviation,deviation_details=EXCLUDED.deviation_details`,
			uuid.NewString(), workflowID, workItemID, mr.ID, impact.serviceID, commitSHA, string(changedRaw), string(impact.paths),
			string(impact.functions), string(impact.acceptance), len(deviations) > 0, string(deviationRaw))
		if err != nil {
			return err
		}
	}
	if err := projectActualOntology(ctx, tx, projectID, workflowID, workItemKey, mr.ID, mr.GitLabMRIID, commitSHA, changedPaths); err != nil {
		return err
	}
	return tx.Commit()
}

func unmatchedPaths(changedPaths, planned []string) []string {
	var deviations []string
	for _, changed := range changedPaths {
		matched := false
		for _, pattern := range planned {
			pattern = strings.TrimSpace(strings.ReplaceAll(pattern, "**", "*"))
			if ok, _ := path.Match(pattern, changed); ok || changed == pattern || strings.HasPrefix(changed, strings.TrimSuffix(pattern, "*")) {
				matched = true
				break
			}
		}
		if !matched {
			deviations = append(deviations, changed)
		}
	}
	return deviations
}
