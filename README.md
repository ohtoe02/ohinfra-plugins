# ohtools first-party plugins

This public monorepo contains the first-party executable plugins for
[`ohtools`](https://github.com/ohtoe02/ohtools). Each plugin is an independent,
statically linked Linux binary with its own version and release lifecycle.

Plugin package names are visible in the catalog and management commands. They
do not alter operator-facing CLI paths: `docker-base`, for example, provides
`ohtools docker ...` and `ohtools compose ...`.

## Production plugins

| Package | CLI namespaces | Current commands |
| --- | --- | --- |
| `system-base` | `system`, `performance` | `system info`, `system health` |
| `storage-base` | `disk` | `disk usage [path]` |
| `systemd-base` | `service`, `logs` | `service status`, `service logs`, `service restart` |
| `docker-base` | `docker`, `compose` | Docker diagnostics and the read-only Compose baseline |
| `server-setup-base` | `setup` | `setup check`, `setup apply [item...]`, `setup upgrade` |

`plugins.json` is the single source for CI build matrices and release tag
authorization. `server-setup-base` is release-enabled only on its completed
implementation branch and retains a separate mandatory
`server-setup-readiness` CI gate.

The roadmap also reserves `network-base`, `postgres-base`, `k8s-base`,
`security-base`, `apt-base`, `java-base`, `kafka-base`,
`gitlab-runner-base`, `monitoring-base`, `backup-base`, `baseline-base`,
`incident-base`, and `runbook-base`. A reserved package is not published to the
catalog until it has a complete implementation, security tests, and immutable
release assets.

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
A release contains the raw `<plugin>_linux_amd64` binary, its SHA-256 file, an
SPDX JSON SBOM, the exact manifest, and a `release-metadata-v1` sidecar for
deterministic catalog import.

Catalog metadata and signing remain in the separate
[`ohtools-plugin-catalog`](https://github.com/ohtoe02/ohtools-plugin-catalog)
trust domain.
