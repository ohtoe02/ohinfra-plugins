---
schema_version: 1
plugin_id: gitlab-runner-base
locale: en
documented_version: 1.0.0
title: GitLab Runner base
summary: Inspect local GitLab Runner service, process, safe configuration, and executor metadata.
command_paths:
  - gitlab-runner status
  - gitlab-runner config
  - gitlab-runner executors
local_only: true
---
# GitLab Runner base

## Purpose and supported scenarios

`gitlab-runner-base` provides read-only local visibility into a GitLab Runner
installation. It combines the installed binary version, fixed systemd unit,
bounded process inventory, a strict metadata view of
`/etc/gitlab-runner/config.toml`, and aggregate executor counts.

Use it to determine whether Runner is present and active, review safe global
concurrency settings, and understand which executor classes are configured.
Version 1 never contacts a GitLab coordinator, verifies registration, starts a
job, or reveals runner identity and credential fields.

## Quick start

```text
ohtools gitlab-runner status
ohtools gitlab-runner config
ohtools gitlab-runner executors
```

All commands are diagnostic, take no arguments, and need neither root,
confirmation, force, nor dry-run.

## Commands

### gitlab-runner status

Runs the fixed `gitlab-runner --version` probe, requests
`LoadState`, `ActiveState`, and `SubState` for
`gitlab-runner.service`, and scans bounded procfs entries for the exact process
name `gitlab-runner`. Returned process data contains only a numeric PID and,
when safely parsed, its numeric real UID.

The command reports info when no local evidence exists, pass when an
installation is detected, and warning when installation evidence exists but
the unit is not active and no matching process is running.

### gitlab-runner config

Reads at most 1 MiB and 16384 lines from the fixed
`/etc/gitlab-runner/config.toml`. A strict bounded TOML subset recognizes scalar
values, arrays, inline tables, ordinary tables, and `[[runners]]`. It rejects
malformed strings and values, duplicate keys in the same scope, unsupported
array tables, invalid ordering, and more than 256 runners.

The result includes path, byte size, runner count, safe global settings
`concurrent`, `check_interval`, `log_level`, and `shutdown_timeout`, plus each
runner's allowlisted executor, numeric `limit`, numeric
`request_concurrency`, and allowlisted shell. Runner names are validated but
not returned. Other tables can be structurally parsed but their values are not
published.

### gitlab-runner executors

Reads the same strict local config and groups runners by executor. It returns a
sorted list of executor name and runner count. An empty valid collection is
informational. The command does not instantiate, test, or invoke an executor.

## How it works

Every command passes protocol v1 read-only planning. Status treats its version,
unit, and process probes independently so safe evidence survives optional
failures. Version text must match a bounded semantic-version shape. Unit values
must belong to compiled systemd state allowlists.

The config parser tracks current TOML table and runner index, validates every
assignment syntax and value shape, rejects duplicates, and copies only a safe
field allowlist into Result data. Integer settings must be between 0 and
1,000,000. Supported global log levels are `debug`, `info`, `warn`, `error`,
`fatal`, and `panic`.

Supported executor metadata values are `shell`, `docker`, `docker-windows`,
`kubernetes`, `custom`, `parallels`, `virtualbox`, `ssh`, `docker+machine`,
`instance`, and `docker-autoscaler`. Shell metadata may be `bash`, `sh`,
`powershell`, or `pwsh`.

## Data access

The plugin executes only:

```text
gitlab-runner --version
systemctl show gitlab-runner.service --property=LoadState,ActiveState,SubState --no-pager
```

It reads `/proc/{pid}/comm`, optional `/proc/{pid}/status`, and
`/etc/gitlab-runner/config.toml`. Command output is bounded to 64 KiB or
32 KiB with 16 KiB stderr. Procfs accepts at most 32768 entries and 64 KiB per
read. No shell, job command, config include, environment file, coordinator,
cache endpoint, or caller-provided path is accessed.

## Results and exit behavior

`gitlab-runner status` emits `gitlab-runner.status` plus safe dependency errors
for unavailable non-missing probes. Missing executables or units can simply
contribute to the not-installed result.

`gitlab-runner config` emits `gitlab-runner.config`. A missing fixed file uses
the dependency exit mapping; unsafe or malformed strict TOML uses the
configuration mapping.

`gitlab-runner executors` emits `gitlab-runner.executors` with pass or info and
uses the same dependency and configuration mappings. Unsupported invocation
uses arguments. Cancellation and timeout propagate. Stdout in JSON mode is
only Result schema v1.

## Configuration

There is no `/etc/ohtools/plugins/gitlab-runner-base.yaml` in version 1. The
only inspected product configuration is:

```text
/etc/gitlab-runner/config.toml
```

An example of fields the metadata view can publish is:

```toml
concurrent = 4
check_interval = 3
log_level = "info"

[[runners]]
name = "local-runner"
executor = "shell"
limit = 2
request_concurrency = 1
shell = "bash"
```

The runner name is deliberately omitted from output. Tokens, URLs, environment,
cache, TLS, and executor-specific nested fields are never published.

## What can be changed

Operators can manage GitLab Runner and its protected TOML using normal GitLab
Runner administration. The four safe global settings and four safe per-runner
metadata fields will reflect valid local changes.

Changing Runner configuration may affect job execution outside this diagnostic.
The plugin never writes it. Probe paths, executable argv, accepted executor and
shell enums, field allowlists, and limits require a new plugin release.

## Fixed behavior

The binary and unit names, procfs classification, config path, 1 MiB and
16384-line limits, 256-runner limit, safe fields, enums, integer range, and
output shape are fixed in 1.0.0.

The plugin never accepts coordinator URL, token, runner selector, arbitrary
path, executable, environment override, registration option, or job command.
It does not run `register`, `unregister`, `run`, or `exec`.

## Safety

All commands are read-only and non-root. Local reads reject symlinks and
oversized files through the shared confined probe. Process command lines and
environments are not read. External output is bounded and parsed through
allowlists.

Runner and registration tokens, cache credentials, URL userinfo, environment
secrets, TLS private-key metadata, runner names, and executor-specific remote
or privileged settings do not enter Result data. Host timeout, policy, audit,
JSON isolation, and recursive redaction remain active.

## Troubleshooting

- If status is informational, check whether the trusted binary, fixed service,
  or exact process name exists locally.
- If installed status is warning, inspect `gitlab-runner.service` and the local
  process lifecycle without exposing job arguments.
- A configuration exit indicates unsafe file access or strict TOML failure;
  check UTF-8, size and line limits, table syntax, duplicate keys, supported
  values, and runner count.
- Every `[[runners]]` entry must contain one supported `executor`.
- Values outside 0 through 1,000,000 and unsupported log level or shell values
  fail closed even if a newer Runner accepts them.

## Limitations and TODO

Version 1 is deliberately local-only. Registration verification, coordinator
health, registration and lifecycle mutations, job execution, remote and cache
access, downloads, and full Docker, Kubernetes, autoscaler, SSH, or custom
executor validation are deferred. Network support requires policy-pinned
coordinators, host-owned token references, TLS identity controls, redirect and
DNS-rebinding protections, bounded responses, and strict secret isolation.

See the maintained
[GitLab Runner roadmap](../../roadmap/gitlab-runner-base.md).

## Compatibility and source

This page documents `gitlab-runner-base` 1.0.0 and plugin protocol v1. The
signed catalog remains authoritative for installation, minimum host version,
asset size, SHA-256, and release history.

Implementation: [internal/gitlabrunner](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/gitlabrunner).
