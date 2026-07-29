---
schema_version: 1
plugin_id: k8s-base
locale: ru
documented_version: 1.0.0
title: Базовая диагностика Kubernetes
summary: Проверка локальных kubectl, kubelet, kubeconfig и статических manifest без обращения к кластеру.
command_paths:
  - k8s status
  - k8s contexts
  - k8s manifests
local_only: true
---
# Базовая диагностика Kubernetes

## Назначение и поддерживаемые сценарии

`k8s-base` собирает ограниченные read-only сведения о компонентах Kubernetes
на текущем сервере. Он обнаруживает локальный клиент `kubectl`, фиксированный
`kubelet.service`, процессы kubelet, четыре kubeadm kubeconfig и статические pod
manifest.

Версия 1 подходит для проверки наличия Kubernetes tooling и kubelet, просмотра
несекретных имён contexts и инвентаризации identities статических workloads
при локальной диагностике. Плагин никогда не подключается к Kubernetes API и
не возвращает workload spec или credential material.

## Быстрый старт

```text
ohtools k8s status
ohtools k8s contexts
ohtools k8s manifests
```

Все команды диагностические. Им не нужны аргументы, root, подтверждение или
dry-run.

## Команды

### k8s status

Выполняет `kubectl version --client --output=json`, читает три фиксированных
поля состояния `kubelet.service` и просматривает ограниченные
`/proc/{pid}/comm` в поиске точного имени `kubelet`. Result содержит только
версию и платформу клиента, состояния load, active и sub для kubelet, признак
работающего процесса и отсортированные PID.

Пробы kubectl и kubelet независимы. Если optional-проба не удалась, команда
сохраняет доступные данные и возвращает skipped или partial check со
структурированной ошибкой.

### k8s contexts

Проверяет фиксированные локальные файлы:

- `/etc/kubernetes/admin.conf`;
- `/etc/kubernetes/kubelet.conf`;
- `/etc/kubernetes/controller-manager.conf`;
- `/etc/kubernetes/scheduler.conf`.

Для каждого файла возвращаются путь, current context, отсортированные имена
contexts и namespaces, количество clusters и users. Адреса API server, user
data, tokens, certificates, keys, auth-provider и exec configuration в Result
не декодируются.

### k8s manifests

Перечисляет непосредственные `.yaml` и `.yml` файлы в
`/etc/kubernetes/manifests`. Для каждого YAML document возвращаются только
путь, номер документа, `apiVersion`, `kind`, metadata name и optional namespace.
Images, commands, arguments, environment, volumes, Secrets и остальная часть
workload spec исключены.

## Как это работает

Каждая команда сначала проходит read-only планирование protocol v1. Затем
плагин работает только внутри локального root:

1. Status запускает две точные allowlisted argv и читает confined procfs.
2. Contexts читает четыре встроенных пути, проверяет bounded regular file,
   разбирает один UTF-8 YAML document и запрещает aliases.
3. Manifests проверяет каждый компонент каталога, запрещает symlinks,
   перечисляет один каталог и разбирает ограниченные YAML documents.
4. Metadata проверяется по размеру, UTF-8 и управляющим символам.
5. Данные сортируются и преобразуются в Result schema v1.

Лимит kubeconfig равен 512 КиБ на файл; clusters, users и contexts может быть
не более 256 каждого вида. Static manifests ограничены 512 КиБ на файл,
4 МиБ суммарно, 256 directory entries и 512 YAML documents. Procfs принимает
не более 4096 entries и читает до 256 байт имени процесса.

## Доступ к данным

Плагин может выполнить только:

```text
kubectl version --client --output=json
systemctl show --no-pager --property=LoadState,ActiveState,SubState -- kubelet.service
```

Он читает `/proc`, четыре kubeconfig и `/etc/kubernetes/manifests`.
Operational runner отклоняет stdin, environment overrides, другие executable и
argv. Shell, sockets, API endpoints, plugin binaries и выбранные пользователем
пути не используются.

## Результаты и коды завершения

`k8s status` возвращает checks `k8s.client` и `k8s.kubelet`. Ошибки optional
dependencies и probes представлены skipped или partial checks и безопасными
structured errors, при этом доступные данные сохраняются.

`k8s contexts` возвращает `k8s.contexts`; отсутствие всех четырёх файлов
информационное. `k8s manifests` возвращает `k8s.manifests`; отсутствие
фиксированного каталога также информационное.

Unsafe, oversized, malformed или multi-document kubeconfig и unsafe manifest
используют configuration exit mapping. Неподдерживаемый вызов использует
arguments mapping. Cancellation и timeout передаются host. JSON stdout остаётся
одним документом Result v1.

## Конфигурация

В версии 1 нет `/etc/ohtools/plugins/k8s-base.yaml` и настраиваемой
конфигурации плагина. Пути, команды, лимиты и схемы встроены.

Файлы Kubernetes управляются Kubernetes tooling. Их изменение влияет на
следующий результат диагностики, но `k8s-base` не управляет ими и не проверяет
их полную продуктовую семантику.

## Что можно изменить

Оператор может установить или удалить локальный kubectl, управлять kubelet
обычными средствами host и поддерживать фиксированные kubeadm kubeconfig и
static manifests. Доступны стандартные host-настройки вывода, timeout и
redaction, если они предусмотрены host interface.

Добавить пути, metadata fields, лимиты или схемы сопровождающие могут только
через reviewed code, adversarial tests и новый immutable release.

## Зафиксированное поведение

Четыре kubeconfig, каталог static manifests, unit и имя процесса kubelet,
command argv, metadata allowlist и все лимиты зафиксированы в 1.0.0. Плагин не
принимает cluster endpoint, context или namespace selector, произвольный
каталог, executable или credential reference.

Status только наблюдает. Contexts и manifests никогда не возвращают исходные
документы целиком.

## Безопасность

Все команды read-only и не требуют root. Компоненты пути и файлы не должны быть
symlink. Чтение, перечисление каталогов, YAML documents и command output
ограничены. YAML aliases и несколько документов в kubeconfig отклоняются.

Плагин не принимает command input вызывающего, не обращается к API server, не
запускает kubeconfig auth plugins и не выводит tokens, keys, certificates,
server, workload spec или process command line. Host policy, timeout, audit,
JSON isolation и recursive redaction сохраняются.

## Диагностика проблем

- Если client metadata пропущена, проверьте trusted `kubectl` и соответствие
  его client-version JSON поддерживаемой форме.
- Если kubelet partial, проверьте одновременно `kubelet.service` и доступность
  локального `/proc`; один источник может остаться в Result.
- При ошибке contexts проверьте тип файла, symlinks, UTF-8, YAML aliases,
  `apiVersion: v1`, `kind: Config`, количество entries и лимит 512 КиБ.
- При ошибке manifests удалите symlinks и malformed documents, проверьте
  per-file, cumulative, entry и document limits.
- Пустой contexts или manifests не является ошибкой, если встроенные пути
  отсутствуют.

## Ограничения и TODO

Версия 1 намеренно local-only. Cluster health, workload inventory, Kubernetes
API queries, CRI socket, корреляция containers и pods, дополнительные
kubeconfig paths, remote execution и downloads отложены. Сетевой режим
потребует policy-pinned endpoints, host-owned credential references, TLS
identity controls, proxy isolation, operation allowlists, response budgets,
timeouts и recursive redaction.

Актуальный backlog находится в
[roadmap Kubernetes](../../roadmap/k8s-base.md).

## Совместимость и исходный код

Страница описывает `k8s-base` 1.0.0 и plugin protocol v1. Подписанный каталог
остаётся источником истины для установки, minimum host version, размера asset,
SHA-256 и release history.

Реализация: [internal/k8s](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/k8s).
