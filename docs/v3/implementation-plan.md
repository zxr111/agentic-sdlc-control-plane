# V3 实施计划

V3 作为一个完整目标交付，但实施过程采用可验证里程碑。每个里程碑结束时，主分支必须保持可构建、可迁移、可回滚和兼容现有 V2 工作流。

## 里程碑 0：基线与重构

交付：

- 建立 V3 Feature Flags。
- 将 `engine.go` 按 Intake、Planning、Architecture、Execution、Quality、Release、Incident 拆分。
- 为现有流程补齐 Characterization Tests。
- 固化 V2 端到端基线和数据库快照。

验收：现有测试全部通过，相同输入产生兼容状态跳转、Artifact、Gate 和 Outbox。

## 里程碑 1：Agent 可观测性与 Registry

交付：

- Prompt、Model、Agent Profile Registry。
- Context Manifest。
- Agent Usage、Trace 和成本记录。
- 当前四个 Agent 回填为初始版本。

验收：每次 Requirement、PRD、Test、Architecture Run 都能追溯实际 Prompt、模型、Schema、Context 和 Token。

## 里程碑 2：评测平台

交付：

- Evaluation Suite、Case、Run 和 Score。
- 历史 Workflow 重放。
- 确定性 Scorer 和 LLM Judge。
- Dashboard 对比视图。

验收：候选 Prompt 能与当前 Active 版本在固定 Case 上重放，且评测不修改真实 Workflow。

## 里程碑 3：知识库与项目记忆

交付：

- PostgreSQL Full Text Search 与 pgvector。
- Confluence、GitLab、Artifact 和质量证据摄取。
- Hybrid Retrieval、Reranker 和 Citation Validation。
- Candidate/Approved Memory 生命周期。

验收：检索结果可追溯到来源版本；撤销来源后不会进入新 Context；权威来源优先级不可被历史内容覆盖。

## 里程碑 4：多 Agent

交付：

- Primary、Critic、Security/Reliability、Judge 编排。
- 独立 Agent Run 和结构化 Opinion。
- 共识、分歧、少数意见和风险展示。
- Requirement 与 Architecture 首批多 Agent 流程。

验收：成员独立运行；Judge 保留冲突；Engineer Gate 可以查看每个意见和证据。

## 里程碑 5：Tool Registry、MCP 与 Skills

交付：

- Tool Definition、Version、Policy 和 Call Trace。
- MCP Gateway。
- 只读检索工具和受控 GitLab 写工具。
- 版本化 Agent Skills。

验收：跨项目、跨阶段和未授权工具调用被拒绝；写操作仍通过 Outbox 或 Engineer Gate。

## 里程碑 6：模型路由与持续改进

交付：

- 风险和预算感知 Model Router。
- Provider 健康和受控 Fallback。
- Shadow Run、Canary 和快速回滚。
- 失败轨迹聚类与改进候选。

验收：高风险 Agent 不静默降级；候选不能自动激活；版本切换和回滚可审计。

## 里程碑 7：Dashboard、部署与端到端验收

交付：

- V3 Dashboard 页面。
- Compose 中的 Agent Runtime 与 pgvector。
- Kubernetes Deployment、NetworkPolicy、Secret 和资源限制。
- 完整测试环境 Runbook。

端到端验收路径：

```text
GitLab Intake Issue
-> Confluence Snapshot
-> RAG Context
-> Multi-Agent Requirement Gate
-> PRD/Test Gates
-> Multi-Agent Architecture Gate
-> visible Codex Coding
-> independent Quality
-> exact-SHA Code Review
-> Staging
-> Release Gate
-> Observation
-> Evaluation Replay
```

## Feature Flags

建议至少提供：

- `V3_REGISTRY_ENABLED`
- `V3_CONTEXT_MANIFEST_ENABLED`
- `V3_EVALUATION_ENABLED`
- `V3_RAG_ENABLED`
- `V3_MEMORY_ENABLED`
- `V3_MULTI_AGENT_ENABLED`
- `V3_TOOL_GATEWAY_ENABLED`
- `V3_MODEL_ROUTER_ENABLED`

新能力默认关闭，按项目逐步启用。Feature Flag 不能关闭现有安全检查或 Engineer Gate。

## 必需验证

每个里程碑至少执行：

```bash
go test ./...
go vet ./...
kubectl kustomize deploy/overlays/test
```

引入 Python Agent Runtime 后还需执行单元、契约、RAG、评测和安全测试。端到端测试使用专用 GitLab 项目、合成 Confluence 页面和无生产权限的测试凭据。

