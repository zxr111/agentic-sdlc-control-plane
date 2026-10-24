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

## Coding multi-agent state flow

The platform owns a persisted sub-state machine for each dispatch:

```text
PLANNING -> TEST_DESIGN -> CODING -> REVIEWING
                                  -> REPAIRING -> REVIEWING
                     REVIEWING -> QUALITY_CHECKING -> READY_FOR_MR
                                  -> WAITING_HUMAN
```

The platform creates logical Agent Runs for `PLANNER`, `TEST_DESIGNER`, `CODER`, `REVIEWER`, `SECURITY_RELIABILITY`, `JUDGE`, `REPAIR`, and `QUALITY`. These records do not start a headless process. An engineer-visible Pi client lists pending runs and attaches its visible provider session before executing one.

Reviewer and Security/Reliability runs are created together for the same exact commit. The platform creates the Judge only after both formal artifacts have completed. Blocking findings are aggregated deterministically and cannot be discarded by a Judge returning `READY`. Repair creates a new exact commit, so the platform creates a fresh review pair. After three repair cycles the session moves to `WAITING_HUMAN`.

Role tool policies are included in every run input. Only Coder and Repair receive controlled write and commit capabilities. Merge and deployment remain outside every Coding Agent policy.

### API sequence

All calls require `X-AI-Factory-Token` with `CODING_AGENT_SHARED_SECRET`.

1. `POST /coding-agent/dispatches/<dispatch-id>/sessions` creates or reads the session and initial Planner run.
2. `GET /coding-agent/sessions/<session-id>/runs/pending` lists logical work for visible clients.
3. `POST /coding-agent/runs/<run-id>/start` binds a visible `provider_session_id`.
4. `POST /coding-agent/runs/<run-id>/complete` submits a schema-shaped artifact, manifest hash, exact commits, decision, and deterministic result fields.
5. `POST /coding-agent/runs/<run-id>/fail` accepts only bounded failure codes; arbitrary error text is not persisted because it may contain credentials.
6. `GET /coding-agent/sessions/<session-id>` returns the authoritative state.

Every transition is written to `coding_stage_transitions`; producer/consumer lineage is written to `coding_artifacts` and `coding_run_inputs`. Agent responses never name or directly start the next role—the platform calculates that transition.
