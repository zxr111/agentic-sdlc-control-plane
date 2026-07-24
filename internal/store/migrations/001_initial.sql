CREATE TABLE IF NOT EXISTS schema_migrations (
    version VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS workflows (
    id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    gitlab_project_id BIGINT NOT NULL,
    issue_iid BIGINT NOT NULL,
    issue_title VARCHAR(512) NOT NULL,
    state VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    source_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
    revision INT NOT NULL DEFAULT 1,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    UNIQUE KEY uq_workflow_issue (gitlab_project_id, issue_iid),
    KEY idx_workflow_state (state, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS source_snapshots (
    id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    workflow_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    confluence_page_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    source_version INT NOT NULL,
    title VARCHAR(512) NOT NULL,
    source_url VARCHAR(2048) NOT NULL,
    source_updated_at VARCHAR(64) NOT NULL DEFAULT '',
    content_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    normalized_text LONGTEXT NOT NULL,
    raw_storage LONGTEXT NOT NULL,
    images_json JSON NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    UNIQUE KEY uq_snapshot_version (workflow_id, confluence_page_id, source_version, content_hash),
    KEY idx_snapshot_workflow (workflow_id, created_at),
    CONSTRAINT fk_snapshot_workflow FOREIGN KEY (workflow_id) REFERENCES workflows(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS artifacts (
    id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    workflow_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    artifact_type VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    artifact_version INT NOT NULL,
    source_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    content_json JSON NOT NULL,
    markdown LONGTEXT NOT NULL,
    model VARCHAR(128) NOT NULL,
    prompt_version VARCHAR(64) NOT NULL,
    generated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    UNIQUE KEY uq_artifact_version (workflow_id, artifact_type, artifact_version),
    KEY idx_artifact_latest (workflow_id, artifact_type, generated_at),
    CONSTRAINT fk_artifact_workflow FOREIGN KEY (workflow_id) REFERENCES workflows(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS gates (
    id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    workflow_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    gate_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    artifact_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    revision INT NOT NULL,
    reviewer_ids JSON NOT NULL,
    opened_at DATETIME(6) NOT NULL,
    decided_at DATETIME(6) NULL,
    decision_actor BIGINT NOT NULL DEFAULT 0,
    feedback TEXT NOT NULL,
    KEY idx_gate_workflow_status (workflow_id, status, gate_type),
    CONSTRAINT fk_gate_workflow FOREIGN KEY (workflow_id) REFERENCES workflows(id),
    CONSTRAINT fk_gate_artifact FOREIGN KEY (artifact_id) REFERENCES artifacts(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS gate_decisions (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    gate_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    action VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    actor_id BIGINT NOT NULL,
    actor_username VARCHAR(255) NOT NULL,
    feedback TEXT NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    UNIQUE KEY uq_gate_actor_action (gate_id, actor_id, action),
    CONSTRAINT fk_decision_gate FOREIGN KEY (gate_id) REFERENCES gates(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS event_queue (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    dedupe_key VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    event_type VARCHAR(96) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    payload_json JSON NOT NULL,
    status VARCHAR(24) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'READY',
    attempts INT NOT NULL DEFAULT 0,
    available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    locked_by VARCHAR(255) NOT NULL DEFAULT '',
    lease_until DATETIME(6) NULL,
    last_error TEXT NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    processed_at DATETIME(6) NULL,
    UNIQUE KEY uq_event_dedupe (dedupe_key),
    KEY idx_event_claim (status, available_at, id),
    KEY idx_event_lease (status, lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS outbox_messages (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    dedupe_key VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    message_type VARCHAR(96) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    payload_json JSON NOT NULL,
    status VARCHAR(24) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'READY',
    attempts INT NOT NULL DEFAULT 0,
    available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    locked_by VARCHAR(255) NOT NULL DEFAULT '',
    lease_until DATETIME(6) NULL,
    last_error TEXT NOT NULL,
    external_id VARCHAR(255) NOT NULL DEFAULT '',
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    delivered_at DATETIME(6) NULL,
    UNIQUE KEY uq_outbox_dedupe (dedupe_key),
    KEY idx_outbox_claim (status, available_at, id),
    KEY idx_outbox_lease (status, lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS audit_events (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    workflow_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    event_type VARCHAR(96) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    actor_id BIGINT NOT NULL DEFAULT 0,
    details_json JSON NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    KEY idx_audit_workflow (workflow_id, created_at),
    CONSTRAINT fk_audit_workflow FOREIGN KEY (workflow_id) REFERENCES workflows(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
