---
schema_version: 1
plugin_id: systemd-base
locale: en
documented_version: 1.0.1
title: Systemd base
summary: Inspect local systemd units, read redacted journal entries, and perform a verified restart.
command_paths:
  - service status
  - service logs
  - service restart
local_only: false
---
# Systemd base

## Purpose and supported scenarios

`systemd-base` exposes a small, validated interface for one local systemd unit
at a time. It reports stable unit properties, returns bounded and redacted
journal lines, and supports a host-authorized restart followed by active-state
verification.

Use it for service diagnostics and a controlled recovery attempt. It is not a
general wrapper around `systemctl` or `journalctl`: callers cannot supply
arbitrary verbs, properties, journal expressions, or multiple units.

## Quick start

Inspect one unit:

```text
ohtools service status ssh.service
```

Read up to 100 recent lines from the last 30 minutes:

```text
ohtools service logs ssh.service --since 30m --lines 100
```

Review a restart without executing it, then perform the approved operation:

```text
ohtools --dry-run service restart ssh.service
ohtools --yes service restart ssh.service
```

Restart requires root, policy authorization, available audit, and confirmation
unless the operator supplies `--yes`.

## Commands

### service status

Runs a fixed `systemctl show` query for `Id`, `LoadState`, `ActiveState`,
`SubState`, `UnitFileState`, `MainPID`, and `ExecMainStartTimestamp`. Returned
property names are converted to snake case and placed in Result data together
with the validated unit name.

### service logs

Runs a fixed `journalctl` query for exactly one unit, a positive lookback
duration, and 1 through 100000 lines. Command flags override the YAML defaults
for this invocation. Empty output lines are removed and every returned line is
passed through secret redaction.

### service restart

The plan reads the current stable properties, reports a `current-state` info
check, declares one planned restart change, and identifies temporary service
unavailability as a risk.

Immediately before execution, the plugin rebuilds the current plan and requires
the approved plan digest to match. It calls `systemctl restart`, then polls
`systemctl is-active --quiet` every 250 milliseconds until success or the
configured verification timeout. Success emits `verify-active`, the plan
digest, and a completed change.

## How it works

Every invocation first loads and validates the system plugin configuration.
The unit must contain 1 through 255 characters, begin with an ASCII letter or
digit, and then contain only ASCII letters, digits, colon, underscore, dot,
at-sign, or hyphen. Slash, backslash, whitespace, option prefixes, and shell
metacharacters are rejected before an executable is resolved.

Status and restart planning use the same fixed property query. Restart
execution is bound to that freshly generated plan by its protocol v1 digest.
Verification gets its own bounded context and does not accept success until
systemd reports the unit active.

If restart or verification fails, the plugin makes a best-effort diagnostic
query for the same properties and the last 50 journal lines. Diagnostics and
the primary error are recursively redacted before Result v1 is returned.

## Data access

The plugin reads:

- `/etc/ohtools/plugins/systemd-base.yaml`;
- stable unit properties through `systemctl show`;
- unit journal entries through `journalctl`;
- active state through `systemctl is-active`.

Restart invokes `systemctl restart -- UNIT`. Executables are resolved through
the trusted system resolver and called with direct argv, never through a shell
or `sudo`. Stdout is limited to 10 MiB and stderr to 1 MiB for each command.

## Results and exit behavior

Successful status data includes `unit`, `id`, `load_state`, `active_state`,
`sub_state`, `unit_file_state`, `main_p_i_d`, and
`exec_main_start_timestamp` when emitted by systemd. Values remain strings.
Successful logs data includes the unit, normalized duration, and redacted
`lines`.

Restart success contains the `verify-active` pass check, one completed change,
and `data.plan_digest`. Restart failure has status `error`, a failed change,
structured `restart_failed` or `verify_timeout`, and redacted diagnostics when
available. No rollback is claimed: a service that failed to restart may remain
inactive and requires operator diagnosis.

Invalid command shape, unit, log range, or plan digest uses exit 2.
Configuration errors use exit 10. The host enforces the restart privilege exit
3 and maps cancellation/timeout through the stable Result/exit contract.
Missing `systemctl` or `journalctl` is a dependency failure and a nonzero tool
exit is reported without placing unredacted diagnostics in the result.

## Configuration

The optional system configuration is:

```text
/etc/ohtools/plugins/systemd-base.yaml
```

Defaults:

```yaml
logs_since: 1h
logs_lines: 200
restart_verify_timeout: 15s
```

`logs_since` and `restart_verify_timeout` must be positive Go durations.
`logs_lines` must be within 1 through 100000. The `service logs` flags may
override `logs_since` and `logs_lines`, but not the restart timeout.

The configuration is strict single-document YAML, limited to 1 MiB, root-owned,
regular, non-symlink, and not group/world-writable. Unknown or duplicate keys
and unsafe metadata produce exit 10. Host `--config`, user configuration, XDG
paths, and environment variables cannot override it.

## What can be changed

Operators can select a validated unit, adjust default journal lookback and line
count, override those two log values per command, and tune how long restart
verification waits. The host policy decides whether restart is authorized, and
the operator decides whether to confirm the reviewed plan.

Maintainers can alter the property set, diagnostics, validation grammar, or
restart lifecycle only through source review, tests, and a new immutable
plugin release.

## Fixed behavior

The systemd property allowlist, unit-name grammar, direct argv, output limits,
250-millisecond verification interval, 50-line failure journal, plan contents,
and active-state success condition are compiled into version 1.0.1.

YAML cannot supply arbitrary systemctl actions, journal filters, executable
paths, environment variables, unit groups, shell fragments, or rollback
commands. The plugin never enables, disables, starts, stops, masks, edits, or
reloads units.

## Safety

Status and logs are diagnostic and do not require root in the manifest.
Operating-system permissions may still restrict journal or unit access.
Restart is operational, requires root and confirmation, and supports host-owned
dry-run planning.

Unit validation plus the `--` option terminator prevents option smuggling.
Direct argv prevents shell interpretation. Plan-digest verification rejects a
stale or altered approval. Output bounds and recursive redaction apply to
success, errors, and restart failure diagnostics.

## Troubleshooting

- If a unit name is rejected, use its exact systemd name without a path,
  whitespace, wildcard, or leading option.
- If status fails, run the read-only query again after verifying that systemd
  knows the unit and that `systemctl` is installed.
- If logs are empty, widen `--since`, increase `--lines`, and verify journal
  retention and caller permissions.
- On exit 10, validate positive durations, `logs_lines`, YAML keys, ownership,
  and permissions.
- If the approved plan digest no longer matches, review the newly generated
  plan; do not reuse the old approval.
- After `restart_failed` or `verify_timeout`, inspect the redacted properties
  and journal in Result data, then use `service status` before deciding on
  further recovery.

## Limitations and TODO

Version 1.0.1 operates on one local unit per invocation, although its published
metadata is not part of the new local-only plugin wave. Dependency-aware
multi-unit orchestration is deferred until deterministic planning,
confirmation scope, rollback limits, and audit correlation are designed.

The plugin does not stream logs, follow journal updates, expose arbitrary
journal selectors, or perform rollback after a failed restart. See the
[systemd roadmap](../../roadmap/systemd-base.md).

## Compatibility and source

This page documents `systemd-base` 1.0.1 and plugin protocol v1. The signed
catalog remains authoritative for installation, minimum host version, asset
size, SHA-256, and release history.

Implementation: [internal/systemd](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/systemd).
