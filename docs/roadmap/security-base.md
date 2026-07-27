# security-base roadmap

## Implemented in v1

- Bounded local account, group, and password-lock metadata without hashes.
- Local SSH posture from safe directives and fixed effective-config argv.
- Local nftables/UFW availability summaries without raw ruleset output.

## Deferred

### Remote scanners and reputation services

- Reason: v1 is intentionally local-only.
- Prerequisites: endpoint policy and host-owned authentication references.
- Security design required: data-minimization, response limits, and credential isolation.
- Acceptance criteria: remote findings contain no account secrets or raw protected config.
