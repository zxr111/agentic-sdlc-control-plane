# Test and Acceptance Contract

## Automated layers

- Domain tests validate allowed/forbidden transitions and Gate command feedback rules.
- Webhook tests validate secret rejection, project allowlisting, event filtering, and durable enqueue behavior.
- Connector tests validate Confluence URL extraction, normalized text, and embedded-image order.
- Agent renderer tests ensure source visuals precede detailed requirement text.
- Store integration tests validate migration, queue/outbox deduplication, concurrent lease claims, expired lease recovery, retries, and workflow uniqueness against PostgreSQL 16.
- End-to-end staging tests use a dedicated labeled GitLab Issue and synthetic Confluence content.

## End-to-end acceptance

1. An open Issue without `automation::enabled` produces no workflow.
2. An eligible Issue with a Confluence URL produces immutable source records, GitLab-hosted visuals, and a Requirement Gate comment.
3. An unlisted or inactive project member cannot decide a Gate and the attempt is audited.
4. Request Changes or Reject creates a new artifact/Gate revision and does not advance.
5. Requirement approval creates only the approved independently deliverable child Issues.
6. PRD and Test Gates are independent; approving one does not advance the workflow.
7. Both planning approvals move the workflow to `READY_FOR_ARCHITECTURE`.
8. Duplicate webhooks, worker restarts, and connector timeouts do not duplicate managed comments or child Issues.
9. A changed Confluence version, text hash, or visual hash invalidates downstream approval.
10. No V1 action writes code, opens/merges a merge request, runs an application deployment, or accesses production.
