# ohtools first-party plugins

This public monorepo contains the first-party executable plugins for
[`ohtools`](https://github.com/ohtoe02/ohtools). Each plugin is an independent,
statically linked Linux binary with its own version and release lifecycle.

Plugin package names are visible in the catalog and management commands. They
do not alter operator-facing CLI paths: `docker-base`, for example, provides
`ohtools docker ...` and `ohtools compose ...`.

## Implemented plugins

| Package | CLI namespaces | Current commands | Release |
| --- | --- | --- | --- |
| `system-base` | `system` | `system info`, `system health` | `1.0.1` |
| `storage-base` | `disk` | `disk usage [path]` | `1.0.1` |
| `systemd-base` | `service` | `service status`, `service logs`, `service restart` | `1.0.1` |
| `docker-base` | `docker`, `compose` | Docker diagnostics and the read-only Compose baseline | `1.0.1` |
| `server-setup-base` | `setup` | `setup check`, `setup apply`, `setup upgrade` | `1.0.0 planned` |
| `network-base` | `network`, `tls` | Local network inventory and TLS file inspection | `1.0.0 planned` |
| `security-base` | `security` | Local account, SSH, firewall, and aggregate audit diagnostics | `1.0.0 planned` |
| `apt-base` | `apt` | Local package state, upgrade simulation, sources, and history | `1.0.0 planned` |
| `baseline-base` | `baseline` | Compiled local profile inspection and evaluation | `1.0.0 planned` |
| `postgres-base` | `postgres` | Local installation, cluster, and configuration diagnostics | `1.0.0 planned` |
| `k8s-base` | `k8s` | Local client, kubelet, kubeconfig, and static manifest diagnostics | `1.0.0 planned` |
| `java-base` | `java` | Local runtime, process, and bounded `jcmd` diagnostics | `1.0.0 planned` |
| `kafka-base` | `kafka` | Local installation, configuration, and storage diagnostics | `1.0.0 planned` |
| `gitlab-runner-base` | `gitlab-runner` | Local service, safe configuration, and executor diagnostics | `1.0.0 planned` |
| `monitoring-base` | `monitoring` | Local monitoring product inventory and configuration metadata | `1.0.0 planned` |
| `backup-base` | `backup` | Local backup tool, schedule, and sanitized history diagnostics | `1.0.0 planned` |
| `incident-base` | `incident` | Bounded local incident snapshots, services, and timeline metadata | `1.0.0 planned` |
| `runbook-base` | `runbook` | Strict local documentation-only runbook discovery and validation | `1.0.0 planned` |

`inventory-base` remains deferred. A package is not published to the catalog
until it has a complete implementation, security tests, and immutable release
assets.

The new plugins are local-only. Network endpoints, credentials, remote
orchestration, and package downloads remain documented TODOs under
[`docs/roadmap`](docs/roadmap).

## Build and test

Go 1.26 is required:

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath ./cmd/system-base
```

Every binary implements plugin protocol v1:

```sh
./system-base manifest --protocol=1
printf '%s' '{"protocol_version":1,"request_id":"demo","command_path":["system","info"],"arguments":[],"options":{}}' \
  | ./system-base execute --protocol=1
```

See [development](docs/development.md), [configuration](docs/configuration.md),
[security](SECURITY.md), and [releasing](docs/releasing.md).

## Versioning

Plugins are released independently with tags such as `system-base-v1.0.0`.
A release contains the raw `<plugin>_linux_amd64` binary, its SHA-256 file, and
an SPDX JSON SBOM.

Catalog metadata and signing remain in the separate
[`ohtools-plugin-catalog`](https://github.com/ohtoe02/ohtools-plugin-catalog)
trust domain.
