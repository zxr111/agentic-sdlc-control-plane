package store

import (
	"context"
	"database/sql"
	"time"
)

type DashboardV3 struct {
	Registry    DashboardV3Registry     `json:"registry"`
	Usage       DashboardV3Usage        `json:"usage"`
	AgentRuns   []DashboardV3AgentRun   `json:"agent_runs"`
	Opinions    []DashboardV3Opinion    `json:"opinions"`
	Routes      []DashboardV3Route      `json:"routes"`
	Evaluations []DashboardV3Evaluation `json:"evaluations"`
	Knowledge   DashboardV3Knowledge    `json:"knowledge"`
	ToolCalls   []DashboardV3ToolCall   `json:"tool_calls"`
}

type DashboardV3Registry struct {
	ActivePrompts  int `json:"active_prompts"`
	ActiveModels   int `json:"active_models"`
	ActiveProfiles int `json:"active_profiles"`
	ActiveSkills   int `json:"active_skills"`
	ActiveTools    int `json:"active_tools"`
}

type DashboardV3Usage struct {
	Runs                    int   `json:"runs"`
	InputTokens             int64 `json:"input_tokens"`
	CachedTokens            int64 `json:"cached_tokens"`
	OutputTokens            int64 `json:"output_tokens"`
	ReasoningTokens         int64 `json:"reasoning_tokens"`
	EstimatedCostMicrounits int64 `json:"estimated_cost_microunits"`
	AverageLatencyMS        int64 `json:"average_latency_ms"`
}

type DashboardV3AgentRun struct {
	ID                      string    `json:"id"`
	WorkflowID              string    `json:"workflow_id"`
	AgentType               string    `json:"agent_type"`
	Status                  string    `json:"status"`
	ProfileVersionID        string    `json:"profile_version_id,omitempty"`
	PromptVersionID         string    `json:"prompt_version_id,omitempty"`
	ModelVersionID          string    `json:"model_version_id,omitempty"`
	ContextManifestID       string    `json:"context_manifest_id,omitempty"`
	InputTokens             int64     `json:"input_tokens"`
	OutputTokens            int64     `json:"output_tokens"`
	EstimatedCostMicrounits int64     `json:"estimated_cost_microunits"`
	LatencyMS               int64     `json:"latency_ms"`
	StartedAt               time.Time `json:"started_at"`
}

type DashboardV3Opinion struct {
	AgentRunID string    `json:"agent_run_id"`
	Role       string    `json:"role"`
	Decision   string    `json:"decision"`
	Confidence float64   `json:"confidence"`
	Summary    string    `json:"summary"`
	Minority   bool      `json:"minority"`
	CreatedAt  time.Time `json:"created_at"`
}

type DashboardV3Route struct {
	AgentRunID              string    `json:"agent_run_id,omitempty"`
	SelectedModelVersionID  string    `json:"selected_model_version_id"`
	RiskLevel               string    `json:"risk_level"`
	Fallback                bool      `json:"fallback"`
	EstimatedCostMicrounits int64     `json:"estimated_cost_microunits"`
	Reason                  string    `json:"reason"`
	CreatedAt               time.Time `json:"created_at"`
}

type DashboardV3Evaluation struct {
	RunID        string    `json:"run_id"`
	SuiteKey     string    `json:"suite_key"`
	Status       string    `json:"status"`
	Shadow       bool      `json:"shadow"`
	AverageScore float64   `json:"average_score"`
	CreatedAt    time.Time `json:"created_at"`
}

type DashboardV3Knowledge struct {
	ActiveDocuments   int `json:"active_documents"`
	ActiveVersions    int `json:"active_versions"`
	Chunks            int `json:"chunks"`
	ApprovedMemories  int `json:"approved_memories"`
	CandidateMemories int `json:"candidate_memories"`
}

type DashboardV3ToolCall struct {
	ID             string    `json:"id"`
	AgentRunID     string    `json:"agent_run_id"`
	ToolKey        string    `json:"tool_key"`
	PolicyDecision string    `json:"policy_decision"`
	Status         string    `json:"status"`
	ErrorSummary   string    `json:"error_summary,omitempty"`
	StartedAt      time.Time `json:"started_at"`
}

func (s *Store) loadDashboardV3(ctx context.Context, result *DashboardV3, limit int) error {
	result.AgentRuns = []DashboardV3AgentRun{}
	result.Opinions = []DashboardV3Opinion{}
	result.Routes = []DashboardV3Route{}
	result.Evaluations = []DashboardV3Evaluation{}
	result.ToolCalls = []DashboardV3ToolCall{}
	if err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM prompt_versions WHERE status='ACTIVE'),
		(SELECT count(*) FROM model_versions WHERE status='ACTIVE'),
		(SELECT count(*) FROM agent_profile_versions WHERE status='ACTIVE'),
		(SELECT count(*) FROM skill_versions WHERE status='ACTIVE'),
		(SELECT count(*) FROM tool_versions WHERE status='ACTIVE')`).Scan(
		&result.Registry.ActivePrompts, &result.Registry.ActiveModels, &result.Registry.ActiveProfiles,
		&result.Registry.ActiveSkills, &result.Registry.ActiveTools); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(input_tokens),0),COALESCE(sum(cached_tokens),0),
		COALESCE(sum(output_tokens),0),COALESCE(sum(reasoning_tokens),0),COALESCE(sum(estimated_cost_microunits),0),
		COALESCE(avg(latency_ms)::bigint,0) FROM agent_runs`).Scan(&result.Usage.Runs, &result.Usage.InputTokens,
		&result.Usage.CachedTokens, &result.Usage.OutputTokens, &result.Usage.ReasoningTokens,
		&result.Usage.EstimatedCostMicrounits, &result.Usage.AverageLatencyMS); err != nil {
		return err
	}
	if err := s.loadDashboardV3Runs(ctx, result, limit); err != nil {
		return err
	}
	if err := s.loadDashboardV3Opinions(ctx, result, limit); err != nil {
		return err
	}
	if err := s.loadDashboardV3Routes(ctx, result, limit); err != nil {
		return err
	}
	if err := s.loadDashboardV3Evaluations(ctx, result, limit); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM knowledge_documents WHERE status='ACTIVE'),
		(SELECT count(*) FROM knowledge_versions WHERE status='ACTIVE'),
		(SELECT count(*) FROM knowledge_chunks),
		(SELECT count(*) FROM project_memories WHERE status='ACTIVE' AND (expires_at IS NULL OR expires_at>now())),
		(SELECT count(*) FROM project_memories WHERE status='CANDIDATE')`).Scan(&result.Knowledge.ActiveDocuments,
		&result.Knowledge.ActiveVersions, &result.Knowledge.Chunks, &result.Knowledge.ApprovedMemories,
		&result.Knowledge.CandidateMemories); err != nil {
		return err
	}
	return s.loadDashboardV3ToolCalls(ctx, result, limit)
}

func nullText(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func (s *Store) loadDashboardV3Runs(ctx context.Context, result *DashboardV3, limit int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id::text,workflow_id::text,agent_type,status,
		agent_profile_version_id::text,prompt_version_id::text,model_version_id::text,context_manifest_id::text,
		input_tokens,output_tokens,estimated_cost_microunits,latency_ms,started_at
		FROM agent_runs ORDER BY started_at DESC LIMIT $1`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item DashboardV3AgentRun
		var profile, prompt, model, manifest sql.NullString
		if err := rows.Scan(&item.ID, &item.WorkflowID, &item.AgentType, &item.Status, &profile, &prompt, &model, &manifest,
			&item.InputTokens, &item.OutputTokens, &item.EstimatedCostMicrounits, &item.LatencyMS, &item.StartedAt); err != nil {
			return err
		}
		item.ProfileVersionID = nullText(profile)
		item.PromptVersionID = nullText(prompt)
		item.ModelVersionID = nullText(model)
		item.ContextManifestID = nullText(manifest)
		result.AgentRuns = append(result.AgentRuns, item)
	}
	return rows.Err()
}

func (s *Store) loadDashboardV3Opinions(ctx context.Context, result *DashboardV3, limit int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_run_id::text,role,decision,confidence,summary,minority,created_at FROM agent_opinions ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item DashboardV3Opinion
		if err := rows.Scan(&item.AgentRunID, &item.Role, &item.Decision, &item.Confidence, &item.Summary, &item.Minority, &item.CreatedAt); err != nil {
			return err
		}
		result.Opinions = append(result.Opinions, item)
	}
	return rows.Err()
}

func (s *Store) loadDashboardV3Routes(ctx context.Context, result *DashboardV3, limit int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_run_id::text,selected_model_version_id::text,risk_level,fallback,estimated_cost_microunits,reason,created_at FROM model_route_decisions ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item DashboardV3Route
		var run sql.NullString
		if err := rows.Scan(&run, &item.SelectedModelVersionID, &item.RiskLevel, &item.Fallback, &item.EstimatedCostMicrounits, &item.Reason, &item.CreatedAt); err != nil {
			return err
		}
		item.AgentRunID = nullText(run)
		result.Routes = append(result.Routes, item)
	}
	return rows.Err()
}

func (s *Store) loadDashboardV3Evaluations(ctx context.Context, result *DashboardV3, limit int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id::text,s.suite_key,r.status,r.shadow,COALESCE(avg(sc.score),0),r.created_at
		FROM evaluation_runs r JOIN evaluation_suites s ON s.id=r.suite_id
		LEFT JOIN evaluation_outputs o ON o.evaluation_run_id=r.id LEFT JOIN evaluation_scores sc ON sc.evaluation_output_id=o.id
		GROUP BY r.id,s.suite_key ORDER BY r.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item DashboardV3Evaluation
		if err := rows.Scan(&item.RunID, &item.SuiteKey, &item.Status, &item.Shadow, &item.AverageScore, &item.CreatedAt); err != nil {
			return err
		}
		result.Evaluations = append(result.Evaluations, item)
	}
	return rows.Err()
}

func (s *Store) loadDashboardV3ToolCalls(ctx context.Context, result *DashboardV3, limit int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id::text,c.agent_run_id::text,d.tool_key,c.policy_decision,c.status,c.error_summary,c.started_at
		FROM tool_calls c JOIN tool_versions v ON v.id=c.tool_version_id JOIN tool_definitions d ON d.id=v.tool_definition_id
		ORDER BY c.started_at DESC LIMIT $1`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item DashboardV3ToolCall
		if err := rows.Scan(&item.ID, &item.AgentRunID, &item.ToolKey, &item.PolicyDecision, &item.Status, &item.ErrorSummary, &item.StartedAt); err != nil {
			return err
		}
		result.ToolCalls = append(result.ToolCalls, item)
	}
	return rows.Err()
}
