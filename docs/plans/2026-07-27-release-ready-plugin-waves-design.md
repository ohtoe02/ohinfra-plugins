# Release-ready plugin waves design

## Goal

Deliver release-ready local-first v1 implementations for the fourteen
first-party plugin packages that are reserved but not yet published:

- `server-setup-base`
- `network-base`
- `security-base`
- `apt-base`
- `baseline-base`
- `postgres-base`
- `k8s-base`
- `java-base`
- `kafka-base`
- `gitlab-runner-base`
- `monitoring-base`
- `backup-base`
- `incident-base`
- `runbook-base`

`inventory-base` is not part of this design. The existing `system-base`,
`storage-base`, `systemd-base`, and `docker-base` releases remain compatible.

Every new catalog entry must describe a complete implementation. Placeholders,
`not implemented` commands, draft packages, and unsigned metadata are not
published.

## Product boundary

The first release is a useful local-host baseline rather than the complete
future operational surface.

- All commands are local-only.
- Remote endpoints, network authentication, and credential references are
  deferred.
- Only `server-setup-base` mutates the host.
- The other thirteen plugins are strictly read-only.
- Deferred capabilities are documented as explicit public TODO items with
  prerequisites and acceptance criteria.
- Catalog schema v1 remains unchanged. The catalog contains only immutable,
  implemented, signed plugin releases and never contains TODO metadata.

## Architecture

The plugin monorepo uses a deep shared kernel and small domain modules.

Existing stable modules remain responsible for their current contracts:

- `internal/protocol` owns plugin protocol v1, Result schema v1 normalization,
  plan digests, and exit-code handling.
- `internal/config` owns strict root-controlled YAML loading.
- `internal/execx` owns trusted executable resolution, direct argv execution,
  stable environment construction, output bounds, and timeout cleanup.
- `internal/redact` owns recursive secret redaction.

Three internal modules deepen shared behavior without becoming a public SDK:

- `internal/probe` provides local file, process, systemd, executable, and
  bounded command probes.
- `internal/resultbuilder` produces deterministic checks, dependency errors,
  partial results, and sorted Result v1 data.
- `internal/platform` detects supported Debian and Ubuntu releases and exposes
  platform capabilities.

Each plugin has:

- a minimal `cmd/<plugin>/main.go` composition root;
- one `internal/<domain>` module containing its manifest, plan, execution, and
  domain parsing;
- tests through the same module interface used by the composition root;
- a fixed optional config path at `/etc/ohtools/plugins/<plugin>.yaml`;
- a detailed `docs/roadmap/<plugin>.md`.

Domain modules do not import one another. They may depend only on the shared
internal kernel and the Go standard library. The kernel does not accept
arbitrary executables, argv, shell fragments, URLs, or inline credentials from
configuration.

## Command surface

| Package | Commands |
| --- | --- |
| `server-setup-base` | `setup check`, `setup apply`, `setup upgrade` |
| `network-base` | `network overview`, `network interfaces`, `network routes`, `network listeners`, `tls inspect <path>` |
| `security-base` | `security audit`, `security accounts`, `security ssh`, `security firewall` |
| `apt-base` | `apt status`, `apt updates`, `apt sources`, `apt history` |
| `baseline-base` | `baseline check`, `baseline profile` |
| `postgres-base` | `postgres status`, `postgres clusters`, `postgres config` |
| `k8s-base` | `k8s status`, `k8s contexts`, `k8s manifests` |
| `java-base` | `java runtime`, `java processes`, `java inspect <pid>` |
| `kafka-base` | `kafka status`, `kafka config`, `kafka storage` |
| `gitlab-runner-base` | `gitlab-runner status`, `gitlab-runner config`, `gitlab-runner executors` |
| `monitoring-base` | `monitoring status`, `monitoring inventory`, `monitoring config` |
| `backup-base` | `backup status`, `backup inventory`, `backup history` |
| `incident-base` | `incident snapshot`, `incident services`, `incident timeline [--since]` |
| `runbook-base` | `runbook list`, `runbook inspect <name>`, `runbook validate [name]` |

The local-first boundary has domain-specific consequences:

- PostgreSQL reads local cluster, service, and configuration metadata without
  opening a database connection.
- Kubernetes reads client version, kubeconfig metadata, kubelet state, and
  static manifests without contacting an API server.
- Kafka reads local processes, configuration, and storage metadata without
  contacting a broker.
- GitLab Runner reads service, configuration, and executor metadata without
  verifying a registration or running a job.
- Monitoring inventories local agents, exporters, listeners, and
  configuration without scraping remote targets.
- Backup inspects local tool, repository, configuration, and history metadata
  without starting backup, prune, or restore.
- Runbook lists, inspects, and validates local documents without executing
  instructions.
- TLS inspects local certificate files without contacting a remote peer.

## Data flow

`manifest` performs no platform reads and starts no dependencies.

For `plan` and `execute`, the plugin:

1. strictly decodes invocation and rejects unknown options or extra arguments;
2. loads only its fixed root-controlled configuration;
3. determines platform capabilities and read privileges;
4. performs dependency preflight;
5. executes bounded local probes with context deadlines;
6. parses external output into domain values;
7. recursively redacts external values;
8. sorts checks and data deterministically;
9. returns normalized Result v1.

Read-only commands return a deterministic empty-change plan. The
`server-setup-base` mutation flow returns an exact plan and digest before
execution, requires host policy, audit, root, and confirmation, respects
dry-run, performs atomic writes with backups, and verifies every mutation.
Repeated apply operations converge without unnecessary reloads or writes.

## Error model

- Invalid invocation data returns exit code `2`.
- Insufficient privileges for the primary operation returns `3`.
- A narrow command with no usable primary dependency returns `4`.
- Aggregate `check`, `status`, `inventory`, and `snapshot` commands continue
  where safe and return partial Result status with exit code `9`.
- Deadline expiration returns `8`.
- Unsafe, malformed, unknown, or untrusted configuration returns `10`.
- An absent optional product is informational in aggregate inventory, but an
  absent required product is a structured missing-dependency failure.

External stdout and stderr are bounded and never copied wholesale into audit or
results. Error messages and parsed values pass recursive redaction.

## Security invariants

- Never invoke a shell.
- Resolve compiled executable names only through trusted system directories.
- Ignore caller `PATH`, proxy variables, current directory, and arbitrary
  environment.
- Reject executable paths, argv, shell syntax, URLs, and inline secrets in
  config.
- Reject traversal, unsafe symlinks, untrusted ownership, and unsafe
  permissions.
- Bound config, file, command-output, and parser input sizes.
- Stop complete process groups on cancellation or timeout.
- Keep JSON stdout protocol-clean.
- Never invoke `sudo`.
- Support Debian 10 through 13 and Ubuntu 20.04, 22.04, and 24.04 LTS without
  weakening behavior on older supported hosts.

## Testing

Every behavior is implemented with red-green-refactor TDD.

Per-plugin tests cover:

- manifest and command contracts;
- strict invocation and configuration;
- missing dependency and privilege handling;
- poisoned environment and option smuggling;
- shell metacharacters;
- traversal, symlink, permission, and ownership attacks;
- bounded and malformed external output;
- timeout and process cleanup;
- redaction;
- deterministic Result and plan values;
- supported platform fixtures.

`server-setup-base` additionally covers dry-run, idempotency, atomic backup,
verification, and mutation failure injection.

Repository gates include formatting, unit tests, race tests, vet, coverage,
all static `linux/amd64` builds, manifest validation, branding checks, and
container integration across the supported Debian and Ubuntu matrix.

## Deferred work

Each `docs/roadmap/<plugin>.md` contains:

- the implemented v1 baseline;
- deferred capability;
- reason for deferral;
- prerequisites;
- required security design;
- acceptance criteria.

Remote connections, authentication, credential references, non-setup
mutations, backup execution, incident remediation, and runbook execution are
always deferred from this design.

The web portal stores a short localized TODO summary keyed by published plugin
ID and links to the detailed roadmap. Portal content tests reject summaries for
unknown or unpublished plugin IDs.

## Release waves

Publication is incremental and immutable:

1. Wave 1: `server-setup-base`, `network-base`, `security-base`, `apt-base`,
   and `baseline-base`; publish catalog sequence 4.
2. Wave 2: `postgres-base`, `k8s-base`, and `java-base`; publish sequence 5.
3. Wave 3: `kafka-base`, `gitlab-runner-base`, and `monitoring-base`; publish
   sequence 6.
4. Wave 4: `backup-base`, `incident-base`, and `runbook-base`; publish sequence
   7.

For each plugin, publish an immutable SemVer tag, static `linux/amd64` binary,
SHA-256 file, and SPDX JSON SBOM. Catalog metadata records the exact release
URL, size, SHA-256, manifest, description, release time, and actual minimum host
version.

Each wave must pass sandboxed manifest verification before catalog merge.
Signing runs in the protected catalog environment and increments the sequence
exactly once. The released host then verifies `releases/latest` in a non-root,
read-only, network-constrained container. Finally, the web portal rebuilds and
is checked for complete signed cards, install commands, TODO links, and absence
of unsigned or draft entries.

Historical tags, release assets, and signed catalog snapshots are immutable.

## Acceptance

- All fourteen plugin binaries are complete for their stated v1 commands.
- No command is a placeholder.
- Only `server-setup-base` mutates the host.
- Every plugin passes the full security and platform test matrix.
- Catalog sequences 4 through 7 are valid, signed, monotonic, and consumable by
  the compatible released host.
- The web portal shows every newly signed plugin and its localized deferred
  scope.
- Missing or invalid upstream data cannot replace the last working catalog or
  Pages deployment.
