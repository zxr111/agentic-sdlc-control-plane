CREATE TABLE IF NOT EXISTS service_catalog (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL,
    service_key VARCHAR(255) COLLATE "C" NOT NULL,
    repository VARCHAR(1024) COLLATE "C" NOT NULL,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, service_key)
);

CREATE TABLE IF NOT EXISTS planned_impacts (
    id UUID PRIMARY KEY,
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    sdd_artifact_id UUID NOT NULL REFERENCES artifacts(id),
    work_item_id UUID NOT NULL REFERENCES work_items(id),
    work_item_key VARCHAR(128) COLLATE "C" NOT NULL,
    service_id UUID NOT NULL REFERENCES service_catalog(id),
    change_type VARCHAR(24) COLLATE "C" NOT NULL,
    functional_changes JSONB NOT NULL DEFAULT '[]'::jsonb,
    components JSONB NOT NULL DEFAULT '[]'::jsonb,
    apis JSONB NOT NULL DEFAULT '[]'::jsonb,
    data_changes JSONB NOT NULL DEFAULT '[]'::jsonb,
    likely_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
    acceptance_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    verification JSONB NOT NULL DEFAULT '[]'::jsonb,
    risks JSONB NOT NULL DEFAULT '[]'::jsonb,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (sdd_artifact_id, work_item_key, service_id)
);

CREATE TABLE IF NOT EXISTS actual_impacts (
    id UUID PRIMARY KEY,
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    work_item_id UUID NOT NULL REFERENCES work_items(id),
    merge_request_id UUID NOT NULL REFERENCES merge_requests(id),
    service_id UUID NOT NULL REFERENCES service_catalog(id),
    commit_sha VARCHAR(64) COLLATE "C" NOT NULL,
    changed_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
    planned_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
    implemented_functions JSONB NOT NULL DEFAULT '[]'::jsonb,
    acceptance_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    architecture_deviation BOOLEAN NOT NULL DEFAULT FALSE,
    deviation_details JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (merge_request_id, commit_sha, service_id)
);

CREATE INDEX IF NOT EXISTS idx_planned_impacts_service ON planned_impacts (service_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_actual_impacts_service ON actual_impacts (service_id, created_at DESC);
