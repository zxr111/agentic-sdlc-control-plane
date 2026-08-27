# Local Agent Platform demonstration

The local demo model provider produces deterministic synthetic JSON from the governed output schema. It requires no external model token and is intended only for UI, workflow, and integration demonstrations. Its output is not release evidence and must never be used in production.

Start the stack with the demo overlay:

```bash
GITLAB_WEBHOOK_SECRET=local-webhook \
CALLBACK_SHARED_SECRET=local-callback \
AGENT_RUNTIME_SHARED_SECRET=local-runtime \
OPENAI_API_KEY=local-demo-not-a-credential \
docker compose -f compose.yaml -f compose.demo.yaml up -d --build postgres migrate api agent-runtime model-mock knowledge-indexer
```

Open the delivery dashboard at `http://127.0.0.1:8080/dashboard/` and the Agent Platform management console at `http://127.0.0.1:8080/dashboard/v3/`.

The workflow and dispatcher services still require a configured test GitLab/Confluence endpoint. Start them only when those local mocks or test integrations are available. Do not copy production credentials into this repository.
