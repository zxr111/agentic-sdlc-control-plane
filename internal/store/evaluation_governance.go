package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

var ErrGovernanceRequired = errors.New("evaluation, blind review, canary, and engineer approval are required")

func (s *Store) CreateBlindReview(ctx context.Context, baselineRunID, candidateRunID string, requiredApprovals int) (string, error) {
	if requiredApprovals < 2 {
		requiredApprovals = 2
	}
	id := uuid.NewString()
	err := s.db.QueryRowContext(ctx, `INSERT INTO evaluation_blind_reviews
		(id,baseline_run_id,candidate_run_id,status,required_approvals)
		SELECT $1,$2,$3,'OPEN',$4 WHERE EXISTS(SELECT 1 FROM evaluation_runs WHERE id=$2 AND status='COMPLETED')
		AND EXISTS(SELECT 1 FROM evaluation_runs WHERE id=$3 AND status='COMPLETED')
		ON CONFLICT(baseline_run_id,candidate_run_id) DO UPDATE SET required_approvals=EXCLUDED.required_approvals
		RETURNING id`, id, baselineRunID, candidateRunID, requiredApprovals).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}

// SubmitBlindReview hashes reviewer identity before persistence and exposes no
// provider, model, prompt, or candidate labels to the reviewer.
func (s *Store) SubmitBlindReview(ctx context.Context, reviewID, reviewer, preferredSide, decision, rationale string) error {
	digest := sha256.Sum256([]byte(reviewer))
	reviewerHash := hex.EncodeToString(digest[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO evaluation_blind_submissions
		(id,blind_review_id,reviewer_hash,preferred_side,decision,rationale) VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(blind_review_id,reviewer_hash) DO UPDATE SET preferred_side=EXCLUDED.preferred_side,
		decision=EXCLUDED.decision,rationale=EXCLUDED.rationale,created_at=CURRENT_TIMESTAMP`, uuid.NewString(), reviewID,
		reviewerHash, preferredSide, decision, rationale); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE evaluation_blind_reviews r SET status=CASE
		WHEN EXISTS(SELECT 1 FROM evaluation_blind_submissions s WHERE s.blind_review_id=r.id AND s.decision='REJECT') THEN 'REJECTED'
		WHEN (SELECT count(*) FROM evaluation_blind_submissions s WHERE s.blind_review_id=r.id AND s.decision='APPROVE')>=r.required_approvals THEN 'APPROVED'
		ELSE 'OPEN' END, decided_at=CASE WHEN
		EXISTS(SELECT 1 FROM evaluation_blind_submissions s WHERE s.blind_review_id=r.id AND s.decision='REJECT') OR
		(SELECT count(*) FROM evaluation_blind_submissions s WHERE s.blind_review_id=r.id AND s.decision='APPROVE')>=r.required_approvals
		THEN CURRENT_TIMESTAMP ELSE NULL END WHERE r.id=$1 AND r.status IN('OPEN','APPROVED')`, reviewID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateCanaryRelease(ctx context.Context, candidateType, candidateVersionID, evaluationRunID, blindReviewID string,
	projectScope any, trafficPercent int) (string, error) {
	if trafficPercent <= 0 || trafficPercent > 25 {
		return "", errors.New("canary traffic must be between 1 and 25 percent")
	}
	scope, err := json.Marshal(projectScope)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	result, err := s.db.ExecContext(ctx, `INSERT INTO canary_releases(id,candidate_type,candidate_version_id,evaluation_run_id,
		blind_review_id,project_scope,traffic_percent,status) SELECT $1,$2,$3,$4,$5,$6,$7,'PENDING'
		WHERE EXISTS(SELECT 1 FROM evaluation_runs WHERE id=$4 AND status='COMPLETED' AND shadow=true)
		AND EXISTS(SELECT 1 FROM evaluation_blind_reviews WHERE id=$5 AND status='APPROVED')`, id, candidateType,
		candidateVersionID, evaluationRunID, blindReviewID, string(scope), trafficPercent)
	if err != nil {
		return "", err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if count != 1 {
		return "", ErrGovernanceRequired
	}
	return id, nil
}

func (s *Store) ApproveCanaryRelease(ctx context.Context, id, actor string, metrics any) error {
	raw, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE canary_releases SET status='APPROVED',metrics_json=$1,
		approved_by=$2,approved_at=CURRENT_TIMESTAMP WHERE id=$3 AND status='PENDING'`, string(raw), actor, id)
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
