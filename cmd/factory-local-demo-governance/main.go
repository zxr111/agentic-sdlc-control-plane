// Command factory-local-demo-governance materializes local-only governance
// records after two completed shadow runs. It never activates a Registry item.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
)

func main() {
	if len(os.Args) != 4 {
		slog.Error("usage: factory-local-demo-governance BASELINE_RUN CANDIDATE_RUN PROMPT_VERSION")
		os.Exit(2)
	}
	repository, err := store.Open(os.Getenv("DATABASE_URL"))
	if err != nil {
		fail(err)
	}
	defer repository.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	comparison, err := repository.CompareEvaluationRuns(ctx, os.Args[1], os.Args[2])
	if err != nil {
		fail(err)
	}
	reviewID, err := repository.CreateBlindReview(ctx, os.Args[1], os.Args[2], 2)
	if err != nil {
		fail(err)
	}
	if err := repository.SubmitBlindReview(ctx, reviewID, "local-reviewer-a", "B", "APPROVE", "local deterministic evidence accepted"); err != nil {
		fail(err)
	}
	if err := repository.SubmitBlindReview(ctx, reviewID, "local-reviewer-b", "B", "APPROVE", "local safety regression evidence accepted"); err != nil {
		fail(err)
	}
	canaryID, err := repository.CreateCanaryRelease(ctx, "PROMPT", os.Args[3], os.Args[2], reviewID, []int64{3533}, 5)
	if err != nil {
		fail(err)
	}
	if err := repository.ApproveCanaryRelease(ctx, canaryID, "local-reviewer", map[string]any{"mode": "local-demo", "comparison": comparison.Decision}); err != nil {
		fail(err)
	}
	if _, err := repository.ProposeEvaluationImprovements(ctx, os.Args[2], 1); err != nil {
		fail(err)
	}
	if _, err := repository.ProposeOperationalImprovements(ctx, 3533, ""); err != nil {
		fail(err)
	}
	slog.Info("local governance evidence created", "comparison", comparison.Decision, "blind_review_id", reviewID, "canary_id", canaryID)
}

func fail(err error) {
	slog.Error("local governance demo failed", "error", err)
	os.Exit(1)
}
