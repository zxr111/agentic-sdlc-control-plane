package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/agents"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/evaluation"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
)

// RunPromptEvaluation replays immutable cases against a candidate Prompt in
// shadow mode. It has no path to Workflow, Gate, Outbox, Issue, MR, or deploy
// mutation methods.
func (e *Engine) RunPromptEvaluation(ctx context.Context, suiteID, promptVersionID string) (string, error) {
	return e.runEvaluation(ctx, suiteID, promptVersionID, "")
}

// RunModelEvaluation replays a fixed Prompt and case snapshot against one
// governed candidate model. Normal production routing remains unchanged.
func (e *Engine) RunModelEvaluation(ctx context.Context, suiteID, promptVersionID, modelVersionID string) (string, error) {
	if modelVersionID == "" {
		return "", errors.New("model version is required")
	}
	return e.runEvaluation(ctx, suiteID, promptVersionID, modelVersionID)
}

func (e *Engine) runEvaluation(ctx context.Context, suiteID, promptVersionID, modelVersionID string) (string, error) {
	prompt, err := e.store.PromptRuntime(ctx, promptVersionID)
	if err != nil {
		return "", err
	}
	cases, err := e.store.EvaluationCases(ctx, suiteID)
	if err != nil {
		return "", err
	}
	if len(cases) == 0 {
		return "", fmt.Errorf("evaluation suite %s has no cases", suiteID)
	}
	judge, judgeErr := e.store.ActivePromptVersion(ctx, "evaluation-judge")
	judgeEnabled := judgeErr == nil
	if judgeErr != nil && judgeErr != store.ErrNotFound {
		return "", judgeErr
	}
	var judgeRuntime store.PromptRuntime
	if judgeEnabled {
		judgeRuntime, err = e.store.PromptRuntime(ctx, judge.ID)
		if err != nil {
			return "", err
		}
	}
	models, preferredModel, allowFallback, err := e.store.ActiveRoutingModels(ctx)
	if err != nil {
		return "", err
	}
	modelSnapshot := make([]map[string]any, 0, len(models))
	for _, model := range models {
		modelSnapshot = append(modelSnapshot, map[string]any{
			"version_id": model.ID, "model_key": model.Key, "healthy": model.Healthy,
		})
	}
	parameters := map[string]any{
		"candidate_prompt_version_id":   promptVersionID,
		"candidate_content_schema_hash": prompt.ContentHash,
		"judge_enabled":                 judgeEnabled,
		"deterministic_scorer":          "deterministic-contract@v2",
		"preferred_model_key":           preferredModel,
		"allow_model_fallback":          allowFallback,
		"active_model_snapshot":         modelSnapshot,
		"case_count":                    len(cases),
	}
	var candidateModelKey string
	if modelVersionID != "" {
		candidateModel, modelErr := e.store.EvaluationModel(ctx, modelVersionID)
		if modelErr != nil {
			return "", modelErr
		}
		candidateModelKey = candidateModel.Key
		parameters["candidate_model_version_id"] = candidateModel.ID
		parameters["candidate_model_key"] = candidateModel.Key
		parameters["candidate_model_fixed"] = true
	}
	if judgeEnabled {
		parameters["judge_prompt_version_id"] = judge.ID
		parameters["judge_content_schema_hash"] = judgeRuntime.ContentHash
	}
	runID, err := e.store.StartEvaluationRun(ctx, store.EvaluationRunInput{
		SuiteID: suiteID, PromptVersionID: promptVersionID, ModelVersionID: modelVersionID, Shadow: true, Parameters: parameters,
	})
	if err != nil {
		return "", err
	}
	for _, testCase := range cases {
		started := time.Now()
		var output json.RawMessage
		var candidateTrace agents.Trace
		var runErr error
		if modelVersionID != "" {
			output, candidateTrace, runErr = e.agents.GenerateCandidateWithModel(ctx, runID, prompt.Content,
				testCase.Input, prompt.OutputSchema, modelVersionID, candidateModelKey)
		} else {
			output, candidateTrace, runErr = e.agents.GenerateCandidate(ctx, runID, prompt.Content, testCase.Input, prompt.OutputSchema)
		}
		scores := evaluation.DeterministicScores(output, testCase.Expectations)
		if runErr == nil && judgeEnabled {
			judgeInput, marshalErr := json.Marshal(map[string]any{"candidate_output": json.RawMessage(output), "expectations": testCase.Expectations})
			if marshalErr != nil {
				runErr = marshalErr
			} else {
				judgement, trace, err := e.agents.JudgeEvaluation(ctx, runID, judgeRuntime.Content, judgeInput, judgeRuntime.OutputSchema)
				if err != nil {
					runErr = fmt.Errorf("LLM judge: %w", err)
				} else {
					for _, dimension := range judgement.Dimensions {
						value := dimension.Score
						if value < 0 {
							value = 0
						}
						if value > 1 {
							value = 1
						}
						scores = append(scores, evaluation.Score{ScorerKey: "llm-judge", ScorerVersion: judge.ID,
							Dimension: dimension.Name, Value: value, Evidence: map[string]any{"reason": dimension.Evidence,
								"summary": judgement.Summary, "judge_prompt_version_id": judge.ID,
								"provider_response_id": trace.ProviderResponseID, "model": trace.SelectedModelKey}})
					}
				}
			}
		}
		if recordErr := e.store.RecordEvaluationOutputTrace(ctx, runID, testCase.ID, output, "", time.Since(started), runErr, scores,
			store.EvaluationOutputTrace{ProviderResponseID: candidateTrace.ProviderResponseID, ProviderModelID: candidateTrace.ProviderModelID,
				InputTokens: candidateTrace.InputTokens, OutputTokens: candidateTrace.OutputTokens}); recordErr != nil {
			_ = e.store.FinishEvaluationRun(ctx, runID, recordErr)
			return runID, recordErr
		}
		if runErr != nil {
			_ = e.store.FinishEvaluationRun(ctx, runID, runErr)
			return runID, runErr
		}
	}
	return runID, e.store.FinishEvaluationRun(ctx, runID, nil)
}
