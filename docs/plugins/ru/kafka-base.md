---
schema_version: 1
plugin_id: kafka-base
locale: ru
documented_version: 1.0.0
title: Базовая диагностика Kafka
summary: Проверка локальных пакетов, процессов, service, безопасных настроек и storage metadata Kafka.
command_paths:
  - kafka status
  - kafka config
  - kafka storage
local_only: true
---
# Базовая диагностика Kafka

## Назначение и поддерживаемые сценарии

`kafka-base` выполняет read-only локальную диагностику Kafka на Debian и
Ubuntu. Он сопоставляет фиксированные package names, metadata Java-процессов,
`kafka.service`, встроенные пути server properties, локальные log directories
и optional Kafka storage-info tool.

Плагин помогает подтвердить наличие локальной установки broker, просмотреть
небольшой allowlist несекретных operational settings и оценить форму локального
storage. Версия 1 не открывает соединение с broker или controller.

## Быстрый старт

```text
ohtools kafka status
ohtools kafka config
ohtools kafka storage
```

У команд нет аргументов и plugin flags. Они диагностические, не требуют root
или подтверждения и никогда не изменяют broker state.

## Команды

### kafka status

Читает ограниченный `/var/lib/dpkg/status` для установленных пакетов `kafka`,
`confluent-kafka` и `confluent-server`. Просматривает до 4096 procfs entries и
определяет только Java-процессы, NUL-разделённая command line которых содержит
точный class `kafka.Kafka` или `kafka.server.KafkaServer`. Arguments нужны для
классификации, но никогда не возвращаются.

Также запрашиваются load, active и sub state фиксированного `kafka.service`.
Result содержит installed state, безопасные package versions, отсортированные
PID и counts, а также доступные unit fields.

### kafka config

Читает все существующие файлы из встроенного списка:

- `/etc/kafka/server.properties`;
- `/etc/kafka/kraft/server.properties`;
- `/opt/kafka/config/server.properties`;
- `/opt/kafka/config/kraft/server.properties`.

Плагин разбирает bounded UTF-8 Java properties и возвращает только корректные
значения `node.id`, `broker.id`, `num.partitions`,
`default.replication.factor`, `min.insync.replicas`,
`auto.create.topics.enable` и `process.roles`. Остальные keys и values, включая
listeners, authentication, credentials и remote endpoints, исключаются.

### kafka storage

Выбирает первый существующий config во встроенном порядке, строго читает
`log.dirs` или `log.dir` и отклоняет конфликтующие, повторяющиеся,
неабсолютные, похожие на секрет, корневые и неочищенные пути. Каждый компонент
проверяется внутри локального root без symlinks; в каждом из максимум 16 log
directories считается не более 4096 непосредственных entries.

Затем по очереди пробуются:

```text
kafka-storage.sh info --config <fixed-config-path>
kafka-storage info --config <fixed-config-path>
```

Возвращается только количество строк `Found metadata:`. Storage tool optional:
сведения о каталогах сохраняются, если executable отсутствует.

## Как это работает

Все команды используют read-only plan protocol v1. Status объединяет три
локальных источника и считает systemd probe optional. Config принимает только
однострочные properties с `=` или `:`, запрещает continuations и duplicate
keys, нормализует лишь allowlisted booleans, integers и роли broker/controller.

Storage повторно использует строгий parser, confined каждый absolute Unix path
под локальным probe root, проверяет неизменность открытого каталога и запускает
точный storage argv только с одним встроенным config path. Коллекции
детерминированно сортируются.

Лимиты: 4 МиБ для dpkg status, 512 КиБ и 8192 lines для properties, 64 КиБ на
process command line, 256 КиБ stdout storage-info, 16 log directories и 4096
entries на каталог.

## Доступ к данным

Плагин читает `/var/lib/dpkg/status`, ограниченные `/proc/{pid}/comm` и
`/proc/{pid}/cmdline`, четыре фиксированных Kafka properties и настроенные
локальные log directories.

Он может выполнить только фиксированный `systemctl show` для `kafka.service`
и `kafka-storage.sh` либо `kafka-storage` `info --config` с одним встроенным
config path. Guarded runner отклоняет stdin, environment overrides, другие
program и argv. Shell, broker socket, repository, remote endpoint и
caller-selected path не используются.

## Результаты и коды завершения

`kafka status` возвращает `kafka.installation` как pass или info и
`kafka.systemd` как pass или skipped. Отсутствие optional systemd metadata даёт
dependency error, не отбрасывая package и process evidence.

`kafka config` возвращает `kafka.config`, пути файлов, safe settings map и
количество файлов. Отсутствие поддерживаемого config, unsafe file или malformed
properties используют dependency exit mapping.

`kafka storage` возвращает `kafka.storage.directories` и
`kafka.storage.info`. Unsafe configuration или storage paths используют
dependency mapping; отсутствие optional storage executable даёт skipped check.
Cancellation и timeout передаются host. JSON output содержит только Result v1.

## Конфигурация

В версии 1 нет `/etc/ohtools/plugins/kafka-base.yaml` и настраиваемых probe
paths или limits. Проверяемые Kafka properties являются входом продукта, а не
конфигурацией плагина.

Минимальная локальная storage-настройка, распознаваемая диагностикой:

```properties
log.dirs=/var/lib/kafka
```

Изменение Kafka properties может повлиять на broker. `kafka-base` только
наблюдает сохранённый файл и никогда не редактирует и не применяет его.

## Что можно изменить

Оператор может управлять фиксированными Kafka packages, service, properties и
log directories штатным deployment-процессом. Безопасные operational values в
этих properties будут отражены `kafka config` после validation.

Плагин не даёт override для paths, packages, service, executable или limits.
Смена этих контрактов требует изменения source, тестов и нового immutable
release.

## Зафиксированное поведение

Package names, Java class markers, `kafka.service`, properties paths,
safe-setting keys и ranges, правила log directories, storage executables, argv
и лимиты зафиксированы в 1.0.0.

Плагин не принимает bootstrap servers, topic или group names, credentials,
произвольные config paths, log paths как arguments или administrative
operations. Он не форматирует storage и разбирает raw storage output только по
малой встроенной grammar.

## Безопасность

Все команды локальные, read-only и non-root. External commands используют
точные argv, пустой input environment и bounded output. Пути должны быть clean,
absolute, confined и non-symlinked. Opened directories сравниваются с path
metadata для снижения replacement races.

Raw Java argv, полные properties, storage-tool output, repository или broker
addresses, SASL/JAAS, keystore и truststore data и credentials не возвращаются.
Sensitive-looking storage paths отклоняются, Result рекурсивно редактируется
host.

## Диагностика проблем

- Если status не читает package или procfs metadata, проверьте file type,
  symlinks, permissions, size limits и количество procfs entries.
- Skipped systemd check не отменяет package и process evidence; отдельно
  проверьте фиксированный `kafka.service`.
- При ошибке config проверьте UTF-8, duplicate keys, continuations, assignments,
  лимит 512 КиБ и наличие хотя бы одного встроенного пути.
- При ошибке storage задайте ровно один из `log.dirs` и `log.dir`, используйте
  не более 16 clean absolute non-root paths, удалите symlinks и проверьте entry
  limits.
- Если storage info skipped, установите trusted compatible local tool; download
  и remote lookup не выполняются.

## Ограничения и TODO

Версия 1 намеренно local-only. Broker и controller connections, cluster
metadata, topics, partitions, consumer groups, quorum diagnostics,
administrative mutations, downloads, remote execution, дополнительные layouts
и container attribution отложены. Сетевой режим потребует policy-pinned
endpoints, TLS и SASL credential isolation, DNS-rebinding и proxy controls,
bounded responses и явно read-only operations.

Смотрите актуальный [roadmap Kafka](../../roadmap/kafka-base.md).

## Совместимость и исходный код

Страница описывает `kafka-base` 1.0.0 и plugin protocol v1. Подписанный каталог
остаётся источником истины для установки, minimum host version, asset size,
SHA-256 и release history.

Реализация: [internal/kafka](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/kafka).
