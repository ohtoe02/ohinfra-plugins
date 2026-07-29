---
schema_version: 1
plugin_id: server-setup-base
locale: en
documented_version: 1.0.0
title: Server setup base
summary: Inspect and safely converge the compiled local ohtools server baseline.
command_paths:
  - setup check
  - setup apply
  - setup upgrade
local_only: true
---
# Server setup base

## Purpose and supported scenarios

`server-setup-base` checks whether a supported Debian or Ubuntu host matches the
compiled ohtools baseline. It can plan and apply missing local state and can
upgrade approved packages only when the required package bytes are already
available locally.

Version 1 supports exact profiles for Debian 10, 11, 12, and 13 and Ubuntu
20.04, 22.04, and 24.04. It is intended for initial host preparation, drift
inspection, repeatable convergence, and bounded upgrades of ohtools-owned
state. It is not a general configuration-management engine.

## Quick start

Inspect the host without changing it:

```text
ohtools setup check
```

Preview the exact convergence plan:

```text
ohtools --dry-run setup apply
```

Apply after reviewing the plan. The host requires root, policy authorization,
audit availability, and confirmation unless the operator supplies `--yes`:

```text
ohtools --yes setup apply
```

Inspect and apply only cached package upgrades:

```text
ohtools --dry-run setup upgrade
ohtools --yes setup upgrade
```

## Commands

### setup check

This read-only diagnostic selects the exact compiled profile, inspects every
profile object, and emits checks for packages, the system group and user,
directories, the managed state file, sysctls, and the systemd unit. Missing or
different state normally produces warning checks. An unsupported platform
produces a critical `platform` check and a structured configuration error.

### setup apply

Planning repeats the complete check and converts warning or critical objects
into an ordered change list: packages, groups, users, directories, files,
sysctls, then units. Execution accepts only the digest of the current plan,
rechecks state, applies the transaction, verifies every planned object, and
commits only after verification succeeds.

Package installation uses `apt-get --no-download`; version 1 never retrieves a
package from a repository. A failed change or failed verification initiates a
bounded rollback.

### setup upgrade

The upgrade probe asks `apt-get` to simulate `--no-download --only-upgrade` for
the three compiled packages. Only approved packages reported by that local
simulation enter the plan. Execution uses the same plan-digest, transaction,
rollback, and verification lifecycle as `setup apply`.

This command is not a distribution upgrade and cannot add repositories or
download packages.

## How it works

The plugin detects the platform from the selected local root and resolves
either the matching automatic profile or the exact configured profile. Every
profile contains immutable package, account, directory, managed-file, sysctl,
and unit specifications.

The checker performs these stages:

1. Run bounded `dpkg-query` inspection for `ca-certificates`, `curl`, and `jq`.
2. Read and strictly parse bounded `/etc/group` and `/etc/passwd` data.
3. Inspect directory type, mode, ownership, and path confinement.
4. Read the managed file without following symlinks and compare its SHA-256,
   mode, owner, and group.
5. Read each compiled sysctl with `sysctl -n`.
6. inspect unit enablement with `systemctl is-enabled`.
7. Build Result v1 with deterministic check identifiers and the selected
   profile ID.

Mutation planning rejects skipped, error, partial, or cancelled source checks.
Before execute, the plugin recomputes the plan and compares its digest in
constant time. Transactions record undo operations. Managed files are replaced
atomically, and post-operation checks must pass before commit.

## Data access

The plugin reads:

- `/etc/os-release` below the selected root for platform detection;
- `/etc/group` and `/etc/passwd`, bounded by `account_file_limit_bytes`;
- `/etc/ohtools/setup-state.yaml`, bounded by
  `managed_file_limit_bytes`;
- metadata for `/etc/ohtools`, `/etc/ohtools/plugins`,
  `/var/lib/ohtools`, and `/var/log/ohtools`;
- current sysctl values and systemd enablement;
- the local dpkg database and locally cached APT state through fixed commands.

It invokes trusted absolute-path resolutions of `dpkg-query`, `apt-get`,
`groupadd`, `groupdel`, `useradd`, `userdel`, `sysctl`, and `systemctl` with
fixed argv structures. It never invokes a shell.

Apply may create the `ohtools` system group and user, create or repair compiled
directories, atomically replace the managed state file, set two protected-link
sysctls, and enable `systemd-timesyncd.service`.

## Results and exit behavior

The plugin always uses Result schema v1. Important check ID prefixes are
`package:`, `group:`, `user:`, `directory:`, `file:`, `sysctl:`, and `unit:`.
The `data.profile` field identifies the selected compiled profile.

Read failures that do not make execution unsafe are represented by skipped
checks plus structured errors. Context cancellation and timeout remain fatal.
Configuration and platform-selection failures use the configuration exit
mapping. Missing executables use the dependency exit mapping. Invalid
invocations or stale plan digests use the arguments exit mapping. Transaction,
rollback, or verification failures use the general failure mapping.

With JSON output, stdout contains only the Result document. Diagnostics remain
on stderr and command output is not copied into audit fields.

## Configuration

The only configuration path is:

```text
/etc/ohtools/plugins/server-setup-base.yaml
```

Defaults:

```yaml
profile: auto
managed_file_limit_bytes: 1048576
account_file_limit_bytes: 262144
```

`profile` may be `auto` or one exact compiled ID such as `debian-12` or
`ubuntu-24.04`. An explicit profile must match the detected supported platform.

`managed_file_limit_bytes` accepts 4096 through 8388608 bytes.
`account_file_limit_bytes` accepts 4096 through 1048576 bytes.

The file uses strict YAML. Unknown or duplicate keys, multiple documents,
invalid ranges, symlinks, unsafe ownership, or group/world-writable permissions
produce a configuration error. User config, host `--config`, environment
variables, and XDG paths cannot override this system plugin configuration.

## What can be changed

Operators can select an exact supported profile and reduce or increase the two
bounded inspection limits within their accepted ranges. Operators also decide
whether and when an approved mutation plan is confirmed.

Host policy may further restrict operational commands. Local package cache
contents determine whether `setup apply` can install a missing package and
whether `setup upgrade` can plan a cached upgrade.

Maintainers can change compiled profiles, checks, limits, or transaction
adapters only through reviewed source changes, tests, and a new immutable plugin
release.

## Fixed behavior

The supported platform matrix, three packages, `ohtools` account identity,
directory paths and modes, managed file content, protected-link sysctls, and
systemd unit are compiled into version 1. They cannot be supplied in YAML.

The command does not accept arbitrary package names, paths, sysctls, units,
shell fragments, repository URLs, credentials, or remote hosts. Apply ordering,
plan-digest verification, post-change verification, and rollback are mandatory.

## Safety

`setup check` is read-only and does not require root. Both mutation commands
declare root, dry-run support, and confirmation in their manifest. The host
owns policy authorization, audit fail-closed behavior, confirmation, timeout,
and redaction before plugin execution.

All operational commands use direct argv execution with option terminators.
Compiled names and values are validated before use. Path traversal, symlinked
managed paths, unsafe ownership, oversized reads, incomplete checks, stale plan
digests, and uncached package operations are rejected.

Rollback runs with a five-second bounded context. A rollback failure is
redacted and reported together with the primary transaction failure.

## Troubleshooting

- If no profile is available, verify `/etc/os-release` identifies an exact
  supported Debian or Ubuntu release and remove an incorrect explicit profile.
- If configuration returns exit 10, check strict YAML syntax, permissions,
  ownership, key names, profile ID, and byte ranges.
- If a dependency is missing, restore the distribution-provided executable;
  do not substitute a shell wrapper earlier in `PATH`.
- If package apply reports no cached bytes, populate the approved package cache
  through a separately controlled process. The plugin will not download them.
- If planning reports incomplete checks, resolve the underlying file,
  executable, permission, or output-limit error and run `setup check` again.
- If the plan digest is stale, review the newly generated plan instead of
  retrying the previous digest.
- After a transaction failure, inspect the redacted Result and root audit log,
  correct the local cause, and rerun the read-only check before another apply.

## Limitations and TODO

Version 1 is deliberately local-only. Repository bootstrap, network package
retrieval, credential references, remote orchestration, and multi-host fan-out
are deferred. Future network behavior requires immutable repository
coordinates, expected hashes, endpoint allowlists, HTTPS and signature
verification, redirect limits, secret isolation, and per-host authorization.

The maintained backlog and acceptance criteria are in the
[server setup roadmap](../../roadmap/server-setup-base.md).

## Compatibility and source

This page documents `server-setup-base` 1.0.0 and plugin protocol v1. The
released catalog entry defines the minimum compatible ohtools host and remains
authoritative for installation, asset size, SHA-256, and release history.

Implementation: [internal/serversetup](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/serversetup).

