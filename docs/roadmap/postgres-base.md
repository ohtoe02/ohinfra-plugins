# postgres-base roadmap

## Implemented in v1

- Local PostgreSQL installation status from bounded package, process, systemd, and cluster probes.
- Deterministic cluster inventory through fixed `pg_lsclusters --no-header` argv.
- Bounded local configuration metadata with an explicit public-setting allowlist.
- Fail-closed symlink, path, output, and file-size handling without database connections.

## Deferred

### Authenticated database health and replication checks

- Reason: v1 is intentionally local-only and never uses `psql`, TCP, Unix sockets, or database credentials.
- Prerequisites: host-owned credential references, endpoint allowlists, and a versioned database-query policy.
- Security design required: least-privilege roles, credential lifecycle, TLS verification, timeout and row limits, and recursive redaction.
- Acceptance criteria: connections use only policy-pinned local or remote endpoints and never expose passwords, recovery credentials, connection URI userinfo, or query results outside an approved schema.

### Full semantic validation of PostgreSQL configuration

- Reason: v1 publishes bounded metadata and allowlisted settings without resolving includes or evaluating runtime state.
- Prerequisites: a confined include resolver and version-specific PostgreSQL setting schemas.
- Security design required: include traversal and symlink prevention, cumulative byte/file limits, and secret-bearing setting classification.
- Acceptance criteria: deterministic validation covers all supported PostgreSQL versions while SSL key paths, recovery commands, and authentication material remain omitted.
