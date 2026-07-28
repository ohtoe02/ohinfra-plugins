# monitoring-base roadmap

## Implemented in v1

- Deterministic inventory for Prometheus, node_exporter, Grafana Alloy, Grafana Agent, Telegraf, and Zabbix Agent.
- Local-only package, systemd unit, process, listener, and bounded configuration metadata probes.
- Strict parsing of fixed `systemctl show` and `ss -H -lntu` output without inheriting caller environment or accepting caller-provided argv.
- Fail-closed handling for symlinked, oversized, malformed, truncated, and non-UTF-8 local inputs.
- Metadata-only configuration results that omit bearer tokens, basic authentication, SNMP communities, remote-write credentials, cloud keys, and all raw config content.

## Deferred

### Authenticated metrics and health queries

- Reason: v1 never scrapes metrics endpoints, dashboards, remote-write targets, or monitoring APIs.
- Prerequisites: host-owned credential references, endpoint allowlists, and a versioned query policy.
- Security design required: TLS verification, credential lifecycle, query and response size limits, redirect policy, and recursive redaction.
- Acceptance criteria: authenticated probes reach only policy-pinned endpoints and never expose bearer tokens, basic-auth credentials, SNMP communities, cloud keys, labels classified as secrets, or raw metric samples.

### Remote target and discovery validation

- Reason: v1 reports only local product metadata and does not follow scrape targets, service-discovery URLs, or config includes.
- Prerequisites: a confined include resolver and schemas for each supported monitoring product/version.
- Security design required: traversal and symlink prevention, cumulative file/byte limits, remote-target allowlists, DNS rebinding protection, and SSRF-resistant transport.
- Acceptance criteria: deterministic validation covers supported config versions without making network requests during ordinary local status, inventory, or config commands.

### Product-specific semantic config validation

- Reason: v1 performs bounded structural validation and intentionally does not execute product binaries with validation flags.
- Prerequisites: fixed, versioned validator argv for every supported product and compatibility fixtures for Debian 10–13 and Ubuntu 20.04–24.04 LTS.
- Security design required: trusted executable resolution, stable environment, strict time/output limits, and omission of raw validator diagnostics.
- Acceptance criteria: semantic validation remains local-only, deterministic, non-mutating, and reports sanitized field-level failures without config values.

### Listener-to-process attribution

- Reason: v1 reports only compiled conventional product ports and does not consume privileged socket-owner metadata.
- Prerequisites: a bounded kernel socket/process attribution seam that remains useful without root.
- Security design required: procfs confinement, PID reuse handling, namespace semantics, and deterministic output limits.
- Acceptance criteria: reported ownership is verified locally and never inferred solely from a conventional port number.
