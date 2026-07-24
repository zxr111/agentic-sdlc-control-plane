# AI Native Software Development Factory

The Factory is a test-environment control plane that advances requirement work while preserving explicit Engineer Gates. It reads authoritative Confluence pages, produces skeptical requirement/PRD/test artifacts, records all state in MySQL, and collaborates through GitLab Issues.

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

The API accepts only GitLab Issue and Note webhooks. The worker consumes a durable MySQL queue, delivers a transactional outbox, and performs a ten-minute reconciliation scan for missed Gate comments.

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

To run against a disposable MySQL 8 instance:

```bash
docker compose up -d mysql
export MYSQL_TEST_DSN='factory:factory@tcp(127.0.0.1:3307)/ai_sdlc_factory_test?parseTime=true&charset=utf8mb4&collation=utf8mb4_0900_ai_ci&loc=UTC'
go test -mod=vendor -tags=integration ./internal/store
```

## Configuration

Runtime secrets are required only through environment variables or Kubernetes Secrets:

- `MYSQL_DSN`
- `GITLAB_API_TOKEN`
- `GITLAB_WEBHOOK_SECRET`
- `CONFLUENCE_EMAIL`
- `CONFLUENCE_API_TOKEN`
- `OPENAI_API_KEY`

Non-secret configuration includes `GITLAB_API_URL`, `CONFLUENCE_BASE_URL`, `OPENAI_API_URL`, `OPENAI_MODEL`, worker intervals, and a project configuration file. See [`docs/operations.md`](docs/operations.md).

## Repository map

- `cmd/factory-api`: authenticated Webhook Receiver.
- `cmd/factory-worker`: event, Agent, state-machine, outbox, and reconciliation worker.
- `cmd/factory-migrate`: MySQL migration job.
- `internal/agents`: OpenAI Responses API structured-output contracts and renderers.
- `internal/connectors`: Confluence and GitLab API clients.
- `internal/store`: InnoDB schema, queue leases, outbox, Gates, snapshots, and audit log.
- `deploy/overlays/test`: ACK test-environment manifests.

See [`docs/architecture.md`](docs/architecture.md), [`docs/security.md`](docs/security.md), and [`docs/testing.md`](docs/testing.md) for the implementation contract.
