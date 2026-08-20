package tooling

type BuiltinTool struct {
	Key, DisplayName, Description, RiskLevel, AdapterType, DefaultDecision string
	RequiresGate                                                           bool
	InputSchema, OutputSchema                                              string
}

func BuiltinTools() []BuiltinTool {
	return []BuiltinTool{
		{Key: "knowledge.search", DisplayName: "知识检索", Description: "按项目和权威等级检索已索引知识", RiskLevel: "L1", AdapterType: "internal", DefaultDecision: "ALLOW", InputSchema: `{"type":"object","required":["query"],"properties":{"query":{"type":"string"}}}`, OutputSchema: `{"type":"array"}`},
		{Key: "memory.propose", DisplayName: "提出项目记忆", Description: "创建待工程师审批的项目记忆候选", RiskLevel: "L2", AdapterType: "outbox", DefaultDecision: "ALLOW", InputSchema: `{"type":"object","required":["key","content"],"properties":{"key":{"type":"string"},"content":{"type":"string"}}}`, OutputSchema: `{"type":"object"}`},
		{Key: "gitlab.comment", DisplayName: "GitLab 评论", Description: "通过事务发件箱更新 GitLab 评论", RiskLevel: "L2", AdapterType: "outbox", DefaultDecision: "ALLOW", InputSchema: `{"type":"object","required":["marker","body"],"properties":{"marker":{"type":"string"},"body":{"type":"string"}}}`, OutputSchema: `{"type":"object"}`},
		{Key: "staging.deploy", DisplayName: "测试环境部署", Description: "触发受工程师门禁保护的测试环境部署", RiskLevel: "L3", AdapterType: "outbox", DefaultDecision: "ALLOW", RequiresGate: true, InputSchema: `{"type":"object","required":["commit_sha"],"properties":{"commit_sha":{"type":"string"}}}`, OutputSchema: `{"type":"object"}`},
		{Key: "production.deploy", DisplayName: "生产部署", Description: "生产操作，默认由生产锁禁止", RiskLevel: "L4", AdapterType: "locked", DefaultDecision: "DENY", RequiresGate: true, InputSchema: `{"type":"object"}`, OutputSchema: `{"type":"object"}`},
	}
}
