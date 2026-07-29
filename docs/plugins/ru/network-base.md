---
schema_version: 1
plugin_id: network-base
locale: ru
documented_version: 1.0.0
title: Диагностика сети и локальных TLS-сертификатов
summary: Просмотр локальных интерфейсов, маршрутов, listeners и файлов сертификатов без сетевых подключений.
command_paths:
  - network overview
  - network interfaces
  - network routes
  - network listeners
  - tls inspect
local_only: true
---

## Назначение и поддерживаемые сценарии

`network-base` предоставляет read-only представление сети, видимой в локальном
пространстве имён операционной системы плагина. Он инвентаризирует интерфейсы,
маршруты ядра и слушающие TCP- и UDP-сокеты, а также разбирает локальные PEM-
или DER-файлы сертификатов и возвращает ограниченный набор публичных полей.

Версия 1 предназначена для диагностики хоста и offline-проверки срока
сертификата. Она не подключается к адресам, не разрешает DNS, не проверяет
доступность или удалённый TLS и не изменяет сетевую конфигурацию.

## Быстрый старт

Собрать все локальные сетевые разделы:

```text
ohtools network overview
```

Запросить отдельный раздел:

```text
ohtools network interfaces
ohtools network routes
ohtools network listeners
```

Проверить сертификаты в существующем локальном файле:

```text
ohtools tls inspect /etc/ssl/certs/example.pem
```

## Команды

### network overview

Независимо запускает collectors интерфейсов, маршрутов и listeners. Успешные
разделы попадают в `data.interfaces`, `data.routes` и `data.listeners`.
Недоступный раздел формирует skipped check и структурированную dependency
error, сохраняя остальные данные в partial Result.

### network interfaces

Сначала читает счётчики из procfs и дополняет интерфейс состоянием и MAC из
sysfs. При ошибке разбора procfs запускает фиксированную команду
`ip -j address show`. Результат сортируется по имени, а адреса — по семейству и
значению. Путь только через procfs не может вернуть IP-адреса.

### network routes

Сначала читает локальную IPv4-таблицу маршрутов из procfs. При недоступном или
некорректном источнике запускает `ip -j route show table all`. Записи содержат
проверенные family, destination, необязательные gateway/interface и metric и
сортируются детерминированно.

### network listeners

Разбирает локальные таблицы `tcp`, `tcp6`, `udp` и `udp6` в procfs. Возвращает
только слушающие TCP-сокеты и неподключённые UDP-сокеты. Если ни один procfs
источник непригоден, используется `ss -H -l -n -t -u`. Имена сервисов и
удалённые peers не разрешаются.

### tls inspect

Принимает один очищенный абсолютный путь. Путь не может начинаться с option,
содержать управляющие символы или symlink и обязан указывать на regular file.
Допустим DER-сертификат или PEM-блоки сертификатов размером суммарно до 1 MiB.
Любой PEM-блок private key отклоняет весь файл.

Для каждого сертификата возвращаются subject, issuer, serial, сроки действия,
отсортированные DNS names, CA flag и SHA-256 DER-байтов. Check получает warning
для истёкшего или ещё не действующего сертификата и pass в остальных случаях.
Цепочка доверия, trust store, revocation, key usage и hostname не проверяются.

## Как это работает

Collectors используют confined local reads и ограниченные parsers:

1. Счётчики интерфейсов берутся из `/proc/net/dev`, state и MAC — из
   `/sys/class/net`.
2. Маршруты берутся из `/proc/net/route`.
3. Listeners берутся из четырёх локальных socket tables procfs.
4. Если общий procfs-путь недоступен, пробуется `/proc/PID/net` самого плагина.
5. Локальные `ip` и `ss` с фиксированным argv используются только как fallback.
6. Значения проверяются, рекурсивно редактируются и сортируются до построения
   Result v1.

Чтение procfs и stdout fallback ограничено 1 MiB, stderr — 64 KiB. Усечённый,
некорректный или nonzero output отклоняется. Для TLS сравниваются метаданные
файла до и после открытия, чтобы обнаружить подмену.

## Доступ к данным

Плагин может читать:

- `/proc/net/dev`, `/proc/net/route` и четыре socket tables;
- соответствующие `/proc/PID/net` файлы собственного процесса;
- `/sys/class/net/NAME/operstate` и `/sys/class/net/NAME/address`;
- переданный `tls inspect` абсолютный файл сертификата.

Он может разрешить и запустить локальные `ip` и `ss` с фиксированным argv.
Shell и сетевой трафик не используются.

## Результаты и коды завершения

Все команды возвращают Result schema v1 с нормализованными массивами `checks`,
`changes` и `errors`. Узкие сетевые команды возвращают exit 4, если обязательны
локальный источник и fallback executable недоступны. `network overview`
сохраняет успешные разделы, а ошибки представляет skipped checks и dependency
errors, обычно формируя partial status.

`tls inspect` использует exit 2 для неверного пути, небезопасного или слишком
большого файла, private-key материала и некорректного сертификата. Cancellation
и timeout остаются фатальными context errors. В JSON-режиме stdout содержит
только Result.

## Конфигурация

В версии 1 нет настраиваемых YAML-параметров. Источники, byte limits, fallback
команды, сортировка и TLS validation встроены в плагин. Глобальные настройки
host для timeout, output, JSON и policy продолжают действовать.

Единственный специфичный input — путь сертификата для `tls inspect`; команды
сетевой инвентаризации не принимают arguments и plugin flags.

## Что можно изменить

Оператор может выбрать узкую команду, указать локальный файл сертификата и
изменить само наблюдаемое состояние хоста. Стандартные host controls меняют
timeout и представление результата.

Новые источники, правила проверки и конфигурация появляются только через
проверенное изменение кода, adversarial tests и новый immutable release.

## Зафиксированное поведение

Пути procfs/sysfs, argv `ip` и `ss`, лимиты, классификация listeners, redaction
и сортировка зафиксированы в 1.0.0. Нельзя передать через конфигурацию интерфейс,
namespace, route table, remote host, URL, DNS name, proxy или credentials.

Проверка сертификата только синтаксическая и временная. Pass не доказывает
доверие сертификату для конкретного сервиса.

## Безопасность

Все manifest-команды diagnostic и read-only, без требований root,
confirmation, force и dry-run. Confined readers отклоняют traversal и symlink.
External tools разрешаются как доверенные абсолютные executables и запускаются
напрямую с фиксированными аргументами и ограниченным output.

Subject, issuer, DNS names, сетевые значения и ошибки проходят redaction.
Certificate parser никогда не возвращает и не принимает private key как
допустимое содержимое.

## Диагностика проблем

- При exit 4 проверьте доступность procfs и наличие доверенных `ip` или `ss`.
- При partial overview найдите недоступный collector в structured errors;
  остальные разделы остаются пригодны.
- Пустой список адресов ожидаем, когда успешно прочитан `/proc/net/dev`: этот
  файл содержит счётчики, но не адреса.
- Для TLS используйте канонический абсолютный путь без symlink и файл до 1 MiB.
- Удалите private keys и trailing non-PEM text из certificate bundle.
- Expired warning зависит от часов хоста; сначала исключите неверное время.

## Ограничения и TODO

Версия 1 local-only. Remote reachability, DNS, authenticated probes, удалённый
TLS handshake, проверка цепочки и hostname, proxy и credentials остаются TODO.
Для них потребуются endpoint allowlists, защита от DNS rebinding, redirect
policy, bounded timeouts и host-owned credential references.

Полное получение адресов без `ip` также отложено. См.
[roadmap network-base](../../roadmap/network-base.md).

## Совместимость и исходный код

Страница описывает `network-base` 1.0.0 и plugin protocol v1. Signed catalog
остаётся источником совместимости host и release metadata.

Реализация: [internal/network](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/network).
