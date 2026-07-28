# k8s-base roadmap

## Implemented in v1

- Local `kubectl` client version through fixed `version --client --output=json` argv.
- Local kubelet unit and bounded process metadata without command-line output.
- Bounded kubeconfig context metadata with all credentials, certificates, keys, exec configuration, auth-provider data, and API server addresses omitted.
- Bounded static manifest metadata with symlink rejection and no workload spec or secret-data output.

## Deferred

### Cluster API health and workload inventory

- Reason: v1 is intentionally local-only and never contacts a Kubernetes API server.
- Prerequisites: policy-pinned cluster endpoints, host-owned credential references, and a versioned read-only Kubernetes API allowlist.
- Security design required: TLS trust pinning, proxy and environment isolation, impersonation controls, response limits, timeouts, and recursive redaction.
- Acceptance criteria: only explicitly authorized read-only resources are queried and kubeconfig tokens, certificates, keys, exec args, auth-provider data, and Secret values never enter results or logs.

### Additional kubeconfig and manifest locations

- Reason: v1 inspects only the fixed kubeadm local paths and the direct static-manifest directory.
- Prerequisites: a declarative path allowlist owned by system policy.
- Security design required: canonical confinement, ownership and permission checks, cumulative file and document limits, and deterministic precedence.
- Acceptance criteria: additional locations remain local, non-symlinked, bounded, and cannot override or traverse outside policy roots.

### Container runtime and pod lifecycle correlation

- Reason: v1 records kubelet process and static metadata without querying CRI sockets.
- Prerequisites: a versioned CRI protocol seam and explicit local socket policy.
- Security design required: socket ownership verification, request allowlists, timeout and response limits, and secret-field classification.
- Acceptance criteria: correlation is deterministic and read-only, with no container exec, logs, attach, image pulls, or remote endpoint access.
