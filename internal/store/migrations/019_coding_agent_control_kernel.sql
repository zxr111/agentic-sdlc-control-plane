CREATE TABLE IF NOT EXISTS coding_task_manifests (
    id UUID PRIMARY KEY,
    dispatch_id UUID NOT NULL UNIQUE REFERENCES codex_dispatches(id),
    work_item_id UUID NOT NULL REFERENCES work_items(id),
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    manifest_version INTEGER NOT NULL,
    provider VARCHAR(64) COLLATE "C" NOT NULL,
    manifest_hash CHAR(64) COLLATE "C" NOT NULL UNIQUE,
    repository VARCHAR(1024) NOT NULL,
    base_ref VARCHAR(255) NOT NULL,
    branch_name VARCHAR(255) NOT NULL,
    source_artifact_id UUID NOT NULL REFERENCES artifacts(id),
    source_artifact_hash CHAR(64) COLLATE "C" NOT NULL,
    manifest_json JSONB NOT NULL,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS coding_agent_evidence (
    id UUID PRIMARY KEY,
    dispatch_id UUID NOT NULL REFERENCES codex_dispatches(id),
    manifest_hash CHAR(64) COLLATE "C" NOT NULL REFERENCES coding_task_manifests(manifest_hash),
    provider VARCHAR(64) COLLATE "C" NOT NULL,
    session_id VARCHAR(255) COLLATE "C" NOT NULL,
    evidence_kind VARCHAR(32) COLLATE "C" NOT NULL,
    commit_sha VARCHAR(64) COLLATE "C" NOT NULL DEFAULT '',
    command_text TEXT NOT NULL DEFAULT '',
    exit_code INTEGER,
    payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_coding_agent_evidence UNIQUE (dispatch_id, session_id, evidence_kind, commit_sha, command_text)
);

CREATE INDEX IF NOT EXISTS idx_coding_agent_evidence_dispatch ON coding_agent_evidence(dispatch_id, created_at);
