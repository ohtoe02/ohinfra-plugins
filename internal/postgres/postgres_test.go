package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestDefinitionDeclaresOnlyApprovedReadOnlyCommands(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{Version: "1.0.0", Root: t.TempDir()})
	got := make([][]string, 0, len(definition.Manifest.Commands))
	for _, command := range definition.Manifest.Commands {
		got = append(got, command.Path)
		if command.Category != protocol.CategoryDiagnostic || command.RequiresRoot ||
			command.RequiresForce || command.RequiresConfirmation || command.SupportsDryRun {
			t.Fatalf("command has mutation requirements: %#v", command)
		}
	}
	want := [][]string{
		{"postgres", "status"},
		{"postgres", "clusters"},
		{"postgres", "config"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command paths = %#v, want %#v", got, want)
	}
	if err := protocol.ValidateManifest(definition.Manifest); err != nil {
		t.Fatalf("manifest is invalid: %v", err)
	}

	plan, err := definition.Plan(context.Background(), invocation("postgres", "status"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 || plan.RequiresRoot || plan.RequiresConfirmation {
		t.Fatalf("read-only plan can mutate: %#v", plan)
	}
}

func TestClustersUsesOnlyFixedLocalPGClustersProbe(t *testing.T) {
	t.Setenv("PGHOST", "attacker.invalid")
	t.Setenv("PGPASSWORD", "database-secret")
	t.Setenv("DATABASE_URL", "postgres://alice:uri-secret@attacker.invalid/db")

	var calls []execx.Spec
	definition := NewDefinition(Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			return execx.Output{Stdout: []byte(
				"15 main 5432 online postgres /var/lib/postgresql/15/main /var/log/postgresql/postgresql-15-main.log\n" +
					"malformed\n",
			)}, nil
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("postgres", "clusters"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("probe calls = %#v, want one pg_lsclusters call", calls)
	}
	got := calls[0]
	if got.Program != "pg_lsclusters" || !reflect.DeepEqual(got.Arguments, []string{"--no-header"}) {
		t.Fatalf("probe = %#v, want fixed local pg_lsclusters argv", got)
	}
	if len(got.Environment) != 0 || len(got.Stdin) != 0 {
		t.Fatalf("probe propagates caller input: %#v", got)
	}
	encoded := resultText(t, result)
	for _, forbidden := range []string{
		"database-secret", "uri-secret", "alice", "attacker.invalid", "malformed",
		"psql", "--host", "-h", "postgres://",
	} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("result contains forbidden value %q: %s", forbidden, encoded)
		}
	}
	clusters, ok := result.Data["clusters"].([]Cluster)
	if !ok || len(clusters) != 1 || clusters[0].Version != "15" ||
		clusters[0].Name != "main" || clusters[0].Port != 5432 ||
		clusters[0].Status != "online" {
		t.Fatalf("clusters = %#v", result.Data["clusters"])
	}
}

func TestClustersRequiresLocalPGClustersDependency(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{}, execx.ErrNotFound
		}),
	})
	_, err := definition.Execute(context.Background(), invocation("postgres", "clusters"))
	var failure protocol.ExitError
	if !errors.As(err, &failure) || failure.Code != protocol.ExitDependency {
		t.Fatalf("execute error = %v, want dependency exit 4", err)
	}
}

func TestClustersRejectsUnsafeOrTruncatedProbeOutput(t *testing.T) {
	t.Parallel()

	t.Run("unsafe fields", func(t *testing.T) {
		definition := NewDefinition(Options{
			Root: t.TempDir(),
			Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
				return execx.Output{Stdout: []byte(
					"15 main 5432 online postgres postgres://alice:uri-secret@db.invalid/data /tmp/postgres.log\n",
				)}, nil
			}),
		})
		result, err := definition.Execute(context.Background(), invocation("postgres", "clusters"))
		if err != nil {
			t.Fatal(err)
		}
		if text := resultText(t, result); strings.Contains(text, "uri-secret") ||
			strings.Contains(text, "alice") || strings.Contains(text, "db.invalid") {
			t.Fatalf("unsafe cluster line leaked into result: %s", text)
		}
	})

	t.Run("truncated stderr", func(t *testing.T) {
		definition := NewDefinition(Options{
			Root: t.TempDir(),
			Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
				return execx.Output{
					Stderr: []byte("password=truncated-secret"), StderrTruncated: true,
				}, nil
			}),
		})
		_, err := definition.Execute(context.Background(), invocation("postgres", "clusters"))
		assertExitCode(t, err, protocol.ExitDependency)
		if strings.Contains(err.Error(), "truncated-secret") {
			t.Fatalf("dependency error leaks stderr: %v", err)
		}
	})
}

func TestClustersAreDeterministicallyOrdered(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{Stdout: []byte(
				"16 zeta 5433 down postgres /var/lib/postgresql/16/zeta /var/log/postgresql/zeta.log\n" +
					"15 main 5432 online postgres /var/lib/postgresql/15/main /var/log/postgresql/main.log\n",
			)}, nil
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("postgres", "clusters"))
	if err != nil {
		t.Fatal(err)
	}
	clusters := result.Data["clusters"].([]Cluster)
	if len(clusters) != 2 || clusters[0].Version != "15" || clusters[1].Version != "16" {
		t.Fatalf("clusters are not deterministic: %#v", clusters)
	}
}

func TestClustersPropagatesCancellationWithoutFallback(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	definition := NewDefinition(Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			called = true
			return execx.Output{}, nil
		}),
	})
	_, err := definition.Execute(ctx, invocation("postgres", "clusters"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execute error = %v, want context cancellation", err)
	}
	if called {
		t.Fatal("runner was called after cancellation")
	}
}

func TestStatusReportsAbsentPostgreSQLAsInformational(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program != "pg_lsclusters" && spec.Program != "systemctl" {
				t.Fatalf("unexpected program %q", spec.Program)
			}
			return execx.Output{}, execx.ErrNotFound
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("postgres", "status"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPass || result.Data["installed"] != false {
		t.Fatalf("absent status = %#v", result)
	}
	if len(result.Checks) != 1 || result.Checks[0].Status != protocol.StatusInfo {
		t.Fatalf("absent checks = %#v, want one informational check", result.Checks)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("absent PostgreSQL emitted errors: %#v", result.Errors)
	}
}

func TestStatusCombinesOnlyLocalPackageProcessClusterAndSystemdState(t *testing.T) {
	t.Setenv("PGSERVICE", "attacker-service")
	t.Setenv("PGPASSFILE", "C:\\secret\\pgpass")

	root := t.TempDir()
	writeFixture(t, root, "var/lib/dpkg/status", `Package: postgresql-15
Status: install ok installed
Version: 15.8-0+deb12u1

Package: unrelated
Status: install ok installed
`)
	writeFixture(t, root, "proc/4242/comm", "postgres\n")
	writeFixture(t, root, "proc/4242/status", "Name:\tpostgres\nUid:\t111\t111\t111\t111\n")

	var calls []execx.Spec
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			switch spec.Program {
			case "pg_lsclusters":
				return execx.Output{Stdout: []byte(
					"15 main 5432 online postgres /var/lib/postgresql/15/main /var/log/postgresql/postgresql-15-main.log\n",
				)}, nil
			case "systemctl":
				return execx.Output{Stdout: []byte("LoadState=loaded\nActiveState=active\nSubState=exited\n")}, nil
			default:
				t.Fatalf("unexpected program %q", spec.Program)
				return execx.Output{}, nil
			}
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("postgres", "status"))
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []execx.Spec{
		{
			Program: "pg_lsclusters", Arguments: []string{"--no-header"},
			StdoutLimit: clusterOutputLimit, StderrLimit: 16 << 10,
		},
		{
			Program: "systemctl",
			Arguments: []string{
				"show", "postgresql.service",
				"--property=LoadState,ActiveState,SubState", "--no-pager",
			},
			StdoutLimit: 32 << 10, StderrLimit: 16 << 10,
		},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("local probe calls = %#v, want %#v", calls, wantCalls)
	}
	if result.Data["installed"] != true ||
		result.Data["package_count"] != 1 ||
		result.Data["process_count"] != 1 ||
		result.Data["cluster_count"] != 1 ||
		result.Data["systemd_active_state"] != "active" {
		t.Fatalf("status data = %#v", result.Data)
	}
	text := resultText(t, result)
	for _, forbidden := range []string{
		"attacker-service", "pgpass", "PGSERVICE", "PGPASSFILE", "psql", "postgres://",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("status leaks forbidden value %q: %s", forbidden, text)
		}
	}
}

func TestStatusIgnoresSymlinkedProcEntriesAndBoundsInventory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "proc/100/comm", "postgres\n")
	writeFixture(t, root, "proc/100/status", "Name:\tpostgres\nUid:\t111\t111\t111\t111\n")
	target := filepath.Join(root, "outside")
	if err := os.WriteFile(target, []byte("postgres\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "proc", "101"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "proc", "101", "comm")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		if spec.Program == "systemctl" {
			return execx.Output{Stdout: []byte("LoadState=not-found\n")}, nil
		}
		return execx.Output{}, execx.ErrNotFound
	})
	definition := NewDefinition(Options{Root: root, Runner: runner})
	result, err := definition.Execute(context.Background(), invocation("postgres", "status"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Data["process_count"] != 1 {
		t.Fatalf("process count = %#v, want only regular confined proc entry", result.Data)
	}
}

func TestStatusRejectsMalformedInvocation(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{Root: t.TempDir()})
	bad := invocation("postgres", "status")
	bad.Arguments = []string{strconv.Itoa(1)}
	_, err := definition.Execute(context.Background(), bad)
	var failure protocol.ExitError
	if !errors.As(err, &failure) || failure.Code != protocol.ExitArguments {
		t.Fatalf("execute error = %v, want arguments exit 2", err)
	}
}

func TestStatusRejectsUnexpectedSystemdValues(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program == "systemctl" {
				return execx.Output{Stdout: []byte(
					"LoadState=loaded\n" +
						"ActiveState=postgres://alice:state-secret@attacker.invalid/db\n" +
						"SubState=running\n",
				)}, nil
			}
			return execx.Output{}, execx.ErrNotFound
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("postgres", "status"))
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, result)
	for _, value := range []string{"alice", "state-secret", "attacker.invalid"} {
		if strings.Contains(text, value) {
			t.Fatalf("status leaks unexpected systemd value %q: %s", value, text)
		}
	}
}

func TestConfigReturnsOnlyAllowlistedLocalSettings(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "etc/postgresql/15/main/postgresql.conf", `
port = 5433
listen_addresses = '127.0.0.1'
max_connections = 200
password = 'plain-secret'
primary_conninfo = 'postgres://replica:recovery-secret@db.example/recovery'
ssl_key_file = '/etc/ssl/private/server-secret.key'
restore_command = 'curl https://backup-user:backup-secret@example.invalid/%f'
cluster_name = 'postgres://alice:uri-secret@example.invalid/db'
malformed setting without equals
`)
	writeFixture(t, root, "etc/postgresql/15/main/pg_hba.conf", `
hostssl all all 127.0.0.1/32 scram-sha-256
`)

	called := false
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			called = true
			return execx.Output{}, nil
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("postgres", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("config inspection invoked an external program")
	}
	files, ok := result.Data["files"].([]ConfigFile)
	if !ok || len(files) != 2 {
		t.Fatalf("config files = %#v", result.Data["files"])
	}
	var postgresConfig ConfigFile
	for _, file := range files {
		if strings.HasSuffix(file.Path, "postgresql.conf") {
			postgresConfig = file
		}
	}
	wantSettings := map[string]string{
		"listen_addresses": "127.0.0.1",
		"max_connections":  "200",
		"port":             "5433",
	}
	if !reflect.DeepEqual(postgresConfig.Settings, wantSettings) {
		t.Fatalf("safe settings = %#v, want %#v", postgresConfig.Settings, wantSettings)
	}
	text := resultText(t, result)
	for _, secret := range []string{
		"plain-secret", "recovery-secret", "replica", "db.example",
		"server-secret.key", "/etc/ssl/private", "backup-user", "backup-secret",
		"example.invalid", "alice", "uri-secret", "primary_conninfo",
		"ssl_key_file", "restore_command", "password", "cluster_name",
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("config result leaks %q: %s", secret, text)
		}
	}
}

func TestConfigRequiresPostgreSQLAndRejectsUnsafeFiles(t *testing.T) {
	t.Parallel()

	t.Run("absent", func(t *testing.T) {
		definition := NewDefinition(Options{Root: t.TempDir()})
		_, err := definition.Execute(context.Background(), invocation("postgres", "config"))
		assertExitCode(t, err, protocol.ExitDependency)
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(root, "outside.conf")
		if err := os.WriteFile(outside, []byte("password='outside-secret'\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		configDir := filepath.Join(root, "etc", "postgresql", "15", "main")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(configDir, "postgresql.conf")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		definition := NewDefinition(Options{Root: root})
		_, err := definition.Execute(context.Background(), invocation("postgres", "config"))
		assertExitCode(t, err, protocol.ExitDependency)
		if err != nil && strings.Contains(err.Error(), "outside-secret") {
			t.Fatalf("error leaks symlink target content: %v", err)
		}
	})

	t.Run("oversize", func(t *testing.T) {
		root := t.TempDir()
		tooLarge := strings.Repeat("x", configFileLimit+1)
		writeFixture(t, root, "etc/postgresql/15/main/postgresql.conf", tooLarge)
		definition := NewDefinition(Options{Root: root})
		_, err := definition.Execute(context.Background(), invocation("postgres", "config"))
		assertExitCode(t, err, protocol.ExitDependency)
	})
}

func TestConfigPropagatesCancellation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "etc/postgresql/15/main/postgresql.conf", "port=5432\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	definition := NewDefinition(Options{Root: root})
	_, err := definition.Execute(ctx, invocation("postgres", "config"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execute error = %v, want context cancellation", err)
	}
}

func invocation(path ...string) protocol.Invocation {
	return protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "postgres-test",
		CommandPath:     path,
		Arguments:       []string{},
		Options:         map[string]any{},
	}
}

func assertExitCode(t *testing.T, err error, code int) {
	t.Helper()
	var failure protocol.ExitError
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("execute error = %v, want exit %d", err, code)
	}
}

func writeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func resultText(t *testing.T, result protocol.Result) string {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
