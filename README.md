# AI Native Software Development Factory

The Factory is a test-environment control plane that advances software delivery while preserving explicit Engineer Gates. It reads authoritative Confluence pages, produces skeptical product and architecture artifacts, coordinates engineer-visible Codex tasks, validates MR evidence, and records every state and decision in PostgreSQL and GitLab.

Factory never runs Codex headlessly. Engineers execute code changes in visible Codex Coding tasks and independent Quality tasks. Factory records dispatch, evaluates hard quality evidence, and coordinates merge and delivery only after the applicable Engineer Gate. Production remains disabled by default and no production credential is configured.

## V2 flow

```text
GitLab Intake Issue
  -> immutable Confluence snapshots and GitLab-hosted visuals
  -> Requirement Agent
  -> Engineer Requirement Gate
  -> approved independently deliverable child Issues
  -> PRD Agent + Test Agent
  -> independent PRD and Test Gates
  -> Architecture Agent and Engineer Architecture Gate
  -> assigned Work Items become READY_FOR_CODEX
  -> engineer Dispatcher records /start-codex
  -> visible Codex Coding task + worktree
  -> MR + separate visible Quality task
  -> exact-SHA Engineer Code Review Gate
  -> integration MR + release CI + staging verification
  -> Engineer Release Gate
  -> locked production adapter or test observation
  -> COMPLETED
```

The API accepts GitLab Issue, Note, MR, Pipeline, Job, Deployment, and Push webhooks plus authenticated Jenkins, Quality, and monitoring callbacks. The worker consumes a durable PostgreSQL queue, delivers a transactional outbox, and performs a ten-minute compensation scan. Queue leases recover Factory worker crashes; they are unrelated to Codex execution, which has no lease, renewal, or heartbeat.

## Factory Control Room

The API also serves a read-only operations dashboard at:

```text
http://127.0.0.1:8080/dashboard/
```

It refreshes every ten seconds and presents workflow state-machine progress, Work Items and visible Codex dispatches, open Engineer Gates, latest Agent artifacts, immutable sources, audit activity, queue health, and dead-letter failures. The local Compose port exposes it for development. The test-environment Ingress exposes only exact webhook and authenticated callback paths, never Dashboard paths. In the test cluster, the administrator-only User Center page reads the same JSON contract through the Hermes backend-for-frontend; browsers never call this service directly.

## Gate commands

An authorized active GitLab project member decides a Gate with a comment:

```text
/approve gate:<uuid>
/request-changes gate:<uuid>
Explain the required changes.
/reject gate:<uuid>
Explain why the artifact must be reworked.
```

`Reject` returns the artifact to the same Gate. It does not authorize cancellation, coding, or a later stage.

## Local verification

Go dependencies are vendored so CI and local builds do not disclose the private module path to a public module proxy.

```bash
make verify
```

To run against the disposable PostgreSQL 16 instance:

```bash
docker compose up -d postgres
export DATABASE_TEST_URL='postgres://factory:factory@127.0.0.1:5433/ai_sdlc_factory_test?sslmode=disable'
go test -mod=vendor -tags=integration ./internal/store
```

## Local full stack with Docker Compose

Export the required integration values in the current shell without committing them:

```bash
export GITLAB_API_TOKEN="$(glab config get token --host git.kuainiujinke.com --global)"
export GITLAB_WEBHOOK_SECRET="$(openssl rand -hex 32)"
export CALLBACK_SHARED_SECRET="$(openssl rand -hex 32)"
export CONFLUENCE_EMAIL='service-account@example.com'
read -s 'CONFLUENCE_API_TOKEN?Confluence token: '; echo; export CONFLUENCE_API_TOKEN
read -s 'OPENAI_API_KEY?OpenAI API key: '; echo; export OPENAI_API_KEY
```

Then start PostgreSQL, run the idempotent migration, and launch the API and worker:

```bash
docker compose up -d --build
docker compose ps
curl -i http://127.0.0.1:8080/readyz
```

Open `http://127.0.0.1:8080/dashboard/` after the API is healthy. Use `docker compose logs -f api worker` for runtime logs. The project configuration is mounted read-only at
`/etc/factory/projects.json`; no `FACTORY_PROJECTS_FILE` export is required for Compose.

## Configuration

Runtime secrets are required only through environment variables or Kubernetes Secrets:

- `DATABASE_URL`
- `GITLAB_API_TOKEN`
- `GITLAB_WEBHOOK_SECRET`
- `CALLBACK_SHARED_SECRET`
- `CONFLUENCE_EMAIL`
- `CONFLUENCE_API_TOKEN`
- `OPENAI_API_KEY`
- `DELIVERY_TRIGGER_TOKEN` when the outbound test delivery adapter is enabled

Non-secret configuration includes `GITLAB_API_URL`, `CONFLUENCE_BASE_URL`, `OPENAI_API_URL`, `OPENAI_MODEL`, optional `DELIVERY_TRIGGER_URL`, worker intervals, and a project configuration file. See [`docs/operations.md`](docs/operations.md).

## Repository map

- `cmd/factory-api`: authenticated Webhook Receiver.
- `cmd/factory-worker`: event, Agent, state-machine, outbox, and reconciliation worker.
- `cmd/factory-migrate`: PostgreSQL migration job.
- `internal/agents`: OpenAI Responses API structured-output contracts and renderers.
- `internal/connectors`: Confluence and GitLab API clients.
- `internal/dashboard`: embedded read-only control room and dashboard API.
- `internal/store`: PostgreSQL schema, Factory queue leases, work items, Codex dispatch records, MR/quality/release evidence, Gates, snapshots, and audit log.
- `deploy/overlays/test`: ACK test-environment manifests.

See [`docs/architecture.md`](docs/architecture.md), [`docs/security.md`](docs/security.md), and [`docs/testing.md`](docs/testing.md) for the implementation contract.

## V3 design

The proposed governed Agent platform extension is documented in Chinese under [`docs/v3/`](docs/v3/README.md). It covers versioned prompts and models, context manifests, hybrid RAG, governed project memory, multi-Agent review, tool and MCP policy, evaluation, security, and phased delivery.
