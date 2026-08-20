CREATE TABLE IF NOT EXISTS evaluation_case_revisions (
    id UUID PRIMARY KEY,
    evaluation_case_id UUID NOT NULL REFERENCES evaluation_cases(id),
    revision INTEGER NOT NULL,
    input_json JSONB NOT NULL,
    expected_json JSONB NOT NULL,
    golden_evidence JSONB NOT NULL,
    data_split VARCHAR(24) COLLATE "C" NOT NULL,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(evaluation_case_id,revision)
);

ALTER TABLE evaluation_runs ADD COLUMN IF NOT EXISTS parameters_json JSONB NOT NULL DEFAULT '{}'::jsonb;
