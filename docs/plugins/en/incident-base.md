---
schema_version: 1
plugin_id: incident-base
locale: en
documented_version: 1.0.0
title: Incident base
summary: Collect bounded local operating-system, resource, service, listener, and journal evidence without remediation.
command_paths:
  - incident snapshot
  - incident services
  - incident timeline
local_only: true
---
# Incident base

## Purpose and supported scenarios

`incident-base` collects a small, deterministic set of local evidence for
initial incident triage. It summarizes operating system identity, load, memory,
root filesystem usage, listener count, failed systemd services, and high
priority journal events.

Use it to establish a bounded first snapshot, list failed local services, or
build a sanitized recent timeline before deeper investigation. Version 1 does
not remediate, write an evidence archive, expose raw journal messages, or
contact monitoring, ticketing, cloud, or remote-host systems.

## Quick start

```text
ohtools incident snapshot
ohtools incident services
ohtools incident timeline
ohtools incident timeline --since 24h
```

The three commands are diagnostic and non-root. Only timeline has an option:
`--since` defaults to `1h` and accepts a positive Go-style duration no greater
than `168h`.

## Commands

### incident snapshot

Runs seven independent bounded probes:

1. `/etc/os-release` for Debian or Ubuntu ID and version;
2. `/proc/loadavg` for the one-, five-, and fifteen-minute load values;
3. `/proc/meminfo` for total and available memory and total and free swap;
4. `df -P -B1 -- /` for root filesystem totals and used percentage;
5. `ss -H -lntu` for a listener count;
6. a fixed failed-service `systemctl list-units` query;
7. priority `0..3` journal events from the last hour.

The snapshot returns numeric resource summaries, safe failed-unit state fields,
listener count, count of recent error lines, and count of lines matching
out-of-memory markers. Missing or malformed optional evidence becomes a
structured error while the other probes remain available.

### incident services

Runs only the fixed failed-service query and returns sorted unit name,
load state, active state, and sub state. Human-readable service descriptions
are ignored. A failed or malformed probe yields a partial Result with an empty
collection rather than raw output.

### incident timeline

Runs the fixed priority `0..3` `journalctl` query for the validated lookback
duration. At most 1024 events are returned, each with only a validated timestamp
and one compiled category: `out-of-memory`, `service-failure`,
`authentication`, `kernel`, or `error`.

Classification uses the raw line only in memory. The message, unit description,
username, process fields, and other journal values are discarded.

## How it works

Protocol v1 validates the read-only invocation before execution, and timeline
also validates its duration during planning. All file and command reads have
fixed byte limits. Each parser accepts a narrow expected grammar and converts
it directly to typed numeric or allowlisted fields.

`/etc/os-release` accepts Debian or Ubuntu with a numeric version. Memory values
must be kB integers and are converted to bytes with overflow checks. Disk
output must contain exactly one data row for `/`. Failed service names must end
in `.service` and states must be bounded lowercase tokens.

Journal output is never copied to Result. Snapshot counts lines and OOM markers;
timeline validates timestamps and assigns categories. Cancellation and timeout
stop collection.

## Data access

The plugin reads:

```text
/etc/os-release
/proc/loadavg
/proc/meminfo
```

It may execute only:

```text
df -P -B1 -- /
ss -H -lntu
systemctl list-units --type=service --state=failed --all --no-legend --plain
journalctl --no-pager --output=short-iso --priority=0..3 --since=-{duration}
```

Files are limited to 256 KiB, ordinary command stdout to 256 KiB, journal
stdout to 512 KiB, and stderr to 16 KiB. No shell, arbitrary journal filter,
arbitrary mount, process argv, environment, remote endpoint, or output path is
accepted.

## Results and exit behavior

Snapshot emits `incident.snapshot` with available and total probe counts.
Individual missing or malformed sources create dependency errors; successful
evidence stays in `data.snapshot`.

Services emits `incident.services` as pass or partial and includes
`failed_count` when successful. Timeline emits `incident.timeline` as pass or
partial and includes event count and normalized lookback.

An invalid `--since` value uses the arguments exit mapping. Unsupported
invocations also use arguments. Cancellation and timeout propagate. Other local
probe failures are represented by partial Result v1 rather than leaking command
diagnostics.

## Configuration

Version 1 has no `/etc/ohtools/plugins/incident-base.yaml`. Evidence sources,
commands, limits, journal priority, categories, and the default and maximum
lookback are compiled.

The only operator-controlled plugin input is the timeline flag:

```text
--since 30m
--since 24h
--since 168h
```

Zero, negative, malformed, and longer durations are rejected before collection.

## What can be changed

Operators can choose a valid timeline lookback from greater than zero through
168 hours and standard host output or timeout options. Normal changes to local
OS state, resources, services, listeners, and journal events affect later
snapshots.

The probe list, journal priority, evidence fields, categories, byte and event
limits, and fixed argv cannot be overridden. Maintainers change them through
reviewed code, security tests, and a new immutable release.

## Fixed behavior

The seven snapshot probes, root mount, failed-service query, priority range
`0..3`, default `1h`, maximum `168h`, event categories, parsers, and all limits
are fixed in 1.0.0.

The plugin cannot restart services, kill processes, select arbitrary journal
units or priorities, expose messages, collect user activity, select a remote
host, or write an archive.

## Safety

All commands are read-only, local-only, and non-root. Commands use fixed direct
argv and bounded stdout and stderr. Files use confined bounded reads that reject
unsafe path types. Strict parsers fail closed for malformed, truncated,
oversized, and unexpected input.

Raw journal messages, service descriptions, usernames, credentials,
environment values, process command lines, listener addresses, and command
stderr are not returned. Host policy, timeout, audit, JSON stdout isolation,
and recursive redaction remain active.

## Troubleshooting

- Missing OS, load, or memory evidence usually indicates unsafe, unreadable, or
  malformed fixed local files or a 256 KiB limit violation.
- Disk evidence requires parseable GNU-compatible `df -P -B1` output for `/`.
- Listener evidence requires bounded six-field `ss -H -lntu` records.
- Failed-service evidence requires the fixed systemd query and safe `.service`
  names; descriptions are intentionally ignored.
- Timeline partial status indicates unavailable, malformed, oversized, or more
  than 1024 journal events in the selected window. Reduce `--since` if needed.
- A duration error must be corrected rather than bypassed; the upper bound is
  exactly `168h`.

## Limitations and TODO

Version 1 is deliberately local-only and evidence-only. Remediation, service
restart, process actions, remote evidence, SSH, monitoring and ticketing APIs,
cloud queries, expanded journal and process attribution, archives, output
files, remote execution, and downloads are deferred. Remote collection
requires endpoint and credential policy, TLS identity, redirect and
DNS-rebinding controls, bounded schemas, and tenant-safe redaction. Remediation
requires plans, authorization, audit, confirmation, verification, and recovery.

See the maintained [incident roadmap](../../roadmap/incident-base.md).

## Compatibility and source

This page documents `incident-base` 1.0.0 and plugin protocol v1. The signed
catalog remains authoritative for installation, minimum host version, asset
size, SHA-256, and release history.

Implementation: [internal/incident](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/incident).
