# Plugin configuration

Each plugin uses compiled defaults followed by an optional strict system YAML:

```text
/etc/ohtools/plugins/<plugin-name>.yaml
```

Host `--config`, environment variables, XDG user configuration, and per-user
plugin configuration do not participate.

Unknown keys, duplicate keys, multiple YAML documents, invalid ranges, a
symlink, unsafe ownership, or group/world-writable permissions produce the
canonical configuration error and exit 10.

## `system-base`

```yaml
disk_warning: 80
disk_critical: 90
memory_warning: 85
memory_critical: 95
oom_window: 24h
```

## `storage-base`

```yaml
disk_warning: 80
disk_critical: 90
```

## `systemd-base`

```yaml
logs_since: 1h
logs_lines: 200
restart_verify_timeout: 15s
```

## `docker-base`

```yaml
logs_since: 1h
logs_lines: 200
search_max_depth: 6
search_max_files: 1000
registry_timeout: 10s
```

Configuration must not contain tokens, passwords, private keys, registry
credentials, or Docker credentials. `docker registry-test` supports anonymous
HTTPS probing only.
