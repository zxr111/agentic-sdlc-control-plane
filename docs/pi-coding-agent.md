# Pi coding-agent integration

The Factory remains the authority for workflow state, policy, Gates, and evidence. Pi is an interchangeable execution provider used only from an engineer-visible task; `factory-worker` never starts it headlessly.

## Boundary

1. An assigned engineer records `/start-codex task:<work-item-uuid> client:<client-id>` on the work-item Issue.
2. The visible client reads `GET /coding-agent/dispatches/<dispatch-id>/manifest` with `X-AI-Factory-Token` set to the separately managed `CODING_AGENT_SHARED_SECRET`.
3. The API freezes the active approved SDD impacts into an immutable manifest. It contains the repository, branch, allowed paths, acceptance IDs, required checks, source SDD hash, and capabilities.
4. The visible client passes that manifest to its Pi SDK bridge. `codingagent.PiClient` defines the bridge contract (`POST /v1/sessions` and `POST /v1/sessions/<id>/cancel`).
5. The client records events through `POST /coding-agent/dispatches/<dispatch-id>/evidence`.
6. GitLab webhooks and exact-SHA quality callbacks remain the only route into delivery state transitions. Agent evidence alone cannot approve, merge, or deploy.

The Pi bridge must reject writes outside `allowed_paths`, run only `required_checks`, and expose the Pi session to the assigned engineer. It must never receive GitLab merge credentials or deployment credentials.

## Evidence example

```json
{
  "manifest_hash": "<hash returned by the manifest endpoint>",
  "provider": "pi",
  "session_id": "visible-session-id",
  "kind": "CHECK_RESULT",
  "command": "go test ./...",
  "exit_code": 0,
  "payload": {"summary": "passed"}
}
```

Supported evidence kinds are `SESSION_STARTED`, `TOOL_CALL`, `FILE_CHANGE`, `CHECK_RESULT`, `COMMIT`, `DRAFT_MR`, `SESSION_COMPLETED`, and `SESSION_FAILED`. `COMMIT` and `DRAFT_MR` evidence must include `commit_sha`.

This endpoint stores execution history for audit and debugging. A successful check reported by Pi is not trusted as release evidence until the control plane independently observes the corresponding GitLab commit and CI result.
