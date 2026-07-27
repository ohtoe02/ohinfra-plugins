# runbook-base roadmap

## Implemented in v1

- Deterministic, read-only `runbook list`, `runbook inspect <name>`, and
  `runbook validate [name]` commands.
- Fixed `/etc/ohtools/runbooks` discovery with an absolute canonical test
  seam; relative roots, traversal, symlinked path components, and symlinked
  documents are rejected.
- Strict schema v1 YAML for metadata and documentation-only steps, with
  explicit rejection of unknown fields, duplicate keys, aliases, anchors,
  executable fields, shell, argv, URLs, includes, raw HTML, inline
  credentials, invalid UTF-8, and multiple YAML documents.
- Bounded descriptor-based directory enumeration, per-file bytes, cumulative
  bytes, document count, text length, and step count before returning any
  content.
- Production paths must be root-owned and cannot be group/world-writable;
  files are rechecked after opening to reduce replacement races.
- Inspect output contains normalized plain text only. It never executes a
  step, external command, script, URL, include, plugin binary, or caller
  supplied argument.

## Deferred

### Runbook execution and remediation

- Reason: v1 deliberately has no execute/apply command and treats every step
  as documentation only.
- Prerequisites: a separately approved versioned action schema, exact
  allowlisted plugin command references, host policy integration, and an
  immutable plan/digest contract.
- Security design required: policy and privilege authorization, audit
  availability, confirmation, dry-run, timeout and cancellation cleanup,
  option-smuggling prevention, recursive redaction, verification, and
  rollback or safe recovery for every action class.
- Acceptance criteria: no text is interpreted as shell or argv; every action
  resolves to a compiled capability, appears exactly in the reviewed plan,
  and cannot start when policy, audit, digest, confirmation, or verification
  fails.

### Includes, templates, conditions, and reusable steps

- Reason: v1 rejects includes, YAML aliases/anchors, templates, variables,
  conditions, loops, and executable step fields rather than resolving them.
- Prerequisites: a versioned declarative schema and a confined dependency
  graph with explicit module identities.
- Security design required: traversal and symlink prevention, include-cycle
  detection, cumulative file/node/depth limits, type-safe substitutions, and
  a guarantee that expansion cannot create executable text or hidden
  actions.
- Acceptance criteria: expansion is deterministic, bounded, produces the
  same reviewed plan on every run, and rejects unknown modules or ambiguous
  identities without reading outside the trusted runbook root.

### Signed and remote runbook distribution

- Reason: v1 reads only local root-controlled files and performs no Git, HTTP,
  ticketing, object-storage, or collaboration-service requests.
- Prerequisites: immutable signed bundles, trusted key rotation, endpoint
  allowlists, and host-owned credential references.
- Security design required: signature and expiration verification,
  rollback protection, pinned TLS, redirect and DNS-rebinding controls,
  credential lifecycle, archive-entry confinement, and compressed/expanded
  size limits.
- Acceptance criteria: only authenticated non-rollback bundles from
  policy-pinned sources enter the local store, and no credential, remote raw
  payload, executable file, or unsigned draft reaches inspection output.

### Schema migration and localization

- Reason: v1 accepts one strict English schema and does not translate or
  migrate documents.
- Prerequisites: immutable schema URLs, compatibility fixtures, locale IDs,
  and an explicit migration policy.
- Security design required: strict source and target decoding, deterministic
  migration, preservation of documentation-only semantics, and rejection of
  fields that acquire executable meaning.
- Acceptance criteria: migrated documents revalidate byte-for-byte
  deterministically, retain stable identities, and never weaken v1 limits or
  introduce executable steps, URLs, credentials, raw HTML, or includes.
