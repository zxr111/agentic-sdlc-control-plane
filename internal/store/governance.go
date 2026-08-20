package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/multiagent"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/tooling"
	"github.com/google/uuid"
)

type OpinionRecorder struct {
	Store      *Store
	AgentRunID string
}

type ToolSeed struct {
	Key, DisplayName, Description, RiskLevel, AdapterType string
	InputSchema, OutputSchema                             json.RawMessage
	DefaultDecision                                       string
	RequiresGate                                          bool
}

type SkillSeed struct {
	Key, DisplayName, Description, Instructions string
	TriggerRules, Scope                         any
}

func (s *Store) BootstrapGovernance(ctx context.Context, tools []ToolSeed, skills []SkillSeed) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, seed := range tools {
		definitionID := registryID("tool-definition", seed.Key)
		if _, err := tx.ExecContext(ctx, `INSERT INTO tool_definitions(id,tool_key,display_name,description)
			VALUES ($1,$2,$3,$4) ON CONFLICT (tool_key) DO NOTHING`, definitionID, seed.Key, seed.DisplayName, seed.Description); err != nil {
			return err
		}
		digest := sha256.Sum256(append(append(append([]byte(seed.RiskLevel+":"+seed.AdapterType), 0), seed.InputSchema...), seed.OutputSchema...))
		hash := hex.EncodeToString(digest[:])
		versionID := registryID("tool-version", seed.Key+":"+hash)
		if _, err := tx.ExecContext(ctx, `INSERT INTO tool_versions
			(id,tool_definition_id,version,status,input_schema,output_schema,risk_level,adapter_type,content_hash)
			VALUES ($1,$2,1,'ACTIVE',$3,$4,$5,$6,$7) ON CONFLICT (tool_definition_id,version) DO NOTHING`,
			versionID, definitionID, string(seed.InputSchema), string(seed.OutputSchema), seed.RiskLevel, seed.AdapterType, hash); err != nil {
			return err
		}
		policyID := registryID("tool-policy", seed.Key+":default")
		decision := seed.DefaultDecision
		if decision == "" {
			decision = "DENY"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tool_policies
			(id,tool_version_id,agent_type,workflow_state,decision,requires_gate,conditions_json)
			VALUES ($1,$2,'*','*',$3,$4,'{}') ON CONFLICT (id) DO NOTHING`, policyID, versionID, decision, seed.RequiresGate); err != nil {
			return err
		}
	}
	for _, seed := range skills {
		definitionID := registryID("skill-definition", seed.Key)
		if _, err := tx.ExecContext(ctx, `INSERT INTO skill_definitions(id,skill_key,display_name,description)
			VALUES ($1,$2,$3,$4) ON CONFLICT (skill_key) DO NOTHING`, definitionID, seed.Key, seed.DisplayName, seed.Description); err != nil {
			return err
		}
		trigger, err := json.Marshal(seed.TriggerRules)
		if err != nil {
			return err
		}
		scope, err := json.Marshal(seed.Scope)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(append(append(append([]byte(seed.Instructions), 0), trigger...), scope...))
		hash := hex.EncodeToString(digest[:])
		versionID := registryID("skill-version", seed.Key+":"+hash)
		if _, err := tx.ExecContext(ctx, `INSERT INTO skill_versions
			(id,skill_definition_id,version,status,instructions,trigger_rules,scope_json,content_hash)
			VALUES ($1,$2,1,'ACTIVE',$3,$4,$5,$6) ON CONFLICT (skill_definition_id,version) DO NOTHING`,
			versionID, definitionID, seed.Instructions, string(trigger), string(scope), hash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r OpinionRecorder) RecordOpinion(ctx context.Context, opinion multiagent.Opinion, minority bool) error {
	findings, err := json.Marshal(opinion.Findings)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(opinion.Evidence)
	if err != nil {
		return err
	}
	_, err = r.Store.db.ExecContext(ctx, `INSERT INTO agent_opinions
		(id,agent_run_id,role,decision,confidence,summary,findings_json,evidence_json,minority)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, uuid.NewString(), r.AgentRunID, opinion.Role, opinion.Decision,
		opinion.Confidence, opinion.Summary, string(findings), string(evidence), minority)
	return err
}

type ToolAuthorizationRequest struct {
	AgentRunID     string
	ToolKey        string
	ProjectID      int64
	AgentType      string
	WorkflowState  string
	Input          any
	RedactedInput  any
	Shadow         bool
	ProductionLock bool
	GateID         string
}

type ToolAuthorization struct {
	CallID        string
	ToolVersionID string
	Decision      tooling.Decision
}

func (s *Store) AuthorizeToolCall(ctx context.Context, request ToolAuthorizationRequest) (ToolAuthorization, error) {
	var versionID, risk, rule string
	var requiresGate bool
	err := s.db.QueryRowContext(ctx, `SELECT tv.id,tv.risk_level,COALESCE(tp.decision,'DENY'),COALESCE(tp.requires_gate,false)
		FROM tool_versions tv JOIN tool_definitions td ON td.id=tv.tool_definition_id
		LEFT JOIN LATERAL (SELECT decision,requires_gate FROM tool_policies
			WHERE tool_version_id=tv.id AND (project_id IS NULL OR project_id=$2)
			AND (agent_type='*' OR agent_type=$3) AND (workflow_state='*' OR workflow_state=$4)
			ORDER BY (project_id IS NOT NULL) DESC,(agent_type<>'*') DESC,(workflow_state<>'*') DESC LIMIT 1) tp ON true
		WHERE td.tool_key=$1 AND tv.status='ACTIVE' ORDER BY tv.version DESC LIMIT 1`, request.ToolKey, request.ProjectID, request.AgentType, request.WorkflowState).
		Scan(&versionID, &risk, &rule, &requiresGate)
	if err == sql.ErrNoRows {
		return ToolAuthorization{}, ErrNotFound
	}
	if err != nil {
		return ToolAuthorization{}, err
	}
	decision := tooling.Decide(tooling.Request{ProjectID: request.ProjectID, AgentType: request.AgentType,
		WorkflowState: request.WorkflowState, RiskLevel: risk, ConfiguredRule: rule, Shadow: request.Shadow,
		ProductionLock: request.ProductionLock, HasGate: request.GateID != ""})
	if requiresGate && request.GateID == "" && decision.Action != "DENY" {
		decision = tooling.Decision{Action: "REQUIRE_GATE", Reason: "tool policy requires an Engineer Gate", RequiresGate: true}
	}
	rawInput, err := json.Marshal(request.Input)
	if err != nil {
		return ToolAuthorization{}, err
	}
	redacted, err := json.Marshal(request.RedactedInput)
	if err != nil {
		return ToolAuthorization{}, err
	}
	digest := sha256.Sum256(rawInput)
	callID := uuid.NewString()
	status := "AUTHORIZED"
	if decision.Action == "DENY" || decision.Action == "REQUIRE_GATE" {
		status = decision.Action
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO tool_calls
		(id,agent_run_id,tool_version_id,input_hash,redacted_input_json,policy_decision,gate_id,status,error_summary,finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,CURRENT_TIMESTAMP)`, callID, request.AgentRunID, versionID,
		hex.EncodeToString(digest[:]), string(redacted), decision.Action, nullableUUID(request.GateID), status, decision.Reason)
	if err != nil {
		return ToolAuthorization{}, err
	}
	return ToolAuthorization{CallID: callID, ToolVersionID: versionID, Decision: decision}, nil
}
