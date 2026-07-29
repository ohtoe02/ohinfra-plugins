---
schema_version: 1
plugin_id: apt-base
locale: ru
documented_version: 1.0.0
title: Диагностика локальных APT и dpkg
summary: Проверка dpkg, локальной upgrade simulation, настроенных sources и текущей APT history.
command_paths:
  - apt status
  - apt updates
  - apt sources
  - apt history
local_only: true
---

## Назначение и поддерживаемые сценарии

`apt-base` предоставляет read-only диагностику package state Debian и Ubuntu.
Он считает установленные и незавершённые dpkg records, моделирует upgrades по
уже доступным APT-данным, инвентаризирует локальные sources и сворачивает
текущую APT transaction history.

Версия 1 не обновляет repositories, не скачивает packages и не выполняет
install, remove или upgrade. Она предназначена для локального triage перед
отдельно авторизованной package operation.

## Быстрый старт

Проверить согласованность package database:

```text
ohtools apt status
```

Смоделировать upgrades без locks и downloads:

```text
ohtools apt updates
```

Просмотреть sources и текущую history:

```text
ohtools apt sources
ohtools apt history
```

## Команды

### apt status

Читает `/var/lib/dpkg/status` и отдельно считает records со статусом
`install ok installed` и все остальные package records. Также считает до 1 024
regular entries в `/var/lib/dpkg/updates`.

Check `apt:dpkg-status` получает pass при отсутствии incomplete records и
warning иначе. Ошибка optional updates directory добавляет structured error;
отсутствующий каталог означает ноль pending records.

### apt updates

Запускает точную локальную simulation:

```text
apt-get --simulate --no-download --option Debug::NoLocking=true upgrade
```

Только stdout lines с `Inst` превращаются в package, installed version и
candidate version. Package bytes не скачиваются, repository metadata не
обновляются, dpkg lock не берётся.

### apt sources

Читает `/etc/apt/sources.list`, затем не более 256 regular `.list` или `.sources`
файлов непосредственно в `/etc/apt/sources.list.d`. Разбирает классические
`deb`/`deb-src` lines и базовые deb822 поля types, URIs, suites и components.

Перед output из URI удаляются userinfo, query и fragment. Некорректные URI
заменяются marker. Symlinks, non-regular entries, другие extensions, comments и
malformed records игнорируются.

### apt history

Читает только текущий `/var/log/apt/history.log`. Transaction содержит start
date, необязательный end date и counts для install, upgrade, remove, purge,
downgrade и reinstall. Package names и command lines в summary не публикуются.

Отсутствующая history даёт корректный пустой Result. Unreadable или oversized
file добавляет structured error и partial.

## Как это работает

Плагин использует bounded local sources:

1. Dpkg status сканируется как package stanzas с лимитом 32 MiB.
2. Update fragments и source files перечисляются без symlinks.
3. Source files читаются до 1 MiB каждый и сортируются по path.
4. Trusted `apt-get` запускается напрямую с fixed argv и лимитом 1 MiB на stdout
   и stderr.
5. Текущий history file читается до 4 MiB и сворачивается в action counts.
6. Result v1 получает стабильные checks и recursively redacted errors.

APT output не исполняется и не передаётся shell. Parser simulation принимает
только ожидаемый префикс `Inst`.

## Доступ к данным

Плагин может читать:

- `/var/lib/dpkg/status`;
- regular records в `/var/lib/dpkg/updates`;
- `/etc/apt/sources.list`;
- `.list` и `.sources` в `/etc/apt/sources.list.d`;
- `/var/log/apt/history.log`;
- локальные APT lists и package state косвенно через simulation.

Единственный executable — локальный trusted `apt-get` с fixed arguments. Плагин
не выводит repository credentials и не подключается к repository.

## Результаты и коды завершения

Команды возвращают Result schema v1. Incomplete dpkg state даёт warning.
Unreadable optional sources/history дают structured errors и partial, сохраняя
безопасные данные.

`apt status` возвращает exit 4 при отсутствии основной dpkg database и exit 1
при другой fatal inspection error. `apt updates` даёт exit 4 при missing
`apt-get` и exit 1 при failed/unsafe simulation. Cancellation и timeout
пробрасываются. Invalid invocation даёт exit 2.

Наличие upgrade candidates является inventory data и само по себе не означает
mutation или failure.

## Конфигурация

В версии 1 нет plugin YAML и command-specific arguments. Paths, limits, action
names, source fields и simulation argv встроены.

Стандартные host controls для timeout, JSON, output, policy, audit и redaction
действуют, но не превращают плагин в updater.

## Что можно изменить

Оператор может отдельно авторизованным OS workflow изменить source
declarations, package lists, cache/state или history и повторить диагностику.

Archive support, другая simulation policy, новые fields или repository access
требуют изменения source, security tests и нового immutable release.

## Зафиксированное поведение

`--simulate`, `--no-download` и `Debug::NoLocking=true` обязательны в 1.0.0.
Нельзя передать package names, repository URLs, proxy settings, keys,
credentials, arbitrary APT options или shell fragments.

Читается только текущий uncompressed history. Sources суммируются без полной
semantic validation; URI userinfo/query/fragment всегда удаляются.

## Безопасность

Все команды diagnostic/read-only, без root, confirmation, force и dry-run.
Readers отклоняют traversal и symlink и ограничивают file count/size. External
execution использует trusted absolute executable, fixed argv, no shell,
bounded output и не принимает caller environment options.

Raw APT output не попадает в Result. URI credentials и query удаляются до
recursive redaction.

## Диагностика проблем

- Exit 4 status означает недоступную dpkg database; проверьте платформу и
  regular readable file.
- Warning `apt:dpkg-status` означает incomplete package или pending update
  records; используйте штатную recovery procedure дистрибутива.
- Exit 4 updates означает отсутствие trusted `apt-get`.
- Failed simulation может означать stale или inconsistent local metadata;
  плагин не обновит их.
- Partial sources указывает на unreadable, unsafe, oversized или excessive
  files.
- Empty history ожидаема без текущего log; compressed rotated logs не читаются.

## Ограничения и TODO

Версия 1 local-only. Repository refresh, downloads, signature provenance,
package mutations, remote mirrors и proxy остаются TODO. Нужны allowlists,
signature policy, proxy isolation, redirect/timeout limits, planning,
confirmation и rollback.

Bounded compressed history также отложена. См.
[roadmap apt-base](../../roadmap/apt-base.md).

## Совместимость и исходный код

Страница описывает `apt-base` 1.0.0 и plugin protocol v1. Signed catalog
остаётся источником host compatibility и release metadata.

Реализация: [internal/apt](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/apt).
