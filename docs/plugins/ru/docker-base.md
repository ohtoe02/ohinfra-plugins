---
schema_version: 1
plugin_id: docker-base
locale: ru
documented_version: 1.0.1
title: Базовая диагностика Docker
summary: Ограниченная диагностика Docker Engine и Compose с анонимным HTTPS-probe registry.
command_paths:
  - docker status
  - docker info
  - docker ps
  - docker usage
  - docker logs
  - docker inspect
  - docker health
  - docker registry-test
  - compose find
  - compose validate
  - compose inspect
  - compose diff
local_only: false
---
# Базовая диагностика Docker

## Назначение и поддерживаемые сценарии

`docker-base` предоставляет read-only диагностику локальных Docker Engine и
Docker Compose. Плагин показывает доступность client/daemon, daemon info,
containers, disk usage, redacted logs, object data, container health и
нормализованные Compose models. Он ищет Compose-файлы без перехода по symlinks
и возвращает рядом desired и running Compose state.

Также есть одна сетевая функция: анонимный HTTPS-запрос к registry v2 endpoint.
Плагин никогда не принимает registry credentials. Команды не запускают, не
останавливают, не удаляют, не pull/push/build и не меняют Docker objects.

## Быстрый старт

Проверить локальные client/daemon и список containers:

```text
ohtools docker status
ohtools docker ps
```

Получить ограниченные и редактированные logs:

```text
ohtools docker logs web --since 30m --lines 100
```

Найти и проверить Compose-файл:

```text
ohtools compose find /srv
ohtools compose validate /srv/app/compose.yaml
```

Проверить анонимный доступ к registry v2:

```text
ohtools docker registry-test registry.example.com
```

Все команды diagnostic, без dry-run и confirmation.

## Команды

### docker status

Выполняет `docker version` с JSON formatting. Команда проверяет, что доверенный
Docker client запускается и его default daemon endpoint отвечает. Parsed JSON
возвращается в `data.result`.

### docker info

Выполняет `docker info` с JSON formatting и возвращает daemon information.
Весь result рекурсивно редактируется.

### docker ps

Выполняет `docker ps --no-trunc` с одним JSON object на строку. Упорядоченные
objects разбираются в `data.result`.

### docker usage

Выполняет `docker system df` в JSON-lines формате. Команда только сообщает
disk-usage records Docker и ничего не prune.

### docker logs

Принимает одно валидное имя или ID container. Запускается `docker logs` с
положительным `--since`, `--tail` от 1 до 100000 и option terminator перед
container. Непустые строки редактируются отдельно и возвращаются в
`data.lines`.

### docker inspect

Принимает один валидный Docker object identifier, запускает `docker inspect` и
возвращает recursively redacted JSON. Произвольные inspect templates не
принимаются.

### docker health

Через фиксированный inspect template читает только `.State.Health` выбранного
container. Если Docker health check не настроен, значение может быть пустым или
null; плагин не создаёт вымышленный application check.

### docker registry-test

Добавляет HTTPS к bare host и отправляет `GET /v2/` с configured timeout. Input
должен быть HTTPS host/URL без credentials, query, fragment и path, кроме `/`.
Proxy отключён, redirects должны оставаться HTTPS на том же hostname и имеют
ограниченную цепочку, из response body потребляется не более 64 KiB.

Любой HTTP response означает успешный transport probe. Код находится в
`data.status_code`, а `data.anonymous` true только для 2xx. DNS, TLS, timeout,
redirect и request failures дают `registry_probe_failed`.

### compose find

Ищет от current directory или указанного каталога файлы `compose.yaml`,
`compose.yml`, `docker-compose.yaml` и `docker-compose.yml` без учёта регистра.
Root обязан быть реальным non-symlink directory. Symlink entries игнорируются,
глубина и число matches ограничены config, результаты — отсортированные
absolute paths.

### compose validate

Требует один существующий regular non-symlink file и выполняет
`docker compose -f FILE config --quiet`. Успех означает, что Docker Compose
принял model; deployment не выполняется.

### compose inspect

Выполняет `docker compose ... config --format json`, разбирает normalized
model, рекурсивно редактирует и возвращает как `data.result`.

### compose diff

Выполняет normalized config query и затем `docker compose ... ps` в JSON-lines
формате. `data.desired` и `data.running` возвращаются рядом. Версия 1.0.1 не
вычисляет и не маркирует semantic field-by-field diff; comparison выполняет
caller.

## Как это работает

Каждый вызов загружает и проверяет полный plugin config, даже если команде
нужны не все settings. Docker/Compose command выбирается фиксированным switch и
строит literal argv.

Executable разрешается только из trusted system directories. Subprocess
environment содержит fixed `PATH`, `LANG` и `LC_ALL`, исключая caller proxy,
Docker endpoint variables, credentials и прочее inherited environment.
Working directory — доверенный filesystem root. Compose file преобразуется в
absolute path до запуска Docker.

Docker stdout разбирается как один JSON document или deterministic JSON lines.
Text и JSON рекурсивно редактируются до Result v1. Каждая Docker-команда
допускает в памяти до 10 MiB stdout и 1 MiB stderr.

## Доступ к данным

Плагин может обращаться к:

- `/etc/ohtools/plugins/docker-base.yaml`;
- trusted executable `docker` и его default local daemon socket;
- metadata Docker Engine, container logs и Compose runtime state;
- выбранному Compose-файлу и локальным project inputs, которые сам Docker
  Compose разрешает при evaluation;
- именам каталогов под выбранным Compose search root;
- одному указанному оператором HTTPS registry host через `/v2/`.

Сам плагин не читает Docker credential files и не передаёт environment
credentials caller. Compose-файлы всё равно надо проверять: Docker Compose
может разрешать стандартные local project files во время `config`.

## Результаты и коды завершения

Успех возвращает Result schema v1 со статусом `pass` и command-specific data.
Все команды включают stable tool/host identity и нормализованные пустые arrays.
Docker/Compose JSON остаётся upstream data и никогда не исполняется как код.

Если trusted `docker` отсутствует, возвращается skipped dependency result
`missing_dependency`. Ошибка доступа к Docker socket классифицируется как
privilege `docker_socket_denied`. Другие nonzero exits используют
`docker_command_failed`; неверный JSON — `invalid_docker_json` или
`invalid_compose_json`; registry transport — `registry_probe_failed`.

Неверные аргументы, object IDs, paths и command ranges используют exit 2.
Неверный YAML или global config ranges — exit 10. Context deadline даёт timeout
failure. HTTP non-2xx сам по себе не является plugin error; проверяйте
`anonymous` и `status_code`.

## Конфигурация

Необязательная системная конфигурация:

```text
/etc/ohtools/plugins/docker-base.yaml
```

Значения по умолчанию:

```yaml
logs_since: 1h
logs_lines: 200
search_max_depth: 6
search_max_files: 1000
registry_timeout: 10s
```

`logs_since` и `registry_timeout` должны быть положительными Go durations.
`logs_lines` принимает 1–100000, `search_max_depth` — 0–32,
`search_max_files` — 1–10000. Flags `docker logs` могут переопределить duration
и count для одного вызова.

Файл использует strict single-document YAML до 1 MiB: root-owned, regular,
non-symlink, без group/world write. Unknown/duplicate keys и unsafe metadata
дают exit 10. Tokens, passwords, private keys, Docker credentials и registry
credentials не являются допустимыми полями.

## Что можно изменить

Оператор может выбрать валидный Docker object, Compose search root/file и
anonymous registry host. Можно настроить default log history, per-command log
range, границы Compose discovery и registry timeout.

Filesystem и Docker socket permissions определяют доступные локальные данные.
Host policy и общий timeout дают дополнительные ограничения. Сопровождающие
добавляют команды, inputs или security policy только через reviewed source,
тесты и новый immutable release.

## Зафиксированное поведение

Command argv, JSON formats, grammar object identifier, output memory limits,
Compose filenames, non-symlink rules, стабильный subprocess environment,
registry endpoint `/v2/`, anonymous method, disabled proxy, redirect policy и
response-body limit встроены в версию 1.0.1.

Через YAML нельзя задать executable paths, daemon endpoints, environment,
registry credentials, HTTP headers, proxy URLs, Compose commands или Docker
mutations. `compose diff` возвращает desired/running models и не выполняет
reconciliation.

## Безопасность

Все команды diagnostic по manifest и не требуют confirmation, force, root или
dry-run. Доступ к локальному Docker socket сам по себе может давать права за
пределами плагина; выдавайте только доступ, разрешённый host policy.

Валидация Docker object отклоняет whitespace, semicolon, backslash, `..` и
leading options. Literal argv и option terminators предотвращают shell/option
smuggling. Compose search не следует symlinks, а explicit Compose file должен
быть regular non-symlink.

Anonymous registry probe отклоняет credential-bearing input, отключает
environment proxies, требует HTTPS, ограничивает redirects, имеет context
timeout и читает только bounded prefix body. Returned text и structured Docker
data рекурсивно редактируются.

## Диагностика проблем

- При `missing_dependency` установите distribution Docker CLI в protected
  system directory; wrapper раньше в caller `PATH` игнорируется.
- При `docker_socket_denied` исправьте daemon socket authorization, не ослабляя
  permissions и не запуская непроверенный shell.
- При invalid JSON проверьте совместимость Docker/Compose и возможное
  превышение output bound.
- Если logs пусты, проверьте container, увеличьте `--since` и Docker
  logging-driver retention.
- Если Compose find не находит файл, проверьте допустимое имя, depth, file
  limit и symlink boundaries.
- Если Compose file отклонён, укажите существующий regular non-symlink file и
  выполните `compose validate` до inspect/diff.
- При registry failure передайте только HTTPS host, проверьте DNS/TLS, redirect
  policy и timeout. Ответ 401 — transport success с `anonymous: false`.
- При exit 10 проверьте все ranges, YAML keys, owner и mode.

## Ограничения и TODO

Docker Engine и Compose diagnostics работают локально. Authenticated registry,
pulls, remote Docker endpoints и credential references отложены. Будущая
аутентификация требует host-owned secret references, endpoint allowlists,
redirect pinning и доказательства, что credentials не попадают в config,
output или audit.

Версия 1.0.1 не stream-ит logs, не строит trends, не проверяет image
vulnerabilities, не понимает application health кроме Docker health object и
не выполняет semantic reconciliation Compose state. См.
[roadmap Docker](../../roadmap/docker-base.md).

## Совместимость и исходный код

Страница описывает `docker-base` 1.0.1 и plugin protocol v1. Подписанный каталог
остаётся источником истины для установки, minimum host version, размера asset,
SHA-256 и release history.

Реализация: [internal/docker](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/docker).
