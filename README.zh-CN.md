# Agentic SDLC Control Plane

[English](README.md) | [简体中文](README.zh-CN.md)

一个可审计、由工程师治理的软件交付控制平面：将权威需求转化为经过评审的产物、工程师可见的编码任务、绑定精确提交的质量证据，以及测试环境发布。

项目由两个相互配合的系统组成：

- **交付工作流（Delivery Workflow）**：控制下一步允许发生什么，包括状态流转、工程师门禁、工作项、合并请求、CI、部署、观察和审计证据。
- **Agent 平台（Agent Platform）**：控制 AI 如何开展工作，包括版本化 Prompt 与 Profile、模型路由、RAG、多 Agent 评审、工具策略、评测、灰度治理和持续改进。

AI 负责生成结构化候选结果；确定性策略和授权工程师始终掌握交付决策权。

> 本仓库是测试环境控制平面。生产环境由配置锁定，仓库不保存生产凭据，Factory 也不会在无人可见的情况下运行编码 Agent。

## 为什么需要这个项目

多数 AI 研发演示止步于生成代码或文档，但真实软件交付需要更强的保证：

- 需求能够追溯到不可变的权威来源；
- AI 输出可评审、可复现，并绑定精确版本；
- 编码过程始终对工程师可见；
- 质量审批绑定合并请求的精确提交；
- 部署必须具备证据和明确授权；
- Prompt 与模型变更在激活前必须经过评测；
- 所有决策在服务重启后仍可审计。

本项目把这些保证实现为持久化工作流状态，而不是仅依赖 Prompt 约定。

## 系统架构

```mermaid
flowchart LR
    CF[Confluence 权威需求] --> WF[交付工作流]
    GL[GitLab Issue / MR / Pipeline] <--> WF
    CB[质量与交付回调] --> WF

    WF --> AP[Agent 平台]
    AP --> REG[Prompt / 模型 / Profile 注册表]
    AP --> RAG[混合 RAG 与项目记忆]
    AP --> MA[Primary / Critic / Security / Judge]
    AP --> TG[工具网关与策略]
    AP --> EV[评测与灰度治理]

    WF --> GATE[工程师门禁]
    GATE --> CODEX[工程师可见的 Codex 任务]
    CODEX --> GL
    WF --> TEST[测试部署与观察]

    WF --> DB[(PostgreSQL + pgvector)]
    AP --> DB
```

权限边界是单向的：工作流可以请求 Agent 生成候选结果，但 Agent 不能批准门禁、合并代码、为自己授权工具、激活自己的 Prompt，也不能批准生产发布。

## 端到端交付流程

```text
GitLab Feature Issue
  -> 不可变 Confluence 快照
  -> 需求 Agent + 受治理的多 Agent 评审
  -> 需求工程师门禁
  -> 已批准工作项
  -> PRD Agent + 测试 Agent
  -> 独立的 PRD 与测试门禁
  -> 架构 Agent + 受治理的多 Agent 评审
  -> 架构工程师门禁
  -> READY_FOR_CODEX 工作项
  -> 工程师可见的 Codex 调度
  -> Merge Request
  -> 绑定精确 Head SHA 的独立质量任务
  -> 代码审查工程师门禁
  -> 合并与发布 CI
  -> 测试环境部署和验证
  -> 发布工程师门禁
  -> 观察窗口
  -> COMPLETED
```

本地演示已经完整运行过上述流程，包括 19 次持久化工作流修订、6 个已批准工程师门禁、13 次已完成 Agent Run、1 个已合并工作项、测试部署证据和完整观察窗口。

## Agent 平台

### 不可变注册表与运行证据

- Prompt、模型、Agent Profile、Skill 和 Tool 的版本化记录；
- 受治理的激活、回滚与审计历史；
- 运行时绑定精确 Prompt、模型、Schema、Profile 和上下文版本；
- 记录 Agent Run 阶段、步骤、Provider Response ID、Token、成本、延迟、结束原因和错误分类；
- Profile 可约束输出 Token、推理强度和工具调用预算。

### 知识与上下文

- PostgreSQL 全文检索与 `pgvector` 相似度检索；
- 使用 Reciprocal Rank Fusion 的混合检索；
- 查询改写、上下文压缩、权威性排序和引用验证；
- 不可变 Context Manifest；
- 项目记忆候选的审批、撤销、过期与来源追踪。

### 多 Agent 评审

- Primary、Critic、Security/Reliability 和 Judge 独立运行；
- 每个角色保留独立运行证据和意见；
- 保存置信度、综合结论、未解决风险和少数意见；
- 当前用于需求评审和架构评审。

### 工具与模型治理

- 默认拒绝的 Tool Registry 与策略判定；
- 执行前进行 JSON Schema 校验；
- 按项目、Agent、工作流状态和风险进行授权；
- 写操作需要事务发件箱和对应工程师门禁；
- 明确拒绝生产操作；
- 根据健康度、能力、风险和预算进行模型路由，高风险任务不会静默降级。

### 评测与持续改进

- 不可变评测套件与用例，支持 TEST 和 HOLDOUT 数据划分；
- 确定性契约评分与版本化 LLM Judge 评分；
- 历史工作流回放和影子评测；
- 基线与候选版本的配对比较及置信区间；
- 盲评、限定范围灰度、晋升和回滚治理；
- 根据低分样本和重复运行故障生成仅供评审的改进候选。

任何评测路径都不能修改交付工作流、批准门禁或部署软件。

## 控制中心

API 内嵌两个每 10 秒刷新一次的只读控制台：

| 页面 | 用途 | 本地地址 |
|---|---|---|
| 交付工作流 | 工作流状态、门禁、工作项、产物、来源、队列、故障和审计活动 | `http://127.0.0.1:8080/dashboard/` |
| Agent 平台 | 注册表、运行、用量、路由、意见、评测、知识、工具、治理和改进候选 | `http://127.0.0.1:8080/dashboard/v3/` |

测试集群 Ingress 只暴露经过认证的精确集成路径，不会公开控制台页面。

## 本地无 Token 演示

演示 Overlay 使用确定性的结构化输出模型 Mock，不需要外部模型 Token，可用于展示 UI、工作流、集成、评测与治理能力。合成结果会标记为仅限本地使用，不能作为发布或生产证据。

```bash
GITLAB_WEBHOOK_SECRET=local-webhook \
CALLBACK_SHARED_SECRET=local-callback \
CODING_AGENT_SHARED_SECRET=local-coding-agent \
AGENT_RUNTIME_SHARED_SECRET=local-runtime \
OPENAI_API_KEY=local-demo-not-a-credential \
docker compose -f compose.yaml -f compose.demo.yaml up -d --build \
  postgres migrate api agent-runtime model-mock knowledge-indexer
```

启动后访问：

```text
http://127.0.0.1:8080/dashboard/
http://127.0.0.1:8080/dashboard/v3/
```

完整交付流程还需要测试 GitLab 和 Confluence 端点。仓库提供了用于演示这些集成边界的本地 Fixture 命令，详见[本地 Agent 平台演示](docs/v3/local-demo.md)。

独立的 Hello World 示例接口：

```bash
curl http://127.0.0.1:8080/hello
```

```json
{"message":"Hello, World!","service":"ai-sdlc-factory"}
```

## 连接测试环境

凭据只能通过环境变量或 Kubernetes Secret 提供，禁止写入仓库。

```bash
export GITLAB_API_TOKEN='...'
export GITLAB_WEBHOOK_SECRET='...'
export CALLBACK_SHARED_SECRET='...'
export CODING_AGENT_SHARED_SECRET='...'
export CONFLUENCE_EMAIL='service-account@example.com'
export CONFLUENCE_API_TOKEN='...'
export OPENAI_API_KEY='...'

docker compose up -d --build
docker compose ps
curl -i http://127.0.0.1:8080/readyz
```

可选交付适配器使用 `DELIVERY_TRIGGER_URL` 和 `DELIVERY_TRIGGER_TOKEN`。除非项目配置明确启用，否则生产环境始终关闭；仓库也不会提供生产凭据。

## 工程师命令

获得授权且仍处于活跃状态的 GitLab 项目成员，可通过精确命令决定门禁：

```text
/approve gate:<uuid>
/request-changes gate:<uuid>
说明需要修改的内容。
/reject gate:<uuid>
说明产物必须返工的原因。
```

工程师可见的编码任务通过以下命令记录：

```text
/start-codex task:<work-item-uuid> client:<client-id>
```

调度记录不是租赁式 Runner。Factory 只记录工程师启动了一个可见 Codex 任务，不会自行执行编码任务。

## 验证

依赖已提交到 `vendor`，避免本地和 CI 将私有模块路径泄露给公共模块代理。

```bash
make verify
```

该命令执行：

```bash
go test -mod=vendor ./...
go vet -mod=vendor ./...
go build -mod=vendor ./cmd/...
kubectl kustomize deploy/overlays/test
```

使用一次性 PostgreSQL 16 + pgvector 实例运行集成测试：

```bash
docker compose up -d postgres
export DATABASE_TEST_URL='postgres://factory:factory@127.0.0.1:5433/ai_sdlc_factory_test?sslmode=disable'
go test -mod=vendor -tags=integration ./internal/store ./internal/engine ./internal/toolgateway
```

## 运行组件

| 组件 | 职责 |
|---|---|
| `factory-api` | 经过认证的 Webhook、回调、健康检查与控制台 |
| `factory-worker` | 工作流状态机、队列消费、发件箱投递与补偿处理 |
| `factory-agent-runtime` | 凭据边界与受治理的结构化模型请求 |
| Agent Dispatcher | 需求、规划与架构 Agent 事件 |
| Evaluation Worker | 隔离的影子评测事件 |
| Knowledge Indexer | PostgreSQL/pgvector 知识索引 |
| `factory-migrate` | 有序且可重复执行的数据库迁移 |
| PostgreSQL + pgvector | 工作流、证据、注册表、知识、评测与审计历史 |

## 仓库结构

```text
cmd/                         可执行服务与本地演示命令
internal/agents/             结构化 Agent 契约与模型客户端
internal/agentruntime/       受治理的模型提供方边界
internal/engine/             交付工作流与 Agent 编排
internal/store/              PostgreSQL 状态、证据、注册表与评测
internal/knowledge/          检索与上下文策略
internal/multiagent/         独立角色编排
internal/codingagent/        通用任务清单、Pi Bridge 客户端与执行证据
internal/toolgateway/        工具授权与 MCP 网关
internal/dashboard/          内嵌交付工作流与 Agent 平台控制台
deploy/                      Kubernetes 基础清单与测试 Overlay
docs/                        架构、运维、安全与测试文档
docs/v3/                     Agent 平台设计与运行手册
```

## 安全属性

- Confluence 和 GitLab 内容被视为不可信数据，绝不作为可执行指令；
- 所有外部写操作都经过确定性策略，必要时还需工程师门禁；
- 模型提供方凭据只存在于隔离的 Agent Runtime；
- Worker 只获得自身角色必需的凭据；
- 工具调用默认拒绝并保留完整轨迹；
- 质量与代码审查要求绑定精确 SHA 的证据；
- Prompt/模型激活需要评测和治理证据；
- 生产环境默认关闭，仓库不保存生产凭据。

更多信息参见[架构](docs/architecture.md)、[安全](docs/security.md)、[测试](docs/testing.md)、[运维](docs/operations.md)和 [Agent 平台设计索引](docs/v3/README.md)。

## 当前范围

已经实现并完成本地验证：

- 端到端测试环境交付工作流；
- 不可变来源和产物追踪；
- 工程师门禁与可见 Codex 调度；
- 绑定精确 SHA 的 MR 质量证据；
- Agent 平台注册表、运行证据、RAG、记忆、多 Agent 评审、工具、路由、评测、灰度治理和改进候选；
- Docker Compose 与 Kubernetes 测试部署；
- 只读的交付工作流和 Agent 平台控制台。

明确排除：

- Factory 无人值守地运行编码 Agent；
- Agent 自行审批；
- 自动激活改进候选；
- 未经评审的生产部署或迁移；
- 保存生产凭据。
