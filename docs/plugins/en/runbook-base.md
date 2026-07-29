---
schema_version: 1
plugin_id: runbook-base
locale: en
documented_version: 1.0.0
title: Runbook base
summary: Discover, inspect, and validate trusted local documentation-only runbooks without executing steps.
command_paths:
  - runbook list
  - runbook inspect
  - runbook validate
local_only: true
---
# Runbook base

## Purpose and supported scenarios

`runbook-base` discovers strict YAML runbooks beneath the fixed
`/etc/ohtools/runbooks` directory. Version 1 treats every runbook and step as
plain documentation: it can list, inspect, and validate them but cannot execute
text, commands, scripts, URLs, includes, templates, or other plugins.

Use it to maintain a small trusted set of local operational procedures with
deterministic identities and bounded readable text. The strict schema and trust
checks are designed to prevent a documentation file from acquiring hidden
executable behavior.

## Quick start

List all trusted local documents:

```text
ohtools runbook list
```

Inspect one document by its schema name:

```text
ohtools runbook inspect database-recovery
```

Validate the entire collection or one selected document:

```text
ohtools runbook validate
ohtools runbook validate database-recovery
```

All commands are read-only and non-root. `inspect` requires one name;
`validate` accepts zero or one name.

## Commands

### runbook list

Loads and validates the complete collection before returning anything. It emits
sorted summaries with runbook name, normalized title, normalized description,
and step count. If any candidate YAML document is unsafe or invalid, the whole
collection fails closed rather than silently omitting it.

### runbook inspect

Validates the requested name against
`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`, loads the complete trusted collection, and
returns one matching document. Each step contains a one-based number, normalized
title, and normalized description. Whitespace is collapsed to single spaces.

An invalid name uses the arguments exit mapping. A valid name not present in
the collection uses the dependency mapping.

### runbook validate

Loads the same complete collection, then returns a deterministic validation
view for every document or the selected name. Documents that reach this stage
are marked valid because parsing and trust checks are fail-closed before Result
construction.

An optional name follows the same syntax and not-found behavior as inspect.

## How it works

The loader performs these stages:

1. Require an absolute canonical local root.
2. Inspect the root, `/etc`, `/etc/ohtools`, and the runbook directory without
   following symlinks.
3. On the production root, require every path component and file to be owned by
   UID 0; always reject group/world-writable mode bits.
4. Open the directory, compare opened metadata to the path, and read at most
   129 entries to enforce the 128-entry limit before materialization.
5. Consider only direct `.yaml` and `.yml` regular files, sorted by filename.
6. Recheck each opened file, read it with per-file and cumulative byte limits,
   parse one strict YAML document, and validate the documentation-only schema.
7. Require filename stem and document `name` to match and reject duplicate
   names.

YAML unknown fields, duplicate mapping keys, aliases, anchors, custom tags,
multiple documents, invalid UTF-8, and non-regular files fail closed.

## Data access

The plugin reads only direct YAML files beneath:

```text
/etc/ohtools/runbooks
```

It does not traverse subdirectories or follow symlinks. The collection permits
at most 128 directory entries and 128 runbook files, 64 KiB per file, and
1 MiB total. Each document permits 1 through 64 steps. Title is limited to
128 bytes of normalized text, runbook description to 2048, and each step
description to 4096.

No external executable, shell, plugin binary, URL, socket, include file,
environment variable, or caller-selected root is accessed during production
commands.

## Results and exit behavior

List returns `data.runbooks`; inspect returns `data.runbook`; validate returns
`data.validations`. The successful read-only Results contain no mutation
changes and no operational checks because collection validity is enforced
before building the Result.

An invalid argument uses the arguments mapping. A missing requested document
uses dependency. Unsafe ownership or permissions, symlinks, limits, malformed
YAML, schema violations, forbidden text, duplicate identities, and any invalid
document use the configuration mapping with the safe message
`invalid local runbook collection`. Cancellation and timeout propagate.

## Configuration

There is no `/etc/ohtools/plugins/runbook-base.yaml`. The trusted directory,
schema, and limits are compiled. Operators configure behavior by creating
strict schema v1 documents such as:

```yaml
schema_version: 1
name: database-recovery
title: Database recovery review
description: Review the approved local recovery procedure.
steps:
  - title: Confirm incident ownership
    description: Confirm that an authorized operator owns the incident.
  - title: Review recovery prerequisites
    description: Review the locally approved prerequisites before any separate action.
```

Save the example as `database-recovery.yaml`. In production, the directory,
path components, and file must be root-owned and not group/world-writable.

## What can be changed

Operators can add, edit, or remove trusted local YAML documents, choose their
valid names, titles, descriptions, and 1 through 64 documentation-only steps,
and select a document for inspect or validate. Changes become visible on the
next command only after the entire collection passes validation.

The root path, ownership policy, filename extensions, schema fields, text
restrictions, limits, and non-executable semantics cannot be configured.
Maintainers can change them only through a versioned design, security tests,
and a new immutable release.

## Fixed behavior

Schema v1 contains exactly `schema_version`, `name`, `title`, `description`,
and `steps`; a step contains only `title` and `description`. Unknown and
executable fields are forbidden.

Text cannot contain raw HTML angle brackets, URL references, or an inline
credential assignment using password, passphrase, secret, token, API-key,
authorization, or credential markers. Includes, commands, argv, shell, plugins,
conditions, loops, variables, aliases, anchors, and custom tags have no schema
representation and are rejected.

## Safety

All commands are local-only, read-only, and non-root. Production trust comes
from canonical absolute paths, root ownership, non-writable modes, symlink
rejection, descriptor-based enumeration, metadata rechecks after opening,
strict single-document YAML, and cumulative limits.

Inspect output is normalized plain text. Nothing in a title, description, or
step is interpreted as Markdown, HTML, a URL, shell, argv, template, or plugin
reference. No document content is executed, fetched, included, or written.
Host timeout, policy, audit, JSON isolation, and redaction remain active.

## Troubleshooting

- If the directory is absent, list and validate return an empty collection; a
  selected name remains not found.
- If the collection is invalid, check every direct YAML file because one bad
  document fails the whole collection.
- Verify production ownership is UID 0 and remove group/world write bits from
  every path component and file.
- Remove symlinks, subverted path components, multiple YAML documents, unknown
  fields, duplicate keys, aliases, anchors, custom tags, raw HTML, URLs, and
  inline credentials.
- Ensure filename and `name` are identical, names are unique, text is nonempty
  and within limits, and the collection remains under count and byte budgets.

## Limitations and TODO

Version 1 is deliberately local-only and documentation-only. Runbook execution,
remediation, action references, plans, templates, conditions, includes,
reusable steps, localization and migration, signed or remote distribution,
downloads, and network access are deferred. Execution requires a separate
versioned action schema, host policy, immutable plan digest, audit,
confirmation, timeout cleanup, verification, and recovery. Distribution
requires signed immutable bundles, key rotation, rollback protection, endpoint
allowlists, pinned TLS, and archive limits.

See the maintained [runbook roadmap](../../roadmap/runbook-base.md).

## Compatibility and source

This page documents `runbook-base` 1.0.0 and plugin protocol v1. The signed
catalog remains authoritative for installation, minimum host version, asset
size, SHA-256, and release history.

Implementation: [internal/runbook](https://github.com/ohtoe02/ohtools-plugins/tree/main/internal/runbook).
