CREATE TABLE IF NOT EXISTS risk_assessments (
    id UUID PRIMARY KEY,
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    gate_id UUID NOT NULL REFERENCES gates(id),
    artifact_id UUID NOT NULL REFERENCES artifacts(id),
    risk_level VARCHAR(8) COLLATE "C" NOT NULL,
    eligible BOOLEAN NOT NULL,
    reasons JSONB NOT NULL DEFAULT '[]'::jsonb,
    policy_version VARCHAR(128) COLLATE "C" NOT NULL,
    evidence_hash CHAR(64) COLLATE "C" NOT NULL,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(gate_id,policy_version)
);

ALTER TABLE gate_decisions ADD COLUMN IF NOT EXISTS decision_source VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'HUMAN';
ALTER TABLE gate_decisions ADD COLUMN IF NOT EXISTS policy_version VARCHAR(128) COLLATE "C" NOT NULL DEFAULT '';
ALTER TABLE gate_decisions ADD COLUMN IF NOT EXISTS risk_assessment_id UUID REFERENCES risk_assessments(id);
ALTER TABLE gate_decisions ADD COLUMN IF NOT EXISTS evidence_hash CHAR(64) COLLATE "C" NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_risk_assessments_workflow ON risk_assessments(workflow_id,created_at DESC);
