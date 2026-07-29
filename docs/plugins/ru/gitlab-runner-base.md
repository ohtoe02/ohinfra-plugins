---
schema_version: 1
plugin_id: gitlab-runner-base
locale: ru
documented_version: 1.0.0
title: Базовая диагностика GitLab Runner
summary: Проверка локальных service, process, безопасной configuration и executor metadata GitLab Runner.
command_paths:
  - gitlab-runner status
  - gitlab-runner config
  - gitlab-runner executors
local_only: true
---
# Базовая диагностика GitLab Runner

## Назначение и поддерживаемые сценарии

`gitlab-runner-base` даёт read-only локальную видимость установки GitLab
Runner. Он объединяет версию binary, фиксированный systemd unit, bounded process
inventory, строгий metadata-view `/etc/gitlab-runner/config.toml` и aggregate
counts executors.

Плагин помогает определить наличие и active state Runner, просмотреть
безопасные global concurrency settings и понять, какие классы executors
настроены. Версия 1 не обращается к GitLab coordinator, не проверяет
registration, не запускает jobs и не раскрывает runner identity и credentials.

## Быстрый старт

```text
ohtools gitlab-runner status
ohtools gitlab-runner config
ohtools gitlab-runner executors
```

Все команды диагностические, не принимают аргументы и не требуют root,
confirmation, force или dry-run.

## Команды

### gitlab-runner status

Запускает фиксированный `gitlab-runner --version`, запрашивает `LoadState`,
`ActiveState` и `SubState` для `gitlab-runner.service` и просматривает bounded
procfs по точному имени `gitlab-runner`. Process data содержит только numeric
PID и, если корректно разобран, numeric real UID.

Команда возвращает info при полном отсутствии локальных признаков, pass при
обнаружении установки и warning, если установка найдена, но unit не active и
совпадающий process не работает.

### gitlab-runner config

Читает не более 1 МиБ и 16384 lines фиксированного
`/etc/gitlab-runner/config.toml`. Строгое bounded-подмножество TOML распознаёт
scalar values, arrays, inline tables, обычные tables и `[[runners]]`. Оно
отклоняет malformed strings и values, duplicate keys в одной scope,
unsupported array tables, неверный порядок и более 256 runners.

Result содержит path, size bytes, runner count, безопасные global settings
`concurrent`, `check_interval`, `log_level`, `shutdown_timeout`, а также
allowlisted executor, numeric `limit`, numeric `request_concurrency` и
allowlisted shell каждого runner. Runner names проверяются, но не возвращаются.
Прочие tables могут быть структурно разобраны, но их values не публикуются.

### gitlab-runner executors

Читает тот же strict local config и группирует runners по executor. Возвращает
отсортированный список executor name и runner count. Пустая корректная
collection информационна. Команда не создаёт, не тестирует и не запускает
executor.

## Как это работает

Каждая команда проходит read-only plan protocol v1. Status обрабатывает version,
unit и process probes независимо, поэтому безопасные данные сохраняются при
optional failures. Version должна соответствовать bounded semver shape, unit
values — встроенным systemd state allowlists.

Config parser отслеживает текущую TOML table и runner index, проверяет syntax и
shape каждой assignment, отклоняет duplicates и переносит в Result только
safe-field allowlist. Integer settings принимают значения от 0 до 1 000 000.
Поддерживаемые global log levels: `debug`, `info`, `warn`, `error`, `fatal`,
`panic`.

Executor metadata допускает `shell`, `docker`, `docker-windows`, `kubernetes`,
`custom`, `parallels`, `virtualbox`, `ssh`, `docker+machine`, `instance` и
`docker-autoscaler`. Shell может быть `bash`, `sh`, `powershell` или `pwsh`.

## Доступ к данным

Плагин выполняет только:

```text
gitlab-runner --version
systemctl show gitlab-runner.service --property=LoadState,ActiveState,SubState --no-pager
```

Он читает `/proc/{pid}/comm`, optional `/proc/{pid}/status` и
`/etc/gitlab-runner/config.toml`. Command output ограничен 64 КиБ или 32 КиБ
при 16 КиБ stderr. Procfs допускает до 32768 entries и 64 КиБ на read. Shell,
job command, config include, environment file, coordinator, cache endpoint и
caller-provided path не используются.

## Результаты и коды завершения

`gitlab-runner status` возвращает `gitlab-runner.status` и безопасные dependency
errors для unavailable non-missing probes. Отсутствующие executables или units
могут просто участвовать в результате not-installed.

`gitlab-runner config` возвращает `gitlab-runner.config`. Отсутствие
фиксированного файла использует dependency exit mapping; unsafe или malformed
strict TOML — configuration mapping.

`gitlab-runner executors` возвращает `gitlab-runner.executors` с pass или info
и использует те же mappings. Unsupported invocation использует arguments.
Cancellation и timeout передаются host. Stdout в JSON mode содержит только
Result v1.

## Конфигурация

В версии 1 нет `/etc/ohtools/plugins/gitlab-runner-base.yaml`. Проверяется
только product configuration:

```text
/etc/gitlab-runner/config.toml
```

Пример полей, которые metadata-view может опубликовать:

```toml
concurrent = 4
check_interval = 3
log_level = "info"

[[runners]]
name = "local-runner"
executor = "shell"
limit = 2
request_concurrency = 1
shell = "bash"
```

Runner name намеренно исключается. Tokens, URLs, environment, cache, TLS и
executor-specific nested fields никогда не публикуются.

## Что можно изменить

Оператор может управлять GitLab Runner и защищённым TOML штатными средствами.
Четыре safe global settings и четыре per-runner metadata fields отражают
корректные локальные изменения.

Изменение Runner config может повлиять на jobs вне этой диагностики. Плагин
никогда не пишет файл. Probe paths, executable argv, executor и shell enums,
field allowlists и limits меняются только новым release.

## Зафиксированное поведение

Binary и unit names, procfs classification, config path, лимиты 1 МиБ,
16384 lines и 256 runners, safe fields, enums, integer range и output shape
зафиксированы в 1.0.0.

Плагин не принимает coordinator URL, token, runner selector, arbitrary path,
executable, environment override, registration option или job command. Он не
запускает `register`, `unregister`, `run` или `exec`.

## Безопасность

Все команды read-only и non-root. Local reads отклоняют symlinks и oversized
files через общий confined probe. Process command lines и environments не
читаются. External output bounded и разбирается через allowlists.

Runner и registration tokens, cache credentials, URL userinfo, environment
secrets, TLS private-key metadata, runner names и executor-specific remote или
privileged settings не попадают в Result. Host timeout, policy, audit, JSON
isolation и recursive redaction сохраняются.

## Диагностика проблем

- Если status информационный, проверьте trusted binary, fixed service и exact
  process name.
- При warning проверьте `gitlab-runner.service` и local process lifecycle, не
  публикуя job arguments.
- Configuration exit означает unsafe file или strict TOML failure; проверьте
  UTF-8, size и line limits, table syntax, duplicate keys, supported values и
  runner count.
- Каждый `[[runners]]` должен содержать поддерживаемый `executor`.
- Values вне 0–1 000 000 и unsupported log level или shell fail closed, даже
  если их принимает более новая версия Runner.

## Ограничения и TODO

Версия 1 намеренно local-only. Registration verification, coordinator health,
registration и lifecycle mutations, job execution, remote и cache access,
downloads и полная validation Docker, Kubernetes, autoscaler, SSH или custom
executors отложены. Сеть потребует policy-pinned coordinators, host-owned token
references, TLS identity controls, redirect и DNS-rebinding protections,
bounded responses и строгую secret isolation.

Смотрите актуальный
[roadmap GitLab Runner](../../roadmap/gitlab-runner-base.md).

## Совместимость и исходный код

Страница описывает `gitlab-runner-base` 1.0.0 и plugin protocol v1. Подписанный
каталог остаётся источником истины для установки, minimum host version, asset
size, SHA-256 и release history.

Реализация: [internal/gitlabrunner](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/gitlabrunner).
