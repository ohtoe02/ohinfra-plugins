# network-base roadmap

## Implemented in v1

- Local interface, route, and listener inventory from bounded proc/sys sources.
- Fixed `ip` and `ss` fallbacks with partial/dependency semantics.
- Local PEM/DER certificate inspection without private-key output.

## Deferred

### Remote endpoints and authenticated probes

- Reason: v1 is intentionally local-only.
- Prerequisites: endpoint allowlists, host-owned credential references, and timeout policy.
- Security design required: DNS rebinding protection, redirect policy, and recursive redaction.
- Acceptance criteria: only pinned endpoints are contacted and invalid targets fail closed.

### Complete address enrichment without iproute2

- Reason: `/proc/net/dev` does not expose interface addresses.
- Prerequisites: bounded parsers for additional kernel address sources.
- Security design required: interface-name confinement and deterministic merge rules.
- Acceptance criteria: IPv4 and IPv6 addresses remain complete when `ip` is unavailable.
