ALTER TABLE tool_policies ADD COLUMN IF NOT EXISTS version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE tool_policies ADD COLUMN IF NOT EXISTS status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'ACTIVE';
ALTER TABLE tool_policies ADD COLUMN IF NOT EXISTS approved_by VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE tool_policies ADD COLUMN IF NOT EXISTS approved_at TIMESTAMPTZ(6);

CREATE TABLE IF NOT EXISTS registry_change_approvals (
    id UUID PRIMARY KEY,
    registry_type VARCHAR(32) COLLATE "C" NOT NULL,
    candidate_id UUID NOT NULL,
    actor VARCHAR(255) COLLATE "C" NOT NULL,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(registry_type,candidate_id,actor)
);

CREATE INDEX IF NOT EXISTS idx_registry_change_approvals_candidate
    ON registry_change_approvals(registry_type,candidate_id,created_at);
