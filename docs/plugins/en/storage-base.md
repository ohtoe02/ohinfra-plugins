---
schema_version: 1
plugin_id: storage-base
locale: en
documented_version: 1.0.1
title: Storage base
summary: Report local filesystem capacity, inode usage, and configured pressure thresholds.
command_paths:
  - disk usage
local_only: false
---
# Storage base

## Purpose and supported scenarios

`storage-base` provides a read-only view of mounted local filesystems. It can
report every non-pseudo mount or select the filesystem that contains one
operator-supplied path. Capacity, available space, utilization, and inode
counters are normalized into Result v1.

Use it to locate full filesystems, monitor disk thresholds, or identify the
mount backing an application path. It does not inspect individual files,
calculate directory sizes, clean data, resize volumes, or manage LVM and RAID.

## Quick start

Report every eligible mounted filesystem:

```text
ohtools disk usage
```

Report only the filesystem containing a path:

```text
ohtools disk usage /var/lib/docker/containers
```

Produce machine-readable output:

```text
ohtools --json disk usage /var
```

At most one path is accepted.

## Commands

### disk usage

Without an argument, parses the current process mount table, removes known
pseudo filesystems, deduplicates mount points, calls `statfs` for each mount,
and sorts output by mount point.

With a path, resolves symlinks first and selects the deepest mount-point prefix
containing the resolved path. The result still represents the filesystem, not
the requested directory. A nonexistent path or a path outside the parsed mount
table is an argument/collection failure rather than a fabricated zero value.

Each filesystem produces a `disk:MOUNT` check. A configured warning or
critical percentage affects both the check and overall Result status.

## How it works

The collector reads `/proc/self/mountinfo` and strictly parses the separator,
filesystem type, source, and escaped mount-point fields. Linux mount escapes
for spaces, tabs, newlines, and backslashes are decoded.

Pseudo filesystem types such as `proc`, `sysfs`, `tmpfs`, `devtmpfs`,
`cgroup`, `cgroup2`, `bpf`, and `securityfs` are excluded. Duplicate mount
points are ignored. For a requested path, longest-prefix selection prevents a
parent mount from hiding a nested mount.

Linux `statfs` counters are converted to total, used, and available bytes.
Used percent is based on used blocks divided by used plus blocks available to
an unprivileged process, and is bounded to 0 through 100. Total and used inode
counts are also included.

## Data access

The plugin reads:

- `/proc/self/mountinfo`;
- metadata required to resolve the optional requested path;
- filesystem statistics for each selected mount through the Linux `statfs`
  system call;
- `/etc/ohtools/plugins/storage-base.yaml`.

It does not execute external commands, invoke a shell, traverse directory
contents, read file payloads, or contact a network service.

## Results and exit behavior

The Result v1 `data.filesystems` array contains `source`, `mount_point`, `type`,
`total_bytes`, `used_bytes`, `available_bytes`, `used_percent`,
`total_inodes`, and `used_inodes`. A failed `statfs` item additionally contains
an error and receives a skipped check plus structured `statfs_failed` error.

Overall status is `critical` if a filesystem reaches the critical threshold,
`warning` if it reaches only warning, `partial` if collection for an item
failed without a critical item, otherwise `pass`. Checks and filesystems are
ordered deterministically by mount point.

Too many arguments use exit 2. Strict configuration failures use exit 10.
Cancellation is checked before collection. A failure to read/parse the mount
table, resolve the requested path, or select a containing mount is returned as
a command failure and does not emit misleading capacity data.

## Configuration

The optional system configuration is:

```text
/etc/ohtools/plugins/storage-base.yaml
```

Defaults:

```yaml
disk_warning: 80
disk_critical: 90
```

Both percentages must lie within 0 through 100 and `disk_warning` must be
strictly lower than `disk_critical`.

The configuration is strict single-document YAML, limited to 1 MiB, root-owned,
regular, non-symlink, and not group/world-writable. Unknown or duplicate keys
and invalid trust metadata produce configuration exit 10. User configuration,
host `--config`, XDG paths, and environment variables are ignored.

## What can be changed

Operators can change the warning and critical percentages and can select one
existing path per invocation. Monitoring systems can choose whether to consume
human rendering or Result v1 JSON.

Host policy and the caller's filesystem permissions determine which paths and
mounts are observable. Maintainers can add collectors or storage-stack support
only in a tested new plugin release.

## Fixed behavior

The mount table path, pseudo-filesystem exclusion set, percent formula, inode
calculation, symlink resolution, longest-prefix selection, check IDs, and sort
order are compiled into version 1.0.1.

YAML cannot define additional mount tables, ignore patterns, remote nodes,
external utilities, cleanup commands, or arbitrary filesystem paths. No
mutation, dry-run plan, confirmation, or storage repair is implemented.

## Safety

The manifest declares one diagnostic command that does not require root,
confirmation, force, or dry-run. Collection uses a fixed procfs file and the
`statfs` system call; no shell or subprocess is involved.

The optional path is resolved by the operating system and used only to select a
mount. The plugin does not enumerate or modify the contents below that path.
Collection errors remain explicit instead of being treated as healthy zero
usage.

## Troubleshooting

- If the mount table cannot be read, verify that procfs is mounted and
  `/proc/self/mountinfo` is accessible in the current namespace.
- If a requested path cannot be resolved, supply an existing path and verify
  permissions on its parent components.
- If no filesystem contains the path, inspect namespace/mount visibility rather
  than relying on the host's mount list.
- A skipped `disk:MOUNT` check means `statfs` failed for that mount; inspect
  the associated structured error.
- If a filesystem expected in output is absent, confirm it is not one of the
  compiled pseudo filesystem types.
- On exit 10, check threshold ordering, YAML keys, root ownership, and file
  permissions.

## Limitations and TODO

The command currently observes only the local mount namespace, although the
published metadata is not part of the new local-only plugin wave. Remote
collection requires an authenticated host identity and bounded transport
design.

LVM topology, mdraid state, SMART/NVMe health, thin-pool pressure, quotas,
directory sizing, and cleanup are deferred. Optional adapters must use fixed
executables, bounded output, explicit privilege rules, and partial status when
the storage stack is absent. See the
[storage roadmap](../../roadmap/storage-base.md).

## Compatibility and source

This page documents `storage-base` 1.0.1 and plugin protocol v1. The signed
catalog remains authoritative for installation, minimum host version, asset
size, SHA-256, and release history.

Implementation: [internal/storage](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/storage).
