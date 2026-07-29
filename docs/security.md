# Security Boundaries

- Confluence pages, GitLab Issues, comments, links, images, attachment names, and AI output are untrusted data.
- No Issue or Confluence text is interpreted as a shell command, URL credential, code instruction, or permission grant.
- Gate authorization requires both the immutable Gate reviewer-ID allowlist and a live GitLab active-project-member check.
- The webhook token is compared in constant time. Bodies are limited to 2 MiB and only Issue/Note event names are accepted.
- Tokens are sent only in authorization headers. They are never placed in URLs, queue payloads, audit details, Issue text, or structured logs.
- Connector response bodies are size-limited. Confluence images are limited to 15 MiB and use sanitized base filenames.
- OpenAI requests use `store: false` and a hashed safety identifier. Prompts explicitly distinguish authoritative data from executable instructions.
- The Kubernetes workload runs as a non-root UID, drops Linux capabilities, uses a read-only root filesystem, and has no service-account token.
- V1 has no source repository write, merge, CI mutation, application deployment, production database, or production cluster credential.
- The Control Room is read-only and has no endpoint for Gate decisions, retries, state changes, code execution, or deployment.
- The test Ingress does not expose `/dashboard/` or `/api/dashboard`. The User Center browser reaches only its authenticated, administrator-only Hermes proxy and never receives an upstream credential.

The NetworkPolicy permits webhook ingress from the configured ingress namespace and internal Dashboard API access only from Pods labeled `app=argus-hermes-api` in the `argus-test` namespace. Egress is limited to DNS, HTTPS, and PostgreSQL ports. Cluster-specific endpoints and namespace labels must be verified before applying it.
