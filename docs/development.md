# Plugin development

## Repository layout

```text
cmd/<plugin>/main.go       composition root and release metadata
internal/protocol/         protocol v1 adapter and canonical schemas
internal/config/           strict, trusted system configuration
internal/execx/            bounded trusted executable runner
internal/redact/           recursive secret redaction
internal/<domain>/         collectors, command handlers, and tests
```

The repository intentionally exports no Go SDK. The stable interfaces are the
ohtools CLI, Result schema v1, strict YAML configuration, and executable plugin
protocol v1.

## Adding a command

1. Add its exact CLI path to the owning domain manifest. Do not introduce a
   plugin prefix.
2. Write a failing unit or contract test.
3. Implement validation and collection behind testable adapters.
4. For diagnostics, return a normalized Result v1.
5. For mutations, implement both `plan` and `execute`, repeat the security
   requirements in both paths, and verify the approved plan digest.
6. Add strict configuration fields only when compiled defaults are not enough.
7. Run race tests, vet, static Linux builds, and real-binary protocol smoke
   tests.

Manifest descriptions are optional. When present, they are a single-line UTF-8
string of at most 512 bytes. The catalog description must be byte-for-byte
identical.

## Protocol invocation

The binary accepts exactly:

```text
<plugin> manifest --protocol=1
<plugin> plan --protocol=1
<plugin> execute --protocol=1
```

`plan` and `execute` read one strict JSON Invocation document from stdin.
Unknown or duplicate keys and trailing documents are rejected. JSON is written
only to stdout; diagnostics are written to stderr.

## Command safety checklist

- Validate paths, argument counts, flag types, ranges, identifiers, and
  option-like values before I/O.
- Never use `sh`, `bash`, `eval`, or string-built commands.
- Resolve programs with `execx.SystemResolver`.
- Bound stdout and stderr and honor the invocation deadline.
- Redact command output, nested JSON, errors, checks, and diagnostics.
- `manifest` must not read configuration, execute programs, or access the
  network.
- `plan` must not change the system.
- Cover poisoned `PATH`, metacharacters, option smuggling, symlinks, oversized
  output, timeout cleanup, and secret leakage where applicable.

The catalog repository contains the complete authoring and publication
examples for third-party plugin maintainers.
