# systemd-base roadmap

## Implemented in v1

- Local service status and bounded journal diagnostics.
- Plan-digest protected service restart with post-operation verification.
- Fixed argv execution without shell or sudo.

## Deferred

### Multi-unit orchestration

- Reason: v1 mutates one explicitly validated unit at a time.
- Prerequisites: dependency-aware deterministic planning.
- Security design required: rollback limits, confirmation scope, and audit correlation.
- Acceptance criteria: plans enumerate every affected unit and verify the final state.
