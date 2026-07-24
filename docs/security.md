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

The NetworkPolicy permits webhook ingress only from the configured ingress namespace and limits egress to DNS, HTTPS, and MySQL ports. Cluster-specific endpoints must be verified before applying it.
