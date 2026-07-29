---
schema_version: 1
plugin_id: postgres-base
locale: en
documented_version: 1.0.0
title: Local PostgreSQL diagnostics
summary: Detect local PostgreSQL installations, list Debian clusters, and inspect bounded public configuration metadata.
command_paths:
  - postgres status
  - postgres clusters
  - postgres config
local_only: true
---

## Purpose and supported scenarios

`postgres-base` detects evidence of a local PostgreSQL installation, lists
Debian-style clusters, and summarizes selected local configuration files. It is
designed for package- and process-level triage without connecting to a database.

Version 1 never invokes `psql`, opens a TCP or Unix socket, authenticates,
queries a catalog, changes a cluster, or returns authentication rules and
secret-bearing PostgreSQL settings.

## Quick start

Detect local installation evidence and service posture:

```text
ohtools postgres status
```

List clusters known to `postgresql-common`:

```text
ohtools postgres clusters
```

Inspect bounded configuration metadata:

```text
ohtools postgres config
```

## Commands

### postgres status

Combines five local signals: `pg_lsclusters`, the
`postgresql.service` systemd state, installed dpkg package records, procfs
processes named `postgres`, and presence of `/etc/postgresql`.

The result returns an `installed` boolean, counts and bounded inventories, and
safe systemd load/active/sub states when available. No evidence produces an
info check. Detected installation produces pass, or warning when a known
service is not active and no PostgreSQL process is visible.

Individual optional probe loss does not by itself fail status; the remaining
local evidence is still evaluated.

### postgres clusters

Runs exactly `pg_lsclusters --no-header`. Valid records contain bounded version,
cluster name, port `1..65535`, status, owner, absolute data directory, and
absolute log path. Unsafe, malformed, traversal, URL-like, or overlong records
are skipped. Output is sorted by version and cluster name.

The command is specific to distributions providing `postgresql-common`.

### postgres config

Enumerates regular non-symlink directories under
`/etc/postgresql/VERSION/CLUSTER`, with at most 64 entries per directory. It
looks only for `pg_hba.conf`, `pg_ident.conf`, `postgresql.auto.conf`, and
`postgresql.conf`, each limited to 1 MiB.

For every present file it returns path, size, and non-comment line count. Only
the following `postgresql.conf` and `postgresql.auto.conf` settings may expose
values: `effective_cache_size`, `hot_standby`, `listen_addresses`,
`log_destination`, `logging_collector`, `maintenance_work_mem`,
`max_connections`, `max_wal_senders`, `port`, `shared_buffers`, `timezone`,
`wal_level`, and `work_mem`. A value containing a URI is replaced with a
redaction marker. HBA and ident records are counted but never returned.

## How it works

The plugin uses independent, bounded local probes:

1. A fixed `systemctl show postgresql.service` query obtains allowlisted state.
2. `/var/lib/dpkg/status`, limited to 16 MiB, is filtered to installed
   `postgresql`, `postgresql-common`, and `postgresql-*` packages.
3. At most 32,768 procfs entries are considered; only safe numeric directories
   with a `comm` value for PostgreSQL are retained.
4. `pg_lsclusters` output is limited to 256 KiB and strictly validated.
5. Configuration directories and files are confined, bounded, sorted, and
   reduced to public metadata.
6. Result v1 is built deterministically without including command diagnostics.

Cancellation or timeout aborts active cluster/config collection. Symlinked
configuration directories and files are not followed.

## Data access

The plugin may read:

- `/var/lib/dpkg/status`;
- `/proc/PID/comm` and `/proc/PID/status`;
- the `/etc/postgresql` directory tree and four fixed filenames per cluster;
- systemd state through a local fixed command;
- cluster inventory through local `pg_lsclusters`.

The exact other command is
`systemctl show postgresql.service --property=LoadState,ActiveState,SubState --no-pager`.
No database files, passwords, private keys, sockets, SQL, or network endpoints
are accessed.

## Results and exit behavior

Commands return Result schema v1 with normalized arrays. Status uses info, pass,
or warning depending on detected evidence. Cluster and config success use pass
checks with counts.

`postgres clusters` returns exit 4 if `pg_lsclusters` is unavailable, nonzero,
truncated, or otherwise unusable. `postgres config` returns exit 4 when no safe
configuration file can be obtained. Invalid invocation returns exit 2.
Cancellation and timeout propagate as fatal errors.

`postgres status` is intentionally tolerant: missing optional signals can lead
to fewer records rather than a command failure.

## Configuration

Version 1 has no plugin YAML configuration and accepts no command arguments or
flags. Package prefixes, service name, procfs limits, directory depth, fixed
config filenames, and public-setting allowlist are compiled.

Standard host controls for timeout, output, JSON, policy, audit, and redaction
continue to apply.

## What can be changed

Operators can change the observed local PostgreSQL installation, service,
packages, clusters, processes, and configuration through normal authorized
PostgreSQL and operating-system workflows, then rerun diagnostics.

Supporting other layouts, exposing additional safe settings, or adding database
queries requires reviewed code, security classification, tests, and a new
immutable release.

## Fixed behavior

Only Debian-style `pg_lsclusters` inventory and `/etc/postgresql` layout are
supported. Paths, filenames, service name, package filters, limits, state
allowlists, and public PostgreSQL settings are fixed in 1.0.0.

No DSN, host, port argument, socket path, username, password, query, include
path, arbitrary config path, or executable can be supplied.

## Safety

All manifest commands are diagnostic and declare no root, mutation,
confirmation, force, or dry-run requirement. Filesystem traversal and symlinks
are rejected, directory/file inventories are bounded, and parsed paths must be
absolute local paths without traversal or URL syntax.

Trusted executables are invoked directly with fixed argv and bounded output.
Secret-capable configuration fields and HBA/ident contents are omitted by
allowlist, and URI-like values are redacted.

## Troubleshooting

- Exit 4 from `postgres clusters` usually means `postgresql-common` and its
  trusted `pg_lsclusters` executable are absent or failed.
- `postgres status` can still detect packages, processes, service metadata, or
  config when cluster inventory is unavailable.
- Exit 4 from config means `/etc/postgresql` has no supported safe files or a
  directory/file failed confinement or size checks.
- Empty `settings` for HBA and ident files is intentional; only counts are
  published.
- A configuration value absent from the allowlist is deliberately omitted, not
  necessarily missing from PostgreSQL.
- Status does not prove database readiness; it makes no connection or query.

## Limitations and TODO

Version 1 is local-only. Authenticated health, SQL, replication, socket/TCP
checks, credentials, TLS, remote endpoints, and runtime setting comparison are
TODO. They require least-privilege roles, host-owned credentials, endpoint
allowlists, TLS identity checks, query/row/time limits, and recursive redaction.

Resolving configuration includes and version-specific semantic validation is
also deferred. See the
[postgres-base roadmap](../../roadmap/postgres-base.md).

## Compatibility and source

This page documents `postgres-base` 1.0.0 and plugin protocol v1. The signed
catalog remains authoritative for host compatibility and release metadata.

Implementation: [internal/postgres](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/postgres).
