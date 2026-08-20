package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/evaluation"
	"github.com/google/uuid"
)

type EvaluationCaseInput struct {
	Key            string
	Input          any
	Expected       evaluation.Expectations
	GoldenEvidence any
	DataSplit      string
}

type EvaluationRunInput struct {
	SuiteID               string
	PromptVersionID       string
	ModelVersionID        string
	AgentProfileVersionID string
	Shadow                bool
}

func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) EnsureEvaluationSuite(ctx context.Context, key, targetAgent string, passRules any) (string, error) {
	rules, err := json.Marshal(passRules)
	if err != nil {
		return "", err
	}
	id := registryID("evaluation-suite", key)
	err = s.db.QueryRowContext(ctx, `INSERT INTO evaluation_suites(id,suite_key,target_agent_type,pass_rules,status)
		VALUES ($1,$2,$3,$4,'ACTIVE') ON CONFLICT (suite_key) DO UPDATE SET
		target_agent_type=EXCLUDED.target_agent_type,pass_rules=EXCLUDED.pass_rules RETURNING id`, id, key, targetAgent, string(rules)).Scan(&id)
	return id, err
}

func (s *Store) UpsertEvaluationCase(ctx context.Context, suiteID string, input EvaluationCaseInput) (string, error) {
	if input.DataSplit == "" {
		input.DataSplit = "TEST"
	}
	rawInput, err := json.Marshal(input.Input)
	if err != nil {
		return "", err
	}
	expected, err := json.Marshal(input.Expected)
	if err != nil {
		return "", err
	}
	evidence, err := json.Marshal(input.GoldenEvidence)
	if err != nil {
		return "", err
	}
	id := registryID("evaluation-case", suiteID+":"+input.Key)
	err = s.db.QueryRowContext(ctx, `INSERT INTO evaluation_cases
		(id,suite_id,case_key,input_json,expected_json,golden_evidence,data_split)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (suite_id,case_key) DO UPDATE SET
		input_json=EXCLUDED.input_json,expected_json=EXCLUDED.expected_json,
		golden_evidence=EXCLUDED.golden_evidence,data_split=EXCLUDED.data_split RETURNING id`,
		id, suiteID, input.Key, string(rawInput), string(expected), string(evidence), input.DataSplit).Scan(&id)
	return id, err
}

func (s *Store) StartEvaluationRun(ctx context.Context, input EvaluationRunInput) (string, error) {
	id := uuid.NewString()
	_, err := s.db.ExecContext(ctx, `INSERT INTO evaluation_runs
		(id,suite_id,prompt_version_id,model_version_id,agent_profile_version_id,status,shadow,started_at)
		VALUES ($1,$2,$3,$4,$5,'RUNNING',$6,CURRENT_TIMESTAMP)`, id, input.SuiteID,
		nullableUUID(input.PromptVersionID), nullableUUID(input.ModelVersionID), nullableUUID(input.AgentProfileVersionID), input.Shadow)
	return id, err
}

func (s *Store) RecordEvaluationOutput(ctx context.Context, runID, caseID string, output json.RawMessage,
	artifactID string, latency time.Duration, runError error, scores []evaluation.Score) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	errorSummary := ""
	if runError != nil {
		errorSummary = redactError(runError)
	}
	outputID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO evaluation_outputs
		(id,evaluation_run_id,evaluation_case_id,output_json,artifact_id,latency_ms,error_summary)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (evaluation_run_id,evaluation_case_id) DO UPDATE SET
		output_json=EXCLUDED.output_json,artifact_id=EXCLUDED.artifact_id,latency_ms=EXCLUDED.latency_ms,
		error_summary=EXCLUDED.error_summary RETURNING id`, outputID, runID, caseID, string(output),
		nullableUUID(artifactID), latency.Milliseconds(), errorSummary); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM evaluation_outputs WHERE evaluation_run_id=$1 AND evaluation_case_id=$2`,
		runID, caseID).Scan(&outputID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM evaluation_scores WHERE evaluation_output_id=$1`, outputID); err != nil {
		return err
	}
	for _, score := range scores {
		evidence, err := json.Marshal(score.Evidence)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO evaluation_scores
			(id,evaluation_output_id,scorer_key,scorer_version,dimension,score,evidence_json)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, uuid.NewString(), outputID, score.ScorerKey,
			score.ScorerVersion, score.Dimension, score.Value, string(evidence)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) FinishEvaluationRun(ctx context.Context, runID string, runError error) error {
	status := "COMPLETED"
	if runError != nil {
		status = "FAILED"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE evaluation_runs SET status=$1,finished_at=CURRENT_TIMESTAMP WHERE id=$2 AND status='RUNNING'`, status, runID)
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

type EvaluationComparison struct {
	ID        string             `json:"id"`
	Decision  string             `json:"decision"`
	Baseline  map[string]float64 `json:"baseline"`
	Candidate map[string]float64 `json:"candidate"`
}

func (s *Store) CompareEvaluationRuns(ctx context.Context, baselineRunID, candidateRunID string) (EvaluationComparison, error) {
	load := func(runID string) (map[string]float64, error) {
		rows, err := s.db.QueryContext(ctx, `SELECT es.dimension,AVG(es.score)
			FROM evaluation_scores es JOIN evaluation_outputs eo ON eo.id=es.evaluation_output_id
			JOIN evaluation_runs er ON er.id=eo.evaluation_run_id
			WHERE er.id=$1 AND er.status='COMPLETED' GROUP BY es.dimension`, runID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		result := map[string]float64{}
		for rows.Next() {
			var dimension string
			var score float64
			if err := rows.Scan(&dimension, &score); err != nil {
				return nil, err
			}
			result[dimension] = score
		}
		return result, rows.Err()
	}
	baseline, err := load(baselineRunID)
	if err != nil {
		return EvaluationComparison{}, err
	}
	candidate, err := load(candidateRunID)
	if err != nil {
		return EvaluationComparison{}, err
	}
	if len(baseline) == 0 || len(candidate) == 0 {
		return EvaluationComparison{}, errors.New("completed runs with scores are required")
	}
	decision := "PASS"
	for dimension, baselineScore := range baseline {
		candidateScore, ok := candidate[dimension]
		if !ok || candidateScore < baselineScore {
			decision = "REVIEW"
			break
		}
	}
	comparison := EvaluationComparison{ID: uuid.NewString(), Decision: decision, Baseline: baseline, Candidate: candidate}
	raw, err := json.Marshal(comparison)
	if err != nil {
		return EvaluationComparison{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO evaluation_comparisons
		(id,baseline_run_id,candidate_run_id,result_json,decision) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (baseline_run_id,candidate_run_id) DO UPDATE SET result_json=EXCLUDED.result_json,
		decision=EXCLUDED.decision,created_at=CURRENT_TIMESTAMP`, comparison.ID, baselineRunID, candidateRunID, string(raw), decision)
	if err != nil {
		return EvaluationComparison{}, err
	}
	return comparison, nil
}

func (s *Store) EvaluationCase(ctx context.Context, caseID string) (json.RawMessage, evaluation.Expectations, error) {
	var input, expected []byte
	err := s.db.QueryRowContext(ctx, `SELECT input_json,expected_json FROM evaluation_cases WHERE id=$1`, caseID).Scan(&input, &expected)
	if err == sql.ErrNoRows {
		return nil, evaluation.Expectations{}, ErrNotFound
	}
	if err != nil {
		return nil, evaluation.Expectations{}, err
	}
	var expectations evaluation.Expectations
	if err := json.Unmarshal(expected, &expectations); err != nil {
		return nil, evaluation.Expectations{}, err
	}
	return json.RawMessage(input), expectations, nil
}
