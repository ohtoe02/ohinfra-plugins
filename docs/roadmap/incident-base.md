# incident-base roadmap

## Implemented in v1

- Deterministic, read-only `incident snapshot`, `incident services`, and
  `incident timeline --since` commands.
- Bounded local operating-system, load, memory, root-filesystem, listener,
  failed-service, OOM, and priority `0..3` journal evidence.
- Fixed direct argv for `df`, `ss`, `systemctl`, and `journalctl`; caller
  arguments, environment, paths, and command fragments are never forwarded.
- Strict duration, procfs, os-release, systemd, listener, disk, and journal
  parsing with fail-closed handling for malformed, truncated, oversized, and
  symlinked inputs.
- Sanitized timeline metadata contains only timestamps and compiled event
  categories. Raw journal messages, service descriptions, usernames,
  credentials, environment values, and command lines are omitted.
- Optional missing probes produce a partial Result while preserving usable
  evidence from successful probes. No archive or output file is created.

## Deferred

### Incident remediation

- Reason: v1 collects evidence and never restarts services, kills processes,
  changes configuration, or applies a runbook.
- Prerequisites: separately approved mutation commands, host policy actions,
  deterministic plans, and service-specific verification.
- Security design required: root and policy authorization, audit
  availability, confirmation, dry-run, rollback boundaries, timeout cleanup,
  and recursive redaction.
- Acceptance criteria: every remediation is represented by an exact reviewed
  plan and cannot execute when policy, audit, confirmation, or verification
  fails.

### Remote evidence collection

- Reason: v1 reads only the local host and performs no SSH, HTTP, monitoring,
  ticketing, or cloud API requests.
- Prerequisites: host-owned credential references, endpoint allowlists, and a
  versioned remote evidence schema.
- Security design required: pinned TLS verification, redirect and DNS
  rebinding controls, credential lifecycle, response limits, rate limits, and
  tenant-safe redaction.
- Acceptance criteria: remote collection reaches only policy-approved
  endpoints, remains bounded and deterministic, and never exposes credentials
  or remote raw payloads.

### Evidence archives and output files

- Reason: v1 keeps bounded evidence in Result data and writes no bundle,
  archive, attachment, or caller-selected path.
- Prerequisites: a host-owned atomic output seam, a versioned archive
  manifest, retention rules, and integrity metadata.
- Security design required: traversal and symlink prevention, `0600`
  permissions, ownership checks, cumulative size limits, atomic replacement,
  secret scanning, and archive-entry confinement.
- Acceptance criteria: archives are reproducible, authenticated, bounded,
  written only to approved paths, and contain no raw credentials, command
  lines, usernames, or unclassified journal values.

### Expanded journal and process attribution

- Reason: v1 uses a fixed priority `0..3` journal query and reports only
  timestamps and compiled categories; it does not expose messages, process
  argv, environments, or per-user activity.
- Prerequisites: versioned schemas for additional event classes and a
  confined kernel process-attribution seam.
- Security design required: field-level classification, username policy,
  PID-reuse and namespace handling, per-field byte limits, and adversarial
  redaction fixtures.
- Acceptance criteria: added evidence is useful without exposing raw journal
  values, environment variables, command-line secrets, usernames, or
  unbounded process metadata.
