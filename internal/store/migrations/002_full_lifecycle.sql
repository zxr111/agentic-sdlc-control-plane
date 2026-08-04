ALTER TABLE workflows ADD COLUMN IF NOT EXISTS suspended_state VARCHAR(64) COLLATE "C" NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS work_items (
    id UUID PRIMARY KEY,
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    work_item_key VARCHAR(64) COLLATE "C" NOT NULL,
    gitlab_issue_iid BIGINT NOT NULL DEFAULT 0,
    title VARCHAR(512) NOT NULL,
    state VARCHAR(48) COLLATE "C" NOT NULL,
    owner_role VARCHAR(64) COLLATE "C" NOT NULL,
    assignee_id BIGINT NOT NULL DEFAULT 0,
    branch_name VARCHAR(255) NOT NULL DEFAULT '',
    target_branch VARCHAR(255) NOT NULL DEFAULT 'master',
    acceptance_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    revision INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_work_item_key UNIQUE (workflow_id, work_item_key)
);

CREATE INDEX IF NOT EXISTS idx_work_item_state ON work_items (state, assignee_id, updated_at);
CREATE UNIQUE INDEX IF NOT EXISTS uq_work_item_issue ON work_items (workflow_id, gitlab_issue_iid) WHERE gitlab_issue_iid > 0;

CREATE TABLE IF NOT EXISTS work_item_dependencies (
    work_item_id UUID NOT NULL REFERENCES work_items(id),
    depends_on_id UUID NOT NULL REFERENCES work_items(id),
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (work_item_id, depends_on_id),
    CONSTRAINT ck_work_item_not_self_dependent CHECK (work_item_id <> depends_on_id)
);

CREATE TABLE IF NOT EXISTS codex_dispatches (
    id UUID PRIMARY KEY,
    work_item_id UUID NOT NULL REFERENCES work_items(id),
    client_id VARCHAR(128) COLLATE "C" NOT NULL,
    engineer_id BIGINT NOT NULL,
    coding_thread_id VARCHAR(255) NOT NULL DEFAULT '',
    quality_thread_id VARCHAR(255) NOT NULL DEFAULT '',
    status VARCHAR(32) COLLATE "C" NOT NULL DEFAULT 'STARTED',
    started_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMPTZ(6),
    reset_reason TEXT NOT NULL DEFAULT '',
    CONSTRAINT uq_active_codex_dispatch UNIQUE (work_item_id)
);

CREATE TABLE IF NOT EXISTS agent_runs (
    id UUID PRIMARY KEY,
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    work_item_id UUID REFERENCES work_items(id),
    agent_type VARCHAR(64) COLLATE "C" NOT NULL,
    run_number INTEGER NOT NULL,
    status VARCHAR(32) COLLATE "C" NOT NULL,
    model VARCHAR(128) NOT NULL DEFAULT '',
    input_hash CHAR(64) COLLATE "C" NOT NULL DEFAULT '',
    output_artifact_id UUID REFERENCES artifacts(id),
    error_summary TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMPTZ(6),
    CONSTRAINT uq_agent_run UNIQUE (workflow_id, work_item_id, agent_type, run_number)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_run_with_workflow_scope
    ON agent_runs (workflow_id, COALESCE(work_item_id, '00000000-0000-0000-0000-000000000000'::uuid), agent_type, run_number);

CREATE TABLE IF NOT EXISTS merge_requests (
    id UUID PRIMARY KEY,
    work_item_id UUID NOT NULL REFERENCES work_items(id),
    gitlab_mr_iid BIGINT NOT NULL,
    source_branch VARCHAR(255) NOT NULL,
    target_branch VARCHAR(255) NOT NULL,
    head_sha CHAR(40) COLLATE "C" NOT NULL DEFAULT '',
    state VARCHAR(32) COLLATE "C" NOT NULL,
    draft BOOLEAN NOT NULL DEFAULT TRUE,
    web_url VARCHAR(2048) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_work_item_mr UNIQUE (work_item_id)
);

CREATE TABLE IF NOT EXISTS quality_runs (
    id UUID PRIMARY KEY,
    merge_request_id UUID NOT NULL REFERENCES merge_requests(id),
    head_sha CHAR(40) COLLATE "C" NOT NULL,
    attempt INTEGER NOT NULL,
    status VARCHAR(32) COLLATE "C" NOT NULL,
    acceptance_coverage NUMERIC(5,2) NOT NULL DEFAULT 0,
    test_evidence_coverage NUMERIC(5,2) NOT NULL DEFAULT 0,
    required_ci_passed BOOLEAN NOT NULL DEFAULT FALSE,
    p0_findings INTEGER NOT NULL DEFAULT 0,
    p1_findings INTEGER NOT NULL DEFAULT 0,
    high_security_findings INTEGER NOT NULL DEFAULT 0,
    critical_security_findings INTEGER NOT NULL DEFAULT 0,
    architecture_deviations INTEGER NOT NULL DEFAULT 0,
    out_of_scope_changes INTEGER NOT NULL DEFAULT 0,
    blockers INTEGER NOT NULL DEFAULT 0,
    migration_validated BOOLEAN NOT NULL DEFAULT TRUE,
    rollback_validated BOOLEAN NOT NULL DEFAULT TRUE,
    report_artifact_id UUID REFERENCES artifacts(id),
    started_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMPTZ(6),
    CONSTRAINT uq_quality_attempt UNIQUE (merge_request_id, head_sha, attempt)
);

CREATE TABLE IF NOT EXISTS quality_findings (
    id UUID PRIMARY KEY,
    quality_run_id UUID NOT NULL REFERENCES quality_runs(id),
    category VARCHAR(64) COLLATE "C" NOT NULL,
    severity VARCHAR(16) COLLATE "C" NOT NULL,
    file_path VARCHAR(2048) NOT NULL DEFAULT '',
    line_number INTEGER NOT NULL DEFAULT 0,
    summary TEXT NOT NULL,
    evidence TEXT NOT NULL,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'OPEN',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS pipeline_runs (
    id UUID PRIMARY KEY,
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    work_item_id UUID REFERENCES work_items(id),
    gitlab_pipeline_id BIGINT NOT NULL,
    ref VARCHAR(255) NOT NULL,
    sha CHAR(40) COLLATE "C" NOT NULL,
    status VARCHAR(32) COLLATE "C" NOT NULL,
    web_url VARCHAR(2048) NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ(6),
    finished_at TIMESTAMPTZ(6),
    updated_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_pipeline UNIQUE (workflow_id, gitlab_pipeline_id)
);

CREATE TABLE IF NOT EXISTS release_candidates (
    id UUID PRIMARY KEY,
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    version VARCHAR(128) COLLATE "C" NOT NULL,
    commit_sha CHAR(40) COLLATE "C" NOT NULL,
    status VARCHAR(32) COLLATE "C" NOT NULL,
    release_artifact_id UUID REFERENCES artifacts(id),
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_release_candidate UNIQUE (workflow_id, version)
);

CREATE TABLE IF NOT EXISTS deployments (
    id UUID PRIMARY KEY,
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    release_candidate_id UUID NOT NULL REFERENCES release_candidates(id),
    environment VARCHAR(64) COLLATE "C" NOT NULL,
    external_deployment_id VARCHAR(255) COLLATE "C" NOT NULL,
    status VARCHAR(32) COLLATE "C" NOT NULL,
    production_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    started_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMPTZ(6),
    CONSTRAINT uq_deployment_external UNIQUE (environment, external_deployment_id)
);

CREATE TABLE IF NOT EXISTS observation_windows (
    id UUID PRIMARY KEY,
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    deployment_id UUID REFERENCES deployments(id),
    status VARCHAR(32) COLLATE "C" NOT NULL,
    starts_at TIMESTAMPTZ(6) NOT NULL,
    ends_at TIMESTAMPTZ(6) NOT NULL,
    success_criteria JSONB NOT NULL DEFAULT '{}'::jsonb,
    result_json JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS incidents (
    id UUID PRIMARY KEY,
    workflow_id UUID REFERENCES workflows(id),
    source VARCHAR(32) COLLATE "C" NOT NULL,
    external_id VARCHAR(255) COLLATE "C" NOT NULL,
    severity VARCHAR(16) COLLATE "C" NOT NULL,
    title VARCHAR(512) NOT NULL,
    status VARCHAR(32) COLLATE "C" NOT NULL,
    payload_json JSONB NOT NULL,
    gitlab_issue_iid BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_incident_external UNIQUE (source, external_id)
);

CREATE TABLE IF NOT EXISTS email_relays (
    id UUID PRIMARY KEY,
    message_id_hash CHAR(64) COLLATE "C" NOT NULL,
    engineer_id BIGINT NOT NULL,
    command_text TEXT NOT NULL,
    gitlab_note_id BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(32) COLLATE "C" NOT NULL,
    error_summary TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    relayed_at TIMESTAMPTZ(6),
    CONSTRAINT uq_email_message UNIQUE (message_id_hash)
);
