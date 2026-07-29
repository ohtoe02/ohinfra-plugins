---
schema_version: 1
plugin_id: systemd-base
locale: ru
documented_version: 1.0.1
title: Базовое управление systemd
summary: Проверка локальных systemd units, чтение редактированного journal и проверяемый restart.
command_paths:
  - service status
  - service logs
  - service restart
local_only: false
---
# Базовое управление systemd

## Назначение и поддерживаемые сценарии

`systemd-base` предоставляет небольшой валидируемый интерфейс к одному
локальному systemd unit за раз. Плагин показывает стабильные свойства unit,
возвращает ограниченные и редактированные строки journal и выполняет
разрешённый host restart с последующей проверкой active state.

Используйте его для диагностики службы и контролируемой попытки восстановления.
Это не универсальная обёртка над `systemctl`/`journalctl`: нельзя передать
произвольные verbs, properties, journal expressions или несколько units.

## Быстрый старт

Проверить один unit:

```text
ohtools service status ssh.service
```

Прочитать до 100 строк за последние 30 минут:

```text
ohtools service logs ssh.service --since 30m --lines 100
```

Просмотреть restart без выполнения, затем применить утверждённую операцию:

```text
ohtools --dry-run service restart ssh.service
ohtools --yes service restart ssh.service
```

Для restart требуются root, разрешение policy, доступный audit и confirmation,
если оператор не указал `--yes`.

## Команды

### service status

Выполняет фиксированный `systemctl show` для `Id`, `LoadState`, `ActiveState`,
`SubState`, `UnitFileState`, `MainPID` и `ExecMainStartTimestamp`. Имена
свойств преобразуются в snake case и помещаются в Result data вместе с
проверенным именем unit.

### service logs

Выполняет фиксированный `journalctl` ровно для одного unit, положительного
lookback и от 1 до 100000 строк. Flags команды переопределяют YAML defaults для
текущего вызова. Пустые строки удаляются, каждая возвращённая строка проходит
redaction секретов.

### service restart

Plan читает текущие стабильные свойства, создаёт info check `current-state`,
одно planned restart change и указывает временную недоступность службы как
risk.

Непосредственно перед execute плагин заново строит текущий plan и требует
совпадения approved plan digest. Затем вызывается `systemctl restart`, после
чего каждые 250 ms проверяется `systemctl is-active --quiet` до успеха или
configured timeout. Успех возвращает `verify-active`, plan digest и completed
change.

## Как это работает

Каждый вызов сначала загружает и проверяет системный plugin config. Имя unit
имеет от 1 до 255 символов, начинается с ASCII-буквы или цифры, далее допускает
только ASCII-буквы, цифры, colon, underscore, dot, at-sign и hyphen. Slash,
backslash, whitespace, option prefix и shell metacharacters отклоняются до
разрешения executable.

Status и restart planning используют один фиксированный property query.
Restart execute привязан к свежему plan через digest protocol v1. Verification
получает отдельный bounded context и не считается успешной, пока systemd не
вернёт active.

При ошибке restart или verification выполняется best-effort сбор тех же
properties и последних 50 journal lines. Диагностика и первичная ошибка
рекурсивно редактируются перед возвратом Result v1.

## Доступ к данным

Плагин читает:

- `/etc/ohtools/plugins/systemd-base.yaml`;
- стабильные свойства unit через `systemctl show`;
- journal unit через `journalctl`;
- active state через `systemctl is-active`.

Restart вызывает `systemctl restart -- UNIT`. Executables разрешаются
доверенным system resolver и запускаются прямым argv, без shell и `sudo`.
Stdout ограничен 10 MiB, stderr — 1 MiB для каждого вызова.

## Результаты и коды завершения

Успешный status содержит `unit`, `id`, `load_state`, `active_state`,
`sub_state`, `unit_file_state`, `main_p_i_d` и
`exec_main_start_timestamp`, если их вернул systemd. Значения остаются
строками. Успешные logs содержат unit, нормализованную duration и
редактированный массив `lines`.

Успешный restart содержит pass check `verify-active`, completed change и
`data.plan_digest`. Ошибка restart имеет статус `error`, failed change,
structured `restart_failed` либо `verify_timeout` и доступную redacted
диагностику. Rollback не заявлен: неудачно перезапущенная служба может остаться
inactive и требует действий оператора.

Неверная форма команды, unit, log range или plan digest использует exit 2.
Configuration errors используют exit 10. Host обеспечивает privilege exit 3
для restart и стабильное отображение cancellation/timeout. Отсутствующий
`systemctl`/`journalctl` даёт dependency failure, а nonzero exit внешней
команды возвращается без неотредактированной диагностики.

## Конфигурация

Необязательная системная конфигурация:

```text
/etc/ohtools/plugins/systemd-base.yaml
```

Значения по умолчанию:

```yaml
logs_since: 1h
logs_lines: 200
restart_verify_timeout: 15s
```

`logs_since` и `restart_verify_timeout` должны быть положительными Go
durations. `logs_lines` допускает от 1 до 100000. Flags `service logs` могут
переопределить `logs_since` и `logs_lines`, но не restart timeout.

Используется strict single-document YAML до 1 MiB: root-owned, regular,
non-symlink, без group/world write. Unknown/duplicate keys и unsafe metadata
дают exit 10. Host `--config`, user config, XDG paths и environment не могут
его переопределить.

## Что можно изменить

Оператор может выбрать валидный unit, изменить default journal lookback и
число строк, переопределить эти два значения для команды и настроить время
ожидания restart verification. Host policy решает, разрешён ли restart, а
оператор — подтверждать ли просмотренный plan.

Сопровождающие меняют property set, диагностику, grammar имён и restart
lifecycle только через source review, тесты и новый immutable plugin release.

## Зафиксированное поведение

Allowlist systemd properties, grammar unit, direct argv, output limits,
verification interval 250 ms, failure journal на 50 строк, plan и условие
успешного active state встроены в версию 1.0.1.

Через YAML нельзя задать произвольные systemctl actions, journal filters,
executable paths, environment, группы units, shell fragments или rollback.
Плагин не выполняет enable, disable, start, stop, mask, edit или daemon-reload.

## Безопасность

Status и logs являются diagnostic и не требуют root по manifest. Permissions
ОС всё равно могут ограничивать journal или unit. Restart operational, требует
root и confirmation и поддерживает host-owned dry-run planning.

Валидация unit и option terminator `--` предотвращают option smuggling. Direct
argv исключает shell interpretation. Проверка plan digest отклоняет устаревшее
или изменённое approval. Output bounds и recursive redaction применяются к
успеху, ошибкам и restart diagnostics.

## Диагностика проблем

- Если имя unit отклонено, используйте точное systemd-имя без пути, whitespace,
  wildcard или leading option.
- При ошибке status проверьте, что unit известен systemd и установлен
  `systemctl`, затем повторите read-only запрос.
- Если logs пусты, увеличьте `--since`/`--lines`, проверьте journal retention и
  permissions.
- При exit 10 проверьте положительные durations, `logs_lines`, YAML keys,
  ownership и permissions.
- Если plan digest не совпадает, просмотрите новый plan; не используйте старое
  approval повторно.
- После `restart_failed` или `verify_timeout` изучите redacted properties и
  journal в Result, затем выполните `service status`.

## Ограничения и TODO

Версия 1.0.1 работает с одним локальным unit за вызов, хотя published metadata
не относит её к новой local-only волне. Dependency-aware multi-unit
orchestration отложена до проектирования deterministic plan, confirmation
scope, rollback limits и audit correlation.

Плагин не stream-ит logs, не следует за journal, не принимает произвольные
journal selectors и не делает rollback после failed restart. См.
[roadmap systemd](../../roadmap/systemd-base.md).

## Совместимость и исходный код

Страница описывает `systemd-base` 1.0.1 и plugin protocol v1. Подписанный
каталог остаётся источником истины для установки, minimum host version, размера
asset, SHA-256 и release history.

Реализация: [internal/systemd](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/systemd).
