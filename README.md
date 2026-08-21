# AI 原生软件研发工厂

Factory 是一个面向测试环境的软件研发控制平面，在推进软件交付的同时保留明确的工程师门禁。它读取权威的 Confluence 页面，生成经过审慎分析的产品与架构产物，协调工程师可见的 Codex 任务，验证合并请求（MR）证据，并将每个状态和决策记录到 PostgreSQL 与 GitLab 中。

Factory 不会以无界面方式运行 Codex。工程师在可见的 Codex 编码任务和独立的质量任务中执行代码变更。Factory 负责记录任务分发、评估硬性质量证据，并且只在对应的工程师门禁通过后协调合并与交付。生产环境默认禁用，项目中不配置任何生产环境凭据。

## V2 流程

```text
GitLab 需求入口 Issue
  -> 不可变的 Confluence 快照和托管在 GitLab 的视觉资料
  -> 需求 Agent
  -> 工程师需求门禁
  -> 审批通过且可独立交付的子 Issue
  -> PRD Agent + 测试 Agent
  -> 相互独立的 PRD 门禁和测试门禁
  -> 架构 Agent 和工程师架构门禁
  -> 已分配的工作项进入 READY_FOR_CODEX
  -> 工程师调度器记录 /start-codex
  -> 可见的 Codex 编码任务 + worktree
  -> MR + 独立的可见质量任务
  -> 绑定精确提交 SHA 的工程师代码审查门禁
  -> 集成 MR + 发布 CI + 测试环境验证
  -> 工程师发布门禁
  -> 锁定的生产环境适配器或测试环境观察
  -> COMPLETED
```

API 接收 GitLab 的 Issue、Note、MR、Pipeline、Job、Deployment 和 Push Webhook，以及经过身份验证的 Jenkins、质量和监控回调。Worker 消费持久化的 PostgreSQL 队列、投递事务发件箱，并每十分钟执行一次补偿扫描。队列租约用于恢复 Factory Worker 崩溃，与 Codex 执行无关；Codex 执行不使用租约、续租或心跳机制。

## Factory 控制中心

API 还提供只读的运维仪表盘：

```text
http://127.0.0.1:8080/dashboard/
```

仪表盘每十秒刷新一次，展示工作流状态机进度、工作项、可见的 Codex 调度记录、待处理的工程师门禁、最新 Agent 产物、不可变数据源、审计活动、队列健康状态和死信故障。本地开发通过 Compose 端口访问。测试环境的 Ingress 只暴露精确的 Webhook 路径和经过身份验证的回调路径，不暴露仪表盘路径。在测试集群中，仅管理员可访问的用户中心页面通过 Hermes 后端聚合层读取相同的 JSON 接口；浏览器不会直接调用本服务。

## 门禁命令

具备权限且仍是项目有效成员的 GitLab 用户，可以通过评论处理门禁：

```text
/approve gate:<uuid>
/request-changes gate:<uuid>
说明需要进行的修改。
/reject gate:<uuid>
说明该产物必须返工的原因。
```

`Reject` 会将产物退回到同一个门禁，不代表允许取消、开始编码或进入后续阶段。

## 本地验证

Go 依赖已提交到 `vendor` 目录，避免 CI 和本地构建将私有模块路径暴露给公共模块代理。

```bash
make verify
```

如需使用一次性的 PostgreSQL 16 实例运行集成测试：

```bash
docker compose up -d postgres
export DATABASE_TEST_URL='postgres://factory:factory@127.0.0.1:5433/ai_sdlc_factory_test?sslmode=disable'
go test -mod=vendor -tags=integration ./internal/store
```

## 使用 Docker Compose 启动本地完整环境

在当前终端中设置所需的集成参数，不要将它们提交到仓库：

```bash
export GITLAB_API_TOKEN="$(glab config get token --host git.kuainiujinke.com --global)"
export GITLAB_WEBHOOK_SECRET="$(openssl rand -hex 32)"
export CALLBACK_SHARED_SECRET="$(openssl rand -hex 32)"
export CONFLUENCE_EMAIL='service-account@example.com'
read -s 'CONFLUENCE_API_TOKEN?Confluence token: '; echo; export CONFLUENCE_API_TOKEN
read -s 'OPENAI_API_KEY?OpenAI API key: '; echo; export OPENAI_API_KEY
```

然后启动 PostgreSQL，执行可重复运行的数据库迁移，并启动 API 和 Worker：

```bash
docker compose up -d --build
docker compose ps
curl -i http://127.0.0.1:8080/readyz
```

The first local example requirement is available without external credentials:

```bash
curl http://127.0.0.1:8080/hello
```

It returns `{"message":"Hello, World!","service":"ai-sdlc-factory"}`.

API 健康后，打开 `http://127.0.0.1:8080/dashboard/`。使用 `docker compose logs -f api worker` 查看运行日志。项目配置以只读方式挂载到 `/etc/factory/projects.json`；使用 Compose 时不需要设置 `FACTORY_PROJECTS_FILE`。

## 配置说明

运行时密钥只能通过环境变量或 Kubernetes Secret 提供：

- `DATABASE_URL`
- `GITLAB_API_TOKEN`
- `GITLAB_WEBHOOK_SECRET`
- `CALLBACK_SHARED_SECRET`
- `CONFLUENCE_EMAIL`
- `CONFLUENCE_API_TOKEN`
- `OPENAI_API_KEY`
- 启用测试环境外部交付适配器时需要 `DELIVERY_TRIGGER_TOKEN`

非敏感配置包括 `GITLAB_API_URL`、`CONFLUENCE_BASE_URL`、`OPENAI_API_URL`、`OPENAI_MODEL`、可选的 `DELIVERY_TRIGGER_URL`、Worker 时间间隔和项目配置文件。详情参见 [`docs/operations.md`](docs/operations.md)。

## 仓库目录

- `cmd/factory-api`：经过身份验证的 Webhook 接收服务。
- `cmd/factory-worker`：负责事件、Agent、状态机、事务发件箱和补偿处理的 Worker。
- `cmd/factory-migrate`：PostgreSQL 数据库迁移任务。
- `internal/agents`：OpenAI Responses API 结构化输出契约和渲染器。
- `internal/connectors`：Confluence 和 GitLab API 客户端。
- `internal/dashboard`：内嵌的只读控制中心和仪表盘 API。
- `internal/store`：PostgreSQL 表结构、Factory 队列租约、工作项、Codex 调度记录、MR/质量/发布证据、门禁、快照和审计日志。
- `deploy/overlays/test`：ACK 测试环境部署清单。

实现契约详见 [`docs/architecture.md`](docs/architecture.md)、[`docs/security.md`](docs/security.md) 和 [`docs/testing.md`](docs/testing.md)。

## V3 设计

受治理的 Agent 平台扩展方案位于中文文档 [`docs/v3/`](docs/v3/README.md)，涵盖 Prompt 与模型版本管理、上下文清单、混合 RAG、受治理的项目记忆、多 Agent 审查、工具与 MCP 策略、评测、安全设计和分阶段实施计划。
