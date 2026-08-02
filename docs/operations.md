# Test Environment Operations

## Prerequisites

- PostgreSQL 14+ database `ai_sdlc_factory_test`; do not reuse an Argus application schema or database role.
- An ACK namespace named `ai-factory-test`.
- A project-scoped GitLab bot token with only the API access needed for Issues, notes, uploads, and membership checks.
- A read-only Confluence service account.
- A project-scoped OpenAI API key with a test budget.
- HTTPS ingress reachable from the self-managed GitLab host.
- A dedicated registry robot account configured as protected, masked, hidden `REGISTRY_USER` and `REGISTRY_PASSWORD`, plus non-secret `REGISTRY_HOST` and `REGISTRY_IMAGE`.

## Project configuration

Copy `deploy/overlays/test/projects.json` and replace every zero/placeholder with verified GitLab project member data. Each Gate must have at least one numeric reviewer ID. Usernames are notification text only; authorization always uses IDs plus an API check that the actor is still an active project member.

Only open Issues carrying `automation::enabled` are eligible.

## Secret creation

Use a secret manager or protected, masked, hidden GitLab CI variables. Never commit `secret.yaml`.

```bash
kubectl -n ai-factory-test create secret generic ai-sdlc-factory-secrets \
  --from-literal=DATABASE_URL="$DATABASE_URL" \
  --from-literal=GITLAB_API_TOKEN="$GITLAB_API_TOKEN" \
  --from-literal=GITLAB_WEBHOOK_SECRET="$GITLAB_WEBHOOK_SECRET" \
  --from-literal=CALLBACK_SHARED_SECRET="$CALLBACK_SHARED_SECRET" \
  --from-literal=CONFLUENCE_EMAIL="$CONFLUENCE_EMAIL" \
  --from-literal=CONFLUENCE_API_TOKEN="$CONFLUENCE_API_TOKEN" \
  --from-literal=OPENAI_API_KEY="$OPENAI_API_KEY"
```

Configure `OPENAI_API_KEY` as Masked and Hidden before deployment. Rotate any key that has appeared in terminal output, CI logs, Issue text, or chat.

For end-to-end delivery, configure `DELIVERY_TRIGGER_URL` and `DELIVERY_TRIGGER_TOKEN` together. The fixed adapter endpoint receives idempotent `release_ci`, `staging_deploy`, `staging_verify`, and conditionally `production_deploy` requests. It must return progress through the authenticated Jenkins callback. Do not accept a client-supplied adapter URL.

The `staging_verified` callback must declare whether production data or schema migration is required. When it is required, both plans are mandatory and Factory opens a Production Migration Gate before it opens the separate Release Gate:

```json
{
  "external_id": "verify-123",
  "project_id": 3533,
  "workflow_id": "<workflow-uuid>",
  "status": "staging_verified",
  "commit_sha": "<40-character-sha>",
  "requires_production_migration": true,
  "migration_plan": "Exact reviewed migration procedure",
  "rollback_plan": "Exact tested rollback procedure",
  "change_window": "2026-08-01T01:00:00Z"
}
```

## Deployment

The pipeline always compiles deployable Linux binaries. After dedicated registry variables are verified, set `ENABLE_TEST_IMAGE_PUBLISH=true` to enable image publication. After every deployment prerequisite is verified, set `ENABLE_TEST_DEPLOY=true`; `deploy_test` still remains a manual Engineer Gate. It creates the image-pull Secret, applies runtime Secrets, runs the migration Job, and then waits for API and worker rollouts. The migration process uses a PostgreSQL advisory lock and transactional, idempotent DDL.

Before first deployment, verify the ingress class, hostname, TLS secret, registry pull secret, PostgreSQL network route, TLS mode, and NetworkPolicy namespace selectors against the test cluster.

The test Ingress remains webhook-only. The User Center administrator Dashboard reaches `GET /api/dashboard` through the Hermes Pod in `argus-test`; its NetworkPolicy access depends on the exact `app=argus-hermes-api` Pod label. Do not expose the Dashboard paths through the Factory Ingress.

## GitLab webhook

Create one project webhook after the API is healthy:

- URL: `https://ai-factory-test.kuainiu.io/webhooks/gitlab`
- Secret token: the exact `GITLAB_WEBHOOK_SECRET` stored in Kubernetes
- Events: Issues, Comments, Merge requests, Pushes, Pipelines, Jobs, and Deployments
- SSL verification: enabled

Configure authenticated callbacks with the separate `CALLBACK_SHARED_SECRET`:

- `/callbacks/quality`: structured evidence from the independent visible Codex Quality task.
- `/callbacks/jenkins`: release CI, staging, production, and observation status.
- `/callbacks/monitoring`: Sentry or Alertmanager incident intake.

The callback secret is test-only and must not be reused as the GitLab webhook secret.

## Recovery

- A worker crash is recovered when its two-minute lease expires.
- A transient connector/model failure retries with exponential backoff up to eight attempts.
- Dead records remain queryable for manual diagnosis; do not delete audit records to force a retry.
- The ten-minute reconciliation event scans waiting workflows for valid Gate commands that may have missed webhook delivery.
- Engineer Codex Dispatchers query GitLab directly every ten minutes. They do not depend on email discovery and do not run code inside the heartbeat.
- Restoring PostgreSQL restores workflow state; GitLab writes are replay-safe through stable markers and deterministic child-Issue markers.
