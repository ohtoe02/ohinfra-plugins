# Plugin documentation design

## Status

Approved for implementation on 2026-07-28.

## Context

The signed catalog is authoritative for install commands, released versions,
asset sizes, digests, compatibility, and yanked status. It is intentionally not
a prose documentation store. The existing plugin roadmaps describe delivered
and deferred scope, but they do not explain command behavior, configuration,
data sources, safety properties, or troubleshooting in sufficient detail.

The public portal needs complete English and Russian documentation for every
published first-party plugin without changing catalog schema v1, plugin
protocol v1, or Result schema v1.

## Decision

Canonical documents live beside plugin code under `docs/plugins/{en,ru}`.
Every published plugin has one document in each locale. Documents use strict
YAML frontmatter and a conservative Markdown body. MDX, raw HTML, executable
content, credentials, and unsafe links are rejected.

The documentation validator treats executable plugin manifests as the source
of truth for plugin IDs and command paths. It also enforces locale parity,
required sections, safe links, unique headings, and version metadata.

A separate release workflow emits an immutable `plugin-docs-v1.json` bundle.
The portal downloads that bundle only during production builds, verifies a
pinned SHA-256 and strict schema, and combines it with catalog metadata by
plugin ID. Browsers never fetch documentation or catalog upstreams.

## Document contract

Each document declares:

- `schema_version: 1`
- `plugin_id`
- `locale`
- `documented_version`
- `title`
- `summary`
- `command_paths`
- `local_only`

The body contains stable top-level sections for purpose, quick start, command
behavior, implementation, data access, results, configuration, changeable
behavior, fixed behavior, safety, troubleshooting, and limitations/TODO.
Command paths are listed in frontmatter only to provide a machine-checkable
mapping; signed installation and release metadata are not duplicated.

The 14 plugins first released in the local diagnostics waves are marked
`local_only: true`. Their documents must describe network access, remote
execution, and downloads only as deferred work.

## Trust and failure model

The bundle is published from a protected GitHub environment and is immutable.
The portal pins its release URL and aggregate SHA-256, accepts only bounded
credential-free HTTPS responses from approved GitHub release hosts, and rejects
redirect, schema, digest, locale, plugin, version, or safety mismatches.

A failed documentation fetch or validation fails the build. GitHub Pages keeps
the last successful deployment. Upstream Markdown is parsed without raw HTML or
MDX execution.

## Compatibility

Existing plugin releases and catalog sequence 4 remain immutable. Documentation
updates use a new documentation release and portal pin. If documentation work
finds a behavior defect, that defect is fixed through a new plugin patch
release and a later catalog sequence rather than by changing existing assets.

`inventory-base` remains outside the published documentation set.

