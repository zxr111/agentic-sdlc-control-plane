package store

import (
	"context"
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
