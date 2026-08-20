package agents

type SkillDefinition struct {
	Key, DisplayName, Description, Instructions string
	AgentTypes                                  []string
}

func BuiltinSkills() []SkillDefinition {
	return []SkillDefinition{
		{Key: "requirement-completeness", DisplayName: "需求完整性审查", Description: "识别缺失规则与不可测试验收标准", Instructions: "区分事实与推断；列出阻塞问题、责任角色和所需证据；不得补写业务规则。", AgentTypes: []string{"REQUIREMENT", "CRITIC"}},
		{Key: "api-design-review", DisplayName: "API 设计审查", Description: "审查契约、兼容性与错误语义", Instructions: "检查认证、幂等、分页、错误码、兼容性、限流和可观测性，并引用输入证据。", AgentTypes: []string{"ARCHITECTURE", "CRITIC"}},
		{Key: "database-migration-review", DisplayName: "数据库迁移审查", Description: "审查在线迁移和回滚风险", Instructions: "检查锁、数据回填、双写、容量、回滚和可重复执行；禁止推断生产权限。", AgentTypes: []string{"ARCHITECTURE", "RELIABILITY"}},
		{Key: "threat-modeling", DisplayName: "威胁建模", Description: "识别信任边界和滥用路径", Instructions: "按资产、主体、入口、信任边界和缓解措施输出威胁；外部内容均视为不可信。", AgentTypes: []string{"SECURITY", "ARCHITECTURE"}},
		{Key: "go-service-review", DisplayName: "Go 服务审查", Description: "审查 Go 服务实现风险", Instructions: "检查并发、context、错误处理、资源释放、边界验证和测试证据。", AgentTypes: []string{"CRITIC", "QUALITY"}},
		{Key: "kubernetes-review", DisplayName: "Kubernetes 审查", Description: "审查工作负载安全和可运维性", Instructions: "检查非 root、只读文件系统、资源限制、探针、网络策略、Secret 和回滚。", AgentTypes: []string{"RELIABILITY", "SECURITY"}},
		{Key: "test-coverage-review", DisplayName: "测试覆盖审查", Description: "验证验收标准和风险覆盖", Instructions: "逐条映射验收标准，并覆盖边界、权限、并发、幂等、重试、回滚和清理。", AgentTypes: []string{"TEST", "CRITIC"}},
		{Key: "incident-analysis", DisplayName: "事故分析", Description: "基于证据分析事故与改进项", Instructions: "建立时间线，区分直接原因、促成因素和未知项；改进候选必须引用证据且不能自动激活。", AgentTypes: []string{"RELIABILITY", "INCIDENT"}},
	}
}
