# Test and Acceptance Contract

## Automated layers

- Domain tests validate the full workflow transition graph, Work Item commands, hard quality blockers, and Gate command feedback rules.
- Webhook tests validate secret rejection, project allowlisting, event filtering, and durable enqueue behavior.
- Connector tests validate Confluence URL extraction, normalized text, and embedded-image order.
- Agent renderer tests ensure source visuals precede detailed requirement text.
- Store integration tests validate migration, queue/outbox deduplication, concurrent lease claims, expired lease recovery, retries, and workflow uniqueness against PostgreSQL 16.
- Dashboard tests verify embedded assets, GitLab Issue URL enrichment, real PostgreSQL projections, empty collections, and JavaScript syntax.
- End-to-end staging tests use a dedicated labeled GitLab Issue and synthetic Confluence content.

## End-to-end acceptance

1. An open Issue without `automation::enabled` produces no workflow.
2. An eligible Issue with a Confluence URL produces immutable source records, GitLab-hosted visuals, and a Requirement Gate comment.
3. An unlisted or inactive project member cannot decide a Gate and the attempt is audited.
4. Request Changes or Reject creates a new artifact/Gate revision and does not advance.
5. Requirement approval creates only the approved independently deliverable child Issues.
6. PRD and Test Gates are independent; approving one does not advance the workflow.
7. Both planning approvals generate Architecture; approval materializes branches and moves independent work to `READY_FOR_CODEX`.
8. Duplicate webhooks, worker restarts, and connector timeouts do not duplicate managed comments or child Issues.
9. A changed Confluence version, text hash, or visual hash invalidates downstream approval.
10. `/start-codex` rejects an unassigned actor, unmet dependency, or wrong state and returns the same dispatch on retry.
11. Coding happens only in a visible Codex task. A distinct Quality task checks the exact MR SHA.
12. Quality fails on any hard blocker and becomes `BLOCKED` after the third failed fix loop.
13. A new MR commit invalidates Code Review evidence; approved exact-SHA MRs merge only after CI.
14. Multi-item workflows merge through `ai/workflow/<parent-iid>` before the final MR targets `master`.
15. Release CI, staging deployment, staging verification, Release Gate, observation, and completion are auditable and retry-safe.
16. Production remains locked when `production_enabled=false`; no V2 test action accesses production.
