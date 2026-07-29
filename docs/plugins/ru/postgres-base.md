---
schema_version: 1
plugin_id: postgres-base
locale: ru
documented_version: 1.0.0
title: Диагностика локального PostgreSQL
summary: Обнаружение PostgreSQL, список Debian clusters и проверка ограниченных публичных config metadata.
command_paths:
  - postgres status
  - postgres clusters
  - postgres config
local_only: true
---

## Назначение и поддерживаемые сценарии

`postgres-base` обнаруживает признаки локального PostgreSQL, перечисляет
Debian-style clusters и суммирует выбранные configuration files. Он
предназначен для package/process triage без подключения к database.

Версия 1 не вызывает `psql`, не открывает TCP/Unix socket, не выполняет auth или
SQL, не изменяет cluster и не возвращает authentication rules и
secret-bearing settings.

## Быстрый старт

Обнаружить installation evidence и service posture:

```text
ohtools postgres status
```

Получить clusters из `postgresql-common`:

```text
ohtools postgres clusters
```

Проверить bounded config metadata:

```text
ohtools postgres config
```

## Команды

### postgres status

Объединяет пять локальных signals: `pg_lsclusters`, systemd state
`postgresql.service`, installed dpkg packages, procfs processes с именем
`postgres` и наличие `/etc/postgresql`.

Result содержит `installed`, counts/inventories и безопасные systemd
load/active/sub states. Без evidence check имеет info. Installation даёт pass
или warning, если известный service не active и PostgreSQL process не виден.

Потеря отдельного optional probe не отменяет остальные локальные evidence.

### postgres clusters

Запускает `pg_lsclusters --no-header`. Valid record содержит bounded version,
cluster name, port `1..65535`, status, owner, абсолютные data directory и log
path. Malformed, traversal, URL-like и overlong records пропускаются. Сортировка
идёт по version и cluster name.

Команда специфична для дистрибутивов с `postgresql-common`.

### postgres config

Перечисляет regular non-symlink directories в
`/etc/postgresql/VERSION/CLUSTER`, не более 64 entries в каталоге. Ищет только
`pg_hba.conf`, `pg_ident.conf`, `postgresql.auto.conf` и `postgresql.conf`
размером до 1 MiB.

Для файла возвращаются path, size и non-comment line count. Values выдаются
только для `effective_cache_size`, `hot_standby`, `listen_addresses`,
`log_destination`, `logging_collector`, `maintenance_work_mem`,
`max_connections`, `max_wal_senders`, `port`, `shared_buffers`, `timezone`,
`wal_level` и `work_mem`. URI-like value заменяется marker. HBA/ident records
только считаются.

## Как это работает

Используются независимые bounded probes:

1. Fixed `systemctl show postgresql.service` получает allowlisted state.
2. `/var/lib/dpkg/status` до 16 MiB фильтруется по installed `postgresql`,
   `postgresql-common` и `postgresql-*`.
3. Рассматривается до 32 768 procfs entries; сохраняются safe numeric dirs с
   PostgreSQL `comm`.
4. `pg_lsclusters` output до 256 KiB строго проверяется.
5. Config dirs/files confined, bounded, sorted и сворачиваются в public metadata.
6. Result v1 строится без command diagnostics.

Cancellation/timeout прерывают активный collector. Symlink config не читается.

## Доступ к данным

Плагин может читать:

- `/var/lib/dpkg/status`;
- `/proc/PID/comm` и `/proc/PID/status`;
- `/etc/postgresql` и четыре fixed files на cluster;
- systemd state через local command;
- inventory через local `pg_lsclusters`.

Другая точная команда:
`systemctl show postgresql.service --property=LoadState,ActiveState,SubState --no-pager`.
Database files, passwords, private keys, sockets, SQL и network не используются.

## Результаты и коды завершения

Используется Result schema v1. Status имеет info/pass/warning. Clusters/config
при успехе дают pass checks с counts.

`postgres clusters` возвращает exit 4 при missing, nonzero, truncated или
unusable `pg_lsclusters`. `postgres config` даёт exit 4, если safe config file
не получен. Invalid invocation — exit 2. Cancellation/timeout фатальны.

`postgres status` tolerant: missing optional signals уменьшают evidence, а не
обязательно завершают команду ошибкой.

## Конфигурация

В версии 1 нет plugin YAML, arguments и flags. Package prefixes, service,
procfs limits, directory depth, filenames и public settings встроены.

Стандартные host controls timeout, output, JSON, policy, audit и redaction
продолжают действовать.

## Что можно изменить

Оператор может штатно изменить local installation, service, packages, clusters,
processes и config и повторить диагностику.

Другие layouts, новые safe settings или database queries требуют reviewed code,
security classification, tests и нового immutable release.

## Зафиксированное поведение

Поддерживаются Debian-style `pg_lsclusters` и `/etc/postgresql`. Paths,
filenames, service, package filters, limits, states и public settings
зафиксированы в 1.0.0.

Нельзя передать DSN, host, port argument, socket path, username, password,
query, include path, arbitrary config path или executable.

## Безопасность

Все команды diagnostic, без root, mutation, confirmation, force и dry-run.
Traversal/symlink отклоняются, inventories ограничены, parsed paths обязаны
быть local absolute без traversal или URL syntax.

Trusted executables вызываются напрямую с fixed argv и bounded output.
Secret-capable fields и HBA/ident contents исключены allowlist, URI values
редактируются.

## Диагностика проблем

- Exit 4 clusters обычно означает отсутствие или ошибку `pg_lsclusters`.
- Status всё ещё может найти packages, processes, service или config.
- Exit 4 config означает отсутствие supported safe files или ошибку
  confinement/size.
- Empty `settings` для HBA/ident намеренны: выдаются только counts.
- Setting вне allowlist намеренно скрыт, а не обязательно отсутствует.
- Status не доказывает database readiness: connection/query не выполняются.

## Ограничения и TODO

Версия 1 local-only. Authenticated health, SQL, replication, socket/TCP,
credentials, TLS, remote endpoints и runtime comparison остаются TODO. Нужны
least-privilege roles, host-owned credentials, allowlists, TLS identity,
query/row/time limits и recursive redaction.

Config includes и version-specific semantic validation также отложены. См.
[roadmap postgres-base](../../roadmap/postgres-base.md).

## Совместимость и исходный код

Страница описывает `postgres-base` 1.0.0 и plugin protocol v1. Signed catalog
остаётся источником host compatibility и release metadata.

Реализация: [internal/postgres](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/postgres).
