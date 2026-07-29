---
schema_version: 1
plugin_id: java-base
locale: ru
documented_version: 1.0.0
title: Диагностика локального Java runtime и процессов
summary: Проверка runtime, bounded procfs metadata и разрешённых read-only операций jcmd.
command_paths:
  - java runtime
  - java processes
  - java inspect
local_only: true
---

## Назначение и поддерживаемые сценарии

`java-base` показывает локальный Java runtime, инвентаризирует Java processes в
procfs namespace плагина и выполняет bounded read-only inspection одной JVM
через три fixed `jcmd` operations.

Он предназначен для local triage с минимизацией secrets в command lines и
system properties. Версия 1 не использует JMX, не подключается к service, не
входит в container namespaces и не снимает heap/thread/JFR artifacts.

## Быстрый старт

Показать default runtime:

```text
ohtools java runtime
```

Перечислить видимые Java processes:

```text
ohtools java processes
```

Проверить один positive decimal PID:

```text
ohtools java inspect 1234
```

Для inspect может потребоваться тот же OS identity, что у JVM, или privilege,
явно разрешённый local policy.

## Команды

### java runtime

Запускает `java -version` и принимает common first line с `java version` или
`openjdk version`. Возвращает version и, при наличии, bounded runtime/VM lines.
Учитываются stdout и stderr, потому что Java часто пишет version в stderr.

Malformed, non-UTF-8, truncated, nonzero или missing executable output
отклоняется, а не публикуется как opaque text.

### java processes

Перечисляет до 32 768 safe numeric dirs в `/proc`. Process считается Java, если
`comm` или basename argv zero точно равен `java` либо `javaw`. Record содержит
PID, executable и NUL-separated command line.

Values проходят recursive redaction. Sensitive options с markers password,
token, secret, credential, authorization, API/access/private key маскируются.
Для split option маскируется следующий argument, URL userinfo удаляется.

Unsafe procfs entries, excessive list и per-process read failure добавляют
structured error, сохраняя остальные processes.

### java inspect

Принимает decimal PID `1..4194304`, затем запускает:

```text
jcmd PID VM.version
jcmd PID VM.command_line
jcmd PID VM.system_properties
```

Каждый response обязан начинаться с header запрошенного PID. Result содержит
version text, redacted command lines и sorted system properties. Sensitive
property names имеют masked values; остальные values также проходят URL и
recursive redaction.

Arbitrary `jcmd` operation и artifact не поддерживаются.

## Как это работает

Последовательность bounded и deterministic:

1. Проверяется command shape и decimal PID.
2. Local executables разрешаются trusted resolver и запускаются с fixed argv.
3. Для inventory проверяются root/procfs, symlink entries отклоняются, PIDs
   сортируются, читаются `comm` и `cmdline`.
4. Три allowlisted operations вызываются по порядку; failed, truncated,
   malformed, mismatched-PID и non-UTF-8 output отклоняется.
5. Command lines/properties редактируются до sorting и Result v1.

Runtime output ограничен 64 KiB на stream, `comm` — 256 bytes, `cmdline` —
64 KiB. `jcmd` stdout ограничен 256 KiB, stderr — 64 KiB.

## Доступ к данным

Плагин может читать:

- локальный `/proc`;
- `/proc/PID/comm`;
- `/proc/PID/cmdline`.

Он запускает trusted `java` и `jcmd` с указанными fixed arguments. `jcmd`
локально attach к JVM средствами OS; сам плагин не открывает network connection
и не читает JVM credentials.

## Результаты и коды завершения

Используется Result schema v1. Runtime/inspect дают pass. Process inventory
даёт pass при найденных JVM и info при пустом результате. Partial procfs issues
возвращаются structured errors рядом с safe inventory.

Missing `java`, inaccessible procfs или missing `jcmd` дают exit 4. Invalid PID
или invocation — exit 2. Attach denied — exit 3. Другие safe inspection
failures — exit 1. Cancellation/timeout пробрасываются.

## Конфигурация

В версии 1 нет plugin YAML. Names executables, procfs limits, PID range,
classification, secret markers, `jcmd` operations и output limits встроены.

Единственный plugin input — PID для `java inspect`. Standard host timeout,
output, JSON, policy, audit и redaction действуют.

## Что можно изменить

Оператор может штатно выбрать installed default `java`, указать visible PID и
использовать identity, авторизованный для attach. Наблюдаемые JVM options и
properties меняются вне read-only плагина.

Новые operations, detection rules, limits или config требуют reviewed source,
adversarial tests и нового immutable release.

## Зафиксированное поведение

Инвентаризируются только exact `java`/`javaw`. PID range, procfs namespace, три
`jcmd` operations, parsing, limits, sorting и secret markers зафиксированы.

Нельзя запросить JVM option, arbitrary command, namespace, container, endpoint,
URL, credential, output path или diagnostic artifact.

## Безопасность

Все команды diagnostic, без mutation, confirmation, force, dry-run и root
requirement. PID validation предотвращает option smuggling. Procfs root/entries
должны быть absolute clean non-symlink directories, reads ограничены.

Executables trusted и запускаются без shell. Output strict UTF-8 и bounded.
Sensitive options/properties, URL userinfo, errors и Result проходят redaction.

## Диагностика проблем

- Exit 4 runtime означает отсутствие trusted `java` или valid bounded output.
- Exit 4 processes означает missing/unsafe/inaccessible procfs. Успешный empty
  list не является ошибкой.
- Исчезнувшие во время enumeration PIDs могут дать structured errors; обычный
  process exit race безопасно пропускается.
- Exit 2 inspect означает PID вне clean decimal range.
- Exit 3 означает attach denied; используйте authorized identity, не ослабляя
  filesystem/process permissions.
- Exit 1 возможен при exited/non-attachable process, wrong PID header,
  malformed или oversized output.

## Ограничения и TODO

Версия 1 local-only. Remote JMX, service endpoints, credentials, TLS, container
namespaces, heap/thread dumps, JFR и arbitrary diagnostics остаются TODO. Нужны
endpoint allowlists, TLS identity, credential isolation, PID-reuse protection,
operation policy, output budgets, private artifacts, confirmation и cleanup.

См. [roadmap java-base](../../roadmap/java-base.md).

## Совместимость и исходный код

Страница описывает `java-base` 1.0.0 и plugin protocol v1. Signed catalog
остаётся источником host compatibility и release metadata.

Реализация: [internal/java](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/java).
