---
schema_version: 1
plugin_id: kafka-base
locale: en
documented_version: 1.0.0
title: Kafka base
summary: Inspect local Kafka packages, processes, service state, safe settings, and storage metadata.
command_paths:
  - kafka status
  - kafka config
  - kafka storage
local_only: true
---
# Kafka base

## Purpose and supported scenarios

`kafka-base` is a read-only local diagnostic for Kafka installations on Debian
and Ubuntu hosts. It correlates fixed package names, Java process metadata,
`kafka.service`, fixed server-properties locations, local log directories, and
the optional Kafka storage-info tool.

Use it to confirm that a local broker installation is present, review a small
allowlist of non-secret operational settings, and inspect the shape of locally
configured storage. Version 1 does not open a broker or controller connection.

## Quick start

```text
ohtools kafka status
ohtools kafka config
ohtools kafka storage
```

The commands have no arguments or plugin flags. They are diagnostic, do not
require root or confirmation, and never mutate broker state.

## Commands

### kafka status

Reads bounded `/var/lib/dpkg/status` data for installed `kafka`,
`confluent-kafka`, and `confluent-server` packages. It scans up to 4096 procfs
entries and identifies only Java processes whose NUL-separated command line
contains the exact class `kafka.Kafka` or `kafka.server.KafkaServer`. Process
arguments are used for classification but are never returned.

The command also requests load, active, and sub state for the fixed
`kafka.service`. It reports installed state, sanitized package versions, sorted
process IDs and counts, and available unit fields.

### kafka config

Reads every present file from the following compiled list:

- `/etc/kafka/server.properties`;
- `/etc/kafka/kraft/server.properties`;
- `/opt/kafka/config/server.properties`;
- `/opt/kafka/config/kraft/server.properties`.

It parses bounded UTF-8 Java properties and returns only valid values for
`node.id`, `broker.id`, `num.partitions`, `default.replication.factor`,
`min.insync.replicas`, `auto.create.topics.enable`, and `process.roles`.
All other keys and values, including listener, authentication, credential, and
remote endpoint settings, are omitted.

### kafka storage

Selects the first present config in the compiled order, strictly reads
`log.dirs` or `log.dir`, and rejects conflicting, duplicate, non-absolute,
sensitive-looking, root, unclean, or repeated paths. It verifies every path
component beneath the local root without following symlinks and counts at most
4096 direct entries in each of at most 16 log directories.

It then tries, in order:

```text
kafka-storage.sh info --config <fixed-config-path>
kafka-storage info --config <fixed-config-path>
```

Only a count of `Found metadata:` records is returned. The storage tool is
optional; directory evidence remains useful when it is unavailable.

## How it works

All commands use the protocol v1 read-only plan. Status combines three local
sources and makes the fixed systemd probe optional. Config accepts only
single-line properties with `=` or `:`, rejects continuations and duplicate
keys, and normalizes only allowlisted booleans, integers, and broker/controller
roles.

Storage reuses the strict properties parser, confines each configured absolute
Unix path below the local probe root, verifies the opened directory has not
changed, and runs an exact storage argv only against a compiled config path.
Results and collections are deterministically sorted.

Important limits are 4 MiB for dpkg status, 512 KiB and 8192 lines per
properties file, 64 KiB per process command line, 256 KiB of storage-info
stdout, 16 log directories, and 4096 entries per directory.

## Data access

The plugin reads `/var/lib/dpkg/status`, bounded `/proc/{pid}/comm` and
`/proc/{pid}/cmdline`, the four fixed Kafka property paths, and configured
local log directories.

It may execute only the fixed `systemctl show` for `kafka.service` and
`kafka-storage.sh` or `kafka-storage` `info --config` for one compiled config
path. The guarded runner rejects stdin, environment overrides, other programs,
and other argv. No shell, broker socket, repository, remote endpoint, or
caller-selected path is used.

## Results and exit behavior

`kafka status` emits `kafka.installation` as pass or info and
`kafka.systemd` as pass or skipped. Missing optional systemd metadata produces a
dependency error without discarding package and process evidence.

`kafka config` emits `kafka.config` and returns file paths, a safe settings map,
and file count. No supported config file, an unsafe file, or malformed
properties use the dependency exit mapping.

`kafka storage` emits `kafka.storage.directories` and
`kafka.storage.info`. Unsafe configuration or storage paths use the dependency
mapping; an unavailable optional storage executable yields a skipped check.
Cancellation and timeout propagate. JSON output is Result schema v1 only.

## Configuration

Version 1 has no `/etc/ohtools/plugins/kafka-base.yaml` and no configurable
probe paths or limits. The inspected Kafka properties are product input, not
plugin configuration.

A minimal local storage setting recognized by the storage diagnostic is:

```properties
log.dirs=/var/lib/kafka
```

Changing Kafka properties can affect the broker. `kafka-base` only observes the
next saved file and never edits or applies it.

## What can be changed

Operators can manage the fixed Kafka package, service, properties files, and
log directories with their normal deployment process. Within those properties,
the safe operational values shown by `kafka config` can change and will be
reported after they pass validation.

The plugin itself exposes no path, package, service, executable, or limit
override. Maintainers require source changes, tests, and a new immutable release
to change those contracts.

## Fixed behavior

Package names, Java class markers, `kafka.service`, property locations,
safe-setting keys and ranges, log-directory rules, storage executables, argv,
and all limits are fixed in 1.0.0.

The plugin never accepts bootstrap servers, topic or group names, credentials,
arbitrary config paths, arbitrary log paths as arguments, or administrative
operations. It does not format storage or interpret raw storage output beyond
the small compiled grammar.

## Safety

All commands are local, read-only, and non-root. External commands use exact
argv and an empty input environment with bounded output. Paths must be clean,
absolute, confined, and non-symlinked. Opened directories are checked against
the path metadata to reduce replacement races.

Raw Java argv, complete properties, storage-tool output, repository or broker
addresses, SASL/JAAS values, keystore and truststore data, and credentials are
not returned. Sensitive-looking storage paths are rejected, and Result data is
recursively redacted by the host.

## Troubleshooting

- If status cannot read package or procfs metadata, check file type, symlinks,
  permissions, size limits, and procfs entry count.
- A skipped systemd check means package and process evidence may still be
  valid; verify the fixed `kafka.service` separately.
- If config fails, check UTF-8, duplicate keys, unsupported continuations,
  malformed assignments, the 512 KiB limit, and at least one fixed path.
- If storage fails, define exactly one of `log.dirs` and `log.dir`, use at most
  16 clean absolute non-root paths, remove symlinks, and check entry limits.
- If storage info is skipped, install a trusted compatible local storage tool;
  no download or remote lookup is attempted.

## Limitations and TODO

Version 1 is deliberately local-only. Broker and controller connections,
cluster metadata, topics, partitions, consumer groups, quorum diagnostics,
administrative mutations, repository downloads, remote execution, additional
layouts, and container attribution are deferred. Any network mode requires
policy-pinned endpoints, TLS and SASL credential isolation, DNS-rebinding and
proxy controls, bounded responses, and explicitly read-only operations.

See the maintained [Kafka roadmap](../../roadmap/kafka-base.md).

## Compatibility and source

This page documents `kafka-base` 1.0.0 and plugin protocol v1. The signed
catalog remains authoritative for installation, minimum host version, asset
size, SHA-256, and release history.

Implementation: [internal/kafka](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/kafka).
