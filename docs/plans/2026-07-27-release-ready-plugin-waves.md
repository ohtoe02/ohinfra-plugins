# Release-ready plugin waves implementation plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement, test, release, catalog, and document fourteen local-first
ohtools plugins in four immutable publication waves.

**Architecture:** Extend the existing Go monorepo with a deep internal kernel
for deterministic results, supported-platform detection, and bounded local
probes. Each executable keeps a minimal composition root and an isolated domain
module. Only `server-setup-base` mutates the host; every other new v1 command is
read-only.

**Tech Stack:** Go 1.26.0/toolchain 1.26.5, plugin protocol v1, Result schema
v1, strict YAML, GitHub Actions, static `linux/amd64`, SPDX SBOM, signed catalog
schema v1, Astro/Starlight portal.

---

## Repositories and worktrees

Use clean, separate worktrees. Never implement in the dirty host checkout.

```text
F:\Projects\ohtools-plugins
F:\Projects\ohtools-plugin-catalog
F:\Projects\ohtools-web
F:\Projects\ohtools-host-plugin-waves
```

Required publication order for every wave:

1. merge plugin implementation;
2. publish immutable plugin releases;
3. merge any required compatible host release;
4. add exact catalog metadata;
5. validate assets in the catalog sandbox;
6. publish the next signed catalog sequence;
7. update and deploy portal TODO summaries;
8. verify the released host and live Pages output.

Use one PR per plugin implementation unless a shared-kernel PR must land first.
Do not combine releases, catalog signing, and portal changes in an
implementation PR.

## Global behavior contract

Every domain `NewDefinition` must accept injected dependencies:

```go
type Options struct {
	Version    string
	Commit     string
	BuildDate  string
	ConfigPath string
	Host       string
	Root       string
	Runner     execx.Runner
	Now        func() time.Time
}
```

Domain-specific readers may extend `Options`, but callers must never provide
executable names or argv. `main.go` supplies only build metadata.

Each read-only definition exposes a plan:

```go
Plan: func(ctx context.Context, invocation protocol.Invocation) (protocol.Plan, error) {
	return protocol.ReadOnlyPlan(ctx, invocation, manifest)
},
```

Every package test must include:

```go
func TestDefinitionExposesOnlyApprovedCommands(t *testing.T)
func TestDefinitionRejectsUnknownArgumentsAndOptions(t *testing.T)
func TestAggregateCommandReturnsPartialWhenOptionalProbeIsMissing(t *testing.T)
func TestNarrowCommandReturnsDependencyFailureWhenPrimaryToolIsMissing(t *testing.T)
func TestExternalValuesAreRedacted(t *testing.T)
func TestResultIsDeterministic(t *testing.T)
```

For tools that have no narrow dependency command, replace the fourth test with
an absent-product informational-result test.

### Task 1: Deterministic result builder

**Files:**

- Create: `internal/resultbuilder/resultbuilder_test.go`
- Create: `internal/resultbuilder/resultbuilder.go`
- Modify: `internal/protocol/types.go`
- Test: `internal/protocol/protocol_test.go`

**Step 1: Write the failing aggregation test**

```go
func TestBuildSortsChecksAndClassifiesPartial(t *testing.T) {
	got := Build(Input{
		Command: "example status",
		Tool: protocol.Tool{Name: "example-base", Version: "1.0.0"},
		Host: "server01",
		Now: time.Unix(100, 0).UTC(),
		Checks: []protocol.Check{
			{ID: "z", Status: protocol.StatusPass, Summary: "ok"},
			{ID: "a", Status: protocol.StatusSkipped, Summary: "missing"},
		},
		Errors: []protocol.StructuredError{{
			Kind: protocol.ErrorDependency,
			Code: "missing_dependency",
			Message: "optional tool is unavailable",
		}},
	})
	if got.Status != protocol.StatusPartial {
		t.Fatalf("status = %s", got.Status)
	}
	if got.Checks[0].ID != "a" || got.Checks[1].ID != "z" {
		t.Fatalf("checks = %#v", got.Checks)
	}
}
```

**Step 2: Run the test and observe RED**

Run:

```bash
go test ./internal/resultbuilder -run TestBuildSortsChecksAndClassifiesPartial -v
```

Expected: FAIL because the package or `Build` is missing.

**Step 3: Implement the small interface**

`resultbuilder.Input` contains command, tool, host, started/now, checks, data,
changes, and errors. `Build` must:

- copy all caller slices/maps;
- recursively redact data and errors;
- sort checks by ID and changes by object/action;
- classify critical before warning before partial before pass;
- call `protocol.Normalize`;
- never expose raw stderr.

Add `ErrorTimeout` and `ErrorPrivilege` constants to `protocol.ErrorKind` only
if existing domain code already emits those names. Do not change serialized
Result schema fields.

**Step 4: Add failing tests for status precedence and input immutability**

Run:

```bash
go test ./internal/resultbuilder -v
```

Expected: FAIL on the newly added cases before implementation, then PASS after
the minimal changes.

**Step 5: Run protocol regressions**

```bash
go test ./internal/protocol ./internal/resultbuilder
```

Expected: PASS.

**Step 6: Commit**

```bash
git add internal/protocol internal/resultbuilder
git commit -m "feat: add deterministic plugin result builder"
```

### Task 2: Supported-platform module

**Files:**

- Create: `internal/platform/platform_test.go`
- Create: `internal/platform/platform.go`
- Create: `internal/platform/testdata/debian-10`
- Create: `internal/platform/testdata/debian-11`
- Create: `internal/platform/testdata/debian-12`
- Create: `internal/platform/testdata/debian-13`
- Create: `internal/platform/testdata/ubuntu-20.04`
- Create: `internal/platform/testdata/ubuntu-22.04`
- Create: `internal/platform/testdata/ubuntu-24.04`

**Step 1: Write table-driven RED tests**

The wished-for interface is:

```go
type Info struct {
	ID        string
	VersionID string
	Supported bool
}

func Detect(root string) (Info, error)
```

Test every supported fixture plus malformed, missing, duplicate-key, and
unsupported distributions.

**Step 2: Observe RED**

```bash
go test ./internal/platform -v
```

Expected: FAIL because `Detect` is missing.

**Step 3: Implement strict `/etc/os-release` parsing**

Requirements:

- maximum file size 64 KiB;
- only `debian` 10–13 and `ubuntu` 20.04/22.04/24.04 are supported;
- duplicate keys and malformed quoting are errors;
- no shell evaluation;
- returned values contain no unparsed content.

**Step 4: Add traversal and symlink tests**

Observe failure, then reject a symlinked `os-release` when using a non-root
fixture tree.

**Step 5: Run GREEN**

```bash
go test ./internal/platform -v
```

Expected: PASS.

**Step 6: Commit**

```bash
git add internal/platform
git commit -m "feat: detect supported plugin platforms"
```

### Task 3: Bounded local probe module

**Files:**

- Create: `internal/probe/probe_test.go`
- Create: `internal/probe/probe.go`
- Modify: `internal/execx/execx_test.go`
- Modify: `internal/execx/execx.go`

**Step 1: Write RED tests for the desired interface**

```go
type Local struct {
	Root   string
	Runner execx.Runner
}

type Command struct {
	Program     string
	Arguments   []string
	StdoutLimit int64
	StderrLimit int64
}

func (local Local) Read(relative string, limit int64) ([]byte, error)
func (local Local) Lines(relative string, limit int64) ([]string, error)
func (local Local) Run(ctx context.Context, command Command) (execx.Output, error)
func (local Local) Exists(relative string) (bool, error)
```

Tests must cover valid reads, traversal, absolute paths, symlinks, directories,
oversize files, missing files, and bounded commands.

**Step 2: Observe RED**

```bash
go test ./internal/probe -v
```

Expected: FAIL because `Local` is missing.

**Step 3: Implement file confinement and command delegation**

The module must never accept executable paths containing separators. Preserve
`execx.SystemResolver` as the only production resolver.

**Step 4: Add cancellation/process cleanup RED tests**

Use the existing helper-process pattern in `internal/execx/execx_test.go`.
Observe the timeout failure, then make cleanup deterministic.

**Step 5: Run GREEN**

```bash
go test ./internal/probe ./internal/execx -v
go test -race ./internal/probe ./internal/execx
```

Expected: PASS.

**Step 6: Commit**

```bash
git add internal/probe internal/execx
git commit -m "feat: add bounded local probes"
```

### Task 4: Read-only plan helper

**Files:**

- Modify: `internal/protocol/serve.go`
- Modify: `internal/protocol/protocol_test.go`
- Create: `internal/protocol/readonly.go`

**Step 1: Write RED tests**

```go
func TestReadOnlyPlanHasNoChangesOrMutationRequirements(t *testing.T) {
	plan, err := ReadOnlyPlan(context.Background(), Invocation{
		ProtocolVersion: 1,
		CommandPath: []string{"network", "overview"},
	}, Manifest{Commands: []Command{{
		Path: []string{"network", "overview"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 || plan.RequiresRoot ||
		plan.RequiresForce || plan.RequiresConfirmation {
		t.Fatalf("plan = %#v", plan)
	}
}
```

Also test unknown paths, arguments, options, and canceled contexts.

**Step 2: Observe RED**

```bash
go test ./internal/protocol -run ReadOnlyPlan -v
```

**Step 3: Implement `ReadOnlyPlan`**

It validates the command path against the manifest and returns:

```go
protocol.Plan{
	CommandID: strings.Join(invocation.CommandPath, "."),
	Summary: "Read local system state without making changes",
	Checks: []protocol.Check{},
	Changes: []protocol.Change{},
	Risks: []string{},
}
```

Do not loosen manifest or invocation validation.

**Step 4: Run GREEN and full shared tests**

```bash
go test ./internal/protocol ./internal/resultbuilder ./internal/platform ./internal/probe
```

**Step 5: Commit**

```bash
git add internal/protocol
git commit -m "feat: add deterministic read-only plans"
```

## Wave 1

### Task 5: `server-setup-base` manifest, profiles, and check

**Files:**

- Create: `cmd/server-setup-base/main.go`
- Create: `internal/serversetup/plugin.go`
- Create: `internal/serversetup/plugin_test.go`
- Create: `internal/serversetup/config.go`
- Create: `internal/serversetup/config_test.go`
- Create: `internal/serversetup/profile.go`
- Create: `internal/serversetup/profile_test.go`
- Create: `internal/serversetup/check.go`
- Create: `internal/serversetup/check_test.go`

**Step 1: Write the manifest RED test**

Expected paths:

```go
[][]string{
	{"setup", "check"},
	{"setup", "apply"},
	{"setup", "upgrade"},
}
```

`apply` and `upgrade` require root, dry-run, and confirmation. `check` is
diagnostic and does not require root.

**Step 2: Observe RED**

```bash
go test ./internal/serversetup -run Definition -v
```

**Step 3: Add the composition root and minimal definition**

Copy the build-metadata pattern from `cmd/system-base/main.go`. Do not implement
apply or upgrade yet.

**Step 4: Write profile RED tests**

Fixtures cover exact Debian 10–13 and Ubuntu 20.04/22.04/24.04 IDs. Profiles
declare compiled package names, managed files, sysctl keys, and systemd units.
Unsupported distributions must produce configuration/dependency guidance, not
select a near match.

**Step 5: Implement profiles and strict config**

Config may select a compiled profile and thresholds. It must not add packages,
commands, file destinations, URLs, or inline content.

**Step 6: Write `setup check` RED tests**

Tests cover packages, users/groups, directories, managed-file digests, sysctl
values, units, and partial unprivileged inspection.

**Step 7: Implement check and run GREEN**

```bash
go test ./internal/serversetup -v
```

**Step 8: Commit**

```bash
git add cmd/server-setup-base internal/serversetup
git commit -m "feat: add server setup profiles and checks"
```

### Task 6: `server-setup-base` planning and mutations

**Files:**

- Create: `internal/serversetup/plan.go`
- Create: `internal/serversetup/plan_test.go`
- Create: `internal/serversetup/apply.go`
- Create: `internal/serversetup/apply_test.go`
- Create: `internal/serversetup/upgrade.go`
- Create: `internal/serversetup/upgrade_test.go`
- Create: `internal/serversetup/transaction.go`
- Create: `internal/serversetup/transaction_test.go`
- Modify: `internal/serversetup/plugin.go`

**Step 1: Write deterministic-plan RED tests**

The same fixture and config must generate byte-identical normalized plans.
Plans list exact package, file, sysctl, user/group, and unit changes.

**Step 2: Implement minimal planning**

No operation may be inferred during execute that was not present in the plan.

**Step 3: Write dry-run and digest RED tests**

Dry-run must make zero runner or filesystem mutation calls. Execute must reject
missing or mismatched plan digests.

**Step 4: Implement apply through a transaction**

Use injected file and command adapters. Every write uses a same-directory
temporary file, mode/owner validation, fsync, rename, and backup. Do not invoke
`sudo`.

**Step 5: Write failure-injection RED tests**

Inject failures after every transaction stage. Verify preserved backups,
bounded recovery, no secret leakage, and a subsequent convergent run.

**Step 6: Implement `setup upgrade`**

Upgrade only ohtools-owned setup state and the approved package profile. It
must not perform an unrestricted distribution upgrade.

**Step 7: Add idempotency and verification tests**

Two identical apply or upgrade operations produce no second change and no
unnecessary reload/restart.

**Step 8: Run GREEN**

```bash
go test -race ./internal/serversetup
```

**Step 9: Commit**

```bash
git add internal/serversetup
git commit -m "feat: add transactional server setup mutations"
```

### Task 7: `network-base`

**Files:**

- Create: `cmd/network-base/main.go`
- Create: `internal/network/plugin.go`
- Create: `internal/network/network_test.go`
- Create: `internal/network/collect.go`
- Create: `internal/network/parse.go`
- Create: `internal/network/tls.go`
- Create: `internal/network/tls_test.go`

**Step 1: Write manifest RED tests**

Approved paths:

```go
[][]string{
	{"network", "overview"},
	{"network", "interfaces"},
	{"network", "routes"},
	{"network", "listeners"},
	{"tls", "inspect"},
}
```

`tls inspect` requires exactly one non-option path.

**Step 2: Observe RED**

```bash
go test ./internal/network -v
```

**Step 3: Implement definition and local collectors**

Use `/proc/net`, `/sys/class/net`, and compiled `ip`/`ss` argv fallbacks.
Normalize addresses, routes, listeners, interface state, and counters.

**Step 4: Write malformed-output and partial RED tests**

Aggregate overview remains partial when `ss` is absent. Narrow listeners returns
dependency exit `4` if neither proc parsing nor `ss` is usable.

**Step 5: Implement TLS file inspection**

Parse PEM/DER with `crypto/x509`; reject symlinks, non-regular files, paths
beginning with `-`, oversized files, and private-key output.

**Step 6: Run GREEN and commit**

```bash
go test -race ./internal/network
git add cmd/network-base internal/network
git commit -m "feat: add local network and TLS diagnostics"
```

### Task 8: `security-base`

**Files:**

- Create: `cmd/security-base/main.go`
- Create: `internal/security/plugin.go`
- Create: `internal/security/security_test.go`
- Create: `internal/security/accounts.go`
- Create: `internal/security/ssh.go`
- Create: `internal/security/firewall.go`
- Create: `internal/security/audit.go`

**Step 1: Write RED tests for four approved paths**

```go
[][]string{
	{"security", "audit"},
	{"security", "accounts"},
	{"security", "ssh"},
	{"security", "firewall"},
}
```

**Step 2: Implement accounts parsing**

Read bounded `/etc/passwd`, `/etc/group`, and accessible shadow metadata.
Never return password hashes. Report UID 0 duplicates, login shells, locked
state when safely detectable, and unsafe home permissions.

**Step 3: Implement SSH inspection**

Read compiled SSH config paths and, when available, run
`sshd -T -C user=root,host=localhost,addr=127.0.0.1` with direct argv. Redact
paths and values that can contain secrets.

**Step 4: Implement firewall inspection**

Use fixed `nft list ruleset` and `ufw status` argv. A root-only narrow firewall
probe returns privilege exit `3`; aggregate audit remains partial.

**Step 5: Add adversarial tests and run GREEN**

```bash
go test -race ./internal/security
```

**Step 6: Commit**

```bash
git add cmd/security-base internal/security
git commit -m "feat: add local security diagnostics"
```

### Task 9: `apt-base`

**Files:**

- Create: `cmd/apt-base/main.go`
- Create: `internal/apt/plugin.go`
- Create: `internal/apt/apt_test.go`
- Create: `internal/apt/status.go`
- Create: `internal/apt/sources.go`
- Create: `internal/apt/history.go`

**Step 1: Write RED tests for approved paths**

```go
[][]string{
	{"apt", "status"},
	{"apt", "updates"},
	{"apt", "sources"},
	{"apt", "history"},
}
```

**Step 2: Implement read-only collectors**

- status: dpkg database state and interrupted transactions;
- updates: fixed simulation/query argv only;
- sources: bounded `.list` and `.sources` parsing with credentials redacted;
- history: bounded local APT history parsing.

No command runs update, install, upgrade, remove, autoremove, clean, or
download.

**Step 3: Add poisoned proxy and credential URL RED tests**

Caller proxy variables must not reach commands. Userinfo in source URLs is
removed recursively.

**Step 4: Run GREEN and commit**

```bash
go test -race ./internal/apt
git add cmd/apt-base internal/apt
git commit -m "feat: add read-only apt diagnostics"
```

### Task 10: `baseline-base`

**Files:**

- Create: `cmd/baseline-base/main.go`
- Create: `internal/baseline/plugin.go`
- Create: `internal/baseline/baseline_test.go`
- Create: `internal/baseline/profile.go`
- Create: `internal/baseline/check.go`

**Step 1: Write RED tests**

Approved paths are `baseline check` and `baseline profile`. The default profile
contains compiled checks for supported OS, time sync, filesystem ownership,
kernel controls, SSH posture, package state, and required services.

**Step 2: Implement compiled profiles**

Config may enable/disable named compiled checks and adjust numeric thresholds.
It may not define commands, paths, or scripts.

**Step 3: Add deterministic and partial RED tests**

Check order and summaries are stable. Missing optional tools yield partial,
not a false pass.

**Step 4: Run GREEN and commit**

```bash
go test -race ./internal/baseline
git add cmd/baseline-base internal/baseline
git commit -m "feat: add local baseline evaluation"
```

### Task 11: Wave 1 roadmaps and build/release allowlists

**Files:**

- Create: `docs/roadmap/server-setup-base.md`
- Create: `docs/roadmap/network-base.md`
- Create: `docs/roadmap/security-base.md`
- Create: `docs/roadmap/apt-base.md`
- Create: `docs/roadmap/baseline-base.md`
- Modify: `README.md`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`

**Step 1: Write a failing repository test for plugin inventory**

Create `internal/protocol/plugin_inventory_test.go` that asserts every
`cmd/*-base` directory appears in CI and release allowlists and has a roadmap
file.

**Step 2: Observe RED**

```bash
go test ./internal/protocol -run PluginInventory -v
```

**Step 3: Add Wave 1 packages to matrices and allowlists**

Preserve action SHA pinning. Do not loosen the `*-base-v*` SemVer parser.

**Step 4: Write roadmaps**

Each file uses headings:

```markdown
# <plugin> roadmap
## Implemented in v1
## Deferred
### <capability>
- Reason
- Prerequisites
- Security design required
- Acceptance criteria
```

**Step 5: Run repository gates**

```bash
gofmt -w $(find cmd internal tools -name '*.go')
go test ./...
go test -race ./...
go vet ./...
for plugin in server-setup-base network-base security-base apt-base baseline-base; do
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "/tmp/$plugin" "./cmd/$plugin"
  "/tmp/$plugin" manifest --protocol=1 | go run ./tools/manifestcheck
done
```

Expected: PASS.

**Step 6: Commit**

```bash
git add README.md docs/roadmap .github internal/protocol
git commit -m "build: prepare wave one plugin releases"
```

## Wave 2

### Task 12: `postgres-base`

**Files:**

- Create: `cmd/postgres-base/main.go`
- Create: `internal/postgres/plugin.go`
- Create: `internal/postgres/postgres_test.go`
- Create: `internal/postgres/clusters.go`
- Create: `internal/postgres/config.go`
- Create: `internal/postgres/status.go`
- Create: `docs/roadmap/postgres-base.md`

**TDD sequence:**

1. Test paths: `postgres status`, `postgres clusters`, `postgres config`.
2. Observe missing package failure.
3. Implement local `pg_lsclusters`, systemd, process, package, and config-file
   probes.
4. Test that no `psql`, TCP, Unix socket, password, or database connection is
   attempted.
5. Test config redaction for `password`, connection URI userinfo, SSL key
   paths, and recovery credentials.
6. Test absent PostgreSQL as informational for status and dependency exit `4`
   for narrow cluster/config commands.
7. Run:

   ```bash
   go test -race ./internal/postgres
   ```

8. Commit:

   ```bash
   git add cmd/postgres-base internal/postgres docs/roadmap/postgres-base.md
   git commit -m "feat: add local postgres diagnostics"
   ```

### Task 13: `k8s-base`

**Files:**

- Create: `cmd/k8s-base/main.go`
- Create: `internal/k8s/plugin.go`
- Create: `internal/k8s/k8s_test.go`
- Create: `internal/k8s/status.go`
- Create: `internal/k8s/contexts.go`
- Create: `internal/k8s/manifests.go`
- Create: `docs/roadmap/k8s-base.md`

**TDD sequence:**

1. Test paths: `k8s status`, `k8s contexts`, `k8s manifests`.
2. Implement `kubectl version --client --output=json`, kubelet/systemd/process
   state, kubeconfig metadata, and bounded static manifest parsing.
3. Add a runner assertion that no argv can contact a cluster: reject
   `get`, `list`, `auth`, `--server`, and all network-oriented subcommands.
4. Never return kubeconfig tokens, client certificates, keys, exec args, or
   embedded auth-provider data.
5. Reject symlinked or oversized kubeconfigs/manifests.
6. Run `go test -race ./internal/k8s`.
7. Commit with `feat: add local kubernetes diagnostics`.

### Task 14: `java-base`

**Files:**

- Create: `cmd/java-base/main.go`
- Create: `internal/java/plugin.go`
- Create: `internal/java/java_test.go`
- Create: `internal/java/runtime.go`
- Create: `internal/java/processes.go`
- Create: `internal/java/inspect.go`
- Create: `docs/roadmap/java-base.md`

**TDD sequence:**

1. Test paths: `java runtime`, `java processes`, `java inspect <pid>`.
2. Validate PID with decimal-only, positive, bounded parsing; reject leading
   options and whitespace.
3. Parse local Java version output, `/proc` processes, and bounded `jcmd`
   metadata.
4. Use only approved read-only `jcmd <pid> VM.version`,
   `VM.command_line`, and `VM.system_properties` argv.
5. Redact system properties and command-line secrets.
6. Return privilege `3` for attach denial and dependency `4` when `jcmd` is
   required but missing.
7. Run `go test -race ./internal/java`.
8. Commit with `feat: add local java diagnostics`.

### Task 15: Prepare and publish Wave 2

Repeat Task 11 inventory tests for `postgres-base`, `k8s-base`, and
`java-base`. Update CI/release allowlists and README, run all gates, and commit:

```bash
git commit -m "build: prepare wave two plugin releases"
```

## Wave 3

### Task 16: `kafka-base`

**Files:**

- Create: `cmd/kafka-base/main.go`
- Create: `internal/kafka/plugin.go`
- Create: `internal/kafka/kafka_test.go`
- Create: `internal/kafka/status.go`
- Create: `internal/kafka/config.go`
- Create: `internal/kafka/storage.go`
- Create: `docs/roadmap/kafka-base.md`

**TDD sequence:**

1. Test paths: `kafka status`, `kafka config`, `kafka storage`.
2. Inspect only local processes, units, installation metadata, properties
   files, log directories, and fixed storage-info commands.
3. Assert no broker bootstrap option, socket, or remote connection is used.
4. Redact JAAS, passwords, tokens, keystore/truststore passwords, and SASL
   configuration.
5. Bound directory traversal and property-file sizes.
6. Run `go test -race ./internal/kafka`.
7. Commit with `feat: add local kafka diagnostics`.

### Task 17: `gitlab-runner-base`

**Files:**

- Create: `cmd/gitlab-runner-base/main.go`
- Create: `internal/gitlabrunner/plugin.go`
- Create: `internal/gitlabrunner/gitlabrunner_test.go`
- Create: `internal/gitlabrunner/status.go`
- Create: `internal/gitlabrunner/config.go`
- Create: `internal/gitlabrunner/executors.go`
- Create: `docs/roadmap/gitlab-runner-base.md`

**TDD sequence:**

1. Test paths: `gitlab-runner status`, `gitlab-runner config`,
   `gitlab-runner executors`.
2. Parse local unit/process/version and bounded TOML configuration.
3. Return executor names and safe settings only.
4. Never emit runner tokens, registration tokens, cache credentials, clone
   URLs with userinfo, environment secrets, or TLS private-key content.
5. Assert `verify`, `register`, `run`, `exec`, and job execution are never
   invoked.
6. Run `go test -race ./internal/gitlabrunner`.
7. Commit with `feat: add local gitlab runner diagnostics`.

### Task 18: `monitoring-base`

**Files:**

- Create: `cmd/monitoring-base/main.go`
- Create: `internal/monitoring/plugin.go`
- Create: `internal/monitoring/monitoring_test.go`
- Create: `internal/monitoring/status.go`
- Create: `internal/monitoring/inventory.go`
- Create: `internal/monitoring/config.go`
- Create: `docs/roadmap/monitoring-base.md`

**TDD sequence:**

1. Test paths: `monitoring status`, `monitoring inventory`,
   `monitoring config`.
2. Use a compiled product table for Prometheus, node_exporter, Alloy,
   Grafana Agent, Telegraf, and Zabbix Agent.
3. Inspect local packages, units, processes, listeners, and bounded config
   metadata only.
4. Do not scrape metrics or remote targets.
5. Redact bearer tokens, basic auth, SNMP communities, remote_write
   credentials, and cloud keys.
6. Missing products are informational in inventory; malformed installed
   product config makes status partial.
7. Run `go test -race ./internal/monitoring`.
8. Commit with `feat: add local monitoring inventory`.

### Task 19: Prepare and publish Wave 3

Update plugin inventory, CI/release allowlists, README, and roadmap checks for
Wave 3. Run all gates and commit:

```bash
git commit -m "build: prepare wave three plugin releases"
```

## Wave 4

### Task 20: `backup-base`

**Files:**

- Create: `cmd/backup-base/main.go`
- Create: `internal/backup/plugin.go`
- Create: `internal/backup/backup_test.go`
- Create: `internal/backup/status.go`
- Create: `internal/backup/inventory.go`
- Create: `internal/backup/history.go`
- Create: `docs/roadmap/backup-base.md`

**TDD sequence:**

1. Test paths: `backup status`, `backup inventory`, `backup history`.
2. Use a compiled adapter table for local Restic, Borg, rsnapshot, and
   systemd-timer metadata.
3. Inspect binary versions, units/timers, repository type, local config, and
   bounded local logs/history.
4. Do not unlock, mount, check, prune, backup, restore, or access a remote
   repository.
5. Redact repository passwords, environment files, cloud credentials, SSH
   targets, and URL userinfo.
6. Run `go test -race ./internal/backup`.
7. Commit with `feat: add local backup diagnostics`.

### Task 21: `incident-base`

**Files:**

- Create: `cmd/incident-base/main.go`
- Create: `internal/incident/plugin.go`
- Create: `internal/incident/incident_test.go`
- Create: `internal/incident/snapshot.go`
- Create: `internal/incident/services.go`
- Create: `internal/incident/timeline.go`
- Create: `docs/roadmap/incident-base.md`

**TDD sequence:**

1. Test paths: `incident snapshot`, `incident services`,
   `incident timeline --since`.
2. Snapshot aggregates bounded local OS, load, memory, disk, network listener,
   failed-unit, OOM, and recent-error probes through shared kernel modules.
3. Timeline accepts a positive bounded duration and fixed journal priority
   filters.
4. No archive or output file is written; all evidence remains in Result data.
5. Redact environment, command-line secrets, journal values, and usernames
   where policy requires.
6. Missing optional probes produce partial without suppressing usable evidence.
7. Run `go test -race ./internal/incident`.
8. Commit with `feat: add local incident snapshots`.

### Task 22: `runbook-base`

**Files:**

- Create: `cmd/runbook-base/main.go`
- Create: `internal/runbook/plugin.go`
- Create: `internal/runbook/runbook_test.go`
- Create: `internal/runbook/loader.go`
- Create: `internal/runbook/validate.go`
- Create: `internal/runbook/render.go`
- Create: `docs/roadmap/runbook-base.md`

**TDD sequence:**

1. Test paths: `runbook list`, `runbook inspect <name>`,
   `runbook validate [name]`.
2. Use fixed `/etc/ohtools/runbooks` root and strict bounded YAML.
3. Validate metadata and documentation steps only. Reject executable steps,
   shell, argv, URLs, includes, traversal, aliases, unknown fields, duplicate
   keys, and inline credentials.
4. Name validation is lower-case hyphen-case and rejects leading options.
5. Inspect renders normalized plain-text step descriptions, not raw HTML.
6. Runbook execution remains absent and documented in TODO.
7. Run `go test -race ./internal/runbook`.
8. Commit with `feat: add local runbook validation`.

### Task 23: Prepare and publish Wave 4

Update inventory, CI/release allowlists, README, and roadmap checks for Wave 4.
Run all repository gates and commit:

```bash
git commit -m "build: prepare wave four plugin releases"
```

## Cross-repository publication

### Task 24: Add container integration matrix

**Files:**

- Create: `scripts/integration.sh`
- Create: `test/integration/fixtures/`
- Modify: `.github/workflows/ci.yml`
- Modify: `docs/development.md`

**Step 1: Write a failing integration inventory test**

Require cases for Debian 10, 11, 12, 13 and Ubuntu 20.04, 22.04, 24.04.

**Step 2: Implement read-only container runs**

Containers are non-root where possible, read-only, no-new-privileges,
cap-drop-all, bounded CPU/memory/PIDs, and network-none. Root-required
server-setup tests use disposable privileged fixtures only in the protected
integration job.

**Step 3: Run all local gates**

```bash
gofmt -w $(find cmd internal tools -name '*.go')
go test ./...
go test -race ./...
go vet ./...
bash scripts/integration.sh
```

**Step 4: Commit**

```bash
git add scripts test .github/workflows/ci.yml docs/development.md
git commit -m "test: add supported platform integration matrix"
```

### Task 25: Publish plugin releases by wave

For every plugin in the wave:

1. merge its implementation PR;
2. verify the exact main commit;
3. create annotated `<plugin>-v1.0.0`;
4. push the tag;
5. wait for the protected release workflow;
6. verify binary, checksum, SBOM, static linkage, and manifest;
7. record release URL, size, digest, manifest, description, and timestamp.

Never reuse a tag or replace an asset.

Expected tags:

```text
server-setup-base-v1.0.0
network-base-v1.0.0
security-base-v1.0.0
apt-base-v1.0.0
baseline-base-v1.0.0
postgres-base-v1.0.0
k8s-base-v1.0.0
java-base-v1.0.0
kafka-base-v1.0.0
gitlab-runner-base-v1.0.0
monitoring-base-v1.0.0
backup-base-v1.0.0
incident-base-v1.0.0
runbook-base-v1.0.0
```

### Task 26: Add catalog metadata and signed sequences

**Repository:** `ohtoe02/ohtools-plugin-catalog`

**Files:**

- Create per release:
  `plugins/<plugin>/1.0.0.yaml`
- Modify tests only when a real catalog-tool defect is found.

For each wave:

1. copy the exact immutable coordinates from Task 25;
2. write strict YAML with identical manifest/catalog description;
3. run:

   ```bash
   go test ./...
   go run ./cmd/catalogctl validate --plugins plugins
   go run ./cmd/catalogctl materialize --plugins plugins --output verification
   ```

4. open a catalog PR;
5. wait for sandboxed asset verification;
6. merge after review;
7. publish exactly one protected signed sequence:
   wave 1 → 4, wave 2 → 5, wave 3 → 6, wave 4 → 7;
8. verify signatures, expiration, sequence, and immutable release assets with
   the released compatible host.

Catalog schema v1 is unchanged. Do not add TODO fields.

### Task 27: Add localized portal TODO summaries

**Repository:** `ohtoe02/ohtools-web`

**Files:**

- Create: `src/data/plugin-todo.ts`
- Create: `tests/unit/plugin-todo.test.ts`
- Modify: `src/components/PluginDetail.astro`
- Modify: `tests/e2e/portal.spec.ts`

**Step 1: Write RED content tests**

Tests require:

- an English and Russian summary for each published plugin ID;
- no TODO for a plugin missing from generated signed catalog data;
- a valid public roadmap URL under `ohtoe02/ohtools-plugins`;
- plain-text-only summary values.

**Step 2: Observe RED**

```bash
pnpm vitest run tests/unit/plugin-todo.test.ts
```

**Step 3: Add localized data**

```ts
export interface PluginTodoCopy {
  summary: string;
  roadmapUrl: string;
}

export const pluginTodo: Record<
  string,
  { en: PluginTodoCopy; ru: PluginTodoCopy }
> = {
  // Only already-signed plugin IDs for the current wave.
};
```

Do not duplicate catalog versions, commands, digests, or compatibility fields.

**Step 4: Render a Deferred / TODO section**

All upstream text is rendered as text. Links are HTTPS GitHub URLs without
credentials.

**Step 5: Extend Playwright tests**

Verify EN/RU TODO, plugin history, mobile layout, keyboard navigation, and no
client catalog request.

**Step 6: Run portal gates**

```bash
pnpm format:check
pnpm typecheck
pnpm test
pnpm test:e2e
pnpm build
pnpm check:routes
```

**Step 7: Commit and deploy after each signed wave**

```bash
git add src/data/plugin-todo.ts src/components/PluginDetail.astro tests
git commit -m "docs: add deferred scope for plugin wave"
```

Wait for protected CI and Pages deployment. Verify `/plugins/<name>/`,
`/ru/plugins/<name>/`, and `/data/catalog-state.json` over HTTPS.

### Task 28: Final acceptance audit

**Files:**

- Create: `docs/plans/2026-07-27-release-ready-plugin-waves-verification.md`

Record:

- fourteen release tags and immutable asset digests;
- catalog sequences 4–7 and signing key ID;
- host versions used for verification;
- all plugin commands and Result/exit behavior;
- platform matrix results;
- live portal routes;
- deferred roadmap links;
- any residual limitations.

Run final gates in all four repositories. Confirm:

- no open failing checks;
- no unsigned or draft plugin appears in the portal;
- no placeholder command was published;
- only `server-setup-base` mutates;
- all TODO items are outside catalog schema v1;
- old tags, releases, and signed snapshots are unchanged.

Commit:

```bash
git add docs/plans/2026-07-27-release-ready-plugin-waves-verification.md
git commit -m "docs: verify release-ready plugin waves"
```
