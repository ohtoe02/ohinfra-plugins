# docker-base roadmap

## Implemented in v1

- Local Docker daemon, container, usage, log, inspect, and health diagnostics.
- Read-only Compose discovery, validation, inspection, and diff.
- Bounded output, recursive redaction, and fixed Docker argv.

## Deferred

### Authenticated registry diagnostics

- Reason: v1 does not accept registry credentials.
- Prerequisites: host-owned credential references and endpoint policy.
- Security design required: redirect pinning, credential redaction, and response limits.
- Acceptance criteria: credentials never enter plugin config, output, or audit fields.
