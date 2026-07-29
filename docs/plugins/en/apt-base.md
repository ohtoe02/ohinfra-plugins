---
schema_version: 1
plugin_id: apt-base
locale: en
documented_version: 1.0.0
title: Local APT and dpkg diagnostics
summary: Inspect the local dpkg database, cached upgrade simulation, configured sources, and current APT history.
command_paths:
  - apt status
  - apt updates
  - apt sources
  - apt history
local_only: true
---

## Purpose and supported scenarios

`apt-base` provides read-only diagnostics for Debian and Ubuntu package state.
It counts installed and incomplete dpkg records, simulates upgrades from data
already available to APT, inventories local source declarations, and summarizes
the current APT transaction history.

Version 1 never refreshes repositories, downloads packages, installs, removes,
or upgrades software. It is suitable for local triage before a separately
authorized package-management operation.

## Quick start

Inspect local package database consistency:

```text
ohtools apt status
```

Simulate upgrades without locks or downloads:

```text
ohtools apt updates
```

Review normalized repository declarations and current history:

```text
ohtools apt sources
ohtools apt history
```

## Commands

### apt status

Reads `/var/lib/dpkg/status` and counts records with exactly
`install ok installed` separately from every other package record. It also
counts up to 1,024 regular entries in `/var/lib/dpkg/updates`.

Check `apt:dpkg-status` passes when there are no incomplete records and warns
otherwise. Failure to inspect the optional update-record directory adds a
structured error; an absent directory is treated as zero pending records.

### apt updates

Runs the exact local simulation:

```text
apt-get --simulate --no-download --option Debug::NoLocking=true upgrade
```

Only stdout lines beginning with `Inst` are converted into package,
installed-version, and candidate-version metadata. No package bytes are
downloaded, no repository metadata is refreshed, and no dpkg lock is taken.

### apt sources

Reads the classic `/etc/apt/sources.list`, then at most 256 regular `.list` or
`.sources` files directly under `/etc/apt/sources.list.d`. It parses classic
`deb` or `deb-src` lines and basic deb822 fields for types, URIs, suites, and
components.

Source URI userinfo, query, and fragment are removed before output. Invalid URIs
are replaced by a marker. Symlinks, non-regular entries, unrelated extensions,
comments, and malformed records are ignored.

### apt history

Reads only the current `/var/log/apt/history.log`. Each transaction includes
start date, optional end date, and counts for install, upgrade, remove, purge,
downgrade, and reinstall actions. Package names and command lines are not
published by this summary.

Missing current history is a valid empty result. An unreadable or oversized
file produces a structured error and partial status.

## How it works

The plugin uses bounded local sources and deterministic parsers:

1. The dpkg status file is scanned as package stanzas with a 32 MiB bound.
2. Update fragments and source files are enumerated without following symlinks.
3. Source files are read with a 1 MiB per-file bound and sorted by path.
4. Upgrade discovery invokes trusted `apt-get` directly with fixed argv and
   limits stdout and stderr to 1 MiB each.
5. The current history file is read with a 4 MiB bound and reduced to action
   counts.
6. Result v1 is built with stable checks and recursively redacted errors.

No output from APT is executed or passed to a shell. Simulation parsing accepts
only the expected `Inst` prefix and treats all other lines as non-data.

## Data access

The plugin may read:

- `/var/lib/dpkg/status`;
- regular records in `/var/lib/dpkg/updates`;
- `/etc/apt/sources.list`;
- regular `.list` and `.sources` files in `/etc/apt/sources.list.d`;
- `/var/log/apt/history.log`;
- local APT lists and package state indirectly through `apt-get` simulation.

The only executable is trusted local `apt-get` with fixed arguments. The plugin
does not read repository credentials into its result and does not connect to a
repository.

## Results and exit behavior

Commands return Result schema v1 with normalized arrays. Incomplete dpkg state
uses warning. Unreadable optional sources or history use structured errors and
partial status while preserving safe data.

`apt status` returns exit 4 when the main dpkg database is absent and exit 1 for
other fatal inspection failures. `apt updates` returns exit 4 when `apt-get` is
missing and exit 1 when simulation fails or output is unsafe. Cancellation and
timeout propagate. Invalid invocations return exit 2.

The presence of upgrade candidates is inventory data and does not itself make
the command a mutation or a failure.

## Configuration

Version 1 has no plugin YAML settings or command-specific arguments. File
locations, inventory limits, action names, source fields, and the `apt-get`
simulation argv are compiled.

Standard host timeout, JSON, output, policy, audit, and redaction behavior still
applies. Host configuration cannot turn this plugin into a package updater.

## What can be changed

Operators can change local APT source declarations, package lists, cached
package state, or transaction history through separately authorized operating
system workflows and rerun the diagnostics.

Adding archive support, changing simulation policy, exposing new source fields,
or performing repository access requires reviewed source changes, security
tests, and a new immutable release.

## Fixed behavior

`--simulate`, `--no-download`, and `Debug::NoLocking=true` are mandatory in
1.0.0. The plugin cannot accept package names, repository URLs, proxy settings,
keys, credentials, arbitrary APT options, or shell fragments.

Only the current uncompressed history file is read. Sources are summarized
rather than semantically validated, and userinfo/query/fragment components are
always stripped from published URIs.

## Safety

Every command is diagnostic and read-only with no root, confirmation, force, or
dry-run requirement. File readers reject traversal and symlink targets and
bound file count and size. External execution uses a trusted absolute
executable, fixed argv, no shell, bounded output, and no caller-supplied
environment options.

The result contains no raw APT command output. URI credentials and potentially
sensitive query material are removed before recursive redaction.

## Troubleshooting

- Exit 4 from `apt status` means the local dpkg status database is unavailable;
  verify the host is Debian/Ubuntu and the database is a readable regular file.
- A warning from `apt:dpkg-status` means incomplete package or pending update
  records exist; inspect them with the distribution's recovery procedure.
- Exit 4 from `apt updates` means trusted `apt-get` is unavailable.
- A failed simulation can reflect stale or inconsistent local package metadata;
  this plugin will not refresh it.
- Partial sources indicate unreadable, unsafe, oversized, or excessive files.
- Empty history is expected when the current log is absent; compressed rotated
  logs are not inspected.

## Limitations and TODO

Version 1 is local-only. Repository refresh, downloads, signature provenance,
package mutation, remote mirror checks, and proxy use are TODO. They require
release-host allowlists, repository signature policy, isolated proxy handling,
redirect and timeout limits, planning, confirmation, and rollback.

Bounded reading of compressed history archives is also deferred. See the
[apt-base roadmap](../../roadmap/apt-base.md).

## Compatibility and source

This page documents `apt-base` 1.0.0 and plugin protocol v1. The signed catalog
remains authoritative for host compatibility and release metadata.

Implementation: [internal/apt](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/apt).
