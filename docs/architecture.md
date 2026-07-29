# Architecture

## Runtime roles

`factory-api` validates the GitLab webhook secret, accepts only Issue/Note events from configured projects, normalizes them, and persists them before returning HTTP 202.

`factory-worker` claims queue and outbox records with `SELECT ... FOR UPDATE SKIP LOCKED`. Every claim has a lease, attempt count, exponential retry, and dead-letter terminal state. Expired leases are recovered by another worker.

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
```

Source changes invalidate downstream approval and return the workflow to `INGESTING`. Request Changes or Reject returns the affected artifact to its generating state and opens a new revision of the same Gate. The transition allowlist is enforced by code and recorded in `audit_events`.

## Durable records

- `workflows`: one active workflow for each GitLab project/Issue pair.
- `source_snapshots`: immutable Confluence text, storage body, page/version/hash, and visual manifest.
- `artifacts`: immutable structured AI output and rendered Markdown.
- `gates` and `gate_decisions`: reviewer allowlist, revision, decision, actor, feedback, and timestamps.
- `event_queue`: inbound and internally scheduled work.
- `outbox_messages`: idempotent GitLab comments and child-Issue writes.
- `audit_events`: state, Gate, authorization, and intake evidence.

String identifiers used for idempotency use deterministic `C` collation. All timestamps use `TIMESTAMPTZ(6)` and are handled as UTC. Flexible payloads use `JSONB`; workflow routing fields remain indexed relational columns.

## Connector behavior

Confluence storage HTML is normalized into reviewable text. Only images actually embedded in the source document are downloaded. They are uploaded into the GitLab project so readers do not need a Confluence session. Their identity, source version, content hash, order, and GitLab Markdown are retained in the immutable snapshot.

GitLab comments use stable hidden markers and are updated instead of duplicated. Child Issues use a workflow/work-item marker and are looked up before creation after a retry.

OpenAI calls use the Responses API with strict JSON Schema output, `store: false`, a privacy-preserving safety identifier, and configurable model selection. Model output is validated by JSON decoding before it can affect routing.
