# server-setup-base roadmap

## Implemented in v1

- Exact compiled profiles for Debian 10–13 and Ubuntu 20.04/22.04/24.04.
- Local drift checks for packages, accounts, directories, managed files, sysctls, and units.
- Deterministic plan-digest protected apply and bounded local-only upgrade transactions.
- Atomic managed-file replacement, rollback, idempotency, and post-operation verification.

## Deferred

### Network package retrieval and repository bootstrap

- Reason: v1 setup operations are intentionally local-only and do not download artifacts.
- Prerequisites: immutable repository coordinates, expected hashes, and endpoint allowlists.
- Security design required: HTTPS pinning, signature verification, redirect limits, and rollback.
- Acceptance criteria: every downloaded byte is authenticated and present in the approved plan.

### Credential references and remote orchestration

- Reason: v1 accepts no credentials, remote endpoints, or inline commands.
- Prerequisites: host-owned credential references and authenticated host identity.
- Security design required: secret isolation, bounded fan-out, and per-host confirmation.
- Acceptance criteria: secrets never enter plugin config/results and remote drift cannot weaken policy.
