---
schema_version: 1
plugin_id: monitoring-base
locale: en
documented_version: 1.0.0
title: Monitoring base
summary: Inventory supported local monitoring products and inspect bounded service, listener, process, and config metadata.
command_paths:
  - monitoring status
  - monitoring inventory
  - monitoring config
local_only: true
---
# Monitoring base

## Purpose and supported scenarios

`monitoring-base` is a deterministic read-only inventory for six local
monitoring products: Prometheus, Prometheus node_exporter, Grafana Alloy,
Grafana Agent, Telegraf, and Zabbix Agent. It correlates fixed Debian package
names, systemd units, process names, conventional listener ports, and config
paths.

Use it to answer which supported agents are installed, whether conventional
local evidence is present, and whether fixed config files have a safe bounded
structural shape. Version 1 does not scrape metrics, call dashboards or remote
APIs, follow targets, or return raw configuration.

## Quick start

```text
ohtools monitoring status
ohtools monitoring inventory
ohtools monitoring config
```

All commands are argument-free diagnostics. They do not require root,
confirmation, force, or dry-run.

## Commands

### monitoring status

Runs both inventory and config collection and returns the combined `products`
and `configs` arrays. Product presence checks are preserved if optional package,
process, listener, unit, or config probes are incomplete. Unsafe or malformed
config metadata adds product-specific partial checks and structured
configuration errors.

### monitoring inventory

For each compiled product, reports product ID and name, installed state, one
matched package and safe version, unit active/sub state, process count,
conventional listening ports seen in the local socket table, and whether one
fixed config path is present.

Installed means at least one local signal exists: an allowlisted package,
matching process, loaded fixed unit, or readable fixed config. A conventional
port is only listener evidence; version 1 does not attribute that socket to a
specific process.

### monitoring config

Reads every present fixed config up to 256 KiB and returns only product ID,
path, format, size, and structural validity. It recognizes bounded YAML-like,
TOML-assignment, environment-assignment, properties-assignment, and balanced
Alloy-like shapes. It does not return fields or values and does not claim full
product-semantic validation.

## How it works

The compiled product matrix is:

- Alloy: package and process `alloy`, `alloy.service`, port 12345,
  `/etc/alloy/config.alloy`;
- Grafana Agent: `grafana-agent`, `grafana-agent.service`, port 12345,
  `/etc/grafana-agent.yaml`;
- node_exporter: package `prometheus-node-exporter`, unit
  `prometheus-node-exporter.service`, process names `node_exporter` and
  `prometheus-node`, port 9100, `/etc/default/prometheus-node-exporter`;
- Prometheus: `prometheus`, `prometheus.service`, port 9090,
  `/etc/prometheus/prometheus.yml`;
- Telegraf: `telegraf`, `telegraf.service`, port 9273,
  `/etc/telegraf/telegraf.conf`;
- Zabbix Agent: packages `zabbix-agent` or `zabbix-agent2`, matching two fixed
  units and process names, port 10050, and two fixed config paths.

Inventory reads a bounded dpkg database, securely opens a procfs directory and
counts exact process names, parses fixed `systemctl show` fields, and parses
`ss -H -lntu`. Data is emitted in compiled product order with one deterministic
check per product.

## Data access

The plugin reads `/var/lib/dpkg/status`, bounded `/proc/{pid}/comm`, and these
fixed config locations:

```text
/etc/alloy/config.alloy
/etc/grafana-agent.yaml
/etc/default/prometheus-node-exporter
/etc/prometheus/prometheus.yml
/etc/telegraf/telegraf.conf
/etc/zabbix/zabbix_agent2.conf
/etc/zabbix/zabbix_agentd.conf
```

It invokes exact `systemctl show` argv for compiled units and
`ss -H -lntu`. Dpkg input is limited to 4 MiB, procfs to 4096 entries and
256 bytes per name, unit output to 32 KiB, listener output to 128 KiB, stderr
to 16 KiB, and each config to 256 KiB. No shell, metrics request, config
include, product validator, or caller-selected input is used.

## Results and exit behavior

Inventory and status emit checks named
`monitoring.{product}.present` or `monitoring.{product}.missing`. Missing
products are informational. Optional source failures create dependency or
configuration errors while other product evidence remains usable.

Config emits `monitoring.config` as pass, info when no fixed files exist, or
partial when at least one present file is unsafe or structurally malformed.

A fatal inability to build the local inventory uses the dependency exit
mapping. Unsupported invocation uses arguments. Context cancellation and
timeout propagate. Result schema v1 is always used, and no raw command or
config output is copied into JSON.

## Configuration

Version 1 has no `/etc/ohtools/plugins/monitoring-base.yaml`. Products, paths,
ports, process names, formats, and limits are compiled.

The files under the fixed product paths are observed inputs. Their normal
product configuration remains under the operator's deployment process.
`monitoring config` checks only a conservative structural shape; a `valid:
true` value is not a substitute for the product's own semantic validation.

## What can be changed

Operators can install or remove supported packages, manage fixed systemd units,
processes, listeners, and product config files. The next command reflects these
local changes. Operators may also choose standard host output and timeout
options.

The set of products, signals, paths, conventional ports, structural parsers,
and limits cannot be overridden in YAML or command arguments. Those changes
require reviewed code, fixtures, and a new immutable plugin release.

## Fixed behavior

The six product definitions, dpkg and procfs roots, unit names, process names,
ports, config paths and formats, `systemctl` and `ss` argv, and resource limits
are fixed in 1.0.0.

The plugin accepts no scrape URL, dashboard URL, target, include root,
credentials, arbitrary port, arbitrary process, or validation executable.
Listener presence is not process ownership.

## Safety

All commands are read-only, local-only, and non-root. Procfs and config access
reject unsafe paths and symlinks through confined bounded reads. Opened procfs
is compared with path metadata. External output has fixed byte limits and
strict grammars.

Raw configs, metric samples, labels, bearer tokens, basic-auth values, SNMP
communities, remote-write credentials, cloud keys, target URLs, process argv,
and command diagnostics are omitted. The host continues to apply policy,
timeout, audit, JSON stdout isolation, and recursive redaction.

## Troubleshooting

- If a package is not detected, check the exact compiled Debian package name
  and the bounded local dpkg status file.
- If process inventory is incomplete, check procfs type, symlinks, permissions,
  and the 4096-entry limit.
- Unit errors refer only to compiled unit names; a custom unit is not
  discovered in version 1.
- Listener errors require parseable `ss -H -lntu` output. A conventional port
  alone does not prove which process owns it.
- A partial config result means the fixed file was unsafe, oversized,
  non-UTF-8, empty, NUL-containing, or structurally malformed for its compiled
  format.

## Limitations and TODO

Version 1 is deliberately local-only. Metrics and health queries, dashboards,
remote-write checks, discovery targets, includes, authenticated endpoints,
semantic product validators, listener-to-process attribution, remote
execution, and downloads are deferred. Network support requires policy-pinned
endpoints, credential references, TLS verification, SSRF and DNS-rebinding
controls, redirect policy, bounded queries and responses, and recursive
redaction.

See the maintained
[monitoring roadmap](../../roadmap/monitoring-base.md).

## Compatibility and source

This page documents `monitoring-base` 1.0.0 and plugin protocol v1. The signed
catalog remains authoritative for installation, minimum host version, asset
size, SHA-256, and release history.

Implementation: [internal/monitoring](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/monitoring).
