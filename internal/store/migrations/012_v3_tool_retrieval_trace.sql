ALTER TABLE retrieval_runs ADD COLUMN IF NOT EXISTS agent_run_id UUID REFERENCES agent_runs(id);

CREATE INDEX IF NOT EXISTS idx_retrieval_runs_agent_run
    ON retrieval_runs (agent_run_id, started_at DESC)
    WHERE agent_run_id IS NOT NULL;
