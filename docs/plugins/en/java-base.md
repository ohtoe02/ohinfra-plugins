---
schema_version: 1
plugin_id: java-base
locale: en
documented_version: 1.0.0
title: Local Java runtime and process diagnostics
summary: Inspect the local Java runtime, bounded procfs process metadata, and allowlisted read-only jcmd operations.
command_paths:
  - java runtime
  - java processes
  - java inspect
local_only: true
---

## Purpose and supported scenarios

`java-base` reports the installed local Java runtime, inventories Java processes
visible in the plugin's procfs namespace, and performs bounded read-only
inspection of one local JVM through three fixed `jcmd` operations.

It is intended for local triage while minimizing secrets in command lines and
system properties. Version 1 does not use JMX, connect to a service, enter
container namespaces, capture a heap/thread/JFR artifact, or run an arbitrary
diagnostic command.

## Quick start

Show the default local runtime:

```text
ohtools java runtime
```

List visible Java processes:

```text
ohtools java processes
```

Inspect one positive decimal process ID:

```text
ohtools java inspect 1234
```

`java inspect` may require the same operating-system identity as the target JVM
or another privilege explicitly permitted by local policy.

## Commands

### java runtime

Runs `java -version` and accepts the common first line beginning with
`java version` or `openjdk version`. It returns the parsed version and, when
present, bounded runtime and VM description lines. Both stdout and stderr are
considered because Java commonly writes version information to stderr.

Malformed, non-UTF-8, truncated, nonzero, or missing executable output is
rejected rather than returned as opaque text.

### java processes

Enumerates at most 32,768 safe numeric directories under local `/proc`. A
process is classified as Java when `comm` or the basename of argv zero is
exactly `java` or `javaw`. Returned records contain PID, executable name, and
the NUL-separated command line.

Command-line values are recursively redacted. Sensitive options containing
password, token, secret, credential, authorization, API/access/private keys,
and equivalent normalized markers have their value masked. The next argument
is also masked for split sensitive options, and URL userinfo is removed.

Unsafe procfs entries, an excessive process list, or per-process read failure
adds a structured error while preserving other processes.

### java inspect

Accepts one decimal PID within `1..4194304`, then runs exactly:

```text
jcmd PID VM.version
jcmd PID VM.command_line
jcmd PID VM.system_properties
```

It verifies that every response starts with the requested PID header. The
result contains version text, redacted command-line records, and sorted system
properties. Sensitive property names have masked values; other values still
pass URL and recursive secret redaction.

The command does not execute an arbitrary `jcmd` operation or produce an
artifact.

## How it works

The plugin follows bounded deterministic paths:

1. Validate command shape and, for inspect, the decimal PID before execution.
2. Resolve local executables through the trusted resolver and invoke fixed argv
   directly.
3. For inventory, validate the selected root and procfs directory, reject
   symlink entries, sort PIDs, and read `comm` plus `cmdline`.
4. For inspection, call three allowlisted operations in order and reject a
   failed, truncated, malformed, mismatched-PID, or non-UTF-8 response.
5. Redact command-line and property values before stable sorting and Result v1
   construction.

Runtime output is limited to 64 KiB per stream. Procfs `comm` is limited to 256
bytes and `cmdline` to 64 KiB. Each `jcmd` stdout is limited to 256 KiB and
stderr to 64 KiB.

## Data access

The plugin may read:

- the local `/proc` directory;
- `/proc/PID/comm`;
- `/proc/PID/cmdline`.

It may execute trusted local `java` and `jcmd` with the fixed arguments above.
`jcmd` itself attaches to the selected local JVM using operating-system
facilities; the plugin does not open a network connection or read JVM
credentials.

## Results and exit behavior

All commands return Result schema v1 with normalized arrays. Runtime and
successful inspection use pass. Process inventory uses pass when Java processes
are found and info when none are found. Partial procfs issues are structured
errors alongside the safe inventory.

Missing `java`, inaccessible procfs, or missing `jcmd` uses exit 4. Invalid PID
or invocation uses exit 2. An attach permission denial uses exit 3. Other safe
inspection failures use exit 1. Cancellation and timeout propagate without
being converted to dependency failures.

## Configuration

Version 1 has no plugin YAML settings. Runtime executable names, procfs limits,
PID range, Java classification, sensitive-key markers, `jcmd` operations, and
output limits are compiled.

The only plugin-specific input is the PID for `java inspect`. Standard host
timeout, output, JSON, policy, audit, and redaction controls still apply.

## What can be changed

Operators can select the installed default `java` through normal trusted system
administration, choose which visible PID to inspect, and run under an identity
authorized to attach to that JVM. They can change the observed JVM command line
or properties by changing the service outside this read-only plugin.

New operation types, detection rules, limits, or configuration require reviewed
source, adversarial tests, and a new immutable release.

## Fixed behavior

Only executables named exactly `java` or `javaw` are inventoried. The PID range,
procfs namespace, three `jcmd` operations, parsing rules, limits, sorting, and
secret markers are fixed in 1.0.0.

No JVM option, arbitrary command, namespace, container, endpoint, URL,
credential, output path, or diagnostic artifact can be requested.

## Safety

All manifest commands are diagnostic and declare no mutation, confirmation,
force, dry-run, or root requirement. PID validation prevents option smuggling.
Procfs root and entries must be absolute, clean, non-symlink directories, and
all reads are size-bounded.

Executables are trusted and invoked without a shell. Output is strict UTF-8 and
bounded. Sensitive Java option and property values, URL userinfo, structured
errors, and the final Result pass through redaction.

## Troubleshooting

- Exit 4 from runtime means the trusted `java` executable or valid bounded
  version output is unavailable.
- Exit 4 from processes means procfs is missing, unsafe, or inaccessible in the
  plugin's namespace. An empty successful list is not an error.
- If some PIDs disappear during enumeration, structured errors may be present;
  process exit races are otherwise skipped safely.
- Exit 2 inspect means the PID is not a clean decimal in the supported range.
- Exit 3 means `jcmd` found the JVM but attach was denied; use an authorized
  identity rather than weakening file or process permissions.
- Exit 1 can mean the process exited, is not attachable, returned a different
  PID header, malformed output, or exceeded output limits.

## Limitations and TODO

Version 1 is local-only. Remote JMX, service endpoints, credentials, TLS,
container namespace discovery, heap dumps, thread dumps, JFR, and arbitrary
diagnostic commands are TODO. They require endpoint allowlists, TLS identity,
credential isolation, PID-reuse protection, approved operation policy, output
budgets, private artifact lifecycle, confirmation, and timeout cleanup.

See the [java-base roadmap](../../roadmap/java-base.md).

## Compatibility and source

This page documents `java-base` 1.0.0 and plugin protocol v1. The signed catalog
remains authoritative for host compatibility and release metadata.

Implementation: [internal/java](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/java).
