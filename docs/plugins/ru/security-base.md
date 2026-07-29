---
schema_version: 1
plugin_id: security-base
locale: ru
documented_version: 1.0.0
title: Диагностика локальной security posture
summary: Аудит локальных accounts, OpenSSH и firewall без выдачи hashes или raw rulesets.
command_paths:
  - security audit
  - security accounts
  - security ssh
  - security firewall
local_only: true
---

## Назначение и поддерживаемые сценарии

`security-base` собирает ограниченные метаданные security posture локального
хоста. Он находит дублирующиеся UID 0 и небезопасные режимы home directories,
проверяет выбранные authentication settings OpenSSH и сообщает, доступно ли
состояние nftables или UFW.

Плагин предназначен для read-only triage. Он не возвращает password hashes,
group passwords, полную SSH configuration и raw firewall rules. Accounts, SSH
и firewall не изменяются, remote scanner не вызывается.

## Быстрый старт

Запустить объединённый аудит:

```text
ohtools security audit
```

Проверить одну область с узкой exit-семантикой:

```text
ohtools security accounts
ohtools security ssh
ohtools security firewall
```

Manifest не требует root. Более привилегированный локальный запуск может
открыть shadow lock metadata или firewall probes, но команды остаются
read-only.

## Команды

### security audit

Последовательно запускает collectors accounts, SSH и firewall. Разделы
сохраняются в `data.accounts`, `data.ssh` и `data.firewall`. Недоступные
необязательные источники добавляют structured errors и превращают warning
checks в partial; usable partial data возвращается, если нет cancellation или
timeout.

### security accounts

Читает локальные passwd, group и при возможности shadow metadata. Возвращает
name, UID, GID, классификацию login shell, необязательный locked state, наличие
home и небезопасные group/world write bits. Для групп выдаются name, GID и
member count.

Checks предупреждают о нескольких accounts с UID 0 и writable home
directories. Password и group password поля не возвращаются. Shadow entry
сводится к boolean locked.

### security ssh

Читает allowlisted directives из `sshd_config` и не более 128 regular `.conf`
drop-ins, затем запускает
`sshd -T -C user=root,host=localhost,addr=127.0.0.1`. Если effective directives
доступны, checks используют их.

Сохраняются только authentication/posture keys: `PermitRootLogin`,
`PasswordAuthentication`, `PubkeyAuthentication`, `UsePAM` и ограниченный
набор связанных параметров. Warning формируется для root login `yes` или
password authentication `yes`.

### security firewall

Независимо запускает `nft list ruleset` и `ufw status`. Успешный output
сворачивается до available и line count; UFW дополнительно возвращает `active`.
Raw ruleset и command diagnostics не публикуются. Pass означает только наличие
хотя бы одного читаемого источника, а не корректность firewall policy.

## Как это работает

Порядок работы детерминирован:

1. Confined readers читают bounded identity/SSH files без symlinks.
2. Parsers проверяют форму records и сохраняют только безопасные metadata.
3. Account checks вычисляют duplicate UID 0 и home write findings.
4. `sshd`, `nft` и `ufw` запускаются напрямую с фиксированным argv.
5. Ошибки классифицируются как dependency, privilege или general без raw output.
6. При доступном `/etc/os-release` добавляются platform ID, version и support.
7. Result builder рекурсивно редактирует data, checks и errors.

Некорректные identity rows пропускаются со structured errors. Недоступный
shadow не отменяет account inventory. Узкой команде нужен usable source, а
aggregate audit сохраняет частичные данные.

## Доступ к данным

Плагин может читать:

- `/etc/passwd`, `/etc/group` и `/etc/shadow` с лимитом 256 KiB на файл;
- metadata home directories под authoritative local root;
- `/etc/ssh/sshd_config`;
- regular `/etc/ssh/sshd_config.d/*.conf` до 256 KiB каждый;
- `/etc/os-release`.

Он может запускать локальные `sshd`, `nft` и `ufw`. Private SSH keys и hashes не
читаются в Result, shell и сетевые запросы не используются.

## Результаты и коды завершения

Все команды возвращают Result schema v1 с нормализованными пустыми массивами.
Findings обычно имеют pass или warning. Потеря optional data добавляет
structured errors и может дать partial.

Узкие команды используют exit 4, если нет usable source. `security firewall`
использует exit 3, если все probes отклонены из-за privileges. Cancellation и
timeout фатальны. Неверные path, arguments или options дают exit 2. Aggregate
audit сохраняет partial evidence вместо ошибки только из-за optional source.

## Конфигурация

В версии 1 нет plugin YAML. Пути, directive allowlist, identity limits, лимит
drop-ins, argv и checks встроены. Команды не принимают plugin arguments/flags.

Стандартные host controls для output, JSON, timeout, policy и audit продолжают
действовать и не расширяют набор выдаваемых protected data.

## Что можно изменить

Оператор может штатно изменить accounts, home modes, OpenSSH и firewall state и
повторить аудит. Разрешённый запуск под другой identity может сделать protected
local metadata читаемыми.

Allowlist, findings, limits, sources и output shape меняются только в исходном
коде с тестами и новым immutable release.

## Зафиксированное поведение

Data minimization обязательна: hashes, group passwords, raw firewall rules,
неразрешённые SSH directives и command diagnostics исключены. Встроенные v1
findings — duplicate UID 0, home write bits, root login `yes` и password auth
`yes`.

Нельзя передать arbitrary file path, username, firewall command, SSH match
context, remote host, URL, credential или scanner endpoint.

## Безопасность

Все manifest-команды diagnostic, без mutation, force, confirmation, dry-run и
root requirement. Local readers отклоняют symlinks и oversized files. Home path
ограничивается root, symlink home не считается существующим.

Executables разрешаются до trusted absolute paths и вызываются напрямую.
Output ограничивается и проходит recursive redaction. Permission errors
классифицируются без исходного потенциально чувствительного текста.

## Диагностика проблем

- Exit 4 для accounts обычно означает missing, unreadable, symlinked или
  oversized `/etc/passwd`.
- `shadow_accessible: false` означает отсутствие lock metadata; безопасные
  passwd metadata могут оставаться корректными.
- При отсутствии effective SSH directives проверьте `sshd` и fixed `-T` query;
  file-based allowlist всё ещё может быть usable.
- Unsafe или excessive SSH drop-ins сообщаются, но не читаются.
- Exit 3 firewall означает privilege denial всех probes. Повторный запуск
  допустим только по privilege policy проекта.
- Firewall pass не подтверждает compliance; учитывайте UFW active и прочие
  controls.

## Ограничения и TODO

Версия 1 local-only. Remote scanners, reputation services, fleet comparison,
remediation, remote SSH и интерпретация firewall rules остаются TODO. Remote
расширению нужны endpoint policy, host-owned credentials, data minimization,
response limits и credential isolation.

См. [roadmap security-base](../../roadmap/security-base.md).

## Совместимость и исходный код

Страница описывает `security-base` 1.0.0 и plugin protocol v1. Signed catalog
остаётся источником host compatibility и release metadata.

Реализация: [internal/security](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/security).
