CREATE TABLE IF NOT EXISTS coding_sessions (
    id UUID PRIMARY KEY,
    dispatch_id UUID NOT NULL UNIQUE REFERENCES codex_dispatches(id),
    manifest_hash CHAR(64) COLLATE "C" NOT NULL REFERENCES coding_task_manifests(manifest_hash),
    state VARCHAR(32) COLLATE "C" NOT NULL,
    head_commit VARCHAR(64) COLLATE "C" NOT NULL DEFAULT '',
    repair_attempts INTEGER NOT NULL DEFAULT 0,
    max_repairs INTEGER NOT NULL DEFAULT 3,
    revision INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS coding_agent_runs (
    id UUID PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES coding_sessions(id),
    role VARCHAR(64) COLLATE "C" NOT NULL,
    status VARCHAR(24) COLLATE "C" NOT NULL,
    attempt INTEGER NOT NULL,
    manifest_hash CHAR(64) COLLATE "C" NOT NULL,
    base_commit VARCHAR(64) COLLATE "C" NOT NULL DEFAULT '',
    head_commit VARCHAR(64) COLLATE "C" NOT NULL DEFAULT '',
    provider_session_id VARCHAR(255) COLLATE "C" NOT NULL DEFAULT '',
    input_json JSONB NOT NULL,
    input_hash CHAR(64) COLLATE "C" NOT NULL,
    output_hash CHAR(64) COLLATE "C" NOT NULL DEFAULT '',
    decision VARCHAR(32) COLLATE "C" NOT NULL DEFAULT '',
    blocking_findings INTEGER NOT NULL DEFAULT 0,
    high_risk BOOLEAN NOT NULL DEFAULT FALSE,
    checks_passed BOOLEAN NOT NULL DEFAULT FALSE,
    error_summary TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMPTZ(6),
    finished_at TIMESTAMPTZ(6),
    UNIQUE(session_id,role,attempt,head_commit)
);

CREATE TABLE IF NOT EXISTS coding_artifacts (
    id UUID PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES coding_sessions(id),
    producer_run_id UUID NOT NULL REFERENCES coding_agent_runs(id),
    artifact_type VARCHAR(64) COLLATE "C" NOT NULL,
    schema_version INTEGER NOT NULL DEFAULT 1,
    base_commit VARCHAR(64) COLLATE "C" NOT NULL DEFAULT '',
    head_commit VARCHAR(64) COLLATE "C" NOT NULL DEFAULT '',
    content_json JSONB NOT NULL,
    content_hash CHAR(64) COLLATE "C" NOT NULL,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(producer_run_id,artifact_type)
);

CREATE TABLE IF NOT EXISTS coding_run_inputs (
    consumer_run_id UUID NOT NULL REFERENCES coding_agent_runs(id),
    artifact_id UUID NOT NULL REFERENCES coding_artifacts(id),
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(consumer_run_id,artifact_id)
);

CREATE TABLE IF NOT EXISTS coding_stage_transitions (
    id UUID PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES coding_sessions(id),
    from_state VARCHAR(32) COLLATE "C" NOT NULL,
    to_state VARCHAR(32) COLLATE "C" NOT NULL,
    trigger_run_id UUID REFERENCES coding_agent_runs(id),
    manifest_hash CHAR(64) COLLATE "C" NOT NULL,
    head_commit VARCHAR(64) COLLATE "C" NOT NULL DEFAULT '',
    reason TEXT NOT NULL,
    policy_version VARCHAR(128) COLLATE "C" NOT NULL,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_coding_agent_runs_pending ON coding_agent_runs(status,created_at);
CREATE UNIQUE INDEX IF NOT EXISTS uq_coding_agent_provider_session ON coding_agent_runs(provider_session_id)
    WHERE provider_session_id <> '';
CREATE INDEX IF NOT EXISTS idx_coding_artifacts_session ON coding_artifacts(session_id,created_at);
CREATE INDEX IF NOT EXISTS idx_coding_transitions_session ON coding_stage_transitions(session_id,created_at);
