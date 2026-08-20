# V3 测试环境部署与验收手册

## 前置条件

- Kubernetes 数据库必须安装 PostgreSQL `vector` 扩展；本地 Compose 已使用 `pgvector/pgvector:pg16`。
- 使用不可变测试镜像标签，禁止使用生产凭据。
- `ai-sdlc-factory-secrets` 由集群密钥系统创建，不提交明文 Secret。
- GitLab 测试项目、Confluence 合成需求页和测试模型账号均无生产权限。

## 上线顺序

1. 备份测试数据库并确认可回滚。
2. 运行迁移 Job，确认 `schema_migrations` 已包含 `005_v3_pgvector.sql`。
3. 保持全部 V3 开关关闭，部署 API 和 Worker，验证 V2 基线。
4. 依次开启 Registry、Context Manifest、Evaluation、RAG、Memory、Multi-Agent、Tool Gateway、Model Router。
5. 每开启一个开关，完成对应检查后再继续；失败时关闭当前开关并回滚镜像。

## 全功能测试配置

```env
V3_REGISTRY_ENABLED=true
V3_CONTEXT_MANIFEST_ENABLED=true
V3_EVALUATION_ENABLED=true
V3_RAG_ENABLED=true
V3_MEMORY_ENABLED=true
V3_MULTI_AGENT_ENABLED=true
V3_TOOL_GATEWAY_ENABLED=true
V3_MODEL_ROUTER_ENABLED=true
V3_MODEL_FALLBACK_ENABLED=false
V3_MODEL_BUDGET_MICROUNITS=0
```

高风险 Agent 禁止静默降级。首次验收保持 Fallback 关闭；启用前必须配置模型目录、预算并完成影子评测。

## 部署命令

```bash
kubectl kustomize deploy/overlays/test
kubectl apply -k deploy/overlays/test
kubectl -n ai-factory-test wait --for=condition=complete job/ai-sdlc-factory-migrate --timeout=180s
kubectl -n ai-factory-test rollout status deployment/ai-sdlc-factory-api
kubectl -n ai-factory-test rollout status deployment/ai-sdlc-factory-worker
```

Dashboard 不通过 Ingress 暴露。管理员临时查看时执行：

```bash
kubectl -n ai-factory-test port-forward service/ai-sdlc-factory 18080:8080
```

访问 `http://127.0.0.1:18080/dashboard/`。

## 端到端验收

1. 在测试 GitLab 项目创建带 `automation::enabled` 标签、包含 Confluence 链接的 Issue。
2. 确认 Confluence 快照、Hash、知识版本与 pgvector 分块均已生成。
3. 检查 Agent 的 Prompt、模型、Profile、Context Manifest、Token、费用和路由证据。
4. 确认 RAG 引用可回到来源版本；撤销来源后，新 Run 不得再次选中该来源。
5. 检查 Primary、Critic、Security/Reliability、Judge 均有独立 Run 和 Opinion，少数意见未被覆盖。
6. Engineer Gate 未批准前不得执行写操作；生产部署工具必须拒绝。
7. 运行候选 Prompt 影子评测，确认没有修改真实 Workflow、Gate、Issue、MR 或部署状态。
8. 完成可见 Codex 任务、独立质量检查、精确 SHA 合并、测试环境部署、发布门禁和观察阶段。

## 回滚

- 先关闭本次启用的 V3 Feature Flag，再回滚 Deployment 镜像。
- 数据库迁移向前兼容，不删除 V3 表和证据；V2 代码会忽略这些表。
- 不删除数据库卷，不修改生产配置。
- 队列出现死信时先保留关联 Run ID，再使用修复后的镜像重试，不篡改审计记录。

## 发布前验证

```bash
go test ./...
go vet ./...
kubectl kustomize deploy/overlays/test
```

还必须使用带 pgvector 的隔离 PostgreSQL 执行 `-tags=integration` 测试。
