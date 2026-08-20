package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type ImprovementCandidateInput struct {
	ID                  string
	CandidateType       string
	TargetKey           string
	SourceRefs          any
	ImpactScope         any
	ExpectedImprovement string
	RiskSummary         string
	RecommendedSuiteID  string
}

func (s *Store) CreateImprovementCandidate(ctx context.Context, input ImprovementCandidateInput) (string, error) {
	if input.CandidateType == "" || input.TargetKey == "" || input.ExpectedImprovement == "" || input.RiskSummary == "" || input.SourceRefs == nil {
		return "", errors.New("improvement candidate requires type, target, source references, expected improvement, and risk")
	}
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	sources, err := json.Marshal(input.SourceRefs)
	if err != nil {
		return "", err
	}
	if string(sources) == "null" || string(sources) == "[]" || string(sources) == "{}" {
		return "", errors.New("improvement candidate requires non-empty source references")
	}
	impact, err := json.Marshal(input.ImpactScope)
	if err != nil {
		return "", err
	}
	if input.ImpactScope == nil {
		impact = []byte(`{}`)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO improvement_candidates
		(id,candidate_type,target_key,source_refs,impact_scope,expected_improvement,risk_summary,recommended_suite_id,status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'CANDIDATE') ON CONFLICT (id) DO NOTHING`, input.ID, input.CandidateType,
		input.TargetKey, string(sources), string(impact), input.ExpectedImprovement, input.RiskSummary, nullableUUID(input.RecommendedSuiteID))
	return input.ID, err
}

// ProposeEvaluationImprovements turns weak evaluation dimensions into reviewable
// evidence. It deliberately cannot activate a prompt, model, profile, tool, or skill.
func (s *Store) ProposeEvaluationImprovements(ctx context.Context, runID string, threshold float64) ([]string, error) {
	if threshold <= 0 || threshold > 1 {
		return nil, errors.New("improvement threshold must be in (0,1]")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT es.dimension,AVG(es.score),COALESCE(pd.prompt_key,'evaluation-output'),er.suite_id::text
		FROM evaluation_runs er JOIN evaluation_outputs eo ON eo.evaluation_run_id=er.id
		JOIN evaluation_cases ec ON ec.id=eo.evaluation_case_id
		JOIN evaluation_scores es ON es.evaluation_output_id=eo.id
		LEFT JOIN prompt_versions pv ON pv.id=er.prompt_version_id LEFT JOIN prompt_definitions pd ON pd.id=pv.prompt_definition_id
		WHERE er.id=$1 AND er.status='COMPLETED' AND ec.data_split<>'HOLDOUT'
		GROUP BY es.dimension,pd.prompt_key,er.suite_id HAVING AVG(es.score)<$2`, runID, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var dimension, target, suiteID string
		var score float64
		if err := rows.Scan(&dimension, &score, &target, &suiteID); err != nil {
			return nil, err
		}
		id := registryID("improvement", runID+":"+dimension)
		created, err := s.CreateImprovementCandidate(ctx, ImprovementCandidateInput{
			ID: id, CandidateType: "EVALUATION_FINDING", TargetKey: target,
			SourceRefs:          map[string]any{"evaluation_run_id": runID, "dimension": dimension, "score": score, "threshold": threshold},
			ImpactScope:         map[string]any{"dimension": dimension},
			ExpectedImprovement: fmt.Sprintf("raise %s score above %.2f", dimension, threshold),
			RiskSummary:         "candidate requires independent evaluation and governed promotion before activation",
			RecommendedSuiteID:  suiteID,
		})
		if err != nil {
			return nil, err
		}
		ids = append(ids, created)
	}
	return ids, rows.Err()
}

type operationalImprovementCluster struct {
	CandidateType       string
	TargetKey           string
	SourceKind          string
	SourceIDs           []string
	ExpectedImprovement string
	RiskSummary         string
}

// ProposeOperationalImprovements clusters deterministic operational failure
// evidence. It creates reviewable candidates only and cannot activate any
// Prompt, Skill, retrieval policy, Evaluation Case, or project memory.
func (s *Store) ProposeOperationalImprovements(ctx context.Context, projectID int64, recommendedSuiteID string) ([]string, error) {
	if projectID <= 0 {
		return nil, errors.New("operational improvements require a project")
	}
	clusters := []operationalImprovementCluster{}
	appendClusters := func(query, candidateType, sourceKind, expected, risk string) error {
		rows, err := s.db.QueryContext(ctx, query, projectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var target string
			var rawIDs []byte
			if err := rows.Scan(&target, &rawIDs); err != nil {
				return err
			}
			var sourceIDs []string
			if err := json.Unmarshal(rawIDs, &sourceIDs); err != nil {
				return err
			}
			if len(sourceIDs) == 0 {
				continue
			}
			clusters = append(clusters, operationalImprovementCluster{CandidateType: candidateType,
				TargetKey: target, SourceKind: sourceKind, SourceIDs: sourceIDs,
				ExpectedImprovement: expected, RiskSummary: risk})
		}
		return rows.Err()
	}
	if err := appendClusters(`SELECT lower(g.gate_type),json_agg(gd.id::text ORDER BY gd.id)
		FROM gate_decisions gd JOIN gates g ON g.id=gd.gate_id JOIN workflows w ON w.id=g.workflow_id
		WHERE w.gitlab_project_id=$1 AND gd.action IN ('REQUEST_CHANGES','REJECT')
		AND gd.created_at>=CURRENT_TIMESTAMP-INTERVAL '90 days' GROUP BY lower(g.gate_type)`,
		"PROMPT_CHANGE", "GATE_FEEDBACK", "reduce repeated Engineer Gate change requests without weakening the Gate",
		"prompt changes can hide requirements or overfit recent reviewer feedback"); err != nil {
		return nil, err
	}
	if err := appendClusters(`SELECT lower(qf.category),json_agg(qf.id::text ORDER BY qf.id)
		FROM quality_findings qf JOIN quality_runs qr ON qr.id=qf.quality_run_id
		JOIN merge_requests mr ON mr.id=qr.merge_request_id JOIN work_items wi ON wi.id=mr.work_item_id
		JOIN workflows w ON w.id=wi.workflow_id WHERE w.gitlab_project_id=$1
		AND qf.created_at>=CURRENT_TIMESTAMP-INTERVAL '90 days' GROUP BY lower(qf.category)`,
		"SKILL_REVISION", "QUALITY_FINDING", "improve the governed review skill for recurring quality findings",
		"skill changes require independent evaluation and may introduce false positives"); err != nil {
		return nil, err
	}
	if err := appendClusters(`SELECT rr.stop_reason,json_agg(rr.id::text ORDER BY rr.id)
		FROM retrieval_runs rr JOIN workflows w ON w.id=rr.workflow_id WHERE w.gitlab_project_id=$1
		AND rr.stop_reason IN ('query_budget_exhausted','no_valid_sources')
		AND rr.started_at>=CURRENT_TIMESTAMP-INTERVAL '90 days' GROUP BY rr.stop_reason`,
		"RETRIEVAL_POLICY", "RETRIEVAL_RUN", "improve retrieval coverage while preserving project and authority filters",
		"retrieval changes can increase cost or admit lower-authority evidence"); err != nil {
		return nil, err
	}
	if err := appendClusters(`SELECT 'pipeline-failure',json_agg(pr.id::text ORDER BY pr.id)
		FROM pipeline_runs pr JOIN workflows w ON w.id=pr.workflow_id WHERE w.gitlab_project_id=$1
		AND pr.status='failed' AND pr.updated_at>=CURRENT_TIMESTAMP-INTERVAL '90 days' GROUP BY 1`,
		"EVALUATION_CASE", "PIPELINE_FAILURE", "add regression cases for recurring delivery failures",
		"evaluation cases must be sanitized and split without contaminating Holdout data"); err != nil {
		return nil, err
	}
	incidentQuery := `SELECT lower(i.severity),json_agg(i.id::text ORDER BY i.id)
		FROM incidents i JOIN workflows w ON w.id=i.workflow_id WHERE w.gitlab_project_id=$1
		AND i.created_at>=CURRENT_TIMESTAMP-INTERVAL '90 days' GROUP BY lower(i.severity)`
	if err := appendClusters(incidentQuery, "EVALUATION_CASE", "INCIDENT", "add safety and rollback regression cases from incidents",
		"incident evidence must be sanitized and must never grant production authority"); err != nil {
		return nil, err
	}
	if err := appendClusters(incidentQuery, "PROJECT_MEMORY", "INCIDENT", "propose reviewable operational lessons from incident evidence",
		"project memory can become stale or conflict with authoritative requirements and requires Engineer Approval"); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(clusters))
	for _, cluster := range clusters {
		sourceRefs := map[string]any{"project_id": projectID, "source_kind": cluster.SourceKind, "source_ids": cluster.SourceIDs,
			"window_days": 90, "cluster_key": cluster.TargetKey}
		raw, _ := json.Marshal(sourceRefs)
		digest := sha256.Sum256(raw)
		id := registryID("operational-improvement", fmt.Sprintf("%d:%s:%s:%x", projectID, cluster.CandidateType, cluster.TargetKey, digest[:]))
		created, err := s.CreateImprovementCandidate(ctx, ImprovementCandidateInput{ID: id,
			CandidateType: cluster.CandidateType, TargetKey: cluster.TargetKey, SourceRefs: sourceRefs,
			ImpactScope:         map[string]any{"project_id": projectID, "cluster": cluster.TargetKey},
			ExpectedImprovement: cluster.ExpectedImprovement, RiskSummary: cluster.RiskSummary,
			RecommendedSuiteID: recommendedSuiteID})
		if err != nil {
			return nil, err
		}
		ids = append(ids, created)
	}
	return ids, nil
}

func (s *Store) ReviewImprovementCandidate(ctx context.Context, id, decision, actor string) error {
	status := "REJECTED"
	if decision == "APPROVE" {
		status = "APPROVED"
	} else if decision != "REJECT" {
		return errors.New("improvement review decision must be APPROVE or REJECT")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE improvement_candidates SET status=$1,reviewed_by=$2,reviewed_at=CURRENT_TIMESTAMP
		WHERE id=$3 AND status='CANDIDATE'`, status, actor, id)
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
	return nil
}
