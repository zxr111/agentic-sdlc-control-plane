# Agentic SDLC Control Plane

An auditable, engineer-governed software delivery control plane that turns an authoritative requirement into reviewed artifacts, visible coding work, exact-commit quality evidence, and a test-environment release.

The project combines two complementary systems:

- **Delivery Workflow** controls what may happen next: state transitions, Engineer Gates, work items, merge requests, CI, deployment, observation, and audit evidence.
- **Agent Platform** controls how AI work is performed: versioned prompts and profiles, model routing, RAG, multi-agent review, tool policy, evaluation, canary governance, and continuous improvement.

AI generates structured candidates. Deterministic policy and authorized engineers retain delivery authority.

> This repository is a test-environment control plane. Production is locked by configuration, production credentials do not belong here, and the Factory never runs coding agents headlessly.

## Why this project exists

Most AI development demos stop after generating code or a document. Real delivery needs stronger guarantees:

- requirements remain traceable to an immutable source;
- AI output is reviewable, reproducible, and bound to exact versions;
- coding work remains visible to an engineer;
- quality approval refers to the exact merge-request commit;
- deployment requires evidence and explicit authorization;
- prompt and model changes are evaluated before activation;
- every decision survives restarts and remains auditable.

This control plane implements those guarantees as persisted workflow state rather than prompt conventions.

## System overview

```mermaid
flowchart LR
    CF[Confluence requirement] --> WF[Delivery Workflow]
    GL[GitLab Issue / MR / Pipeline] <--> WF
    CB[Quality and delivery callbacks] --> WF

    WF --> AP[Agent Platform]
    AP --> REG[Prompt / Model / Profile Registry]
    AP --> RAG[Hybrid RAG and Project Memory]
    AP --> MA[Primary / Critic / Security / Judge]
    AP --> TG[Tool Gateway and Policy]
    AP --> EV[Evaluation and Canary Governance]

    WF --> GATE[Engineer Gates]
    GATE --> CODEX[Engineer-visible Codex tasks]
    CODEX --> GL
    WF --> TEST[Test deployment and observation]

    WF --> DB[(PostgreSQL + pgvector)]
    AP --> DB
```

The relationship is intentionally one-way at the authority boundary: the workflow may ask an Agent to produce a candidate, but an Agent cannot approve a Gate, merge code, grant itself a tool, activate its own prompt, or authorize production.

## End-to-end delivery lifecycle

```text
GitLab Feature Issue
  -> immutable Confluence snapshot
  -> Requirement Agent + governed multi-agent review
  -> Requirement Engineer Gate
  -> approved work items
  -> PRD Agent + Test Agent
  -> independent PRD and Test Gates
  -> Architecture Agent + governed multi-agent review
  -> Architecture Engineer Gate
  -> READY_FOR_CODEX work item
  -> engineer-visible Codex dispatch
  -> Merge Request
  -> independent quality task bound to the exact head SHA
  -> Code Review Engineer Gate
  -> merge and release CI
  -> test deployment and verification
  -> Release Engineer Gate
  -> observation window
  -> COMPLETED
```

The local demonstration has exercised this complete lifecycle with 19 persisted workflow revisions, six approved Engineer Gates, thirteen completed Agent Runs, one merged work item, test deployment evidence, and a completed observation window.

## Agent Platform

### Immutable registry and runtime evidence

- versioned Prompt, Model, Agent Profile, Skill, and Tool records;
- governed activation, rollback, and audit history;
- runtime binding to exact prompt, model, schema, profile, and context versions;
- Agent Run phases, steps, provider response IDs, token usage, cost, latency, finish reason, and error classification;
- profile budgets for output tokens, reasoning effort, and tool calls.

### Knowledge and context

- PostgreSQL full-text search plus `pgvector` similarity search;
- hybrid retrieval with reciprocal-rank fusion;
- query rewriting, context compression, authority ranking, and citation validation;
- immutable Context Manifests;
- governed project-memory candidates with approval, revocation, expiry, and source lineage.

### Multi-agent review

- independent Primary, Critic, Security/Reliability, and Judge runs;
- separate run evidence and opinions for every role;
- confidence, synthesis, unresolved risk, and minority-opinion preservation;
- currently applied to requirement and architecture review.

### Tool and model governance

- deny-by-default Tool Registry and policy evaluation;
- JSON Schema validation before execution;
- authorization by project, Agent, workflow state, and risk;
- transactional outbox and Engineer Gate requirements for writes;
- explicit production denial;
- health-, capability-, risk-, and budget-aware model routing with no silent high-risk fallback.

### Evaluation and continuous improvement

- immutable suites and cases, including TEST and HOLDOUT splits;
- deterministic contract scoring and versioned LLM Judge scoring;
- historical workflow replay and shadow evaluation;
- paired baseline/candidate comparisons with confidence intervals;
- blind review, scoped canary release, promotion, and rollback governance;
- review-only improvement candidates created from weak scores and recurring operational failures.

No evaluation path can mutate a delivery workflow, approve a Gate, or deploy software.

## Control rooms

The API embeds two read-only dashboards that refresh every ten seconds:

| View | Purpose | Local URL |
|---|---|---|
| Delivery Workflow | Workflow state, Gates, work items, artifacts, sources, queues, failures, and audit activity | `http://127.0.0.1:8080/dashboard/` |
| Agent Platform | Registry, runs, usage, routing, opinions, evaluation, knowledge, tools, governance, and improvement candidates | `http://127.0.0.1:8080/dashboard/v3/` |

The test-cluster Ingress exposes only the exact authenticated integration routes. It does not expose these dashboard paths publicly.

## Local token-free demonstration

The demo overlay uses a deterministic structured-output model fixture. It is suitable for UI, workflow, integration, evaluation, and governance demonstrations without an external model token. Synthetic output is marked as local-only and is never valid release or production evidence.

```bash
GITLAB_WEBHOOK_SECRET=local-webhook \
CALLBACK_SHARED_SECRET=local-callback \
AGENT_RUNTIME_SHARED_SECRET=local-runtime \
OPENAI_API_KEY=local-demo-not-a-credential \
docker compose -f compose.yaml -f compose.demo.yaml up -d --build \
  postgres migrate api agent-runtime model-mock knowledge-indexer
```

Then open:

```text
http://127.0.0.1:8080/dashboard/
http://127.0.0.1:8080/dashboard/v3/
```

The demo model requires no external token. A complete delivery run also needs test GitLab and Confluence endpoints; the repository includes local-only fixture commands used to exercise those integration boundaries. See [Local Agent Platform demonstration](docs/v3/local-demo.md).

The standalone sample endpoint is available at:

```bash
curl http://127.0.0.1:8080/hello
```

```json
{"message":"Hello, World!","service":"ai-sdlc-factory"}
```

## Running with test integrations

Provide credentials only through environment variables or Kubernetes Secrets. Never write them into the repository.

```bash
export GITLAB_API_TOKEN='...'
export GITLAB_WEBHOOK_SECRET='...'
export CALLBACK_SHARED_SECRET='...'
export CONFLUENCE_EMAIL='service-account@example.com'
export CONFLUENCE_API_TOKEN='...'
export OPENAI_API_KEY='...'

docker compose up -d --build
docker compose ps
curl -i http://127.0.0.1:8080/readyz
```

Optional delivery adapters use `DELIVERY_TRIGGER_URL` and `DELIVERY_TRIGGER_TOKEN`. Production remains disabled unless the project configuration explicitly enables it, and no production credential is supplied by this repository.

## Engineer commands

Authorized and currently active GitLab project members decide Gates through exact commands:

```text
/approve gate:<uuid>
/request-changes gate:<uuid>
Explain the required changes.
/reject gate:<uuid>
Explain why the artifact must be reworked.
```

Visible coding work is recorded with:

```text
/start-codex task:<work-item-uuid> client:<client-id>
```

A dispatch record is not a leased runner. The Factory records that an engineer started a visible Codex task; it does not execute the coding task itself.

## Verification

Dependencies are vendored so local and CI verification do not leak the private module path to a public module proxy.

```bash
make verify
```

This runs:

```bash
go test -mod=vendor ./...
go vet -mod=vendor ./...
go build -mod=vendor ./cmd/...
kubectl kustomize deploy/overlays/test
```

PostgreSQL integration tests can be run with a disposable PostgreSQL 16 + pgvector instance:

```bash
docker compose up -d postgres
export DATABASE_TEST_URL='postgres://factory:factory@127.0.0.1:5433/ai_sdlc_factory_test?sslmode=disable'
go test -mod=vendor -tags=integration ./internal/store ./internal/engine ./internal/toolgateway
```

## Runtime components

| Component | Responsibility |
|---|---|
| `factory-api` | Authenticated webhooks, callbacks, health endpoints, and dashboards |
| `factory-worker` | Workflow state machine, queue consumption, outbox delivery, and reconciliation |
| `factory-agent-runtime` | Credential boundary and governed structured model requests |
| Agent Dispatcher | Requirement, planning, and architecture Agent events |
| Evaluation Worker | Isolated shadow evaluation events |
| Knowledge Indexer | PostgreSQL/pgvector knowledge indexing |
| `factory-migrate` | Ordered, idempotent database migrations |
| PostgreSQL + pgvector | Workflow state, evidence, registry, knowledge, evaluation, and audit history |

## Repository map

```text
cmd/                         Executable services and local demo commands
internal/agents/             Structured Agent contracts and model client
internal/agentruntime/       Governed provider boundary
internal/engine/             Delivery workflow and Agent orchestration
internal/store/              PostgreSQL state, evidence, registry, and evaluation
internal/knowledge/          Retrieval and context policies
internal/multiagent/         Independent role orchestration
internal/toolgateway/        Tool authorization and MCP gateway
internal/dashboard/          Embedded Delivery and Agent Platform control rooms
deploy/                      Kubernetes base and test overlay
docs/                        Architecture, operations, security, and testing
docs/v3/                     Agent Platform design and runbooks
```

## Security properties

- Confluence and GitLab content is treated as untrusted data, never as executable instructions.
- Every external write passes deterministic policy and, where required, an Engineer Gate.
- Model provider credentials exist only in the isolated Agent Runtime.
- Workers receive only the credentials required for their role.
- Tool calls are deny-by-default and fully traced.
- Exact-SHA evidence is required for quality and code review.
- Prompt/model activation requires evaluation and governance evidence.
- Production is disabled by default and production credentials are absent.

See [Architecture](docs/architecture.md), [Security](docs/security.md), [Testing](docs/testing.md), [Operations](docs/operations.md), and the [Agent Platform design index](docs/v3/README.md).

## Current scope

Implemented and locally exercised:

- end-to-end test delivery workflow;
- immutable source and artifact traceability;
- Engineer Gates and visible Codex dispatch;
- exact-SHA MR quality evidence;
- Agent Platform registry, runtime evidence, RAG, memory, multi-agent review, tools, routing, evaluation, canary governance, and improvement candidates;
- Docker Compose and Kubernetes test deployment;
- read-only Delivery and Agent Platform dashboards.

Deliberately excluded:

- headless coding-agent execution by the Factory;
- self-approval by an Agent;
- automatic activation of improvement candidates;
- unreviewed production deployment or migration;
- storage of production credentials.
