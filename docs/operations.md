# Test Environment Operations

## Prerequisites

- PostgreSQL 14+ database `ai_sdlc_factory_test`; do not reuse an Argus application schema or database role.
- An ACK namespace named `ai-factory-test`.
- A project-scoped GitLab bot token with only the API access needed for Issues, notes, uploads, and membership checks.
- A read-only Confluence service account.
- A project-scoped OpenAI API key with a test budget.
- HTTPS ingress reachable from the self-managed GitLab host.
- GitLab Container Registry enabled for this project and a project Deploy Token with `read_registry`, exposed as protected, masked `CI_DEPLOY_USER` and `CI_DEPLOY_PASSWORD` variables.
- A protected Kubernetes file variable or GitLab Agent context that allows only the `ai-factory-test` namespace. Set `KUBE_CONTEXT` only when the kubeconfig contains more than one context.

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

Merge-request and branch pipelines run unit, integration, vet, and manifest verification. A push to the protected default branch publishes both the immutable commit image and the moving `test` tag to the project GitLab Container Registry, then automatically deploys that exact commit image. The deploy job validates every required protected variable before touching the cluster, creates the long-lived image-pull Secret from the read-only Deploy Token, applies runtime Secrets, runs the migration Job, and waits for API and worker rollouts. The migration process uses a PostgreSQL advisory lock and transactional, idempotent DDL.

Required protected CI variables are `DATABASE_URL`, `GITLAB_API_TOKEN`, `GITLAB_WEBHOOK_SECRET`, `CALLBACK_SHARED_SECRET`, `CONFLUENCE_EMAIL`, `CONFLUENCE_API_TOKEN`, `OPENAI_API_KEY`, `CI_DEPLOY_USER`, and `CI_DEPLOY_PASSWORD`. Kubernetes authentication must be supplied as a protected file variable (normally `KUBECONFIG`) or by a GitLab Agent context. `DELIVERY_TRIGGER_TOKEN` is optional and must be paired with the fixed `DELIVERY_TRIGGER_URL` configuration when enabled.

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
