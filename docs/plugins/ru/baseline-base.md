---
schema_version: 1
plugin_id: baseline-base
locale: ru
documented_version: 1.0.0
title: Встроенный baseline локальной операционной системы
summary: Выбор и выполнение ограниченных локальных проверок для поддерживаемых Debian и Ubuntu.
command_paths:
  - baseline check
  - baseline profile
local_only: true
---

## Назначение и поддерживаемые сценарии

`baseline-base` оценивает встроенный read-only baseline операционной системы.
Он проверяет платформу, time sync, безопасность account files, два kernel
control, OpenSSH posture, dpkg records и обязательные local services.

Оператор может включать и отключать только известные compiled checks и менять
один package threshold. Версия 1 не принимает scripts, commands, paths, inline
policy, organization profiles или remote targets.

## Быстрый старт

Показать effective profile и checks:

```text
ohtools baseline profile
```

Выполнить выбранные проверки:

```text
ohtools baseline check
```

Минимальный strict config с отключением одной проверки:

```yaml
profile: default
enabled_checks: []
disabled_checks:
  - ssh-posture
max_pending_package_records: 0
```

## Команды

### baseline profile

Загружает и проверяет system configuration, определяет локальную платформу и
возвращает compiled profile, ordered check IDs, platform ID/version и
`max_pending_package_records`. Сами probes не выполняются.

### baseline check

Выполняет checks в стабильном порядке:

- `supported-os` подтверждает поддерживаемый Debian или Ubuntu.
- `time-sync` запускает
  `timedatectl show --property=NTPSynchronized --value` и ожидает `yes`.
- `filesystem-ownership` требует regular `/etc/passwd` и `/etc/shadow` без
  group/world write и с root owner при live Linux root.
- `kernel-controls` ожидает `kernel.randomize_va_space=2` и
  `net.ipv4.conf.all.rp_filter=1` через procfs.
- `ssh-posture` выполняет fixed effective query и ожидает root login `no` или
  `prohibit-password`, а password authentication — `no`.
- `package-state` сравнивает число не полностью установленных dpkg records с
  configured maximum.
- `required-services` запускает `systemctl is-active -- ssh` и затем `cron`.

Mismatch даёт warning, небезопасные account-file metadata — critical.
Unavailable optional probes дают skipped checks и structured errors.

## Как это работает

Порядок детерминирован:

1. Strict system YAML накладывается на compiled defaults.
2. Проверяются profile ID, known check IDs, duplicates, exclusions и range.
3. Платформа определяется из local root; unsupported release отклоняется.
4. Непустой `enabled_checks` служит allowlist, иначе candidates — все checks.
   Затем вычитается `disabled_checks`.
5. Не менее одного check выполняется в fixed order.
6. Только cancellation/timeout немедленно останавливают работу; обычная потеря
   probe выражается skipped и structured error.
7. Result v1 получает profile, platform и stable check IDs.

Stdout/stderr каждого command probe ограничен 64 KiB, dpkg status — 32 MiB.

## Доступ к данным

В зависимости от selections плагин читает:

- `/etc/os-release`;
- metadata `/etc/passwd` и `/etc/shadow`;
- `/proc/sys/kernel/randomize_va_space`;
- `/proc/sys/net/ipv4/conf/all/rp_filter`;
- `/var/lib/dpkg/status`;
- `/etc/ohtools/plugins/baseline-base.yaml`.

Он может запускать trusted `timedatectl`, `sshd` и `systemctl` с fixed argv.
Shell, mutations и network не используются.

## Результаты и коды завершения

Используется Result schema v1. Compliant checks проходят, deviation даёт
warning, unsafe account files — critical, недоступный probe — skipped и
structured error. Смесь usable и unavailable checks обычно даёт partial.

Configuration errors дают exit 10. Missing/unsupported platform — exit 4.
Invalid invocation — exit 2. Cancellation/timeout пробрасываются. Missing probe
executable остаётся внутри usable Result, не отменяя остальные checks.

## Конфигурация

Фиксированный путь:

```text
/etc/ohtools/plugins/baseline-base.yaml
```

Defaults:

```yaml
profile: default
enabled_checks: []
disabled_checks: []
max_pending_package_records: 0
```

`profile` обязан быть `default`. Arrays содержат только семь перечисленных
check IDs без duplicates. Пустой `enabled_checks` включает все candidates,
`disabled_checks` всегда вычитает. Должен остаться хотя бы один check.

`max_pending_package_records` принимает `0..100000`. YAML strict: unknown keys,
duplicates, multiple documents, wrong types, symlink, unsafe owner/mode и файл
больше 1 MiB дают exit 10.

## Что можно изменить

Оператор выбирает subset checks и допустимое число incomplete dpkg records, а
также исправляет локальное состояние и повторяет read-only evaluation.

Новый check, expected kernel values, platforms, services или SSH policy требуют
изменения source и нового immutable release.

## Зафиксированное поведение

Единственный profile — `default`. Реализация и order checks, paths,
executables, kernel values, `ssh`/`cron` и SSH expectations встроены. YAML не
может добавить path, command, expected value, script, include, remote endpoint
или arbitrary check.

`baseline check` только сообщает deviation и не применяет remediation.

## Безопасность

Обе команды diagnostic, без root, confirmation, force и dry-run. YAML читается
только из trusted fixed path. Local reads отклоняют traversal/symlink; account
files отдельно проверяются на ownership и permissions.

Executables разрешаются до trusted absolute paths и запускаются напрямую с
fixed argv и bounded output. Никакое содержимое YAML/profile не исполняется.

## Диагностика проблем

- Exit 10 означает ошибку strict YAML или file trust; проверьте keys, IDs,
  duplicates, range, owner и mode.
- Если checks не осталось, устраните конфликт enable/disable.
- Exit 4 до checks обычно означает missing `/etc/os-release` или unsupported OS.
- Skipped time/SSH/service check указывает dependency в structured error.
- Critical filesystem check требует проверить owner, regular type, symlink и
  group/world write bits.
- Package warning сравнивает dpkg records, а не available upgrades.

## Ограничения и TODO

Версия 1 local-only и compiled. Organization-authored/signed profiles, custom
checks, remediation, fleet evaluation, remote commands/endpoints и credentials
остаются TODO. Нужны signed schema, provenance, path allowlists, migration,
authenticated inventory и bounded fan-out.

См. [roadmap baseline-base](../../roadmap/baseline-base.md).

## Совместимость и исходный код

Страница описывает `baseline-base` 1.0.0 и plugin protocol v1. Signed catalog
остаётся источником host compatibility и release metadata.

Реализация: [internal/baseline](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/baseline).
