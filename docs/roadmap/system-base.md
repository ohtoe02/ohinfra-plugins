# system-base roadmap

## Implemented in v1

- Local operating-system identity and health checks.
- Bounded CPU, memory, load, uptime, OOM, and filesystem observations.
- Strict compiled thresholds and normalized Result v1 output.

## Deferred

### Remote host collection

- Reason: v1 trusts only local host state.
- Prerequisites: authenticated transport and host identity model.
- Security design required: credential isolation, host pinning, and bounded responses.
- Acceptance criteria: remote failures cannot expose secrets or alter local checks.
