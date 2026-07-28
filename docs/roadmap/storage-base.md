# storage-base roadmap

## Implemented in v1

- Local filesystem usage for an approved path or all mounted filesystems.
- Bounded mount parsing and deterministic threshold evaluation.
- Read-only Result v1 output.

## Deferred

### LVM, RAID, and hardware health

- Reason: the v1 collector stays portable and filesystem-focused.
- Prerequisites: compiled local adapters for each supported storage stack.
- Security design required: fixed executables, bounded output, and privilege handling.
- Acceptance criteria: absent optional stacks return partial without false health claims.
