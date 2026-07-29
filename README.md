# AI Native Software Development Factory

The Factory is a test-environment control plane that advances requirement work while preserving explicit Engineer Gates. It reads authoritative Confluence pages, produces skeptical requirement/PRD/test artifacts, records all state in PostgreSQL, and collaborates through GitLab Issues.

V1 deliberately stops at `READY_FOR_ARCHITECTURE`. It cannot write code, merge changes, deploy applications, release to production, or bypass an Engineer Gate.

## V1 flow

```text
GitLab Intake Issue
  -> immutable Confluence snapshots and GitLab-hosted visuals
  -> Requirement Agent
  -> Engineer Requirement Gate
  -> approved independently deliverable child Issues
  -> PRD Agent + Test Agent
  -> independent PRD and Test Gates
  -> READY_FOR_ARCHITECTURE
```

The API accepts only GitLab Issue and Note webhooks. The worker consumes a durable PostgreSQL queue, delivers a transactional outbox, and performs a ten-minute reconciliation scan for missed Gate comments.

## Factory Control Room

The API also serves a read-only operations dashboard at:

```text
http://127.0.0.1:8080/dashboard/
```

It refreshes every ten seconds and presents workflow state-machine progress, open Engineer Gates and copyable approval commands, latest Agent artifacts, immutable Confluence source versions, audit activity, queue health, and dead-letter failures. The local Compose port exposes it for development. The test-environment Ingress exposes only the exact GitLab webhook path. In the test cluster, the administrator-only User Center page reads the same JSON contract through the Hermes backend-for-frontend; browsers never call this service directly.

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

Export the six required integration values in the current shell without committing them:

```bash
export GITLAB_API_TOKEN="$(glab config get token --host git.kuainiujinke.com --global)"
export GITLAB_WEBHOOK_SECRET="$(openssl rand -hex 32)"
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
- `CONFLUENCE_EMAIL`
- `CONFLUENCE_API_TOKEN`
- `OPENAI_API_KEY`

Non-secret configuration includes `GITLAB_API_URL`, `CONFLUENCE_BASE_URL`, `OPENAI_API_URL`, `OPENAI_MODEL`, worker intervals, and a project configuration file. See [`docs/operations.md`](docs/operations.md).

## Repository map

- `cmd/factory-api`: authenticated Webhook Receiver.
- `cmd/factory-worker`: event, Agent, state-machine, outbox, and reconciliation worker.
- `cmd/factory-migrate`: PostgreSQL migration job.
- `internal/agents`: OpenAI Responses API structured-output contracts and renderers.
- `internal/connectors`: Confluence and GitLab API clients.
- `internal/dashboard`: embedded read-only control room and dashboard API.
- `internal/store`: PostgreSQL schema, queue leases, outbox, Gates, snapshots, and audit log.
- `deploy/overlays/test`: ACK test-environment manifests.

See [`docs/architecture.md`](docs/architecture.md), [`docs/security.md`](docs/security.md), and [`docs/testing.md`](docs/testing.md) for the implementation contract.
