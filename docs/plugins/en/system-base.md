---
schema_version: 1
plugin_id: system-base
locale: en
documented_version: 1.0.1
title: System base
summary: Inspect local operating-system identity and evaluate host health from bounded system probes.
command_paths:
  - system info
  - system health
local_only: false
---
# System base

## Purpose and supported scenarios

`system-base` reports a normalized snapshot of the local Linux host and performs
read-only health checks for operating-system support, memory and filesystem
pressure, clock synchronization, required reboot state, failed systemd units,
and recent out-of-memory events.

Use it for host inventory, first-response diagnostics, and a deterministic
health summary. It does not change the host, restart services, clean storage, or
remediate a failing check.

## Quick start

Collect identity and capacity information:

```text
ohtools system info
```

Run the configured health policy:

```text
ohtools system health
```

Use Result v1 JSON for automation:

```text
ohtools --json system health
```

Neither command accepts positional arguments or command-specific flags.

## Commands

### system info

Collects the operating-system ID, release, kernel, architecture, hostname,
uptime, CPU model and logical-core count, memory and swap totals, load averages,
virtualization hint, timezone, NTP synchronization state, and reboot-required
marker. This command uses compiled collectors and does not load
`system-base.yaml`.

Individual probe failures do not discard the remaining snapshot. The result is
`partial` and contains structured errors for unavailable fields.

### system health

Loads and validates the health thresholds, collects the same system snapshot,
and obtains filesystem observations through the storage collector. It emits
checks named `os`, `memory`, `load`, `disk:MOUNT`, `time-sync`,
`reboot-required`, `failed-units`, and `oom`.

The operating-system check in version 1.0.1 passes only for Debian 12 and
Debian 13. Other detected systems receive a critical `os` check; this check is
independent from the wider host packaging support matrix.

## How it works

The collector performs independent local probes so one unavailable source can
produce a partial result without hiding successful observations:

1. Parse identity from `/etc/os-release` and the kernel release from procfs.
2. Read hostname, uptime, CPU, memory, swap, and load data.
3. Detect containers or hypervisors from local marker files, cgroups, DMI, and
   finally `systemd-detect-virt` when available.
4. Resolve the timezone from `/etc/timezone`, `/etc/localtime`, or the process
   clock and read the reboot-required marker.
5. Ask `timedatectl` whether NTP is synchronized.
6. For health, enumerate real mounted filesystems and evaluate configured disk
   and memory percentages.
7. Query failed systemd units and search the kernel journal for OOM messages
   inside the configured window.

External programs are resolved through the trusted system resolver and invoked
directly with fixed argv. Each external stdout and stderr stream is limited to
1 MiB.

## Data access

The plugin reads these local sources:

- `/etc/os-release`, `/etc/timezone`, and `/etc/localtime`;
- `/proc/sys/kernel/osrelease`, `/proc/uptime`, `/proc/cpuinfo`,
  `/proc/meminfo`, `/proc/loadavg`, `/proc/1/cgroup`, and
  `/proc/self/mountinfo`;
- `/.dockerenv`, `/run/.containerenv`, DMI product information, and
  `/var/run/reboot-required`;
- filesystem capacity and inode counters through `statfs`;
- `/etc/ohtools/plugins/system-base.yaml` for `system health` only.

It may execute `systemd-detect-virt`, `timedatectl`, `systemctl`, and
`journalctl`. The commands are read-only and never use a shell or `sudo`.

## Results and exit behavior

Both commands return normalized Result schema v1 with tool identity, host,
timestamp, duration, and always-present checks, changes, and errors arrays.
`system info` places the snapshot in `data.system`. `system health` also places
filesystem observations in `data.filesystems`.

Health aggregation uses `critical` when any check is critical, then `partial`
when probes failed or checks were skipped, then `warning`, otherwise `pass`.
The load check is informational and is not compared with a threshold. Recent
OOM lines are critical; failed units, an unsynchronized clock, and a required
reboot are warnings.

Invalid arguments use exit 2. Invalid plugin configuration uses exit 10.
Result status is mapped by the host to the stable warning, critical, partial,
timeout, and cancellation exits. Missing optional diagnostic executables are
reported as structured dependency failures instead of panics.

## Configuration

The optional system configuration is:

```text
/etc/ohtools/plugins/system-base.yaml
```

Defaults:

```yaml
disk_warning: 80
disk_critical: 90
memory_warning: 85
memory_critical: 95
oom_window: 24h
```

Warning and critical percentages must be within 0 through 100, with warning
strictly lower than critical. `oom_window` must be a positive Go duration such
as `30m`, `24h`, or `168h`.

The file is limited to 1 MiB and uses strict single-document YAML. Unknown or
duplicate keys, a symlink, a non-regular file, a non-root owner, or
group/world-writable permissions produce exit 10. Host `--config`, user files,
XDG paths, and environment variables do not override this path.

## What can be changed

Operators may tune disk and memory warning/critical percentages and the recent
OOM search window. Lower thresholds make the health result more sensitive;
longer OOM windows increase the amount of journal history inspected.

Host policy can restrict whether the commands may run and can impose an overall
timeout. Maintainers may change collectors or checks only through reviewed
source, tests, and a new immutable plugin release.

## Fixed behavior

The data sources, check IDs, health aggregation order, filesystem discovery,
virtualization heuristics, and supported-OS health rule are compiled into
version 1.0.1. YAML cannot add arbitrary paths, commands, health checks,
remote hosts, or shell fragments.

The commands are diagnostic only. There is no plan or execute mutation phase,
confirmation prompt, cleanup action, or configurable load-average threshold.

## Safety

The manifest marks both commands diagnostic, non-root, without confirmation,
force, or dry-run support because no mutation is available. Fixed executable
names and argv are passed without a shell. Output from external commands is
bounded, and plugin errors are returned through Result v1.

The plugin does not invoke `sudo`, accept credentials, or intentionally read
secret-bearing configuration. Normal operating-system permissions still apply
to journal and systemd observations; inaccessible probes become partial or
skipped results.

## Troubleshooting

- If `system info` is partial, inspect `errors` for the exact unavailable file
  or executable and verify the corresponding procfs, systemd, or timezone
  source.
- If `os` is critical on a host otherwise supported by ohtools packaging,
  remember that version 1.0.1 health classification passes only Debian 12/13.
- If memory is skipped, verify that `/proc/meminfo` contains `MemTotal` and
  `MemAvailable`.
- If disk checks are skipped, check `/proc/self/mountinfo` and permissions for
  the affected mount.
- If `time-sync`, `failed-units`, or `oom` is skipped, restore the relevant
  systemd executable and journal access.
- On exit 10, validate threshold ordering, the positive duration, YAML keys,
  ownership, and permissions.

## Limitations and TODO

Current collection is local-only even though the published documentation
classification is not restricted to the new local-only plugin wave. Remote
host collection is deferred until authenticated transport, host identity,
credential isolation, endpoint pinning, and bounded response rules are
designed.

Version 1.0.1 does not assess CPU saturation, load relative to core count,
network reachability, package updates, or application-specific health. See the
[system roadmap](../../roadmap/system-base.md) for the maintained backlog.

## Compatibility and source

This page documents `system-base` 1.0.1 and plugin protocol v1. The signed
catalog remains authoritative for installation, minimum host version, asset
size, SHA-256, and release history.

Implementation: [internal/system](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/system).
