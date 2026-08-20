CREATE TABLE IF NOT EXISTS model_health_events (
    id UUID PRIMARY KEY,
    model_version_id UUID NOT NULL REFERENCES model_versions(id),
    healthy BOOLEAN NOT NULL,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    error_summary TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS model_route_decisions (
    id UUID PRIMARY KEY,
    workflow_id UUID REFERENCES workflows(id),
    agent_run_id UUID REFERENCES agent_runs(id),
    requested_model_version_id UUID REFERENCES model_versions(id),
    selected_model_version_id UUID NOT NULL REFERENCES model_versions(id),
    risk_level VARCHAR(16) COLLATE "C" NOT NULL,
    fallback BOOLEAN NOT NULL DEFAULT FALSE,
    estimated_cost_microunits BIGINT NOT NULL DEFAULT 0,
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_model_health_latest ON model_health_events (model_version_id, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_model_route_workflow ON model_route_decisions (workflow_id, created_at DESC);
