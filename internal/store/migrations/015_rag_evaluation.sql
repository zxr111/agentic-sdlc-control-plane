CREATE TABLE IF NOT EXISTS rag_evaluation_cases (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL,
    case_key VARCHAR(255) COLLATE "C" NOT NULL,
    dataset_version VARCHAR(64) COLLATE "C" NOT NULL,
    query_text TEXT NOT NULL,
    expected_chunk_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    expected_answer TEXT NOT NULL DEFAULT '',
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, dataset_version, case_key)
);

CREATE TABLE IF NOT EXISTS rag_evaluation_results (
    id UUID PRIMARY KEY,
    evaluation_case_id UUID NOT NULL REFERENCES rag_evaluation_cases(id),
    retrieval_run_id UUID REFERENCES retrieval_runs(id),
    configuration_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    recall_at_k DOUBLE PRECISION NOT NULL DEFAULT 0,
    mrr DOUBLE PRECISION NOT NULL DEFAULT 0,
    ndcg_at_k DOUBLE PRECISION NOT NULL DEFAULT 0,
    citation_precision DOUBLE PRECISION NOT NULL DEFAULT 0,
    citation_recall DOUBLE PRECISION NOT NULL DEFAULT 0,
    unsupported_claim_rate DOUBLE PRECISION NOT NULL DEFAULT 0,
    passed BOOLEAN NOT NULL DEFAULT FALSE,
    details_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_rag_evaluation_results_case
    ON rag_evaluation_results (evaluation_case_id, created_at DESC);
