---
schema_version: 1
plugin_id: baseline-base
locale: en
documented_version: 1.0.0
title: Compiled local operating-system baseline
summary: Select and evaluate bounded local checks for supported Debian and Ubuntu hosts.
command_paths:
  - baseline check
  - baseline profile
local_only: true
---

## Purpose and supported scenarios

`baseline-base` evaluates a compiled, read-only operating-system baseline. It
checks supported platform metadata, time synchronization, account-file safety,
two kernel controls, selected OpenSSH posture, dpkg record state, and required
local services.

Operators may enable or disable only known compiled checks and adjust one
numeric package threshold. Version 1 does not accept scripts, commands, paths,
inline policy, organization profiles, or remote targets.

## Quick start

Show exactly which profile and checks would be used:

```text
ohtools baseline profile
```

Evaluate the selected checks:

```text
ohtools baseline check
```

A minimal strict configuration that disables one compiled check is:

```yaml
profile: default
enabled_checks: []
disabled_checks:
  - ssh-posture
max_pending_package_records: 0
```

## Commands

### baseline profile

Loads and validates system configuration, detects the local platform, and
returns the effective compiled profile, ordered check IDs, platform ID/version,
and `max_pending_package_records`. It does not run the individual probes.

### baseline check

Runs selected checks in stable compiled order:

- `supported-os` confirms that the detected Debian or Ubuntu release is in the
  supported matrix.
- `time-sync` runs
  `timedatectl show --property=NTPSynchronized --value` and expects `yes`.
- `filesystem-ownership` checks that `/etc/passwd` and `/etc/shadow` are regular,
  not group/world writable, and root-owned when inspecting the live Linux root.
- `kernel-controls` expects `kernel.randomize_va_space` to be `2` and
  `net.ipv4.conf.all.rp_filter` to be `1` through bounded procfs reads.
- `ssh-posture` runs the fixed effective-config query and expects root login to
  be `no` or `prohibit-password` and password authentication to be `no`.
- `package-state` counts dpkg records not equal to `install ok installed` and
  compares the count with the configured maximum.
- `required-services` runs `systemctl is-active -- ssh` and then `cron` and
  expects both to be active.

Mismatch produces warning or, for unsafe account-file metadata, critical.
Unavailable optional probes produce skipped checks and structured errors.

## How it works

Execution is deterministic:

1. Load strict system YAML over compiled defaults.
2. Validate the profile ID, known check IDs, duplicates, exclusions, and
   threshold range.
3. Detect platform from the selected local root and reject unsupported releases.
4. Select checks: a non-empty `enabled_checks` is an allowlist; otherwise all
   compiled checks are candidates. `disabled_checks` is then subtracted.
5. Run at least one selected check in the fixed compiled order.
6. Stop immediately only for cancellation or timeout; represent ordinary probe
   loss as skipped plus a structured error.
7. Build Result v1 with profile, platform, and stable check IDs.

External command stdout and stderr are limited to 64 KiB for each baseline
probe. The dpkg status read is limited to 32 MiB.

## Data access

Depending on the selected checks, the plugin may read:

- `/etc/os-release`;
- metadata for `/etc/passwd` and `/etc/shadow`;
- `/proc/sys/kernel/randomize_va_space`;
- `/proc/sys/net/ipv4/conf/all/rp_filter`;
- `/var/lib/dpkg/status`;
- `/etc/ohtools/plugins/baseline-base.yaml`.

It may run trusted local `timedatectl`, `sshd`, and `systemctl` with fixed argv.
It does not invoke a shell, mutate a setting, or use the network.

## Results and exit behavior

Results use schema v1. Compliant checks pass. State differing from the compiled
baseline warns, unsafe required account-file metadata is critical, and an
unavailable probe is skipped with a structured general or dependency error.
Mixed usable and unavailable checks normally yield partial status.

Configuration errors use exit 10. Missing or unsupported platform metadata uses
exit 4. Invalid invocations use exit 2. Cancellation and timeout propagate.
Ordinary missing probe executables remain represented inside a usable Result
rather than aborting all other checks.

## Configuration

The fixed configuration path is:

```text
/etc/ohtools/plugins/baseline-base.yaml
```

Compiled defaults:

```yaml
profile: default
enabled_checks: []
disabled_checks: []
max_pending_package_records: 0
```

`profile` must be `default`. Check arrays may contain only the seven IDs listed
under `baseline check`, without duplicates. When `enabled_checks` is empty all
checks start enabled. `disabled_checks` always removes matching candidates. At
least one check must remain.

`max_pending_package_records` accepts `0..100000`. YAML is strict: unknown keys,
duplicates, multiple documents, invalid types, symlink config, unsafe ownership
or group/world write access, and files over 1 MiB fail with exit 10.

## What can be changed

Operators can choose a subset of compiled checks and set the tolerated count of
incomplete dpkg records. They can remediate the local state being evaluated and
rerun the read-only check.

Adding a check, changing expected kernel values, supported platforms, required
services, or SSH policy requires a reviewed source change and a new immutable
plugin release.

## Fixed behavior

The only profile is `default`. Check implementation, ordering, paths,
executables, expected kernel values, required `ssh` and `cron` services, and SSH
expectations are compiled. YAML cannot provide a new path, command, expected
value, script, include, remote endpoint, or arbitrary check.

`baseline check` reports deviation; it never applies a remediation.

## Safety

Both manifest commands are diagnostic and declare no root, confirmation, force,
or dry-run requirement. System YAML is loaded only from its trusted fixed path.
Local reads reject traversal and unsafe symlinks; account files are checked for
unsafe ownership and permissions rather than trusted blindly.

Executables are resolved to trusted absolute paths and run directly with fixed
argv and bounded output. No check executes content from YAML or from a local
profile file.

## Troubleshooting

- Exit 10 means the strict YAML or its filesystem trust requirements failed;
  check key names, known IDs, duplicates, the numeric range, owner, and mode.
- If no checks remain, remove conflicting enable/disable selections.
- Exit 4 before checks usually means `/etc/os-release` is missing or the release
  is outside the compiled Debian/Ubuntu matrix.
- A skipped time, SSH, or service check identifies its dependency in the
  structured error; restore the trusted executable and rerun.
- A critical filesystem check requires inspecting ownership, regular-file type,
  symlinks, and group/world write bits before trusting identity databases.
- Package-state warning compares records, not available upgrades.

## Limitations and TODO

Version 1 is local-only and compiled. Organization-authored or signed profiles,
custom checks, remediation, fleet evaluation, remote commands, remote endpoints,
and credential handling are TODO. Signed schemas, provenance, path allowlists,
versioned migration, authenticated inventory transport, and bounded fan-out are
required first.

See the [baseline-base roadmap](../../roadmap/baseline-base.md).

## Compatibility and source

This page documents `baseline-base` 1.0.0 and plugin protocol v1. The signed
catalog remains authoritative for host compatibility and release metadata.

Implementation: [internal/baseline](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/baseline).
