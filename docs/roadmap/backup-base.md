# backup-base roadmap

## Implemented in v1

- Deterministic read-only `backup status`, `backup inventory`, and `backup history` commands.
- Compiled local adapters for Restic, BorgBackup, rsnapshot, and fixed systemd service/timer metadata.
- Exact allowlisted version and `systemctl show` argv with bounded output, an empty command environment, and no caller-controlled arguments.
- Fixed, confined, bounded local configuration and history paths; no directory discovery, config includes, environment files, or symlinks are followed.
- Sanitized repository classification and history aggregates that expose only product IDs, versions, presence, repository type, timestamps, and counts.
- Fail-closed partial results for oversized, malformed, truncated, non-UTF-8, or symlinked local inputs without returning raw configs, logs, stderr, repository values, SSH targets, URL userinfo, cloud credentials, or passwords.

## Deferred

### Remote repository availability and authentication

- Reason: v1 performs no network access and never opens a Restic, Borg, rsnapshot, object-storage, SSH, or HTTP repository.
- Prerequisites: host-owned credential references, endpoint and repository allowlists, and versioned per-product transport policy.
- Security design required: TLS and SSH host verification, redirect and DNS-rebinding policy, proxy isolation, credential lifecycle, request/response limits, and recursive redaction.
- Acceptance criteria: probes contact only policy-pinned repositories and never expose repository passwords, URL userinfo, SSH targets, environment files, cloud credentials, or remote response bodies.

### Backup, restore, mount, unlock, prune, and retention operations

- Reason: v1 is diagnostic-only and does not mutate repositories, local snapshots, locks, mounts, schedules, or restored files.
- Prerequisites: separately approved mutation commands, host policy, confirmation and dry-run plans, audit integration, transaction recovery, and product/version compatibility fixtures.
- Security design required: exact compiled argv, trusted executable resolution, confined source and destination paths, privilege boundaries, lock ownership, cancellation cleanup, and fail-closed verification.
- Acceptance criteria: every mutation has a deterministic plan, explicit authorization, bounded execution, audit coverage, rollback or safe recovery, and adversarial tests for traversal, symlinks, option smuggling, poisoned environments, and interrupted operations.

### Repository integrity checks

- Reason: Restic/Borg check, repair, compact, prune, and lock cleanup can be expensive or mutating and are never invoked by v1.
- Prerequisites: explicit read-only versus mutating modes, resource budgets, repository capability detection, and versioned product adapters.
- Security design required: CPU, memory, I/O, duration, and output limits; lock semantics; cancellation behavior; and safe handling of damaged repositories.
- Acceptance criteria: integrity diagnostics cannot mutate by default, respect resource budgets, and produce only bounded sanitized summaries.

### Dynamic schedules and configuration includes

- Reason: v1 inspects only compiled config paths and conventional service/timer units; it does not discover arbitrary files, environment files, include trees, cron entries, or user-defined units.
- Prerequisites: a confined cumulative-budget directory/include resolver and schemas for supported product configuration versions.
- Security design required: traversal and symlink prevention, bounded directory reads before materialization, ownership and permission policy, include-cycle detection, and omission of all secret-bearing values.
- Acceptance criteria: discovery is deterministic, bounded, local-only, and reports metadata without exposing repository names, targets, credentials, or raw configuration.

### Rich journal and snapshot history

- Reason: v1 consumes only fixed bounded local history files and returns timestamp/count aggregates without raw messages.
- Prerequisites: a shared bounded journal adapter and stable event schemas for Restic, BorgBackup, and rsnapshot.
- Security design required: fixed units and filters, strict timestamp/outcome parsing, cumulative byte/event limits, and redaction of messages, usernames, paths, repository identifiers, and environment data.
- Acceptance criteria: history remains deterministic and local-only, exposes no raw journal or tool output, and degrades to partial when event data is malformed or truncated.
