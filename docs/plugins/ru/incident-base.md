---
schema_version: 1
plugin_id: incident-base
locale: ru
documented_version: 1.0.0
title: Базовый сбор данных об инциденте
summary: Сбор bounded локальных данных ОС, ресурсов, services, listeners и journal без remediation.
command_paths:
  - incident snapshot
  - incident services
  - incident timeline
local_only: true
---
# Базовый сбор данных об инциденте

## Назначение и поддерживаемые сценарии

`incident-base` собирает малый детерминированный набор локальных данных для
первичного incident triage. Он суммирует identity ОС, load, memory, использование
root filesystem, количество listeners, failed systemd services и high-priority
journal events.

Плагин помогает получить bounded initial snapshot, список failed services или
санитизированную недавнюю timeline перед глубоким расследованием. Версия 1 не
выполняет remediation, не пишет evidence archive, не раскрывает raw journal
messages и не обращается к monitoring, ticketing, cloud или remote hosts.

## Быстрый старт

```text
ohtools incident snapshot
ohtools incident services
ohtools incident timeline
ohtools incident timeline --since 24h
```

Три команды диагностические и non-root. Только timeline имеет option:
`--since` по умолчанию `1h` и принимает positive Go-style duration не более
`168h`.

## Команды

### incident snapshot

Выполняет семь независимых bounded probes:

1. `/etc/os-release` для Debian или Ubuntu ID и version;
2. `/proc/loadavg` для load за одну, пять и пятнадцать минут;
3. `/proc/meminfo` для total и available memory, total и free swap;
4. `df -P -B1 -- /` для root filesystem totals и used percent;
5. `ss -H -lntu` для listener count;
6. фиксированный `systemctl list-units` для failed services;
7. journal priority `0..3` за последний час.

Snapshot содержит numeric resource summaries, безопасные state fields failed
units, listener count, количество recent error lines и OOM markers. Missing или
malformed optional evidence превращается в structured error, остальные probes
сохраняются.

### incident services

Запускает только фиксированный failed-service query и возвращает
отсортированные unit name, load, active и sub state. Human-readable
descriptions игнорируются. Ошибка или malformed output даёт partial Result с
пустой collection вместо raw output.

### incident timeline

Запускает фиксированный priority `0..3` `journalctl` для validated lookback.
Возвращается не более 1024 events, каждый содержит только validated timestamp и
одну категорию: `out-of-memory`, `service-failure`, `authentication`, `kernel`
или `error`.

Raw line используется для классификации только в памяти. Message, unit
description, username, process fields и прочие journal values отбрасываются.

## Как это работает

Protocol v1 проверяет read-only invocation, а timeline дополнительно валидирует
duration на стадии plan. Все file и command reads имеют fixed byte limits.
Каждый parser принимает узкую grammar и преобразует input только в typed numeric
или allowlisted fields.

`/etc/os-release` принимает Debian или Ubuntu с numeric version. Memory values
должны быть kB integers и преобразуются в bytes с overflow check. Disk output
должен содержать одну data row для `/`. Failed service names заканчиваются
`.service`, states являются bounded lowercase tokens.

Journal output не копируется в Result. Snapshot считает lines и OOM markers,
timeline валидирует timestamps и назначает categories. Cancellation и timeout
останавливают сбор.

## Доступ к данным

Плагин читает:

```text
/etc/os-release
/proc/loadavg
/proc/meminfo
```

Он может выполнить только:

```text
df -P -B1 -- /
ss -H -lntu
systemctl list-units --type=service --state=failed --all --no-legend --plain
journalctl --no-pager --output=short-iso --priority=0..3 --since=-{duration}
```

Files ограничены 256 КиБ, обычный command stdout — 256 КиБ, journal stdout —
512 КиБ, stderr — 16 КиБ. Shell, arbitrary journal filter, mount, process argv,
environment, remote endpoint и output path не принимаются.

## Результаты и коды завершения

Snapshot возвращает `incident.snapshot` с available и total probe counts.
Missing или malformed source создаёт dependency error, успешные данные остаются
в `data.snapshot`.

Services возвращает `incident.services` как pass или partial и включает
`failed_count` при успехе. Timeline возвращает `incident.timeline` как pass или
partial и включает event count и normalized lookback.

Неверный `--since` использует arguments exit mapping. Unsupported invocation
также использует arguments. Cancellation и timeout передаются host. Остальные
local probe failures представлены partial Result v1 без command diagnostics.

## Конфигурация

В версии 1 нет `/etc/ohtools/plugins/incident-base.yaml`. Evidence sources,
commands, limits, journal priority, categories, default и maximum lookback
встроены.

Единственный operator-controlled input плагина:

```text
--since 30m
--since 24h
--since 168h
```

Zero, negative, malformed и longer durations отклоняются до collection.

## Что можно изменить

Оператор может выбрать valid timeline lookback больше нуля и до 168 часов, а
также стандартные host output и timeout options. Изменение локальной ОС,
resources, services, listeners и journal events отражается в новых snapshots.

Probe list, journal priority, evidence fields, categories, byte и event limits
и fixed argv нельзя переопределить. Они меняются через reviewed code, security
tests и новый immutable release.

## Зафиксированное поведение

Семь snapshot probes, root mount, failed-service query, priority `0..3`,
default `1h`, maximum `168h`, event categories, parsers и limits зафиксированы
в 1.0.0.

Плагин не может restart service, kill process, выбрать arbitrary journal unit
или priority, раскрыть messages, собрать user activity, выбрать remote host или
записать archive.

## Безопасность

Все команды read-only, local-only и non-root. Используются fixed direct argv и
bounded stdout/stderr. Files читаются confined bounded seam с отклонением unsafe
path types. Strict parsers fail closed для malformed, truncated, oversized и
unexpected input.

Raw journal messages, service descriptions, usernames, credentials,
environment values, process command lines, listener addresses и stderr не
возвращаются. Host policy, timeout, audit, JSON stdout isolation и recursive
redaction сохраняются.

## Диагностика проблем

- Missing OS, load или memory evidence обычно означает unsafe, unreadable или
  malformed fixed file либо превышение 256 КиБ.
- Disk evidence требует parseable GNU-compatible `df -P -B1` для `/`.
- Listener evidence требует bounded six-field output `ss -H -lntu`.
- Failed-service evidence требует fixed systemd query и safe `.service` names;
  descriptions намеренно игнорируются.
- Partial timeline означает unavailable, malformed, oversized или более 1024
  events в выбранном окне. При необходимости уменьшите `--since`.
- Duration error нужно исправить, а не обходить; upper bound ровно `168h`.

## Ограничения и TODO

Версия 1 намеренно local-only и evidence-only. Remediation, service restart,
process actions, remote evidence, SSH, monitoring и ticketing APIs, cloud
queries, expanded journal и process attribution, archives, output files, remote
execution и downloads отложены. Remote collection потребует endpoint и
credential policy, TLS identity, redirect и DNS-rebinding controls, bounded
schemas и tenant-safe redaction. Remediation потребует plans, authorization,
audit, confirmation, verification и recovery.

Смотрите актуальный [roadmap incident](../../roadmap/incident-base.md).

## Совместимость и исходный код

Страница описывает `incident-base` 1.0.0 и plugin protocol v1. Подписанный
каталог остаётся источником истины для установки, minimum host version, asset
size, SHA-256 и release history.

Реализация: [internal/incident](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/incident).
