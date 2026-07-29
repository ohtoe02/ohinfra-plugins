---
schema_version: 1
plugin_id: monitoring-base
locale: ru
documented_version: 1.0.0
title: Базовая диагностика мониторинга
summary: Инвентаризация локальных monitoring products и проверка bounded service, listener, process и config metadata.
command_paths:
  - monitoring status
  - monitoring inventory
  - monitoring config
local_only: true
---
# Базовая диагностика мониторинга

## Назначение и поддерживаемые сценарии

`monitoring-base` — детерминированный read-only inventory шести локальных
monitoring products: Prometheus, Prometheus node_exporter, Grafana Alloy,
Grafana Agent, Telegraf и Zabbix Agent. Он сопоставляет фиксированные Debian
packages, systemd units, process names, conventional listener ports и config
paths.

Плагин отвечает, какие поддерживаемые agents установлены, какие локальные
сигналы доступны и имеют ли фиксированные config files безопасную bounded
структуру. Версия 1 не получает metrics, не вызывает dashboards или remote APIs,
не следует targets и не возвращает raw configuration.

## Быстрый старт

```text
ohtools monitoring status
ohtools monitoring inventory
ohtools monitoring config
```

Все команды диагностические и не принимают аргументы. Root, confirmation,
force и dry-run не требуются.

## Команды

### monitoring status

Запускает одновременно inventory и config collection, возвращая массивы
`products` и `configs`. Product presence checks сохраняются, если optional
package, process, listener, unit или config probe неполна. Unsafe или malformed
config metadata добавляет product-specific partial checks и structured
configuration errors.

### monitoring inventory

Для каждого встроенного product возвращает ID и name, installed state, один
совпавший package с безопасной version, active/sub state unit, process count,
conventional listening ports из локальной socket table и наличие одного
фиксированного config path.

Installed означает наличие хотя бы одного local signal: allowlisted package,
matching process, loaded fixed unit или readable fixed config. Conventional
port — только listener evidence; версия 1 не приписывает socket конкретному
process.

### monitoring config

Читает каждый существующий фиксированный config до 256 КиБ и возвращает только
product ID, path, format, size и structural validity. Поддерживаются bounded
YAML-like, TOML assignments, environment assignments, properties assignments и
balanced Alloy-like shapes. Fields и values не возвращаются, полная product
semantic validation не заявляется.

## Как это работает

Встроенная product matrix:

- Alloy: package и process `alloy`, `alloy.service`, port 12345,
  `/etc/alloy/config.alloy`;
- Grafana Agent: `grafana-agent`, `grafana-agent.service`, port 12345,
  `/etc/grafana-agent.yaml`;
- node_exporter: package `prometheus-node-exporter`, unit
  `prometheus-node-exporter.service`, process names `node_exporter` и
  `prometheus-node`, port 9100, `/etc/default/prometheus-node-exporter`;
- Prometheus: `prometheus`, `prometheus.service`, port 9090,
  `/etc/prometheus/prometheus.yml`;
- Telegraf: `telegraf`, `telegraf.service`, port 9273,
  `/etc/telegraf/telegraf.conf`;
- Zabbix Agent: packages `zabbix-agent` или `zabbix-agent2`, два фиксированных
  unit и process name, port 10050 и два config path.

Inventory читает bounded dpkg database, безопасно открывает procfs и считает
точные process names, разбирает фиксированные `systemctl show` fields и
`ss -H -lntu`. Данные выдаются во встроенном порядке с одним deterministic
check на product.

## Доступ к данным

Плагин читает `/var/lib/dpkg/status`, bounded `/proc/{pid}/comm` и:

```text
/etc/alloy/config.alloy
/etc/grafana-agent.yaml
/etc/default/prometheus-node-exporter
/etc/prometheus/prometheus.yml
/etc/telegraf/telegraf.conf
/etc/zabbix/zabbix_agent2.conf
/etc/zabbix/zabbix_agentd.conf
```

Он вызывает точные `systemctl show` для встроенных units и `ss -H -lntu`.
Dpkg input ограничен 4 МиБ, procfs — 4096 entries и 256 байт на name, unit
output — 32 КиБ, listener output — 128 КиБ, stderr — 16 КиБ, config — 256 КиБ.
Shell, metrics request, config include, product validator и caller-selected
input не используются.

## Результаты и коды завершения

Inventory и status возвращают `monitoring.{product}.present` или
`monitoring.{product}.missing`. Отсутствующие products информационны. Optional
source failures создают dependency или configuration errors, оставляя
остальные product evidence.

Config возвращает `monitoring.config` как pass, info при отсутствии файлов или
partial, если существующий файл unsafe либо structurally malformed.

Фатальная невозможность собрать local inventory использует dependency exit
mapping. Unsupported invocation использует arguments. Context cancellation и
timeout передаются host. Всегда используется Result v1; raw command и config
output в JSON не копируется.

## Конфигурация

В версии 1 нет `/etc/ohtools/plugins/monitoring-base.yaml`. Products, paths,
ports, process names, formats и limits встроены.

Файлы фиксированных product paths — наблюдаемые inputs. Product configuration
остаётся под обычным deployment-процессом оператора. `monitoring config`
проверяет только conservative structural shape; `valid: true` не заменяет
semantic validation самого продукта.

## Что можно изменить

Оператор может устанавливать и удалять поддерживаемые packages, управлять
фиксированными systemd units, processes, listeners и product configs. Следующая
команда отразит локальные изменения. Также доступны стандартные host output и
timeout options.

Набор products, signals, paths, conventional ports, structural parsers и limits
нельзя переопределить YAML или command arguments. Их изменение требует reviewed
code, fixtures и нового immutable release.

## Зафиксированное поведение

Шесть product definitions, dpkg и procfs roots, unit names, process names,
ports, config paths и formats, `systemctl` и `ss` argv и resource limits
зафиксированы в 1.0.0.

Плагин не принимает scrape URL, dashboard URL, target, include root,
credentials, arbitrary port, process или validation executable. Listener
presence не означает process ownership.

## Безопасность

Все команды read-only, local-only и non-root. Procfs и config access отклоняют
unsafe paths и symlinks через confined bounded reads. Открытый procfs
сравнивается с path metadata. External output имеет фиксированные byte limits и
strict grammars.

Raw configs, metric samples, labels, bearer tokens, basic-auth, SNMP
communities, remote-write credentials, cloud keys, target URLs, process argv и
command diagnostics исключаются. Host продолжает применять policy, timeout,
audit, JSON stdout isolation и recursive redaction.

## Диагностика проблем

- Если package не найден, проверьте exact compiled Debian package name и
  bounded local dpkg status.
- При неполном process inventory проверьте procfs type, symlinks, permissions и
  лимит 4096 entries.
- Unit errors относятся только к встроенным unit names; custom unit версия 1 не
  обнаруживает.
- Listener errors требуют parseable `ss -H -lntu`. Conventional port сам по
  себе не доказывает ownership.
- Partial config означает unsafe, oversized, non-UTF-8, empty, NUL-containing
  или structurally malformed fixed file.

## Ограничения и TODO

Версия 1 намеренно local-only. Metrics и health queries, dashboards,
remote-write, discovery targets, includes, authenticated endpoints, semantic
product validators, listener-to-process attribution, remote execution и
downloads отложены. Сеть потребует policy-pinned endpoints, credential
references, TLS verification, SSRF и DNS-rebinding controls, redirect policy,
bounded queries и responses и recursive redaction.

Смотрите актуальный
[roadmap мониторинга](../../roadmap/monitoring-base.md).

## Совместимость и исходный код

Страница описывает `monitoring-base` 1.0.0 и plugin protocol v1. Подписанный
каталог остаётся источником истины для установки, minimum host version, asset
size, SHA-256 и release history.

Реализация: [internal/monitoring](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/monitoring).
