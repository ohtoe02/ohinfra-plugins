# java-base roadmap

## Implemented in v1

- Local Java runtime version detection with fixed bounded `java -version` argv.
- Bounded local `/proc` inventory for Java processes with command-line secret redaction.
- Read-only process inspection through fixed `jcmd` `VM.version`, `VM.command_line`, and
  `VM.system_properties` operations.
- Strict positive decimal PID validation, deterministic results, and explicit dependency
  and attach-privilege failures.

## Deferred

### Remote JMX and service endpoints

- Reason: v1 is intentionally local-only and never opens a network connection.
- Prerequisites: endpoint allowlists, host-owned credential references, and protocol timeouts.
- Security design required: DNS rebinding protection, TLS identity pinning, credential isolation,
  and recursive response redaction.
- Acceptance criteria: only policy-pinned endpoints are contacted and all untrusted or redirected
  targets fail closed.

### Heap, thread, JFR, and diagnostic-command capture

- Reason: these operations can pause a JVM, expose application data, or create large artifacts.
- Prerequisites: versioned operation policy, output budgets, artifact lifecycle, and operator
  confirmation semantics.
- Security design required: privilege boundaries, sensitive-content classification, atomic private
  output, timeout cleanup, and denial-of-service limits.
- Acceptance criteria: no diagnostic operation runs without an approved plan and bounded,
  recursively redacted output.

### Container and namespace-aware discovery

- Reason: v1 inspects only the plugin's local procfs namespace.
- Prerequisites: host-owned namespace inventory and stable container identity mapping.
- Security design required: no namespace entry from plugin input, PID-reuse protection, and strict
  procfs confinement.
- Acceptance criteria: container attribution is deterministic without entering an untrusted
  namespace or weakening local-only execution.
