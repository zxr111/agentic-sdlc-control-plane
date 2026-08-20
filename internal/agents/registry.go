package agents

import "encoding/json"

type Definition struct {
	AgentType    string
	PromptKey    string
	SchemaName   string
	DisplayName  string
	Instructions string
	OutputSchema json.RawMessage
}

func BuiltinDefinitions() []Definition {
	return []Definition{
		{AgentType: "REQUIREMENT", PromptKey: "requirement-review", SchemaName: "requirement_review_v1", DisplayName: "需求审查 Agent", Instructions: requirementInstructions, OutputSchema: requirementSchema},
		{AgentType: "PRD", PromptKey: "prd", SchemaName: "prd_v1", DisplayName: "PRD Agent", Instructions: prdInstructions, OutputSchema: prdSchema},
		{AgentType: "TEST", PromptKey: "test-plan", SchemaName: "test_plan_v1", DisplayName: "测试计划 Agent", Instructions: testInstructions, OutputSchema: testPlanSchema},
		{AgentType: "ARCHITECTURE", PromptKey: "architecture", SchemaName: "architecture_v2", DisplayName: "架构 Agent", Instructions: architectureInstructions, OutputSchema: architectureSchema},
	}
}
