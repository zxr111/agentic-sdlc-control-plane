# Test Environment Operations

## Prerequisites

- MySQL 8.0+ InnoDB database `ai_sdlc_factory_test`; do not reuse an Argus application schema.
- An ACK namespace named `ai-factory-test`.
- A project-scoped GitLab bot token with only the API access needed for Issues, notes, uploads, and membership checks.
- A read-only Confluence service account.
- A project-scoped OpenAI API key with a test budget.
- HTTPS ingress reachable from the self-managed GitLab host.

## Project configuration

Copy `deploy/overlays/test/projects.json` and replace every zero/placeholder with verified GitLab project member data. Each Gate must have at least one numeric reviewer ID. Usernames are notification text only; authorization always uses IDs plus an API check that the actor is still an active project member.

Only open Issues carrying `automation::enabled` are eligible.

## Secret creation

Use a secret manager or protected, masked, hidden GitLab CI variables. Never commit `secret.yaml`.

```bash
kubectl -n ai-factory-test create secret generic ai-sdlc-factory-secrets \
  --from-literal=MYSQL_DSN="$MYSQL_DSN" \
  --from-literal=GITLAB_API_TOKEN="$GITLAB_API_TOKEN" \
  --from-literal=GITLAB_WEBHOOK_SECRET="$GITLAB_WEBHOOK_SECRET" \
  --from-literal=CONFLUENCE_EMAIL="$CONFLUENCE_EMAIL" \
  --from-literal=CONFLUENCE_API_TOKEN="$CONFLUENCE_API_TOKEN" \
  --from-literal=OPENAI_API_KEY="$OPENAI_API_KEY"
```

Configure `OPENAI_API_KEY` as Masked and Hidden before deployment. Rotate any key that has appeared in terminal output, CI logs, Issue text, or chat.

## Deployment

The GitLab `deploy_test` job is manual. It applies the Secret from protected variables, runs the migration Job, and then waits for API and worker rollouts. The migration process uses a MySQL advisory migration lock and idempotent DDL.

Before first deployment, verify the ingress class, hostname, TLS secret, registry pull secret, MySQL network route, and NetworkPolicy namespace selectors against the test cluster.

## GitLab webhook

Create one project webhook after the API is healthy:

- URL: `https://ai-factory-test.kuainiu.io/webhooks/gitlab`
- Secret token: the exact `GITLAB_WEBHOOK_SECRET` stored in Kubernetes
- Events: Issues and Comments only
- SSL verification: enabled

Do not enable Push, Merge Request, Pipeline, Deployment, or Job events in V1.

## Recovery

- A worker crash is recovered when its two-minute lease expires.
- A transient connector/model failure retries with exponential backoff up to eight attempts.
- Dead records remain queryable for manual diagnosis; do not delete audit records to force a retry.
- The ten-minute reconciliation event scans waiting workflows for valid Gate commands that may have missed webhook delivery.
- Restoring MySQL restores workflow state; GitLab writes are replay-safe through stable markers and deterministic child-Issue markers.
