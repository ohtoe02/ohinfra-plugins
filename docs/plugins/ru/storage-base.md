---
schema_version: 1
plugin_id: storage-base
locale: ru
documented_version: 1.0.1
title: Базовая диагностика хранилища
summary: Отчёт об объёме, inode и настроенных порогах заполнения локальных файловых систем.
command_paths:
  - disk usage
local_only: false
---
# Базовая диагностика хранилища

## Назначение и поддерживаемые сценарии

`storage-base` предоставляет read-only представление локально смонтированных
файловых систем. Плагин показывает все непсевдо-filesystems либо выбирает
файловую систему, содержащую один указанный оператором путь. Объём, доступное
место, заполнение и inode нормализуются в Result v1.

Используйте его для поиска заполненных файловых систем, контроля thresholds и
определения mount, на котором находится путь приложения. Плагин не анализирует
отдельные файлы, не считает размеры каталогов, не очищает данные, не меняет
размер volumes и не управляет LVM/RAID.

## Быстрый старт

Показать все подходящие файловые системы:

```text
ohtools disk usage
```

Показать файловую систему, содержащую путь:

```text
ohtools disk usage /var/lib/docker/containers
```

Машиночитаемый результат:

```text
ohtools --json disk usage /var
```

Допускается не более одного пути.

## Команды

### disk usage

Без аргумента команда разбирает mount table текущего процесса, исключает
известные pseudo filesystems, удаляет повторяющиеся mount points, вызывает
`statfs` для каждого mount и сортирует результат.

С аргументом сначала разрешает symlinks, затем выбирает самый глубокий
mount-point prefix, содержащий разрешённый путь. Result описывает файловую
систему, а не запрошенный каталог. Несуществующий путь или путь вне mount table
даёт ошибку аргумента/сбора, а не вымышленное нулевое значение.

Каждая файловая система создаёт check `disk:MOUNT`. Настроенный warning или
critical percentage влияет и на check, и на общий статус Result.

## Как это работает

Collector читает `/proc/self/mountinfo` и строго разбирает separator, тип
файловой системы, source и escaped mount point. Декодируются Linux mount
escapes для пробелов, tab, newline и backslash.

Pseudo filesystem types, включая `proc`, `sysfs`, `tmpfs`, `devtmpfs`,
`cgroup`, `cgroup2`, `bpf` и `securityfs`, исключаются. Повторные mount points
игнорируются. Для указанного пути выбор самого длинного prefix не позволяет
родительскому mount скрыть вложенный.

Linux-счётчики `statfs` преобразуются в total, used и available bytes. Процент
used считается как used blocks, делённые на used плюс blocks, доступные
непривилегированному процессу, и ограничивается диапазоном 0–100. Также
возвращаются total и used inode.

## Доступ к данным

Плагин читает:

- `/proc/self/mountinfo`;
- метаданные для разрешения необязательного указанного пути;
- статистику выбранных mounts через системный вызов Linux `statfs`;
- `/etc/ohtools/plugins/storage-base.yaml`.

Плагин не запускает внешние команды или shell, не обходит содержимое каталогов,
не читает payload файлов и не обращается к сети.

## Результаты и коды завершения

Массив Result v1 `data.filesystems` содержит `source`, `mount_point`, `type`,
`total_bytes`, `used_bytes`, `available_bytes`, `used_percent`,
`total_inodes` и `used_inodes`. При ошибке `statfs` элемент также содержит
ошибку, skipped check и structured error `statfs_failed`.

Общий статус — `critical`, если файловая система достигла critical threshold,
`warning`, если достигнут только warning, `partial` при ошибке отдельного
элемента без critical, иначе `pass`. Checks и filesystems детерминированно
сортируются по mount point.

Лишние аргументы используют exit 2, strict configuration failures — exit 10.
Cancellation проверяется до сбора. Ошибка чтения/разбора mount table,
разрешения пути или выбора mount возвращается как command failure без
недостоверных данных об объёме.

## Конфигурация

Необязательная системная конфигурация:

```text
/etc/ohtools/plugins/storage-base.yaml
```

Значения по умолчанию:

```yaml
disk_warning: 80
disk_critical: 90
```

Оба процента должны быть в диапазоне 0–100, а `disk_warning` должен быть строго
меньше `disk_critical`.

Используется строгий single-document YAML до 1 MiB: root-owned, regular,
non-symlink, без group/world write. Неизвестные или повторные keys и неверные
trust metadata дают exit 10. User config, host `--config`, XDG paths и
environment игнорируются.

## Что можно изменить

Оператор может изменить warning/critical проценты и выбрать один существующий
путь в каждом запуске. Система мониторинга может использовать human rendering
или Result v1 JSON.

Host policy и filesystem permissions вызывающего процесса определяют
наблюдаемые пути и mounts. Сопровождающие могут добавить collectors или
поддержку storage stack только в протестированном новом release плагина.

## Зафиксированное поведение

Путь mount table, набор исключённых pseudo filesystems, формула процентов,
расчёт inode, разрешение symlinks, выбор longest prefix, ID checks и сортировка
встроены в версию 1.0.1.

Через YAML нельзя задать другие mount tables, ignore patterns, remote nodes,
внешние utilities, cleanup commands или произвольные filesystem paths. Mutation,
dry-run plan, confirmation и repair не реализованы.

## Безопасность

Manifest объявляет одну diagnostic-команду без root, confirmation, force и
dry-run. Сбор использует фиксированный procfs-файл и системный вызов `statfs`;
shell и subprocess отсутствуют.

Необязательный путь разрешается ОС и используется только для выбора mount.
Плагин не перечисляет и не меняет содержимое под этим путём. Ошибки сбора
остаются явными и не считаются здоровым нулевым заполнением.

## Диагностика проблем

- Если mount table не читается, проверьте, что procfs смонтирован и
  `/proc/self/mountinfo` доступен в текущем namespace.
- Если путь не разрешается, укажите существующий путь и проверьте permissions
  родительских компонентов.
- Если mount для пути не найден, проверьте namespace/mount visibility, а не
  только список mounts хоста.
- Skipped check `disk:MOUNT` означает ошибку `statfs`; подробность находится
  в связанном structured error.
- Если ожидаемая файловая система отсутствует, проверьте, не входит ли её тип в
  встроенный список pseudo filesystems.
- При exit 10 проверьте порядок thresholds, YAML keys, root ownership и
  permissions.

## Ограничения и TODO

Команда наблюдает только локальный mount namespace, хотя published metadata не
относит этот исторический плагин к новой local-only волне. Для remote
collection требуется проект authenticated host identity и bounded transport.

Отложены LVM topology, mdraid, SMART/NVMe health, thin-pool pressure, quotas,
подсчёт каталогов и cleanup. Optional adapters должны использовать fixed
executables, bounded output, явные privilege rules и partial status при
отсутствии stack. См. [roadmap хранилища](../../roadmap/storage-base.md).

## Совместимость и исходный код

Страница описывает `storage-base` 1.0.1 и plugin protocol v1. Подписанный
каталог остаётся источником истины для установки, minimum host version, размера
asset, SHA-256 и release history.

Реализация: [internal/storage](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/storage).
