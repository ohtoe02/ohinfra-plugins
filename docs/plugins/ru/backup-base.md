---
schema_version: 1
plugin_id: backup-base
locale: ru
documented_version: 1.0.0
title: Базовая диагностика резервного копирования
summary: Проверка локальных Restic, BorgBackup и rsnapshot, schedules, repository classes и bounded history.
command_paths:
  - backup status
  - backup inventory
  - backup history
local_only: true
---
# Базовая диагностика резервного копирования

## Назначение и поддерживаемые сценарии

`backup-base` выполняет read-only локальную диагностику Restic, BorgBackup и
rsnapshot. Он обнаруживает фиксированные executables и config files,
классифицирует repository location без публикации значения, проверяет
conventional systemd service и timer units и суммирует bounded local history.

Плагин помогает подтвердить наличие поддерживаемого backup tool, загрузку
фиксированных schedule units и наличие распознанных success или failure в
локальной истории. Он никогда не открывает repository и не запускает backup,
restore, check, prune, mount или unlock.

## Быстрый старт

```text
ohtools backup status
ohtools backup inventory
ohtools backup history
```

Все команды диагностические и без аргументов. Root, confirmation, force и
dry-run не нужны; локальное и remote state не изменяется.

## Команды

### backup status

Объединяет полный product inventory с фиксированными service и timer metadata.
Для каждого продукта запрашивает `LoadState`, `ActiveState` и `SubState`:

- `borg-backup.service` и `borg-backup.timer`;
- `restic-backup.service` и `restic-backup.timer`;
- `rsnapshot.service` и `rsnapshot.timer`.

Result содержит безопасные product metadata и schedule states вроде
`active/running` или `active/waiting`. Отсутствие conventional unit
информационно; malformed или unavailable optional metadata даёт partial.

### backup inventory

Запускает точные version probes `borg --version`, `restic version` и
`rsnapshot -V`. Output обязан соответствовать встроенной grammar, после чего
сохраняется только numeric version.

Также читается один фиксированный config каждого продукта:

- `/etc/borgmatic/config.yaml`;
- `/etc/restic/restic.conf`;
- `/etc/rsnapshot.conf`.

Возвращается только repository class `local`, `ssh`, `object`, `remote` или
`unknown`. Само repository value никогда не выводится. Product presence
определяется valid executable probe или существующим config.

### backup history

Читает `/var/log/borg/backup.log`, `/var/log/restic/backup.log` и
`/var/log/rsnapshot.log`. Каждая непустая line должна начинаться RFC3339
timestamp и содержать case-insensitive marker успеха `success` или `completed`
либо ошибки `failed` или `error`.

Result содержит product ID, total entries, successes, failures и latest UTC
timestamp. Raw history lines и messages не возвращаются.

## Как это работает

Плагин перебирает встроенные adapters в детерминированном порядке Borg, Restic,
rsnapshot. Version commands имеют exact program, argv, stdout и stderr limits.
Config files читаются через bounded confined seam и малые product-specific
parsers:

1. Restic требует одно точное assignment `RESTIC_REPOSITORY`.
2. Borg принимает первое непустое repository field `path:` в borgmatic file.
3. rsnapshot принимает двухпольную строку `snapshot_root`.
4. Значение классифицируется по syntax и немедленно отбрасывается.
5. History агрегируется без сохранения message text.

Config ограничен 256 КиБ. Version stdout — 4 КиБ, stderr — 8 КиБ. Unit output —
16 КиБ. History — 128 КиБ и 512 непустых lines на продукт.

## Доступ к данным

Плагин использует только три config path, три history path, три version
executable и шесть systemd units, перечисленных выше. Запускаются fixed direct
argv; caller arguments, stdin, environment overrides, config includes,
environment files, arbitrary units и paths не передаются.

Repository URL, SSH host, object-storage endpoint, socket, credential store,
remote response, tool stderr и raw config или log content не попадают в Result.

## Результаты и коды завершения

Inventory возвращает `backup.{product}.present` или
`backup.{product}.missing` для каждого adapter. Version и config problems
добавляют structured dependency или configuration errors, сохраняя остальные
products.

Status добавляет `backup.{product}.timer` или
`backup.{product}.schedule_missing`. Отсутствующий conventional schedule —
info, unsafe metadata — partial.

History возвращает `backup.{product}.history` как pass, info при отсутствии
фиксированного log или partial для unsafe, oversized, non-UTF-8 и malformed.
Fatal inventory failures используют dependency mapping; unsupported invocation
— arguments. Cancellation и timeout передаются host. Output следует Result v1.

## Конфигурация

В версии 1 нет `/etc/ohtools/plugins/backup-base.yaml`. Adapter paths,
commands, schedules, parsers и limits встроены.

Плагин наблюдает небольшие части существующей product configuration. Например,
он распознаёт Restic assignment:

```text
RESTIC_REPOSITORY=/srv/restic
```

Это не пример полной рекомендуемой backup configuration. Оператор обязан
использовать безопасный config и secret-management process соответствующего
продукта.

## Что можно изменить

Оператор может устанавливать инструменты, управлять тремя fixed configs,
repository locations, conventional units и local history штатными backup
процессами. Следующая диагностика отразит сохранённые изменения.

Плагин не позволяет настраивать adapters, paths, unit names, grammar, outcome
markers или limits. Они меняются только через source review, adversarial
fixtures и новый immutable release.

## Зафиксированное поведение

Три продукта, executable argv, version grammars, config и history paths, unit
names, repository classifications, success/failure markers и resource limits
зафиксированы в 1.0.0.

Плагин не принимает repository, password file, credential, service, timer,
output path, retention policy, backup source или restore destination. Он
никогда не вызывает repository operation.

## Безопасность

Все команды read-only, local-only и non-root. Files читаются bounded confined
probes, отклоняющими symlinks и unsafe paths. External commands используют
точные argv и bounded output. Missing optional dependencies не запускают
неограниченный fallback discovery.

Repository values, URL userinfo, SSH targets, passwords, environment files,
cloud credentials, raw configs, histories, stderr и remote payloads исключены.
Host policy, timeout, audit, clean JSON stdout и recursive redaction
сохраняются.

## Диагностика проблем

- Если tool отсутствует, проверьте точный trusted executable и поддерживаемый
  version-output format.
- При partial config проверьте fixed file, symlinks, UTF-8, лимит 256 КиБ и
  точное repository field соответствующего продукта.
- `unknown` означает, что непустое value распознано, но не совпало со
  встроенной local, SSH, object или URL shape.
- При отсутствии schedule metadata помните, что custom unit names версия 1 не
  обнаруживает.
- При partial history оставьте не более 512 непустых RFC3339 lines и
  распознанный success или failure marker в каждой строке.

## Ограничения и TODO

Версия 1 намеренно local-only. Repository availability и authentication,
remote checks, backup и restore, mount, unlock, prune, retention, integrity
checks, dynamic schedules, config includes, journal collection, archive output,
remote execution и downloads отложены. Сеть потребует policy-pinned
repositories, TLS или SSH host verification, credential references, proxy и
redirect controls, resource budgets и recursive redaction. Mutations требуют
отдельного plan, confirmation, audit, verification и recovery design.

Смотрите актуальный [roadmap backup](../../roadmap/backup-base.md).

## Совместимость и исходный код

Страница описывает `backup-base` 1.0.0 и plugin protocol v1. Подписанный каталог
остаётся источником истины для установки, minimum host version, asset size,
SHA-256 и release history.

Реализация: [internal/backup](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/backup).
