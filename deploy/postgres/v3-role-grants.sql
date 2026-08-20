\set ON_ERROR_STOP on

-- Run this as the database owner after creating login roles
-- ai_factory_agent and ai_factory_evaluation with externally managed passwords.
REVOKE CREATE ON SCHEMA public FROM ai_factory_agent, ai_factory_evaluation;
GRANT CONNECT ON DATABASE ai_sdlc_factory_test TO ai_factory_agent, ai_factory_evaluation;
GRANT USAGE ON SCHEMA public TO ai_factory_agent, ai_factory_evaluation;

GRANT SELECT ON
    schema_migrations, projects, source_snapshots, artifacts, gates, gate_decisions,
    work_items, work_item_dependencies, merge_requests, pipeline_runs, quality_runs,
    prompt_definitions, prompt_versions, model_providers, model_versions, model_policies,
    agent_profiles, agent_profile_versions, skill_definitions, skill_versions,
    tool_definitions, tool_versions, tool_policies, knowledge_documents,
    knowledge_versions, knowledge_chunks, project_memories, evaluation_suites,
    evaluation_cases
TO ai_factory_agent;

GRANT SELECT, INSERT, UPDATE ON
    workflows, event_queue, outbox_messages, artifacts, gates, gate_decisions,
    agent_runs, agent_steps, agent_opinions, context_manifests, context_entries,
    tool_calls, retrieval_runs, retrieval_results, model_route_decisions,
    model_health_events, improvement_candidates
TO ai_factory_agent;

GRANT SELECT ON
    schema_migrations, workflows, source_snapshots, artifacts, gates,
    prompt_definitions, prompt_versions, model_providers, model_versions, model_policies,
    agent_profiles, agent_profile_versions, tool_definitions, tool_versions,
    evaluation_suites, evaluation_cases,
    evaluation_case_revisions
TO ai_factory_evaluation;

GRANT SELECT, INSERT, UPDATE ON
    event_queue, evaluation_runs, evaluation_outputs, evaluation_scores,
    evaluation_comparisons, blind_reviews, blind_review_submissions,
    canary_releases, model_health_events
TO ai_factory_evaluation;

REVOKE ALL ON schema_migrations FROM ai_factory_agent, ai_factory_evaluation;
GRANT SELECT ON schema_migrations TO ai_factory_agent, ai_factory_evaluation;
