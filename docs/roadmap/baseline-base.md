# baseline-base roadmap

## Implemented in v1

- Compiled checks for platform, time sync, file metadata, kernel controls, SSH, packages, and services.
- Strict selection of known check IDs and bounded numeric thresholds.
- Stable ordering with partial results for unavailable optional probes.

## Deferred

### Organization-authored profiles

- Reason: v1 accepts no commands, paths, scripts, or inline policy content.
- Prerequisites: signed profile schema and provenance model.
- Security design required: signature verification, path allowlists, and versioned migration.
- Acceptance criteria: unsigned or unknown checks fail closed and never execute external content.

### Fleet and remote evaluation

- Reason: v1 evaluates only the local host.
- Prerequisites: authenticated inventory transport and host identity.
- Security design required: credential isolation and bounded fan-out.
- Acceptance criteria: remote failures cannot weaken the local compiled baseline.
