# apt-base roadmap

## Implemented in v1

- Read-only local dpkg state, APT sources, upgrade simulation, and history.
- Credential-safe source normalization and poisoned-environment isolation.
- Fixed `apt-get --simulate --no-download` argv with bounded output.

## Deferred

### Repository refresh and network provenance

- Reason: v1 performs no network access or package mutation.
- Prerequisites: release-host allowlists, repository signature policy, and network timeouts.
- Security design required: proxy isolation, key provenance, redirect limits, and rollback policy.
- Acceptance criteria: refresh is separately planned, confirmed, bounded, and signature verified.

### Archived compressed history

- Reason: v1 reads the current bounded local history file.
- Prerequisites: bounded gzip reader and deterministic archive ordering.
- Security design required: decompression limits and malformed-archive handling.
- Acceptance criteria: archives cannot exceed total output or memory limits.
