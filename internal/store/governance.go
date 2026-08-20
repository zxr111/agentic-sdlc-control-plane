package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

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
	AdapterConfig                                         any
	DefaultDecision                                       string
	RequiresGate                                          bool
}

type SkillSeed struct {
	Key, DisplayName, Description, Instructions string
	TriggerRules, Scope                         any
}

type ActiveSkill struct {
	VersionID    string
	Key          string
	Instructions string
	ContentHash  string
}

func (s *Store) ActiveSkillsForAgent(ctx context.Context, agentType string, allowlist []string) ([]ActiveSkill, error) {
	allowed := map[string]bool{}
	for _, key := range allowlist {
		allowed[key] = true
	}
	if len(allowed) == 0 {
		return []ActiveSkill{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sv.id,sd.skill_key,sv.instructions,sv.content_hash,sv.trigger_rules
		FROM skill_versions sv JOIN skill_definitions sd ON sd.id=sv.skill_definition_id
		WHERE sv.status='ACTIVE' ORDER BY sd.skill_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ActiveSkill{}
	for rows.Next() {
		var skill ActiveSkill
		var triggerRaw []byte
		if err := rows.Scan(&skill.VersionID, &skill.Key, &skill.Instructions, &skill.ContentHash, &triggerRaw); err != nil {
			return nil, err
		}
		if !allowed[skill.Key] {
			continue
		}
		var trigger struct {
			AgentTypes []string `json:"agent_types"`
		}
		if err := json.Unmarshal(triggerRaw, &trigger); err != nil {
			return nil, err
		}
		matched := false
		for _, value := range trigger.AgentTypes {
			if value == "*" || strings.Contains(strings.ToUpper(agentType), strings.ToUpper(value)) {
				matched = true
				break
			}
		}
		if matched {
			result = append(result, skill)
		}
	}
	return result, rows.Err()
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
		adapterConfig, err := json.Marshal(seed.AdapterConfig)
		if err != nil {
			return err
		}
		if seed.AdapterConfig == nil {
			adapterConfig = []byte(`{}`)
		}
		versionMaterial := append(append(append(append([]byte(seed.RiskLevel+":"+seed.AdapterType), 0), seed.InputSchema...), seed.OutputSchema...), adapterConfig...)
		digest := sha256.Sum256(versionMaterial)
		hash := hex.EncodeToString(digest[:])
		var versionID, activeHash string
		err = tx.QueryRowContext(ctx, `SELECT id,content_hash FROM tool_versions WHERE tool_definition_id=$1 AND status='ACTIVE'
			ORDER BY version DESC LIMIT 1`, definitionID).Scan(&versionID, &activeHash)
		if err == sql.ErrNoRows {
			versionID = registryID("tool-version", seed.Key+":"+hash)
			if _, err := tx.ExecContext(ctx, `INSERT INTO tool_versions
				(id,tool_definition_id,version,status,input_schema,output_schema,risk_level,adapter_type,adapter_config,content_hash)
				VALUES ($1,$2,1,'ACTIVE',$3,$4,$5,$6,$7,$8)`, versionID, definitionID, string(seed.InputSchema),
				string(seed.OutputSchema), seed.RiskLevel, seed.AdapterType, string(adapterConfig), hash); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if activeHash != hash {
			candidateID := registryID("tool-version", seed.Key+":"+hash)
			if _, err := tx.ExecContext(ctx, `INSERT INTO tool_versions
				(id,tool_definition_id,version,status,input_schema,output_schema,risk_level,adapter_type,adapter_config,content_hash)
				SELECT $1,$2,COALESCE(MAX(version),0)+1,'DRAFT',$3,$4,$5,$6,$7,$8 FROM tool_versions WHERE tool_definition_id=$2
				ON CONFLICT DO NOTHING`, candidateID, definitionID, string(seed.InputSchema),
				string(seed.OutputSchema), seed.RiskLevel, seed.AdapterType, string(adapterConfig), hash); err != nil {
				return err
			}
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
		var activeID, activeHash string
		err = tx.QueryRowContext(ctx, `SELECT id,content_hash FROM skill_versions WHERE skill_definition_id=$1 AND status='ACTIVE'
			ORDER BY version DESC LIMIT 1`, definitionID).Scan(&activeID, &activeHash)
		if err == sql.ErrNoRows {
			versionID := registryID("skill-version", seed.Key+":"+hash)
			if _, err := tx.ExecContext(ctx, `INSERT INTO skill_versions
				(id,skill_definition_id,version,status,instructions,trigger_rules,scope_json,content_hash)
				VALUES ($1,$2,1,'ACTIVE',$3,$4,$5,$6)`, versionID, definitionID, seed.Instructions, string(trigger), string(scope), hash); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if activeHash != hash {
			candidateID := registryID("skill-version", seed.Key+":"+hash)
			if _, err := tx.ExecContext(ctx, `INSERT INTO skill_versions
				(id,skill_definition_id,version,status,instructions,trigger_rules,scope_json,content_hash)
				SELECT $1,$2,COALESCE(MAX(version),0)+1,'DRAFT',$3,$4,$5,$6 FROM skill_versions WHERE skill_definition_id=$2
				ON CONFLICT DO NOTHING`, candidateID, definitionID, seed.Instructions, string(trigger), string(scope), hash); err != nil {
				return err
			}
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
	AgentRunID       string
	ToolKey          string
	ProjectID        int64
	AgentType        string
	WorkflowState    string
	Input            any
	RedactedInput    any
	Shadow           bool
	ProductionLock   bool
	GateID           string
	Actor            string
	EvidenceVersion  int
	BudgetMicrounits int64
}

type ToolAuthorization struct {
	CallID        string
	ToolVersionID string
	AdapterType   string
	AdapterConfig json.RawMessage
	InputSchema   json.RawMessage
	Decision      tooling.Decision
}

func (s *Store) AuthorizeToolCall(ctx context.Context, request ToolAuthorizationRequest) (ToolAuthorization, error) {
	var versionID, risk, rule, adapterType string
	var adapterConfig, conditionsRaw, inputSchema []byte
	var requiresGate bool
	err := s.db.QueryRowContext(ctx, `SELECT tv.id,tv.risk_level,COALESCE(tp.decision,'DENY'),COALESCE(tp.requires_gate,false),tv.adapter_type,tv.adapter_config,COALESCE(tp.conditions_json,'{}'::jsonb),tv.input_schema
		FROM tool_versions tv JOIN tool_definitions td ON td.id=tv.tool_definition_id
		LEFT JOIN LATERAL (SELECT decision,requires_gate,conditions_json FROM tool_policies
			WHERE tool_version_id=tv.id AND (project_id IS NULL OR project_id=$2)
			AND (agent_type='*' OR agent_type=$3) AND (workflow_state='*' OR workflow_state=$4)
			AND status='ACTIVE'
			ORDER BY (project_id IS NOT NULL) DESC,(agent_type<>'*') DESC,(workflow_state<>'*') DESC LIMIT 1) tp ON true
		WHERE td.tool_key=$1 AND tv.status='ACTIVE' ORDER BY tv.version DESC LIMIT 1`, request.ToolKey, request.ProjectID, request.AgentType, request.WorkflowState).
		Scan(&versionID, &risk, &rule, &requiresGate, &adapterType, &adapterConfig, &conditionsRaw, &inputSchema)
	if err == sql.ErrNoRows {
		return ToolAuthorization{}, ErrNotFound
	}
	if err != nil {
		return ToolAuthorization{}, err
	}
	var conditions struct {
		AllowedActors   []string `json:"allowed_actors"`
		MinimumEvidence int      `json:"minimum_evidence_version"`
		MaximumBudget   int64    `json:"maximum_budget_microunits"`
	}
	if err := json.Unmarshal(conditionsRaw, &conditions); err != nil {
		return ToolAuthorization{}, err
	}
	actorAllowed := len(conditions.AllowedActors) == 0
	for _, actor := range conditions.AllowedActors {
		if actor == "*" || actor == request.Actor {
			actorAllowed = true
		}
	}
	evidenceOK := conditions.MinimumEvidence == 0 || request.EvidenceVersion >= conditions.MinimumEvidence
	budgetOK := conditions.MaximumBudget == 0 || request.BudgetMicrounits <= conditions.MaximumBudget
	hasGate := false
	if request.GateID != "" {
		var gateRevision int
		err := s.db.QueryRowContext(ctx, `SELECT g.revision FROM gates g JOIN agent_runs ar ON ar.workflow_id=g.workflow_id
			WHERE g.id=$1 AND ar.id=$2 AND g.status='APPROVED'`, request.GateID, request.AgentRunID).Scan(&gateRevision)
		if err != nil && err != sql.ErrNoRows {
			return ToolAuthorization{}, err
		}
		hasGate = err == nil && request.EvidenceVersion == gateRevision
	}
	decision := tooling.Decide(tooling.Request{ProjectID: request.ProjectID, AgentType: request.AgentType,
		WorkflowState: request.WorkflowState, RiskLevel: risk, ConfiguredRule: rule, Shadow: request.Shadow,
		ProductionLock: request.ProductionLock, HasGate: hasGate, Actor: request.Actor,
		ActorAllowed: actorAllowed, EvidenceOK: evidenceOK, BudgetOK: budgetOK})
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
	return ToolAuthorization{CallID: callID, ToolVersionID: versionID, AdapterType: adapterType,
		AdapterConfig: json.RawMessage(adapterConfig), InputSchema: json.RawMessage(inputSchema), Decision: decision}, nil
}

func (s *Store) EnqueueGovernedToolOutbox(ctx context.Context, callID, toolKey, gateID string, input map[string]any) (json.RawMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var workflowID string
	var projectID, issueIID int64
	if err := tx.QueryRowContext(ctx, `SELECT ar.workflow_id::text,w.gitlab_project_id,w.issue_iid FROM tool_calls tc
		JOIN agent_runs ar ON ar.id=tc.agent_run_id JOIN workflows w ON w.id=ar.workflow_id
		WHERE tc.id=$1 AND tc.policy_decision='OUTBOX' AND tc.status='AUTHORIZED'`, callID).Scan(&workflowID, &projectID, &issueIID); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var messageType string
	var payload map[string]any
	switch toolKey {
	case "gitlab.comment":
		body, _ := input["body"].(string)
		marker, _ := input["marker"].(string)
		if strings.TrimSpace(body) == "" || len(body) > 64*1024 || strings.TrimSpace(marker) == "" {
			return nil, errors.New("gitlab.comment requires bounded body and marker")
		}
		messageType = "gitlab.upsert_note"
		payload = map[string]any{"project_id": projectID, "issue_iid": issueIID, "marker": marker, "body": body}
	case "staging.deploy":
		commitSHA, _ := input["commit_sha"].(string)
		if len(commitSHA) < 7 || len(commitSHA) > 64 || gateID == "" {
			return nil, errors.New("staging.deploy requires commit_sha and an approved gate")
		}
		var evidenceMatches bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gates g JOIN artifacts a ON a.id=g.artifact_id
			WHERE g.id=$1 AND g.workflow_id=$2 AND g.status='APPROVED' AND a.source_hash=$3)`, gateID, workflowID, commitSHA).Scan(&evidenceMatches); err != nil {
			return nil, err
		}
		if !evidenceMatches {
			return nil, errors.New("staging deploy SHA is not the exact approved evidence")
		}
		messageType = "delivery.trigger"
		payload = map[string]any{"request_id": uuid.NewString(), "action": "staging_deploy", "workflow_id": workflowID,
			"project_id": projectID, "issue_iid": issueIID, "commit_sha": commitSHA, "environment": "test"}
	default:
		return nil, errors.New("tool has no governed outbox adapter")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox_messages(dedupe_key,message_type,payload_json,last_error)
		VALUES ($1,$2,$3,'') ON CONFLICT (dedupe_key) DO NOTHING`, "tool-call:"+callID, messageType, string(raw)); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status='QUEUED',result_hash=$1,finished_at=CURRENT_TIMESTAMP WHERE id=$2`,
		hashBytes(raw), callID); err != nil {
		return nil, err
	}
	result, _ := json.Marshal(map[string]any{"status": "QUEUED", "tool_call_id": callID, "message_type": messageType})
	return result, tx.Commit()
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func (s *Store) FinishToolCall(ctx context.Context, callID, status, resultHash string, callError error) error {
	errorSummary := ""
	if callError != nil {
		errorSummary = redactError(callError)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE tool_calls SET status=$1,result_hash=$2,error_summary=$3,
		finished_at=CURRENT_TIMESTAMP WHERE id=$4`, status, resultHash, errorSummary, callID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrNotFound
	}
	return nil
}
