---
schema_version: 1
plugin_id: docker-base
locale: en
documented_version: 1.0.1
title: Docker base
summary: Run bounded Docker Engine and Compose diagnostics plus an anonymous HTTPS registry probe.
command_paths:
  - docker status
  - docker info
  - docker ps
  - docker usage
  - docker logs
  - docker inspect
  - docker health
  - docker registry-test
  - compose find
  - compose validate
  - compose inspect
  - compose diff
local_only: false
---
# Docker base

## Purpose and supported scenarios

`docker-base` provides read-only diagnostics for the local Docker Engine and
Docker Compose. It reports client/daemon availability, daemon information,
containers, disk usage, redacted logs, object data, container health, and
normalized Compose models. It can discover Compose files without following
symlinks and present desired and running Compose state together.

The plugin also has one network feature: an anonymous HTTPS request to a
registry v2 endpoint. It never accepts registry credentials. No command starts,
stops, removes, pulls, pushes, builds, or modifies Docker objects.

## Quick start

Check the local client and daemon, then list containers:

```text
ohtools docker status
ohtools docker ps
```

Read bounded and redacted logs:

```text
ohtools docker logs web --since 30m --lines 100
```

Find and validate a Compose file:

```text
ohtools compose find /srv
ohtools compose validate /srv/app/compose.yaml
```

Probe anonymous registry v2 access:

```text
ohtools docker registry-test registry.example.com
```

Every command is diagnostic and has no dry-run or confirmation phase.

## Commands

### docker status

Executes `docker version` with JSON formatting. It verifies that the trusted
Docker client can run and that its configured default daemon endpoint responds.
The parsed JSON is returned as `data.result`.

### docker info

Executes `docker info` with JSON formatting and returns the parsed daemon
information. The complete result is recursively redacted.

### docker ps

Executes `docker ps --no-trunc` with one JSON object per output line. The
ordered objects are parsed into `data.result`.

### docker usage

Executes `docker system df` with JSON-line formatting. It reports Docker's
read-only disk-usage records; it does not prune any objects.

### docker logs

Accepts one validated container name or ID. It calls `docker logs` with a
positive `--since` duration, a `--tail` value from 1 through 100000, and an
option terminator before the container. Nonempty output lines are individually
redacted and returned in `data.lines`.

### docker inspect

Accepts one validated Docker object identifier, invokes `docker inspect`, and
returns the recursively redacted JSON value. Arbitrary inspect templates are
not accepted.

### docker health

Reads only the selected container's `.State.Health` through a fixed inspect
template. A container without a configured Docker health check may return an
empty or null health value; the plugin does not invent an application check.

### docker registry-test

Normalizes a bare host to HTTPS and sends `GET /v2/` with the configured
timeout. Input must be an HTTPS host or URL without credentials, query,
fragment, or a path other than `/`. Proxies are disabled, redirects must remain
HTTPS on the same hostname and the chain is bounded, and at most 64 KiB of the
response body is consumed.

Any HTTP response is a successful transport probe. `data.status_code` contains
the code and `data.anonymous` is true only for 2xx. DNS, TLS, timeout, redirect,
or request failures produce `registry_probe_failed`.

### compose find

Searches the current directory or one supplied directory for `compose.yaml`,
`compose.yml`, `docker-compose.yaml`, and `docker-compose.yml`, case
insensitively. The root must be a real non-symlink directory. Symlink entries
are ignored, traversal depth and number of matches are bounded by
configuration, and results are sorted absolute paths.

### compose validate

Requires one existing regular non-symlink file and runs
`docker compose -f FILE config --quiet`. Success means Docker Compose accepted
the model; it does not deploy it.

### compose inspect

Runs `docker compose ... config --format json`, parses the normalized model,
recursively redacts it, and returns it as `data.result`.

### compose diff

Runs the same normalized config query and then `docker compose ... ps` with
JSON-line formatting. It returns `data.desired` and `data.running` side by
side. Version 1.0.1 does not calculate or label a semantic field-by-field diff;
the caller performs any higher-level comparison.

## How it works

Every invocation loads and validates the complete plugin configuration, even
when the selected command does not use every setting. Docker and Compose
commands are selected from a fixed switch and build literal argv.

Executables are resolved only from trusted system directories. The subprocess
environment contains a fixed `PATH`, `LANG`, and `LC_ALL`, excluding caller
proxy variables, Docker endpoint variables, credentials, and other inherited
environment. The working directory is a trusted filesystem root. Compose file
arguments are converted to absolute paths before invoking Docker.

Docker stdout is parsed either as one JSON document or deterministic JSON
lines. Text and JSON are recursively redacted before Result v1. Each Docker
command permits at most 10 MiB stdout and 1 MiB stderr in plugin memory.

## Data access

The plugin may access:

- `/etc/ohtools/plugins/docker-base.yaml`;
- the trusted `docker` executable and its default local daemon socket;
- local Docker Engine metadata, container logs, and Compose runtime state;
- a selected Compose file and the local project inputs that Docker Compose
  itself resolves while evaluating that file;
- directory names below a selected Compose search root;
- one operator-supplied HTTPS registry host through `/v2/`.

The plugin itself does not read Docker credential files or pass caller
environment credentials. Operators should still review Compose files because
Docker Compose may resolve standard local project files as part of `config`.

## Results and exit behavior

Success returns Result schema v1 with status `pass` and command-specific data.
All commands include stable tool and host identity and normalized empty arrays.
Docker/Compose JSON is upstream data and remains data, never executable content.

A missing trusted `docker` executable produces a skipped dependency result with
`missing_dependency`. Docker socket permission messages are classified as
privilege errors with `docker_socket_denied`. Other nonzero Docker exits use
`docker_command_failed`; invalid JSON uses `invalid_docker_json` or
`invalid_compose_json`; registry transport failures use
`registry_probe_failed`.

Invalid arguments, object identifiers, paths, or per-command ranges use exit 2.
Invalid YAML or global configuration ranges use exit 10. Context deadlines are
reported as timeout failures. HTTP non-2xx alone is not a plugin error; inspect
`anonymous` and `status_code`.

## Configuration

The optional system configuration is:

```text
/etc/ohtools/plugins/docker-base.yaml
```

Defaults:

```yaml
logs_since: 1h
logs_lines: 200
search_max_depth: 6
search_max_files: 1000
registry_timeout: 10s
```

`logs_since` and `registry_timeout` must be positive Go durations.
`logs_lines` accepts 1 through 100000. `search_max_depth` accepts 0 through 32,
and `search_max_files` accepts 1 through 10000. The `docker logs` flags may
override the configured log duration and count for one invocation.

The file is strict single-document YAML, limited to 1 MiB, root-owned, regular,
non-symlink, and not group/world-writable. Unknown or duplicate keys and unsafe
metadata produce exit 10. Tokens, passwords, private keys, Docker credentials,
and registry credentials are not valid configuration fields.

## What can be changed

Operators may select validated Docker objects, a Compose search root or file,
and an anonymous registry host. They may tune default log history, per-command
log range, Compose discovery bounds, and registry request timeout.

Filesystem and Docker socket permissions determine observable local data. Host
policy and the command timeout provide additional bounds. Maintainers may add
commands, supported inputs, or security policy only through reviewed source,
tests, and a new immutable release.

## Fixed behavior

Command argv, JSON formats, object identifier grammar, output memory limits,
Compose filenames, non-symlink requirements, stable subprocess environment,
registry endpoint `/v2/`, anonymous method, disabled proxy, redirect policy,
and response-body limit are compiled into version 1.0.1.

YAML cannot specify executable paths, daemon endpoints, environment variables,
registry credentials, arbitrary HTTP headers, proxy URLs, Compose commands, or
Docker mutation actions. `compose diff` always returns desired and running
models rather than executing reconciliation.

## Safety

All commands are diagnostic in the manifest and do not require confirmation,
force, root, or dry-run. Local Docker socket permissions may effectively grant
power outside this plugin; operators should grant only the access required by
their host policy.

Docker object validation rejects whitespace, semicolons, backslashes, `..`, and
leading options. Literal argv and option terminators prevent shell and option
smuggling. Compose search never follows symlinks, and an explicit Compose file
must be a regular non-symlink file.

The anonymous registry probe rejects credential-bearing input, disables
environment proxies, requires HTTPS, bounds redirects, has a context timeout,
and consumes only a bounded body prefix. All returned text and structured
Docker data is recursively redacted.

## Troubleshooting

- For `missing_dependency`, install the distribution's trusted Docker CLI in a
  protected system directory; a wrapper earlier in caller `PATH` is ignored.
- For `docker_socket_denied`, correct daemon socket authorization rather than
  running an unreviewed shell command or weakening file permissions.
- If JSON is invalid, verify Docker/Compose version compatibility and whether
  its output exceeded the plugin bound.
- If logs are empty, verify the container name, widen `--since`, and inspect
  Docker logging-driver retention.
- If Compose find misses a file, check the accepted filenames, configured
  depth, file limit, and symlink boundaries.
- If a Compose file is rejected, provide an existing regular non-symlink file;
  then run `compose validate` before inspect or diff.
- For registry failure, supply only an HTTPS host, verify DNS/TLS, redirect
  policy, and timeout. A 401 response is transport success with
  `anonymous: false`.
- On exit 10, validate every configuration range, YAML key, owner, and mode.

## Limitations and TODO

Docker Engine and Compose diagnostics are local. Authenticated registry
diagnostics, pulls, remote Docker endpoints, and credential references are
deferred. Any future authentication requires host-owned secret references,
endpoint allowlists, redirect pinning, and proof that credentials never enter
configuration, output, or audit.

Version 1.0.1 does not stream logs, compute trends, evaluate image
vulnerabilities, understand application health beyond Docker's health object,
or semantically reconcile Compose desired/running state. See the
[Docker roadmap](../../roadmap/docker-base.md).

## Compatibility and source

This page documents `docker-base` 1.0.1 and plugin protocol v1. The signed
catalog remains authoritative for installation, minimum host version, asset
size, SHA-256, and release history.

Implementation: [internal/docker](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/docker).
