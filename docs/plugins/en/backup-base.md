---
schema_version: 1
plugin_id: backup-base
locale: en
documented_version: 1.0.0
title: Backup base
summary: Inspect local Restic, BorgBackup, and rsnapshot installations, schedules, repository classes, and bounded history.
command_paths:
  - backup status
  - backup inventory
  - backup history
local_only: true
---
# Backup base

## Purpose and supported scenarios

`backup-base` provides read-only local diagnostics for Restic, BorgBackup, and
rsnapshot. It detects fixed executables and config files, classifies repository
location without publishing its value, inspects conventional systemd service
and timer units, and summarizes bounded local history files.

Use it to confirm which supported backup tools appear installed, whether their
fixed schedule units are loaded, and whether recent local history contains
recognized success or failure outcomes. It never opens a repository or runs a
backup, restore, check, prune, mount, or unlock operation.

## Quick start

```text
ohtools backup status
ohtools backup inventory
ohtools backup history
```

All commands are argument-free diagnostics. They need no root, confirmation,
force, or dry-run and do not mutate local or remote state.

## Commands

### backup status

Combines the complete product inventory with fixed service and timer metadata.
For each product it requests `LoadState`, `ActiveState`, and `SubState` for:

- `borg-backup.service` and `borg-backup.timer`;
- `restic-backup.service` and `restic-backup.timer`;
- `rsnapshot.service` and `rsnapshot.timer`.

The result reports sanitized product metadata and schedule states such as
`active/running` or `active/waiting`. A missing conventional unit is
informational; malformed or unavailable optional metadata is partial.

### backup inventory

Runs exact version probes for `borg --version`, `restic version`, and
`rsnapshot -V`. Each output must match a compiled grammar before only the
numeric version is retained.

It also reads one fixed config per product:

- `/etc/borgmatic/config.yaml`;
- `/etc/restic/restic.conf`;
- `/etc/rsnapshot.conf`.

Only repository class `local`, `ssh`, `object`, `remote`, or `unknown` is
returned. The actual repository value is never emitted. Product presence is
derived from a valid executable probe or present config.

### backup history

Reads fixed `/var/log/borg/backup.log`,
`/var/log/restic/backup.log`, and `/var/log/rsnapshot.log`. Each nonempty line
must begin with an RFC3339 timestamp and contain a recognized case-insensitive
success marker (`success` or `completed`) or failure marker (`failed` or
`error`).

The result contains product ID, total entries, successes, failures, and latest
UTC timestamp. It never returns raw history lines or messages.

## How it works

The plugin iterates a compiled adapter list in deterministic Borg, Restic, and
rsnapshot order. Version commands have exact program, argv, stdout, and stderr
limits. Config files use bounded confined reads and small product-specific
parsers:

1. Restic requires one exact `RESTIC_REPOSITORY` assignment.
2. Borg accepts the first nonempty `path:` repository field in the fixed
   borgmatic file.
3. rsnapshot accepts a two-field `snapshot_root` line.
4. Values are classified by syntax and immediately discarded.
5. History is aggregated without preserving message text.

Each config is limited to 256 KiB. Version stdout is limited to 4 KiB and
stderr to 8 KiB. Unit output is limited to 16 KiB. History is limited to
128 KiB and 512 nonempty lines per product.

## Data access

The plugin accesses only the three config paths, three history paths, the three
version executables, and six compiled systemd units listed above. It invokes
fixed direct argv; caller arguments, stdin, environment overrides, config
includes, environment files, arbitrary units, and arbitrary paths are not
forwarded.

No repository URL, SSH host, object-storage endpoint, socket, credential store,
remote response, tool stderr, or raw config and log content is included in
Result data.

## Results and exit behavior

Inventory emits one `backup.{product}.present` or
`backup.{product}.missing` check per adapter. Version or config problems add
structured dependency or configuration errors while other products remain
visible.

Status adds `backup.{product}.timer` or
`backup.{product}.schedule_missing`. Missing conventional schedules are info;
unsafe metadata is partial.

History emits `backup.{product}.history` as pass, info when the fixed log is
absent, or partial when unsafe, oversized, non-UTF-8, or malformed. Fatal local
inventory failures use the dependency mapping; unsupported invocation uses
arguments. Cancellation and timeout propagate. Output follows Result schema v1.

## Configuration

Version 1 has no `/etc/ohtools/plugins/backup-base.yaml`. Adapter paths,
commands, schedules, parsers, and limits are compiled.

The plugin observes small fragments of existing product configuration. For
example, it recognizes a Restic assignment shaped like:

```text
RESTIC_REPOSITORY=/srv/restic
```

This is not a recommended complete backup configuration. Operators must use
each product's own secure configuration and secret-management process.

## What can be changed

Operators can install supported tools, manage their three fixed config files,
repository locations, conventional units, and local history through normal
backup operations. The next diagnostic reflects those saved local changes.

The plugin does not expose adapters, paths, unit names, parsing grammar,
outcome markers, or limits as user configuration. Maintainers change them only
with source review, adversarial fixtures, and a new immutable release.

## Fixed behavior

The three products, executable argv, version grammars, config and history
paths, unit names, repository classifications, success/failure markers, and
resource limits are fixed in 1.0.0.

The plugin cannot accept a repository, password file, credential, service,
timer, output path, retention policy, backup source, or restore destination.
It never invokes a repository operation.

## Safety

All commands are read-only, local-only, and non-root. Files are read through
bounded confined probes that reject symlinks and unsafe path shapes. External
commands use exact argv and bounded output. Missing optional dependencies
degrade instead of causing unbounded fallback discovery.

Repository values, URL userinfo, SSH targets, passwords, environment files,
cloud credentials, raw configs, raw histories, stderr, and remote payloads are
omitted. Host policy, timeout, audit, clean JSON stdout, and recursive redaction
remain active.

## Troubleshooting

- If a tool is missing, verify the exact trusted executable and its supported
  version-output format.
- If config metadata is partial, check the fixed file for symlinks, UTF-8, the
  256 KiB limit, and the exact repository field expected for that product.
- An `unknown` repository class means a nonempty value was recognized but did
  not match a compiled local, SSH, object, or URL shape.
- If schedule metadata is missing, remember that custom unit names are not
  discovered in version 1.
- If history is partial, ensure no more than 512 nonempty RFC3339 lines and use
  a recognized success or failure marker on every line.

## Limitations and TODO

Version 1 is deliberately local-only. Repository availability and
authentication, remote checks, backup and restore, mount, unlock, prune,
retention, integrity checks, dynamic schedules, config includes, journal
collection, archive output, remote execution, and downloads are deferred.
Network support requires policy-pinned repositories, TLS or SSH host
verification, credential references, proxy and redirect controls, resource
budgets, and recursive redaction. Mutations require a separate plan,
confirmation, audit, verification, and recovery design.

See the maintained [backup roadmap](../../roadmap/backup-base.md).

## Compatibility and source

This page documents `backup-base` 1.0.0 and plugin protocol v1. The signed
catalog remains authoritative for installation, minimum host version, asset
size, SHA-256, and release history.

Implementation: [internal/backup](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/backup).
