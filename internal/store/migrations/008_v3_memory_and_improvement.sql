CREATE TABLE IF NOT EXISTS project_memory_revisions (
    id UUID PRIMARY KEY,
    project_memory_id UUID NOT NULL REFERENCES project_memories(id),
    revision INTEGER NOT NULL,
    content TEXT NOT NULL,
    status VARCHAR(24) COLLATE "C" NOT NULL,
    source_document_id UUID REFERENCES knowledge_documents(id),
    evidence_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    actor VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_memory_id,revision)
);

CREATE TABLE IF NOT EXISTS improvement_candidates (
    id UUID PRIMARY KEY,
    candidate_type VARCHAR(32) COLLATE "C" NOT NULL,
    target_key VARCHAR(128) COLLATE "C" NOT NULL,
    source_refs JSONB NOT NULL,
    impact_scope JSONB NOT NULL DEFAULT '{}'::jsonb,
    expected_improvement TEXT NOT NULL,
    risk_summary TEXT NOT NULL,
    recommended_suite_id UUID REFERENCES evaluation_suites(id),
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'CANDIDATE',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    reviewed_by VARCHAR(255) NOT NULL DEFAULT '',
    reviewed_at TIMESTAMPTZ(6)
);

CREATE INDEX IF NOT EXISTS idx_improvement_candidates_status ON improvement_candidates(status,created_at DESC);
