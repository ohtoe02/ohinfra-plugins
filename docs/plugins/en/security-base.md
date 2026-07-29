---
schema_version: 1
plugin_id: security-base
locale: en
documented_version: 1.0.0
title: Local security posture diagnostics
summary: Audit local accounts, OpenSSH settings, and firewall availability without exposing hashes or raw rulesets.
command_paths:
  - security audit
  - security accounts
  - security ssh
  - security firewall
local_only: true
---

## Purpose and supported scenarios

`security-base` collects bounded security posture metadata from the local host.
It identifies duplicate UID 0 accounts and unsafe home-directory modes, checks
selected OpenSSH authentication settings, and reports whether nftables or UFW
state can be inspected.

The plugin is designed for read-only triage. It never returns password hashes,
group passwords, complete SSH configuration, or raw firewall rules. It does not
change accounts, SSH, firewall policy, or contact a remote scanner.

## Quick start

Run the combined audit:

```text
ohtools security audit
```

Inspect one area when narrower exit semantics are useful:

```text
ohtools security accounts
ohtools security ssh
ohtools security firewall
```

Root is not declared as required. Running with greater local privileges may
make shadow lock metadata or firewall probes available, but does not turn any
command into a mutation.

## Commands

### security audit

Runs accounts, SSH, and firewall collectors in order. Each section is preserved
under `data.accounts`, `data.ssh`, or `data.firewall`. Unavailable optional
sources add structured errors and convert warning checks to partial checks; the
aggregate command returns usable partial data unless cancellation or timeout is
fatal.

### security accounts

Reads local passwd, group, and optionally shadow metadata. It returns account
name, UID, GID, login-shell classification, optional locked state, home
presence, and whether an existing home is group- or world-writable. Group
records contain name, GID, and member count.

Checks warn when more than one account has UID 0 or when inspected home
directories have unsafe write bits. Password and group password fields are
never returned. Shadow entries are reduced to a boolean locked state.

### security ssh

Reads allowlisted directives from `sshd_config` and up to 128 regular `.conf`
drop-ins, then attempts the fixed effective-config command
`sshd -T -C user=root,host=localhost,addr=127.0.0.1`. Effective directives take
precedence for checks when available.

Only authentication and posture keys such as `PermitRootLogin`,
`PasswordAuthentication`, `PubkeyAuthentication`, `UsePAM`, and bounded related
settings are retained. Checks warn for root login set to `yes` or password
authentication set to `yes`.

### security firewall

Runs `nft list ruleset` and `ufw status` independently. Successful output is
reduced to availability and line count; UFW also reports an `active` boolean.
The raw ruleset and command diagnostics are not returned. A pass check means at
least one of the two local sources was usable, not that firewall policy is
correct or complete.

## How it works

The collector sequence is deterministic:

1. Confined readers load bounded identity or SSH files without following
   symlinks.
2. Parsers validate record shape and retain only safe metadata.
3. Account checks derive duplicate UID 0 and home-mode findings.
4. The local `sshd`, `nft`, and `ufw` executables are invoked directly with
   fixed argv and bounded output.
5. Errors are classified as dependency, privilege, or general without copying
   raw command output.
6. Platform ID, version, and supported state are added when `/etc/os-release`
   can be detected.
7. Result building recursively redacts data, checks, and errors.

Malformed identity rows are skipped with structured errors. An unavailable
shadow file does not prevent account inventory. A narrow command requires at
least one usable source, while the aggregate audit is intentionally resilient.

## Data access

The plugin may read:

- `/etc/passwd`, `/etc/group`, and `/etc/shadow`, each limited to 256 KiB;
- account home-directory metadata under the authoritative local root;
- `/etc/ssh/sshd_config`;
- regular `/etc/ssh/sshd_config.d/*.conf` files, each limited to 256 KiB;
- `/etc/os-release` for platform metadata.

It may run local `sshd`, `nft`, and `ufw`. It does not read private SSH keys,
return hash fields, execute a shell, or make network requests.

## Results and exit behavior

Every command returns Result schema v1 with empty arrays normalized. Findings
normally use pass or warning. Missing optional data adds structured errors and
can produce partial status.

Narrow commands use exit 4 if no required source is usable. `security firewall`
uses exit 3 when all probes are denied for privilege reasons. Cancellation and
timeout propagate as fatal context failures. Invalid command paths, arguments,
or options use exit 2. The aggregate audit preserves partial local evidence
instead of failing solely because an optional executable or protected file is
unavailable.

## Configuration

Version 1 has no plugin YAML configuration. File paths, directive allowlists,
identity limits, drop-in count, command argv, and checks are compiled. The
commands accept no plugin arguments or flags.

Operators can still use standard host output, JSON, timeout, policy, and audit
controls. Those controls do not expand which protected data the plugin emits.

## What can be changed

An operator can change the underlying account, home-directory, OpenSSH, and
firewall state through the normal operating-system administration process and
rerun the audit. Running under an authorized identity can make protected local
metadata readable.

The directive allowlist, findings, limits, supported source set, and output
shape can change only in reviewed source with tests and a new immutable release.

## Fixed behavior

The plugin always minimizes data: hashes, group passwords, raw firewall rules,
unallowlisted SSH directives, and external-command diagnostics are excluded.
UID 0 duplication, home write bits, root-login `yes`, and password-authentication
`yes` are the compiled v1 findings.

No arbitrary file path, username, firewall command, SSH match context, remote
host, URL, credential, or scanner endpoint can be supplied.

## Safety

All manifest commands are diagnostic and declare no mutation, force,
confirmation, dry-run, or root requirement. Local readers reject symlinks and
oversized files. Home paths are confined under the selected root and symlink
homes are not treated as present.

Programs are resolved to trusted absolute paths and executed directly. Output
is bounded and recursively redacted. Permission-denied diagnostics are
classified without including their potentially sensitive original text.

## Troubleshooting

- Exit 4 from `security accounts` usually means `/etc/passwd` is missing,
  unreadable, symlinked, or over 256 KiB.
- `shadow_accessible: false` means lock-state metadata was unavailable; account
  names and safe passwd metadata may still be valid.
- If SSH results lack effective directives, verify that `sshd` is installed and
  can run the fixed `-T` query. The file-based allowlist may still be usable.
- Unsafe or excessive SSH drop-ins are reported rather than followed.
- Exit 3 from `security firewall` means every available firewall probe was
  privilege-denied. Rerun only under the project's privilege policy.
- A firewall pass confirms an inspectable source, not active rules or policy
  compliance; inspect the returned UFW active flag and other controls.

## Limitations and TODO

Version 1 is local-only. Remote scanners, reputation services, fleet-wide
comparison, remediation, remote SSH queries, and firewall rule interpretation
are TODO. Any remote extension requires endpoint policy, host-owned credentials,
data minimization, response limits, and credential isolation.

See the [security-base roadmap](../../roadmap/security-base.md).

## Compatibility and source

This page documents `security-base` 1.0.0 and plugin protocol v1. The signed
catalog remains authoritative for host compatibility and release metadata.

Implementation: [internal/security](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/security).
