# gitlab-runner-base roadmap

## Implemented in v1

- Local GitLab Runner version and systemd unit inspection through fixed, bounded,
  read-only argv.
- Confined local procfs inventory that publishes only deterministic process IDs
  and numeric user IDs.
- Bounded strict TOML configuration parsing with fail-closed symlink, malformed
  input, duplicate-key, size, and entry-limit handling.
- Safe configuration metadata limited to compiled global settings, executor
  enums, shell enums, and numeric concurrency limits.
- Deterministic executor inventory that publishes only allowlisted executor
  names and runner counts.
- Runner and registration tokens, cache credentials, URL userinfo, environment
  secrets, TLS private-key metadata, and runner names are never published.

## Deferred

### Registration verification and coordinator health

- Reason: v1 is intentionally local-only and never verifies a runner
  registration or contacts a GitLab coordinator.
- Prerequisites: host-owned credential references, endpoint allowlists, and a
  versioned read-only coordinator protocol.
- Security design required: DNS rebinding protection, TLS identity pinning,
  redirect denial, credential isolation, bounded responses, and recursive
  redaction.
- Acceptance criteria: only policy-pinned coordinators are contacted, no token
  reaches output or logs, and network failure cannot alter local runner state.

### Runner registration and lifecycle operations

- Reason: `register`, `unregister`, `run`, `exec`, and job execution can mutate
  configuration or start untrusted workloads and are excluded from v1.
- Prerequisites: versioned mutation plans, host policy authorization,
  confirmation, audit, rollback, and post-change verification.
- Security design required: exact compiled argv, atomic configuration updates,
  secret references instead of raw tokens, executor confinement, and failure
  injection across every transaction stage.
- Acceptance criteria: no lifecycle or job operation runs without an approved
  plan digest, and every failed mutation restores the prior protected
  configuration.

### Full executor-specific configuration validation

- Reason: v1 deliberately omits Docker, Kubernetes, cache, autoscaler, SSH, and
  custom executor fields because they can contain credentials, remote
  endpoints, mounts, privileged settings, and executable hooks.
- Prerequisites: versioned executor-specific schemas and a public safe-field
  classification for each supported GitLab Runner release.
- Security design required: secret-bearing field denial, URL and path
  confinement, mount and privilege policy, cumulative resource limits, and
  forward-compatible schema versioning.
- Acceptance criteria: all published fields are schema-allowlisted, no
  credential or private-key material is emitted, and unknown executor settings
  remain fail-closed or explicitly omitted.
