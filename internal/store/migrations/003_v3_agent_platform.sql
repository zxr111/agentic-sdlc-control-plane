CREATE TABLE IF NOT EXISTS prompt_definitions (
    id UUID PRIMARY KEY,
    prompt_key VARCHAR(128) COLLATE "C" NOT NULL UNIQUE,
    display_name VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS prompt_versions (
    id UUID PRIMARY KEY,
    prompt_definition_id UUID NOT NULL REFERENCES prompt_definitions(id),
    version INTEGER NOT NULL,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'DRAFT',
    content TEXT NOT NULL,
    output_schema JSONB NOT NULL DEFAULT '{}'::jsonb,
    content_hash CHAR(64) COLLATE "C" NOT NULL,
    created_by VARCHAR(255) NOT NULL DEFAULT '',
    approved_by VARCHAR(255) NOT NULL DEFAULT '',
    approved_at TIMESTAMPTZ(6),
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (prompt_definition_id, version),
    UNIQUE (prompt_definition_id, content_hash)
);

CREATE TABLE IF NOT EXISTS model_providers (
    id UUID PRIMARY KEY,
    provider_key VARCHAR(64) COLLATE "C" NOT NULL UNIQUE,
    display_name VARCHAR(255) NOT NULL,
    base_url VARCHAR(2048) NOT NULL DEFAULT '',
    secret_reference VARCHAR(512) NOT NULL DEFAULT '',
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS model_versions (
    id UUID PRIMARY KEY,
    provider_id UUID NOT NULL REFERENCES model_providers(id),
    model_key VARCHAR(128) COLLATE "C" NOT NULL,
    capabilities JSONB NOT NULL DEFAULT '{}'::jsonb,
    input_cost_microunits BIGINT NOT NULL DEFAULT 0,
    output_cost_microunits BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (provider_id, model_key)
);

CREATE TABLE IF NOT EXISTS model_policies (
    id UUID PRIMARY KEY,
    policy_key VARCHAR(128) COLLATE "C" NOT NULL UNIQUE,
    rules_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    fallback_model_version_id UUID REFERENCES model_versions(id),
    allow_fallback BOOLEAN NOT NULL DEFAULT FALSE,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS skill_definitions (
    id UUID PRIMARY KEY,
    skill_key VARCHAR(128) COLLATE "C" NOT NULL UNIQUE,
    display_name VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS skill_versions (
    id UUID PRIMARY KEY,
    skill_definition_id UUID NOT NULL REFERENCES skill_definitions(id),
    version INTEGER NOT NULL,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'DRAFT',
    instructions TEXT NOT NULL,
    trigger_rules JSONB NOT NULL DEFAULT '{}'::jsonb,
    scope_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    content_hash CHAR(64) COLLATE "C" NOT NULL,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (skill_definition_id, version)
);

CREATE TABLE IF NOT EXISTS agent_profiles (
    id UUID PRIMARY KEY,
    profile_key VARCHAR(128) COLLATE "C" NOT NULL UNIQUE,
    display_name VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS agent_profile_versions (
    id UUID PRIMARY KEY,
    agent_profile_id UUID NOT NULL REFERENCES agent_profiles(id),
    version INTEGER NOT NULL,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'DRAFT',
    prompt_version_id UUID NOT NULL REFERENCES prompt_versions(id),
    model_policy_id UUID REFERENCES model_policies(id),
    context_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
    tool_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
    skill_version_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    budget_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (agent_profile_id, version)
);

CREATE TABLE IF NOT EXISTS context_manifests (
    id UUID PRIMARY KEY,
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    purpose VARCHAR(128) COLLATE "C" NOT NULL,
    policy_version VARCHAR(64) COLLATE "C" NOT NULL DEFAULT '',
    total_tokens INTEGER NOT NULL DEFAULT 0,
    content_hash CHAR(64) COLLATE "C" NOT NULL,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS knowledge_documents (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL,
    source_type VARCHAR(32) COLLATE "C" NOT NULL,
    source_key VARCHAR(2048) COLLATE "C" NOT NULL,
    title VARCHAR(1024) NOT NULL DEFAULT '',
    authority_level INTEGER NOT NULL DEFAULT 0,
    access_scope JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, source_type, source_key)
);

CREATE TABLE IF NOT EXISTS knowledge_versions (
    id UUID PRIMARY KEY,
    document_id UUID NOT NULL REFERENCES knowledge_documents(id),
    source_version VARCHAR(255) COLLATE "C" NOT NULL,
    content_hash CHAR(64) COLLATE "C" NOT NULL,
    raw_content TEXT NOT NULL,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'ACTIVE',
    fetched_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (document_id, source_version)
);

CREATE TABLE IF NOT EXISTS knowledge_chunks (
    id UUID PRIMARY KEY,
    knowledge_version_id UUID NOT NULL REFERENCES knowledge_versions(id),
    chunk_index INTEGER NOT NULL,
    parent_path VARCHAR(2048) NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    token_count INTEGER NOT NULL DEFAULT 0,
    content_hash CHAR(64) COLLATE "C" NOT NULL,
    embedding_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    search_vector TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED,
    UNIQUE (knowledge_version_id, chunk_index)
);

CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_search ON knowledge_chunks USING GIN (search_vector);

CREATE TABLE IF NOT EXISTS context_entries (
    id UUID PRIMARY KEY,
    context_manifest_id UUID NOT NULL REFERENCES context_manifests(id),
    ordinal INTEGER NOT NULL,
    source_type VARCHAR(32) COLLATE "C" NOT NULL,
    source_id UUID,
    knowledge_chunk_id UUID REFERENCES knowledge_chunks(id),
    authority_level INTEGER NOT NULL DEFAULT 0,
    compression_method VARCHAR(64) COLLATE "C" NOT NULL DEFAULT 'none',
    token_count INTEGER NOT NULL DEFAULT 0,
    content_hash CHAR(64) COLLATE "C" NOT NULL,
    citation_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (context_manifest_id, ordinal)
);

CREATE TABLE IF NOT EXISTS retrieval_runs (
    id UUID PRIMARY KEY,
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    query_text TEXT NOT NULL,
    filters_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    strategy VARCHAR(64) COLLATE "C" NOT NULL,
    started_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMPTZ(6)
);

CREATE TABLE IF NOT EXISTS retrieval_results (
    id UUID PRIMARY KEY,
    retrieval_run_id UUID NOT NULL REFERENCES retrieval_runs(id),
    knowledge_chunk_id UUID NOT NULL REFERENCES knowledge_chunks(id),
    rank INTEGER NOT NULL,
    lexical_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    vector_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    rerank_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    selected BOOLEAN NOT NULL DEFAULT FALSE,
    exclusion_reason TEXT NOT NULL DEFAULT '',
    UNIQUE (retrieval_run_id, knowledge_chunk_id)
);

CREATE TABLE IF NOT EXISTS project_memories (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL,
    memory_key VARCHAR(255) COLLATE "C" NOT NULL,
    content TEXT NOT NULL,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'CANDIDATE',
    source_document_id UUID REFERENCES knowledge_documents(id),
    evidence_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    approved_by VARCHAR(255) NOT NULL DEFAULT '',
    approved_at TIMESTAMPTZ(6),
    expires_at TIMESTAMPTZ(6),
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, memory_key)
);

CREATE TABLE IF NOT EXISTS tool_definitions (
    id UUID PRIMARY KEY,
    tool_key VARCHAR(128) COLLATE "C" NOT NULL UNIQUE,
    display_name VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS tool_versions (
    id UUID PRIMARY KEY,
    tool_definition_id UUID NOT NULL REFERENCES tool_definitions(id),
    version INTEGER NOT NULL,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'DRAFT',
    input_schema JSONB NOT NULL DEFAULT '{}'::jsonb,
    output_schema JSONB NOT NULL DEFAULT '{}'::jsonb,
    risk_level VARCHAR(16) COLLATE "C" NOT NULL DEFAULT 'LOW',
    adapter_type VARCHAR(64) COLLATE "C" NOT NULL,
    adapter_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    content_hash CHAR(64) COLLATE "C" NOT NULL,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (tool_definition_id, version)
);

CREATE TABLE IF NOT EXISTS tool_policies (
    id UUID PRIMARY KEY,
    tool_version_id UUID NOT NULL REFERENCES tool_versions(id),
    project_id BIGINT,
    agent_type VARCHAR(64) COLLATE "C" NOT NULL DEFAULT '*',
    workflow_state VARCHAR(64) COLLATE "C" NOT NULL DEFAULT '*',
    decision VARCHAR(16) COLLATE "C" NOT NULL DEFAULT 'DENY',
    requires_gate BOOLEAN NOT NULL DEFAULT FALSE,
    conditions_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS evaluation_suites (
    id UUID PRIMARY KEY,
    suite_key VARCHAR(128) COLLATE "C" NOT NULL UNIQUE,
    target_agent_type VARCHAR(64) COLLATE "C" NOT NULL,
    pass_rules JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS evaluation_cases (
    id UUID PRIMARY KEY,
    suite_id UUID NOT NULL REFERENCES evaluation_suites(id),
    case_key VARCHAR(128) COLLATE "C" NOT NULL,
    input_json JSONB NOT NULL,
    expected_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    golden_evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
    data_split VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'TEST',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (suite_id, case_key)
);

CREATE TABLE IF NOT EXISTS evaluation_runs (
    id UUID PRIMARY KEY,
    suite_id UUID NOT NULL REFERENCES evaluation_suites(id),
    prompt_version_id UUID REFERENCES prompt_versions(id),
    model_version_id UUID REFERENCES model_versions(id),
    agent_profile_version_id UUID REFERENCES agent_profile_versions(id),
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'QUEUED',
    shadow BOOLEAN NOT NULL DEFAULT TRUE,
    started_at TIMESTAMPTZ(6),
    finished_at TIMESTAMPTZ(6),
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS evaluation_outputs (
    id UUID PRIMARY KEY,
    evaluation_run_id UUID NOT NULL REFERENCES evaluation_runs(id),
    evaluation_case_id UUID NOT NULL REFERENCES evaluation_cases(id),
    output_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    artifact_id UUID REFERENCES artifacts(id),
    latency_ms BIGINT NOT NULL DEFAULT 0,
    error_summary TEXT NOT NULL DEFAULT '',
    UNIQUE (evaluation_run_id, evaluation_case_id)
);

CREATE TABLE IF NOT EXISTS evaluation_scores (
    id UUID PRIMARY KEY,
    evaluation_output_id UUID NOT NULL REFERENCES evaluation_outputs(id),
    scorer_key VARCHAR(128) COLLATE "C" NOT NULL,
    scorer_version VARCHAR(64) COLLATE "C" NOT NULL,
    dimension VARCHAR(128) COLLATE "C" NOT NULL,
    score DOUBLE PRECISION NOT NULL,
    evidence_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS evaluation_comparisons (
    id UUID PRIMARY KEY,
    baseline_run_id UUID NOT NULL REFERENCES evaluation_runs(id),
    candidate_run_id UUID NOT NULL REFERENCES evaluation_runs(id),
    result_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    decision VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'REVIEW',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (baseline_run_id, candidate_run_id)
);

ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS agent_profile_version_id UUID REFERENCES agent_profile_versions(id);
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS prompt_version_id UUID REFERENCES prompt_versions(id);
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS model_version_id UUID REFERENCES model_versions(id);
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS context_manifest_id UUID REFERENCES context_manifests(id);
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS provider_response_id VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS input_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS cached_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS output_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS reasoning_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS estimated_cost_microunits BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS latency_ms BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS finish_reason VARCHAR(64) COLLATE "C" NOT NULL DEFAULT '';
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS cancel_requested_at TIMESTAMPTZ(6);

CREATE TABLE IF NOT EXISTS agent_steps (
    id UUID PRIMARY KEY,
    agent_run_id UUID NOT NULL REFERENCES agent_runs(id),
    ordinal INTEGER NOT NULL,
    step_type VARCHAR(64) COLLATE "C" NOT NULL,
    status VARCHAR(24) COLLATE "C" NOT NULL,
    input_hash CHAR(64) COLLATE "C" NOT NULL DEFAULT '',
    output_hash CHAR(64) COLLATE "C" NOT NULL DEFAULT '',
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMPTZ(6),
    UNIQUE (agent_run_id, ordinal)
);

CREATE TABLE IF NOT EXISTS agent_opinions (
    id UUID PRIMARY KEY,
    agent_run_id UUID NOT NULL REFERENCES agent_runs(id),
    role VARCHAR(64) COLLATE "C" NOT NULL,
    decision VARCHAR(32) COLLATE "C" NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    summary TEXT NOT NULL,
    findings_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    evidence_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    minority BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS tool_calls (
    id UUID PRIMARY KEY,
    agent_run_id UUID NOT NULL REFERENCES agent_runs(id),
    agent_step_id UUID REFERENCES agent_steps(id),
    tool_version_id UUID NOT NULL REFERENCES tool_versions(id),
    input_hash CHAR(64) COLLATE "C" NOT NULL,
    redacted_input_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    policy_decision VARCHAR(24) COLLATE "C" NOT NULL,
    gate_id UUID REFERENCES gates(id),
    result_hash CHAR(64) COLLATE "C" NOT NULL DEFAULT '',
    status VARCHAR(24) COLLATE "C" NOT NULL,
    error_summary TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMPTZ(6)
);

CREATE INDEX IF NOT EXISTS idx_agent_runs_profile ON agent_runs (agent_profile_version_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_context_manifests_workflow ON context_manifests (workflow_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_project_memories_active ON project_memories (project_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_policies_lookup ON tool_policies (tool_version_id, project_id, agent_type, workflow_state);
