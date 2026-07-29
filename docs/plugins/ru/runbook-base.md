---
schema_version: 1
plugin_id: runbook-base
locale: ru
documented_version: 1.0.0
title: Базовые локальные runbooks
summary: Обнаружение, просмотр и проверка trusted локальных documentation-only runbooks без выполнения steps.
command_paths:
  - runbook list
  - runbook inspect
  - runbook validate
local_only: true
---
# Базовые локальные runbooks

## Назначение и поддерживаемые сценарии

`runbook-base` обнаруживает strict YAML runbooks в фиксированном
`/etc/ohtools/runbooks`. Версия 1 считает каждый runbook и step plain
documentation: их можно перечислить, просмотреть и проверить, но нельзя
выполнить text, commands, scripts, URLs, includes, templates или другие plugins.

Плагин предназначен для небольшого trusted набора локальных operational
procedures с детерминированными identities и bounded readable text. Строгая
schema и trust checks не позволяют документации получить скрытое executable
поведение.

## Быстрый старт

Список trusted local documents:

```text
ohtools runbook list
```

Просмотр документа по schema name:

```text
ohtools runbook inspect database-recovery
```

Проверка всей collection или одного документа:

```text
ohtools runbook validate
ohtools runbook validate database-recovery
```

Все команды read-only и non-root. `inspect` требует одно name; `validate`
принимает ноль или одно name.

## Команды

### runbook list

Загружает и валидирует всю collection до возврата данных. Выдаёт
отсортированные summaries с name, normalized title, description и step count.
Если хотя бы один YAML unsafe или invalid, вся collection fail closed, а
документ не пропускается молча.

### runbook inspect

Проверяет name по `^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`, загружает полную trusted
collection и возвращает matching document. Каждый step содержит one-based
number, normalized title и description. Whitespace сворачивается до single
spaces.

Invalid name использует arguments exit mapping. Valid name, отсутствующий в
collection, использует dependency mapping.

### runbook validate

Загружает ту же полную collection, затем возвращает deterministic validation
view для всех документов или selected name. Документы на этом этапе отмечены
valid, потому что parsing и trust checks fail closed до Result construction.

Optional name имеет ту же syntax и not-found behavior, что inspect.

## Как это работает

Loader выполняет:

1. Требует absolute canonical local root.
2. Проверяет root, `/etc`, `/etc/ohtools` и runbook directory без symlinks.
3. На production root требует UID 0 для каждого path component и file; всегда
   запрещает group/world-writable modes.
4. Открывает directory, сравнивает opened metadata с path и читает максимум 129
   entries, чтобы применить лимит 128 до materialization.
5. Рассматривает только direct `.yaml` и `.yml` regular files, сортируя по
   filename.
6. Повторно проверяет opened file, читает с per-file и cumulative limits,
   разбирает один strict YAML document и валидирует documentation-only schema.
7. Требует совпадения filename stem и `name`, запрещает duplicate names.

YAML unknown fields, duplicate mapping keys, aliases, anchors, custom tags,
multiple documents, invalid UTF-8 и non-regular files fail closed.

## Доступ к данным

Плагин читает только direct YAML files в:

```text
/etc/ohtools/runbooks
```

Subdirectories не обходятся, symlinks не follow. Допускается максимум 128
directory entries и 128 runbook files, 64 КиБ на file и 1 МиБ total. Document
содержит 1–64 steps. Title ограничен 128 байтами normalized text, runbook
description — 2048, step description — 4096.

External executable, shell, plugin binary, URL, socket, include, environment
variable и caller-selected root не используются в production commands.

## Результаты и коды завершения

List возвращает `data.runbooks`, inspect — `data.runbook`, validate —
`data.validations`. Successful read-only Results не имеют mutation changes и
operational checks, потому что collection validity enforced до build Result.

Invalid argument использует arguments mapping. Missing requested document —
dependency. Unsafe ownership или permissions, symlinks, limits, malformed YAML,
schema violations, forbidden text, duplicate identities и любой invalid
document используют configuration mapping с safe message
`invalid local runbook collection`. Cancellation и timeout передаются host.

## Конфигурация

В версии 1 нет `/etc/ohtools/plugins/runbook-base.yaml`. Trusted directory,
schema и limits встроены. Поведение задаётся strict schema v1 documents:

```yaml
schema_version: 1
name: database-recovery
title: Database recovery review
description: Review the approved local recovery procedure.
steps:
  - title: Confirm incident ownership
    description: Confirm that an authorized operator owns the incident.
  - title: Review recovery prerequisites
    description: Review the locally approved prerequisites before any separate action.
```

Сохраните пример как `database-recovery.yaml`. В production directory, path
components и file должны принадлежать root и не быть group/world-writable.

## Что можно изменить

Оператор может добавлять, изменять и удалять trusted local YAML, выбирать valid
names, titles, descriptions и 1–64 documentation-only steps, а также выбирать
документ для inspect или validate. Изменение становится видимым, только если
вся collection проходит validation.

Root path, ownership policy, filename extensions, schema fields, text
restrictions, limits и non-executable semantics не настраиваются. Сопровождающие
могут изменить их только через versioned design, security tests и новый
immutable release.

## Зафиксированное поведение

Schema v1 содержит только `schema_version`, `name`, `title`, `description` и
`steps`; step содержит только `title` и `description`. Unknown и executable
fields запрещены.

Text не может содержать raw HTML angle brackets, URL references или inline
credential assignment с password, passphrase, secret, token, API-key,
authorization или credential markers. Includes, commands, argv, shell, plugins,
conditions, loops, variables, aliases, anchors и custom tags отсутствуют в
schema и отклоняются.

## Безопасность

Все команды local-only, read-only и non-root. Production trust обеспечивают
canonical absolute paths, root ownership, non-writable modes, symlink
rejection, descriptor-based enumeration, metadata rechecks после open, strict
single-document YAML и cumulative limits.

Inspect output — normalized plain text. Title, description и step не
интерпретируются как Markdown, HTML, URL, shell, argv, template или plugin
reference. Document content не выполняется, не загружается, не включается и не
записывается. Host timeout, policy, audit, JSON isolation и redaction
сохраняются.

## Диагностика проблем

- Если directory отсутствует, list и validate возвращают empty collection;
  selected name остаётся not found.
- При invalid collection проверьте каждый direct YAML: один bad document
  отклоняет всю collection.
- Проверьте production ownership UID 0 и удалите group/world write bits со всех
  path components и files.
- Удалите symlinks, подменённые components, multiple YAML documents, unknown
  fields, duplicate keys, aliases, anchors, custom tags, raw HTML, URLs и inline
  credentials.
- Убедитесь, что filename и `name` совпадают, names unique, text nonempty и в
  limits, collection не превышает count и byte budgets.

## Ограничения и TODO

Версия 1 намеренно local-only и documentation-only. Runbook execution,
remediation, action references, plans, templates, conditions, includes,
reusable steps, localization и migration, signed или remote distribution,
downloads и network access отложены. Execution потребует отдельной versioned
action schema, host policy, immutable plan digest, audit, confirmation, timeout
cleanup, verification и recovery. Distribution потребует signed immutable
bundles, key rotation, rollback protection, endpoint allowlists, pinned TLS и
archive limits.

Смотрите актуальный [roadmap runbook](../../roadmap/runbook-base.md).

## Совместимость и исходный код

Страница описывает `runbook-base` 1.0.0 и plugin protocol v1. Подписанный каталог
остаётся источником истины для установки, minimum host version, asset size,
SHA-256 и release history.

Реализация: [internal/runbook](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/runbook).
