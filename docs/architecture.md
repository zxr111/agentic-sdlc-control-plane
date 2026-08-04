# Architecture

## Runtime roles

`factory-api` validates source-specific shared secrets, accepts configured GitLab lifecycle events and authenticated Jenkins, Quality, and monitoring callbacks, normalizes them, and persists them before returning HTTP 202.

The same binary serves the local read-only Factory Control Room. `GET /api/dashboard` assembles bounded operational projections from workflow, Gate, artifact, source, audit, event queue, and outbox tables. The embedded browser UI never performs state transitions or Gate decisions; authorization remains in GitLab comments. In the test cluster, the Hermes backend-for-frontend is the only application caller allowed to read this endpoint, and its authenticated administrator page consumes the shared JSON contract.

`factory-worker` claims queue and outbox records with `SELECT ... FOR UPDATE SKIP LOCKED`. Every claim has a lease, attempt count, exponential retry, and dead-letter terminal state. Expired leases are recovered by another worker.

Codex is not a worker role. An engineer-owned Dispatcher task queries GitLab for assigned work and records `/start-codex`; coding and quality run in separate visible Codex tasks and worktrees. A dispatch remains owned until the engineer completes or explicitly resets it. It has no lease, renewal, timeout, or silent reassignment.

## State machine

```text
NEW
-> INGESTING
-> REQUIREMENT_ANALYSIS
-> WAITING_REQUIREMENT_REVIEW
-> MATERIALIZING_WORK_ITEMS
-> PRD_GENERATING
-> WAITING_PRD_AND_TEST_REVIEW
-> READY_FOR_ARCHITECTURE
-> ARCHITECTURE_GENERATING
-> WAITING_ARCHITECTURE_REVIEW
-> PLANNING
-> EXECUTING_WORK_ITEMS
-> ASSEMBLING_RELEASE
-> RELEASE_CI_RUNNING
-> STAGING_DEPLOYING
-> STAGING_VERIFYING
-> WAITING_RELEASE_APPROVAL
-> PRODUCTION_DEPLOYING (only when enabled)
-> OBSERVING
-> COMPLETED
```

Work items follow `PLANNED -> WAITING_DEPENDENCY -> READY_FOR_CODEX -> CODING -> DRAFT_MR -> AI_QUALITY_CHECKS -> REWORK/CI_RUNNING -> WAITING_CODE_REVIEW -> MERGE_QUEUED -> MERGED -> COMPLETED`. A separate integration work item creates `ai/workflow/<parent-iid>` and a final MR to `master` when a workflow has multiple delivery items.

After staging verification, a release that declares a production migration must pass the Production Migration Gate with explicit migration and rollback plans before the independent Release Gate can open. High and critical monitoring events similarly open an Incident Gate; approval records authorization but does not silently run remediation code.

Source changes before architecture invalidate downstream approval and return the workflow to intake. Changes after architecture are audited and require an explicit impact decision instead of silently rewriting an active implementation. Request Changes or Reject returns the affected artifact or work item for rework. The transition allowlist is enforced by code and recorded in `audit_events`.

## Durable records

- `workflows`: one active workflow for each GitLab project/Issue pair.
- `source_snapshots`: immutable Confluence text, storage body, page/version/hash, and visual manifest.
- `artifacts`: immutable structured AI output and rendered Markdown.
- `gates` and `gate_decisions`: reviewer allowlist, revision, decision, actor, feedback, and timestamps.
- `event_queue`: inbound and internally scheduled work.
- `outbox_messages`: idempotent GitLab comments and child-Issue writes.
- `audit_events`: state, Gate, authorization, and intake evidence.
- `work_items` and `work_item_dependencies`: delivery graph, assigned engineer, branch, target, and acceptance trace.
- `codex_dispatches`: idempotent visible-task dispatch; no lease columns.
- `agent_runs`, `merge_requests`, `quality_runs`, and `quality_findings`: exact model/MR/SHA evidence and hard blockers.
- `pipeline_runs`, `release_candidates`, `deployments`, and `observation_windows`: immutable delivery evidence.
- `incidents` and `email_relays`: monitoring intake and idempotent engineer-command relay audit.

String identifiers used for idempotency use deterministic `C` collation. All timestamps use `TIMESTAMPTZ(6)` and are handled as UTC. Flexible payloads use `JSONB`; workflow routing fields remain indexed relational columns.

## Connector behavior

Confluence storage HTML is normalized into reviewable text. Only images actually embedded in the source document are downloaded. They are uploaded into the GitLab project so readers do not need a Confluence session. Their identity, source version, content hash, order, and GitLab Markdown are retained in the immutable snapshot.

GitLab comments use stable hidden markers and are updated instead of duplicated. Child Issues use a workflow/work-item marker and are looked up before creation after a retry.

`/start-codex task:<uuid> client:<id>` is atomic: Factory verifies `READY_FOR_CODEX`, dependency completion, active assignment, and dispatch uniqueness before transitioning to `CODING`. Duplicate commands return the existing dispatch. Code Review approval is bound to the exact MR head SHA; a changed SHA is rejected.

OpenAI calls use the Responses API with strict JSON Schema output, `store: false`, a privacy-preserving safety identifier, and configurable model selection. Model output is validated by JSON decoding before it can affect routing.
