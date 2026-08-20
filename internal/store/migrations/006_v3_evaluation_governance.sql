CREATE TABLE IF NOT EXISTS evaluation_blind_reviews (
    id UUID PRIMARY KEY,
    baseline_run_id UUID NOT NULL REFERENCES evaluation_runs(id),
    candidate_run_id UUID NOT NULL REFERENCES evaluation_runs(id),
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'OPEN',
    required_approvals INTEGER NOT NULL DEFAULT 2,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at TIMESTAMPTZ(6),
    UNIQUE (baseline_run_id, candidate_run_id)
);

CREATE TABLE IF NOT EXISTS evaluation_blind_submissions (
    id UUID PRIMARY KEY,
    blind_review_id UUID NOT NULL REFERENCES evaluation_blind_reviews(id),
    reviewer_hash CHAR(64) COLLATE "C" NOT NULL,
    preferred_side VARCHAR(8) COLLATE "C" NOT NULL,
    decision VARCHAR(24) COLLATE "C" NOT NULL,
    rationale TEXT NOT NULL,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (blind_review_id, reviewer_hash)
);

CREATE TABLE IF NOT EXISTS canary_releases (
    id UUID PRIMARY KEY,
    candidate_type VARCHAR(32) COLLATE "C" NOT NULL,
    candidate_version_id UUID NOT NULL,
    evaluation_run_id UUID NOT NULL REFERENCES evaluation_runs(id),
    blind_review_id UUID NOT NULL REFERENCES evaluation_blind_reviews(id),
    project_scope JSONB NOT NULL DEFAULT '[]'::jsonb,
    traffic_percent INTEGER NOT NULL DEFAULT 0,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'PENDING',
    metrics_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    approved_by VARCHAR(255) NOT NULL DEFAULT '',
    approved_at TIMESTAMPTZ(6),
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS registry_activation_audits (
    id UUID PRIMARY KEY,
    registry_type VARCHAR(32) COLLATE "C" NOT NULL,
    definition_key VARCHAR(128) COLLATE "C" NOT NULL,
    previous_version_id UUID,
    activated_version_id UUID NOT NULL,
    evaluation_run_id UUID REFERENCES evaluation_runs(id),
    blind_review_id UUID REFERENCES evaluation_blind_reviews(id),
    canary_release_id UUID REFERENCES canary_releases(id),
    action VARCHAR(24) COLLATE "C" NOT NULL,
    actor VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_blind_review_status ON evaluation_blind_reviews(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_canary_candidate ON canary_releases(candidate_type, candidate_version_id, created_at DESC);
