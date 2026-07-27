# kafka-base roadmap

## Implemented in v1

- Read-only local Kafka status from bounded Debian package metadata, process inventory, and the
  fixed `kafka.service` systemd probe.
- Strict parsing of fixed local Kafka properties files with only non-secret operational settings
  returned.
- Confined, bounded inventory of configured local log directories.
- Fixed local `kafka-storage.sh info --config` and `kafka-storage info --config` probes with
  summarized output only.
- Deterministic results, fail-closed path handling, exact executable/argv allowlists, and recursive
  result redaction.

## Deferred

### Broker connections and cluster metadata

- Reason: v1 is intentionally local-only and never opens a broker socket or accepts a bootstrap
  server.
- Prerequisites: host-owned endpoint and credential references, protocol timeouts, and cluster
  identity policy.
- Security design required: DNS rebinding protection, TLS identity pinning, SASL credential
  isolation, redirect-free connections, and recursively redacted responses.
- Acceptance criteria: only policy-pinned brokers are contacted, credentials never enter plugin
  arguments or results, and untrusted endpoints fail closed.

### Topic, partition, consumer-group, and quorum diagnostics

- Reason: these diagnostics require a broker or controller connection and can expose tenant or
  application identifiers.
- Prerequisites: versioned read-only operation policy, identity-scoped authorization, and bounded
  result schemas.
- Security design required: per-operation endpoint allowlists, strict response budgets, secret and
  tenant-data classification, and timeout cleanup.
- Acceptance criteria: every remote request is demonstrably read-only, bounded, policy-authorized,
  and safe under malformed or adversarial responses.

### Kafka mutations and administrative operations

- Reason: topic changes, partition reassignment, feature upgrades, storage formatting, and broker
  configuration changes are outside the diagnostic v1 boundary.
- Prerequisites: a separately approved mutation design with planning, confirmation, audit,
  idempotency, verification, and rollback semantics.
- Security design required: exact operation allowlists, immutable plan digests, privilege
  separation, fail-closed audit, and failure-injection coverage.
- Acceptance criteria: no mutation runs without an approved matching plan and every supported
  failure point preserves or restores the prior state.

### Additional installation layouts and container attribution

- Reason: v1 uses a fixed allowlist of Debian/Ubuntu filesystem layouts and the current procfs
  namespace.
- Prerequisites: signed layout metadata and stable host-owned namespace/container identity.
- Security design required: no paths or namespace handles from plugin input, symlink-safe
  confinement, PID-reuse protection, and strict traversal limits.
- Acceptance criteria: each added layout has adversarial fixtures and container attribution never
  enters an untrusted namespace or weakens local-only execution.
