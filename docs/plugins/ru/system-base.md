---
schema_version: 1
plugin_id: system-base
locale: ru
documented_version: 1.0.1
title: Базовая диагностика системы
summary: Сбор сведений о локальной ОС и оценка состояния сервера ограниченными системными probes.
command_paths:
  - system info
  - system health
local_only: false
---
# Базовая диагностика системы

## Назначение и поддерживаемые сценарии

`system-base` формирует нормализованный снимок локального Linux-сервера и
выполняет read-only проверки поддержки ОС, нагрузки на память и файловые
системы, синхронизации времени, необходимости перезагрузки, failed systemd
units и недавних событий нехватки памяти.

Плагин подходит для инвентаризации сервера, первичной диагностики и
детерминированной сводки health. Он не меняет сервер, не перезапускает службы,
не очищает диски и не исправляет проваленные проверки.

## Быстрый старт

Сбор сведений об ОС и ресурсах:

```text
ohtools system info
```

Запуск настроенной health policy:

```text
ohtools system health
```

Result v1 JSON для автоматизации:

```text
ohtools --json system health
```

Команды не принимают позиционные аргументы или собственные flags.

## Команды

### system info

Собирает ID и выпуск ОС, kernel, архитектуру, hostname, uptime, модель CPU и
число логических ядер, объёмы памяти и swap, load average, признак
виртуализации, timezone, состояние NTP и признак требуемой перезагрузки. Эта
команда использует встроенные collectors и не загружает `system-base.yaml`.

Ошибка отдельного probe не удаляет остальные данные. Result становится
`partial` и содержит structured errors для недоступных полей.

### system health

Загружает и проверяет health thresholds, собирает тот же системный снимок и
получает сведения о файловых системах через storage collector. Формируются
checks `os`, `memory`, `load`, `disk:MOUNT`, `time-sync`,
`reboot-required`, `failed-units` и `oom`.

В версии 1.0.1 проверка ОС проходит только для Debian 12 и Debian 13. Для
остальных обнаруженных систем check `os` становится critical; это правило не
равно более широкой матрице дистрибутивов, на которых распространяется host.

## Как это работает

Collector выполняет независимые локальные probes, поэтому недоступность одного
источника даёт partial result, не скрывая успешные наблюдения:

1. Разбирает identity из `/etc/os-release`, а выпуск kernel — из procfs.
2. Читает hostname, uptime, CPU, память, swap и load average.
3. Определяет контейнер или hypervisor по marker-файлам, cgroups, DMI и затем
   через `systemd-detect-virt`, если команда доступна.
4. Определяет timezone по `/etc/timezone`, `/etc/localtime` или часам процесса
   и проверяет reboot-required marker.
5. Через `timedatectl` проверяет синхронизацию NTP.
6. Для health перечисляет реальные mounts и сравнивает disk и memory с
   настроенными процентами.
7. Запрашивает failed systemd units и ищет сообщения OOM в kernel journal за
   указанное окно.

Внешние программы разрешаются доверенным system resolver и вызываются напрямую
с фиксированным argv. Stdout и stderr каждого запуска ограничены 1 MiB.

## Доступ к данным

Плагин читает локальные источники:

- `/etc/os-release`, `/etc/timezone` и `/etc/localtime`;
- `/proc/sys/kernel/osrelease`, `/proc/uptime`, `/proc/cpuinfo`,
  `/proc/meminfo`, `/proc/loadavg`, `/proc/1/cgroup` и
  `/proc/self/mountinfo`;
- `/.dockerenv`, `/run/.containerenv`, сведения DMI и
  `/var/run/reboot-required`;
- счётчики объёма и inode файловых систем через `statfs`;
- `/etc/ohtools/plugins/system-base.yaml` только для `system health`.

Могут выполняться `systemd-detect-virt`, `timedatectl`, `systemctl` и
`journalctl`. Все вызовы read-only, без shell и `sudo`.

## Результаты и коды завершения

Обе команды возвращают нормализованный Result schema v1 с tool identity, host,
timestamp, duration и всегда присутствующими массивами checks, changes и
errors. `system info` помещает снимок в `data.system`. В `system health`
добавляется `data.filesystems`.

Агрегация health выбирает `critical`, если есть critical check, затем
`partial` при ошибке probe или skipped check, затем `warning`, иначе `pass`.
Load имеет информационный статус и не сравнивается с threshold. Недавний OOM
даёт critical; failed units, несинхронизированное время и требуемая
перезагрузка дают warning.

Ошибочные аргументы используют exit 2, неверный plugin config — exit 10.
Статус Result отображается host в стабильные warning, critical, partial,
timeout и cancellation exits. Отсутствующие optional diagnostic executables
возвращаются как structured dependency failures, без panic.

## Конфигурация

Необязательная системная конфигурация:

```text
/etc/ohtools/plugins/system-base.yaml
```

Значения по умолчанию:

```yaml
disk_warning: 80
disk_critical: 90
memory_warning: 85
memory_critical: 95
oom_window: 24h
```

Warning и critical проценты должны находиться от 0 до 100, причём warning
строго меньше critical. `oom_window` должен быть положительной Go duration,
например `30m`, `24h` или `168h`.

Файл ограничен 1 MiB и использует строгий single-document YAML. Неизвестные и
повторяющиеся keys, symlink, не regular file, владелец не root или разрешение
на запись для группы/остальных дают exit 10. Host `--config`, пользовательские
файлы, XDG paths и environment не переопределяют этот путь.

## Что можно изменить

Оператор может настроить warning/critical проценты диска и памяти, а также окно
поиска недавних OOM. Более низкие thresholds повышают чувствительность health,
а более длинное окно увеличивает просматриваемую историю journal.

Host policy может ограничивать запуск команд и задавать общий timeout.
Сопровождающие меняют collectors или checks только через reviewed source,
тесты и новый immutable release плагина.

## Зафиксированное поведение

Источники данных, ID checks, порядок агрегации health, поиск файловых систем,
эвристики виртуализации и правило поддерживаемой ОС встроены в версию 1.0.1.
Через YAML нельзя добавить произвольные пути, команды, health checks, remote
hosts или shell fragments.

Команды только диагностические. Нет mutation plan/execute, confirmation,
очистки или настраиваемого threshold для load average.

## Безопасность

Manifest помечает обе команды как diagnostic, без root, confirmation, force и
dry-run, потому что mutation отсутствует. Фиксированные имена executable и argv
передаются без shell. Вывод внешних команд ограничен, ошибки возвращаются через
Result v1.

Плагин не запускает `sudo`, не принимает credentials и намеренно не читает
секретную конфигурацию. Обычные permissions ОС всё равно применяются к journal
и systemd; недоступные probes дают partial или skipped.

## Диагностика проблем

- Если `system info` имеет статус partial, найдите в `errors` недоступный файл
  или executable и проверьте соответствующий procfs, systemd либо timezone
  source.
- Если `os` critical на системе, поддерживаемой host-пакетом, учитывайте, что
  версия 1.0.1 пропускает health check только для Debian 12/13.
- Если memory skipped, проверьте `MemTotal` и `MemAvailable` в
  `/proc/meminfo`.
- Если disk checks skipped, проверьте `/proc/self/mountinfo` и доступ к mount.
- Если skipped `time-sync`, `failed-units` или `oom`, восстановите нужный
  systemd executable и доступ к journal.
- При exit 10 проверьте порядок thresholds, положительную duration, YAML keys,
  ownership и permissions.

## Ограничения и TODO

Текущие probes работают только с локальным сервером, хотя published metadata
не относит этот исторический плагин к новой local-only волне. Remote collection
отложен до проектирования authenticated transport, host identity, изоляции
credentials, endpoint pinning и ограниченных responses.

Версия 1.0.1 не оценивает CPU saturation, load относительно числа ядер,
сетевую доступность, обновления пакетов и application health. Актуальный
backlog находится в [roadmap системы](../../roadmap/system-base.md).

## Совместимость и исходный код

Страница описывает `system-base` 1.0.1 и plugin protocol v1. Подписанный каталог
остаётся источником истины для установки, minimum host version, размера asset,
SHA-256 и release history.

Реализация: [internal/system](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/system).
