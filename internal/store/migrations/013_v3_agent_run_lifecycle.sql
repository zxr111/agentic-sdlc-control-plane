ALTER TABLE agent_runs
    ADD COLUMN IF NOT EXISTS lifecycle_phase VARCHAR(32) COLLATE "C" NOT NULL DEFAULT 'CREATED';

UPDATE agent_runs
SET lifecycle_phase = CASE status
    WHEN 'RUNNING' THEN 'RUNNING'
    WHEN 'QUEUED' THEN 'CREATED'
    WHEN 'COMPLETED' THEN 'COMPLETED'
    WHEN 'CANCELLED' THEN 'CANCELLED'
    WHEN 'FAILED' THEN 'TERMINAL_FAILED'
    ELSE lifecycle_phase
END
WHERE lifecycle_phase = 'CREATED';

CREATE INDEX IF NOT EXISTS idx_agent_runs_lifecycle_phase
    ON agent_runs (lifecycle_phase, started_at DESC);
