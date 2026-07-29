---
schema_version: 1
plugin_id: k8s-base
locale: en
documented_version: 1.0.0
title: Kubernetes base
summary: Inspect the local kubectl client, kubelet, kubeconfigs, and static manifests without contacting a cluster.
command_paths:
  - k8s status
  - k8s contexts
  - k8s manifests
local_only: true
---
# Kubernetes base

## Purpose and supported scenarios

`k8s-base` provides bounded, read-only evidence about Kubernetes components on
the current host. It detects the local `kubectl` client, the fixed
`kubelet.service`, kubelet processes, four kubeadm kubeconfig files, and static
pod manifests.

Version 1 is useful for checking whether Kubernetes tooling and kubelet are
present, reviewing non-secret context names, and inventorying static workload
identities during local troubleshooting. It never connects to a Kubernetes API
server and does not report workload specifications or credential material.

## Quick start

```text
ohtools k8s status
ohtools k8s contexts
ohtools k8s manifests
```

All three commands are diagnostic. They require no command arguments, root,
confirmation, or dry-run mode.

## Commands

### k8s status

Runs `kubectl version --client --output=json`, reads the three fixed state
fields for `kubelet.service`, and scans bounded `/proc/{pid}/comm` files for the
exact process name `kubelet`. The result contains only the client version and
platform plus kubelet load, active, and sub states, a running boolean, and
sorted process IDs.

The kubectl and kubelet probes are independent. If either optional probe fails,
the command preserves available evidence and returns skipped or partial checks
with structured errors.

### k8s contexts

Checks these fixed local files:

- `/etc/kubernetes/admin.conf`;
- `/etc/kubernetes/kubelet.conf`;
- `/etc/kubernetes/controller-manager.conf`;
- `/etc/kubernetes/scheduler.conf`.

For each present kubeconfig, it returns its path, current context, sorted
context names and namespaces, and cluster and user counts. Cluster server
addresses, user data, tokens, certificates, keys, auth-provider fields, and
exec configuration are not decoded into the result.

### k8s manifests

Enumerates direct `.yaml` and `.yml` files in
`/etc/kubernetes/manifests`. For each YAML document, it returns only the file
path, document number, `apiVersion`, `kind`, metadata name, and optional
namespace. Container images, commands, arguments, environment, volumes,
Secrets, and the rest of the workload spec are omitted.

## How it works

Each command first passes the plugin protocol v1 read-only planning seam. The
plugin then works only beneath its local root:

1. Status invokes two exact, allowlisted argv forms and reads a confined procfs
   inventory.
2. Contexts reads only four compiled paths, verifies bounded regular files,
   parses one UTF-8 YAML document, and rejects aliases.
3. Manifests verifies every directory component, rejects symlinks, enumerates
   one directory, and parses bounded YAML documents.
4. Metadata values are validated for size, UTF-8, and control characters.
5. Output is sorted and converted to Result schema v1.

The kubeconfig limit is 512 KiB per file with at most 256 clusters, users, or
contexts. Static manifests are limited to 512 KiB per file, 4 MiB total, 256
directory entries, and 512 YAML documents. Procfs inspection accepts at most
4096 entries and reads at most 256 bytes from each process name.

## Data access

The plugin may execute only:

```text
kubectl version --client --output=json
systemctl show --no-pager --property=LoadState,ActiveState,SubState -- kubelet.service
```

It reads `/proc`, the four kubeconfig paths, and
`/etc/kubernetes/manifests`. The operational runner rejects stdin,
environment overrides, different executables, and different argv. No shell,
socket, API endpoint, plugin binary, or caller-selected path is used.

## Results and exit behavior

`k8s status` emits checks `k8s.client` and `k8s.kubelet`. Optional dependency
or probe failures are represented as skipped or partial checks and safe
structured errors while usable data remains.

`k8s contexts` emits `k8s.contexts`; absence of all four files is informational.
`k8s manifests` emits `k8s.manifests`; absence of the fixed directory is
informational.

Unsafe, oversized, malformed, multi-document kubeconfig input, or unsafe
manifest input fails with the configuration exit mapping. Unsupported
invocations use the arguments mapping. Cancellation and timeout propagate.
JSON stdout remains a single Result v1 document.

## Configuration

Version 1 has no `/etc/ohtools/plugins/k8s-base.yaml` file and no user-settable
plugin configuration. Paths, commands, limits, and schemas are compiled.

The Kubernetes files themselves remain owned by Kubernetes tooling. Editing
them changes what a later diagnostic observes, but `k8s-base` neither manages
nor validates their full product semantics.

## What can be changed

Operators can install or remove the local kubectl client, manage kubelet through
normal host controls, and maintain the fixed kubeadm kubeconfigs and static
manifests. They can select standard host output, timeout, and redaction
behavior where the host interface permits it.

Maintainers can add paths, metadata fields, limits, or supported schemas only
through reviewed code, adversarial tests, and a new immutable plugin release.

## Fixed behavior

The four kubeconfig paths, static manifest directory, kubelet unit and process
name, command argv, metadata allowlist, and all byte and entry limits are fixed
in 1.0.0. The plugin accepts no cluster endpoint, context selector, namespace
selector, arbitrary directory, executable, or credential reference.

Status is observation only. Context and manifest commands never return complete
source documents.

## Safety

All commands are read-only and do not require root. Path components and files
must not be symlinks. Reads, directory enumeration, YAML documents, and command
output are bounded. YAML aliases and kubeconfig multiple documents are
rejected.

The plugin does not inherit caller-provided command input, contact an API
server, execute kubeconfig auth plugins, or expose token, key, certificate,
server, workload spec, or process command-line data. Host policy, timeout,
audit, JSON isolation, and recursive redaction remain in force.

## Troubleshooting

- If client metadata is skipped, verify the trusted `kubectl` executable and
  that its client-version JSON matches the supported shape.
- If kubelet is partial, check both `kubelet.service` and local `/proc`
  accessibility; one source may still be present in the result.
- If contexts fail, check file type, symlinks, UTF-8, YAML aliases, schema
  `apiVersion: v1` and `kind: Config`, entry counts, and the 512 KiB limit.
- If manifests fail, remove symlinks and malformed documents and check the
  per-file, cumulative, entry, and document limits.
- An empty contexts or manifests result is not an error when the compiled paths
  do not exist.

## Limitations and TODO

Version 1 is deliberately local-only. Cluster health, workload inventory,
Kubernetes API queries, CRI socket access, container and pod correlation,
additional kubeconfig locations, remote execution, and downloads are deferred.
Future network support requires policy-pinned endpoints, host-owned credential
references, TLS identity controls, proxy isolation, operation allowlists,
response budgets, timeouts, and recursive redaction.

The maintained backlog is in the
[Kubernetes roadmap](../../roadmap/k8s-base.md).

## Compatibility and source

This page documents `k8s-base` 1.0.0 and plugin protocol v1. The signed catalog
remains authoritative for installation, minimum host version, asset size,
SHA-256, and release history.

Implementation: [internal/k8s](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/k8s).
