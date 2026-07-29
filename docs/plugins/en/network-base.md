---
schema_version: 1
plugin_id: network-base
locale: en
documented_version: 1.0.0
title: Network and local TLS diagnostics
summary: Inspect local interfaces, routes, listeners, and certificate files without opening network connections.
command_paths:
  - network overview
  - network interfaces
  - network routes
  - network listeners
  - tls inspect
local_only: true
---

## Purpose and supported scenarios

`network-base` provides a read-only view of networking visible in the plugin's
local operating-system namespace. It inventories interfaces, kernel routes, and
listening TCP or UDP sockets. It can also parse a local PEM or DER certificate
file and report bounded public certificate metadata.

Version 1 is useful for host triage and offline certificate expiry checks. It
does not connect to an address, resolve DNS, test reachability, inspect remote
TLS handshakes, or modify network configuration.

## Quick start

Collect every local network section independently:

```text
ohtools network overview
```

Request a narrower inventory:

```text
ohtools network interfaces
ohtools network routes
ohtools network listeners
```

Inspect certificates already stored in a regular local file:

```text
ohtools tls inspect /etc/ssl/certs/example.pem
```

## Commands

### network overview

Runs the interface, route, and listener collectors independently. Successful
sections appear under `data.interfaces`, `data.routes`, and `data.listeners`.
An unavailable section produces a skipped check and a structured dependency
error while the other usable sections remain in a partial Result.

### network interfaces

Reads counters from procfs first and enriches interface state and hardware
address from sysfs. If procfs cannot be parsed, it runs the fixed command
`ip -j address show`. The result is sorted by interface name; addresses are
sorted by family and value. The procfs-only path cannot supply IP addresses.

### network routes

Reads the local kernel IPv4 route table from procfs first. If that source is
unavailable or malformed, it runs `ip -j route show table all`. Returned
records contain bounded family, destination, optional gateway and interface,
and metric fields in deterministic order.

### network listeners

Parses local `tcp`, `tcp6`, `udp`, and `udp6` procfs socket tables. Only
listening TCP sockets and unconnected UDP sockets are returned. If no procfs
socket source is usable, the fallback is `ss -H -l -n -t -u`. The command does
not resolve service names or remote peers.

### tls inspect

Requires one clean absolute path. The path must not begin with an option, contain
control characters, use a symlink, or identify anything except a regular file.
The file may contain a DER certificate or PEM certificate blocks and must be at
most 1 MiB. Any private-key PEM block causes the entire input to be rejected.

For each certificate the command returns subject, issuer, serial number,
validity timestamps, sorted DNS names, CA flag, and SHA-256 of the DER bytes.
The check is warning when the certificate is expired or not valid yet and pass
otherwise. It does not validate a chain, trust store, revocation, key usage, or
hostname.

## How it works

All collectors use confined local reads and bounded parsers:

1. Interface counters come from `/proc/net/dev`, with state and MAC enrichment
   from `/sys/class/net`.
2. Routes come from `/proc/net/route`.
3. Listeners come from `/proc/net/tcp`, `tcp6`, `udp`, and `udp6`.
4. The process-specific `/proc/PID/net` form is tried when the common procfs
   path is unavailable.
5. Fixed `ip` or `ss` argv is used only as a local fallback.
6. Parsed values are validated, recursively redacted, and sorted before Result
   v1 is built.

Procfs reads and fallback stdout are limited to 1 MiB; fallback stderr is
limited to 64 KiB. Truncated, malformed, or nonzero command output is rejected.
TLS inspection compares file metadata before and after opening to detect a file
replacement race.

## Data access

The plugin may read:

- `/proc/net/dev`, `/proc/net/route`, and the four local socket tables;
- the equivalent `/proc/PID/net` files for its own process;
- `/sys/class/net/NAME/operstate` and `/sys/class/net/NAME/address`;
- the absolute certificate file supplied to `tls inspect`.

It may resolve and invoke the local `ip` and `ss` executables with fixed argv.
It never invokes a shell and never sends network traffic.

## Results and exit behavior

All commands return Result schema v1 with normalized `checks`, `changes`, and
`errors` arrays. Narrow network commands return exit 4 when their required
local source and fallback executable are unusable. `network overview` instead
preserves successful sections and represents unavailable sections as skipped
checks plus dependency errors, normally producing partial status.

`tls inspect` uses exit 2 for an invalid path, unsafe file, oversized input,
private-key material, or malformed certificate. Cancellation and timeout remain
fatal context errors. In JSON mode stdout contains only the Result document.

## Configuration

Version 1 has no operator YAML settings. The data sources, byte limits,
fallback commands, sorting rules, and TLS validation behavior are compiled
into the plugin. Host global timeout, output, JSON, and policy behavior still
applies.

The only command-specific input is the certificate path passed to
`tls inspect`; network inventory commands accept no arguments or plugin flags.

## What can be changed

An operator can choose a narrower inventory command, select which local
certificate file to inspect, and change the host state being observed. Standard
host controls can change timeout and output presentation.

Maintainers can add sources, change validation rules, or expose configuration
only through reviewed code, adversarial tests, and a new immutable plugin
release.

## Fixed behavior

The procfs and sysfs paths, the `ip` and `ss` argv, all size limits, listener
classification, redaction, and deterministic sorting are fixed in 1.0.0. No
interface, namespace, route table, remote host, URL, DNS name, proxy, or
credential can be supplied by plugin configuration.

Certificate inspection is deliberately syntactic and time-based. A pass result
must not be interpreted as proof that a certificate is trusted for a service.

## Safety

Every manifest command is diagnostic, read-only, and declares no root,
confirmation, force, or dry-run requirement. Confined readers reject traversal
and unsafe symlink paths. External tools are resolved as trusted absolute
executables and called directly with fixed arguments and bounded output.

Certificate subjects, issuers, DNS names, interface values, routes, listeners,
and errors pass through redaction. The parser never returns or accepts private
key material as certificate input.

## Troubleshooting

- If an inventory command returns exit 4, verify procfs is mounted and readable
  and that the distribution's trusted `ip` or `ss` executable is installed.
- If overview is partial, inspect its structured errors to identify the one
  unavailable collector; the remaining sections are still usable.
- Empty address lists can be expected when `/proc/net/dev` succeeds because
  that file has counters but no address records.
- For TLS path errors, use a canonical absolute regular-file path with no
  symlink component and keep the file below 1 MiB.
- Remove private keys and trailing non-PEM text from certificate bundles.
- An expired warning uses the host clock; correct local time before treating it
  as a certificate defect.

## Limitations and TODO

Version 1 is local-only. Remote reachability, DNS, authenticated probes, remote
TLS handshakes, certificate-chain and hostname verification, proxy handling,
and remote endpoint credentials are TODO. Future network behavior requires
endpoint allowlists, DNS-rebinding protection, redirect policy, bounded
timeouts, and host-owned credential references.

Complete address enrichment without `ip` is also deferred. See the
[network-base roadmap](../../roadmap/network-base.md).

## Compatibility and source

This page documents `network-base` 1.0.0 and plugin protocol v1. The signed
catalog remains authoritative for host compatibility and release metadata.

Implementation: [internal/network](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/network).
