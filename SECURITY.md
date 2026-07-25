# Security policy

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub private
vulnerability reporting and include the affected plugin, version, reproduction
steps, and expected impact.

## Execution model

Plugins never invoke a shell. External programs are resolved only from
`/usr/sbin`, `/usr/bin`, `/sbin`, and `/bin`, executed with explicit argv, a
bounded environment and output, a context deadline, and process-group cleanup.

Operational commands expose a deterministic plan before execution. The host
owns policy, privilege checks, audit, confirmation, dry-run, timeout, and final
redaction. A plugin verifies the digest of the approved plan before changing
the system.

Plugin configuration is optional and contains no secrets. On Linux it must be
a root-owned regular file, must not be a symlink, and must not be group- or
world-writable. Secret values belong in separate root-owned files and are
referenced only by plugins that explicitly document such a setting.

Only versions present as non-yanked entries in the signed ohtools plugin
catalog are supported.
