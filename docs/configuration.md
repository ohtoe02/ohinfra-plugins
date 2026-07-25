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

## `server-setup-base`

Mutations require the trusted system profile
`/etc/ohtools/plugins/server-setup-base.yaml`; `setup check` can run with safe
compiled defaults when the file is absent.

```yaml
enabled_items:
  - packages
  - shell-history
  - cron-permissions
  - ssh
  - fail2ban
  - time-sync
  - security-updates
  - logging
  - auditd
  - sysctl
packages:
  - curl
  - vim
administrators:
  - name: operator
    groups: [adm, sudo]
    authorized_key_sources:
      - /etc/ohtools/plugins/keys/operator.pub
ssh_port: 22
manage_firewall: false
zabbix:
  enabled: false
  server: 192.0.2.10
  hostname: web-01
  repository_package_url: https://repo.zabbix.com/example/zabbix-release.deb
  repository_package_size: 1
  repository_package_sha256: 0000000000000000000000000000000000000000000000000000000000000000
```

When Zabbix is enabled, replace all repository package fields with the exact
HTTPS URL, byte size, and lowercase SHA-256 of the immutable package from
`repo.zabbix.com`. Key source files must be regular, root-owned files below
`/etc/ohtools/plugins/keys`, must not be group/world-writable, and may contain
public keys only. Enabling SSH hardening requires at least one administrator
with such a key source. A non-default SSH port additionally requires
`manage_firewall: true`; the managed firewall is applied before sshd is
reloaded.

On releases whose vendor `sshd_config` lacks an early drop-in include, the
plugin transactionally inserts
`Include /etc/ssh/sshd_config.d/*.conf`, validates it, and rolls it back if
effective-state validation or reload fails. Firewall persistence is provided
by the dedicated `ohtools-server-setup-firewall.service`; it loads only the
ohtools-owned nftables file and does not replace the distribution's main
nftables configuration.

The supported OS matrix is Debian 10–13 and Ubuntu 20.04, 22.04, and 24.04 LTS.
Debian 10 requires an active Freexian ELTS source; Ubuntu 20.04 requires an
attached Ubuntu Pro/ESM entitlement before a mutation is planned. `setup
upgrade` plans with a fully removed temporary APT-index workspace and installs
only the exact package versions in the confirmed plan. It never runs
`dist-upgrade`, `autoremove`, or reboot.

Managed-file transactions fail closed when a previous crash leaves a
`.stage`, `.rollback`, `.ohtools-include.stage`, or
`.ohtools-include.rollback` artifact. Before retrying, an administrator must
inspect the target and artifact, restore the known-good file when necessary,
validate the affected subsystem with its native tool, and only then remove the
stale artifact. The plugin never guesses which side of an interrupted rename
is authoritative.
