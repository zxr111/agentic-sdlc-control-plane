package engine

import (
	"context"
	"fmt"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/evaluation"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
)

// RunPromptEvaluation replays immutable cases against a candidate Prompt in
// shadow mode. It has no path to Workflow, Gate, Outbox, Issue, MR, or deploy
// mutation methods.
func (e *Engine) RunPromptEvaluation(ctx context.Context, suiteID, promptVersionID string) (string, error) {
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
	runID, err := e.store.StartEvaluationRun(ctx, store.EvaluationRunInput{SuiteID: suiteID, PromptVersionID: promptVersionID, Shadow: true})
	if err != nil {
		return "", err
	}
	for _, testCase := range cases {
		started := time.Now()
		output, _, runErr := e.agents.GenerateCandidate(ctx, runID, prompt.Content, testCase.Input, prompt.OutputSchema)
		scores := evaluation.DeterministicScores(output, testCase.Expectations)
		if recordErr := e.store.RecordEvaluationOutput(ctx, runID, testCase.ID, output, "", time.Since(started), runErr, scores); recordErr != nil {
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
