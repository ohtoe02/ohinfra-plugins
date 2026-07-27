package security

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/redact"
)

var fixedNow = time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)

func TestManifestExposesOnlyApprovedReadOnlyCommands(t *testing.T) {
	definition := NewDefinition(Options{Version: "1.0.0"})
	got := make([][]string, 0, len(definition.Manifest.Commands))
	for _, command := range definition.Manifest.Commands {
		got = append(got, command.Path)
		if command.Category != protocol.CategoryDiagnostic ||
			command.RequiresRoot || command.RequiresForce ||
			command.RequiresConfirmation || command.SupportsDryRun {
			t.Fatalf("command is not strictly read-only: %+v", command)
		}
	}
	want := [][]string{
		{"security", "audit"},
		{"security", "accounts"},
		{"security", "ssh"},
		{"security", "firewall"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command paths = %#v, want %#v", got, want)
	}

	plan, err := definition.Plan(context.Background(), invocation("security", "audit"))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Changes) != 0 || plan.RequiresRoot || plan.RequiresConfirmation || plan.RequiresForce {
		t.Fatalf("read-only plan contains mutation requirements: %+v", plan)
	}
}

func TestAccountsNeverReturnPasswordHashesAndReportRiskMetadata(t *testing.T) {
	root := securityFixture(t)
	writeFixture(t, root, "etc/passwd", strings.Join([]string{
		"root:x:0:0:root:/root:/bin/bash",
		"admin:x:0:1000:Admin:/home/admin:/bin/sh",
		"locked:x:1001:1001:Locked:/home/locked:/usr/sbin/nologin",
	}, "\n")+"\n", 0o644)
	writeFixture(t, root, "etc/group", strings.Join([]string{
		"root:x:0:",
		"admin:grouppassword:1000:admin",
	}, "\n")+"\n", 0o644)
	writeFixture(t, root, "etc/shadow", strings.Join([]string{
		"root:$6$superhash:20000:0:99999:7:::",
		"admin:!lockedhash:20000:0:99999:7:::",
		"locked:*:20000:0:99999:7:::",
	}, "\n")+"\n", 0o600)
	writeFixture(t, root, "root/.keep", "", 0o600)
	writeFixture(t, root, "home/admin/.keep", "", 0o600)
	writeFixture(t, root, "home/locked/.keep", "", 0o600)

	result, err := definitionFor(root, nil).Execute(
		context.Background(), invocation("security", "accounts"),
	)
	if err != nil {
		t.Fatalf("execute accounts: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{"superhash", "lockedhash", "grouppassword", "$6$"} {
		if strings.Contains(text, secret) {
			t.Fatalf("result leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, `"locked":true`) {
		t.Fatalf("result does not expose safe locked-state metadata: %s", text)
	}
	if !hasCheck(result, "accounts.duplicate-uid-zero", protocol.StatusWarning) {
		t.Fatalf("duplicate UID 0 warning missing: %+v", result.Checks)
	}
	if result.Changes == nil || len(result.Changes) != 0 {
		t.Fatalf("accounts result must normalize empty changes: %#v", result.Changes)
	}
}

func TestSSHAndFirewallUseOnlyFixedDirectArgvAndRedactOutput(t *testing.T) {
	root := securityFixture(t)
	writeFixture(t, root, "etc/ssh/sshd_config", "PasswordAuthentication no\nPermitRootLogin no\n", 0o600)
	recorder := &recordingRunner{outputs: map[string]execx.Output{
		"sshd": {Stdout: []byte("password=topsecret\npermitrootlogin no\n"), ExitCode: 0},
		"nft":  {Stdout: []byte("table inet filter { token=abc123 }\n"), ExitCode: 0},
		"ufw":  {Stdout: []byte("Status: active\n"), ExitCode: 0},
	}}
	definition := definitionFor(root, recorder)

	sshResult, err := definition.Execute(context.Background(), invocation("security", "ssh"))
	if err != nil {
		t.Fatalf("execute ssh: %v", err)
	}
	firewallResult, err := definition.Execute(context.Background(), invocation("security", "firewall"))
	if err != nil {
		t.Fatalf("execute firewall: %v", err)
	}

	want := []execx.Spec{
		{Program: "sshd", Arguments: []string{"-T", "-C", "user=root,host=localhost,addr=127.0.0.1"}, StdoutLimit: probe.DefaultCommandOutputLimit, StderrLimit: probe.DefaultCommandOutputLimit},
		{Program: "nft", Arguments: []string{"list", "ruleset"}, StdoutLimit: probe.DefaultCommandOutputLimit, StderrLimit: probe.DefaultCommandOutputLimit},
		{Program: "ufw", Arguments: []string{"status"}, StdoutLimit: probe.DefaultCommandOutputLimit, StderrLimit: probe.DefaultCommandOutputLimit},
	}
	if !reflect.DeepEqual(recorder.specs, want) {
		t.Fatalf("command specs = %#v, want %#v", recorder.specs, want)
	}
	for _, spec := range recorder.specs {
		if len(spec.Environment) != 0 || len(spec.Stdin) != 0 ||
			strings.Contains(strings.Join(spec.Arguments, " "), "http") ||
			strings.ContainsAny(strings.Join(spec.Arguments, " "), ";\n\r") {
			t.Fatalf("unsafe command spec: %+v", spec)
		}
	}
	encoded, _ := json.Marshal([]protocol.Result{sshResult, firewallResult})
	for _, secret := range []string{"topsecret", "abc123"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("recursive redaction missed %q: %s", secret, encoded)
		}
	}
}

func TestSSHReadsCompiledDropInsWithoutFollowingSymlinks(t *testing.T) {
	root := securityFixture(t)
	writeFixture(t, root, "etc/ssh/sshd_config.d/50-hardening.conf",
		"PasswordAuthentication no\nPermitRootLogin prohibit-password\n", 0o600)
	runner := execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
		return execx.Output{}, execx.ErrNotFound
	})

	result, err := definitionFor(root, runner).Execute(
		context.Background(), invocation("security", "ssh"),
	)
	if err != nil {
		t.Fatalf("compiled drop-in is a usable SSH source: %v", err)
	}
	sshData, ok := result.Data["configured_directives"].(map[string]string)
	if !ok {
		t.Fatalf("configured_directives type = %T", result.Data["configured_directives"])
	}
	if sshData["passwordauthentication"] != redact.Mask ||
		sshData["permitrootlogin"] != "prohibit-password" {
		t.Fatalf("drop-in directives = %#v", sshData)
	}
}

func TestAuditReturnsPartialWhenOptionalLocalSourcesAreUnavailable(t *testing.T) {
	root := securityFixture(t)
	writeFixture(t, root, "etc/passwd", "root:x:0:0:root:/root:/bin/bash\n", 0o644)
	writeFixture(t, root, "etc/group", "root:x:0:\n", 0o644)
	runner := execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
		return execx.Output{}, execx.ErrNotFound
	})

	result, err := definitionFor(root, runner).Execute(
		context.Background(), invocation("security", "audit"),
	)
	if err != nil {
		t.Fatalf("aggregate audit must return a partial result, not an exit error: %v", err)
	}
	if result.Status != protocol.StatusPartial {
		t.Fatalf("audit status = %q, want partial; result=%+v", result.Status, result)
	}
	if len(result.Errors) == 0 {
		t.Fatal("partial audit must identify unavailable sources")
	}
}

func TestNarrowFirewallReturnsPrivilegeExitWhenAllProbesAreDenied(t *testing.T) {
	root := securityFixture(t)
	runner := execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
		return execx.Output{ExitCode: 1, Stderr: []byte("Operation not permitted: password=do-not-leak")}, nil
	})

	_, err := definitionFor(root, runner).Execute(
		context.Background(), invocation("security", "firewall"),
	)
	assertExitCode(t, err, protocol.ExitPrivilege)
	if strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("exit error leaked a secret: %v", err)
	}
}

func TestNarrowCommandsReturnDependencyExitWhenNoUsableSourceExists(t *testing.T) {
	root := securityFixture(t)
	runner := execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
		return execx.Output{}, execx.ErrNotFound
	})
	definition := definitionFor(root, runner)

	_, err := definition.Execute(context.Background(), invocation("security", "firewall"))
	assertExitCode(t, err, protocol.ExitDependency)
	_, err = definition.Execute(context.Background(), invocation("security", "ssh"))
	assertExitCode(t, err, protocol.ExitDependency)
}

func TestAccountsRejectOversizedOrSymlinkedIdentityFiles(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		root := securityFixture(t)
		writeFixture(t, root, "etc/passwd", strings.Repeat("x", int(identityFileLimit+1)), 0o644)
		_, err := definitionFor(root, nil).Execute(
			context.Background(), invocation("security", "accounts"),
		)
		assertExitCode(t, err, protocol.ExitDependency)
	})

	t.Run("symlink", func(t *testing.T) {
		root := securityFixture(t)
		outside := filepath.Join(t.TempDir(), "passwd")
		if err := os.WriteFile(outside, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "etc", "passwd")
		if err := os.Symlink(outside, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink privilege unavailable: %v", err)
			}
			t.Fatal(err)
		}
		_, err := definitionFor(root, nil).Execute(
			context.Background(), invocation("security", "accounts"),
		)
		assertExitCode(t, err, protocol.ExitDependency)
	})
}

func TestExecuteRejectsArgumentsOptionsAndUnknownCommands(t *testing.T) {
	definition := NewDefinition(Options{Version: "1.0.0"})
	bad := []protocol.Invocation{
		{ProtocolVersion: 1, CommandPath: []string{"security", "accounts"}, Arguments: []string{"; id"}, Options: map[string]any{}},
		{ProtocolVersion: 1, CommandPath: []string{"security", "ssh"}, Arguments: []string{}, Options: map[string]any{"config": "/tmp/x"}},
		{ProtocolVersion: 1, CommandPath: []string{"security", "unknown"}, Arguments: []string{}, Options: map[string]any{}},
	}
	for _, input := range bad {
		_, err := definition.Execute(context.Background(), input)
		assertExitCode(t, err, protocol.ExitArguments)
	}
}

func TestCollectorCancellationIsFatalForNarrowAndAggregateCommands(t *testing.T) {
	t.Run("accounts precheck", func(t *testing.T) {
		root := securityFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		collected := collectAccounts(ctx, normalizeOptions(Options{
			Version: "1.0.0", Root: root, Local: probe.Local{Root: root},
		}))
		if !errors.Is(collected.Fatal, context.Canceled) {
			t.Fatalf("accounts fatal = %v, want context.Canceled", collected.Fatal)
		}
	})

	t.Run("narrow ssh deadline", func(t *testing.T) {
		root := securityFixture(t)
		writeFixture(t, root, "etc/ssh/sshd_config", "PermitRootLogin no\n", 0o600)
		runner := execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{}, context.DeadlineExceeded
		})
		_, err := definitionFor(root, runner).Execute(
			context.Background(), invocation("security", "ssh"),
		)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ssh error = %v, want context.DeadlineExceeded", err)
		}
	})

	t.Run("narrow firewall cancellation stops probes", func(t *testing.T) {
		root := securityFixture(t)
		calls := 0
		runner := execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			calls++
			return execx.Output{}, context.Canceled
		})
		_, err := definitionFor(root, runner).Execute(
			context.Background(), invocation("security", "firewall"),
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("firewall error = %v, want context.Canceled", err)
		}
		if calls != 1 {
			t.Fatalf("firewall continued after cancellation: calls=%d", calls)
		}
	})

	t.Run("narrow ufw deadline is fatal after nft success", func(t *testing.T) {
		root := securityFixture(t)
		calls := 0
		runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls++
			if spec.Program == "nft" {
				return execx.Output{ExitCode: 0, Stdout: []byte("table inet filter {}\n")}, nil
			}
			return execx.Output{}, context.DeadlineExceeded
		})
		_, err := definitionFor(root, runner).Execute(
			context.Background(), invocation("security", "firewall"),
		)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("firewall error = %v, want context.DeadlineExceeded", err)
		}
		if calls != 2 {
			t.Fatalf("ufw probe was not reached exactly once: calls=%d", calls)
		}
	})

	t.Run("aggregate cancellation", func(t *testing.T) {
		root := securityFixture(t)
		writeFixture(t, root, "etc/passwd", "root:x:0:0:root:/root:/bin/bash\n", 0o644)
		writeFixture(t, root, "etc/group", "root:x:0:\n", 0o644)
		runner := execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{}, context.Canceled
		})
		_, err := definitionFor(root, runner).Execute(
			context.Background(), invocation("security", "audit"),
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("audit error = %v, want context.Canceled", err)
		}
	})
}

func TestOptionsUseOneAuthoritativeRootAndDefaultHostname(t *testing.T) {
	t.Run("injected local root becomes authoritative", func(t *testing.T) {
		root := securityFixture(t)
		writeFixture(t, root, "etc/passwd", "local-root:x:1000:1000::/home/local-root:/bin/sh\n", 0o644)
		writeFixture(t, root, "etc/group", "local-root:x:1000:\n", 0o644)
		definition := NewDefinition(Options{
			Version: "1.0.0",
			Local:   probe.Local{Root: root},
			Now:     func() time.Time { return fixedNow },
		})
		result, err := definition.Execute(context.Background(), invocation("security", "accounts"))
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if !resultHasAccount(result, "local-root") {
			t.Fatalf("injected Local.Root was not used: %+v", result.Data)
		}
		hostname, hostnameErr := os.Hostname()
		if hostnameErr != nil {
			t.Fatal(hostnameErr)
		}
		if result.Host != hostname {
			t.Fatalf("host = %q, want os.Hostname %q", result.Host, hostname)
		}
	})

	t.Run("explicit root overrides mismatched local root", func(t *testing.T) {
		root := securityFixture(t)
		otherRoot := securityFixture(t)
		writeFixture(t, root, "etc/passwd", "authoritative:x:1000:1000::/home/authoritative:/bin/sh\n", 0o644)
		writeFixture(t, root, "etc/group", "authoritative:x:1000:\n", 0o644)
		writeFixture(t, otherRoot, "etc/passwd", "other-tree:x:1000:1000::/home/other-tree:/bin/sh\n", 0o644)
		writeFixture(t, otherRoot, "etc/group", "other-tree:x:1000:\n", 0o644)
		definition := NewDefinition(Options{
			Version: "1.0.0", Root: root, Host: "fixture",
			Local: probe.Local{Root: otherRoot},
			Now:   func() time.Time { return fixedNow },
		})
		result, err := definition.Execute(context.Background(), invocation("security", "accounts"))
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if !resultHasAccount(result, "authoritative") || resultHasAccount(result, "other-tree") {
			t.Fatalf("collector read a non-authoritative tree: %+v", result.Data)
		}
	})
}

type recordingRunner struct {
	specs   []execx.Spec
	outputs map[string]execx.Output
}

func (runner *recordingRunner) Run(_ context.Context, spec execx.Spec) (execx.Output, error) {
	runner.specs = append(runner.specs, spec)
	output, ok := runner.outputs[spec.Program]
	if !ok {
		return execx.Output{}, execx.ErrNotFound
	}
	return output, nil
}

func definitionFor(root string, runner execx.Runner) protocol.Definition {
	return NewDefinition(Options{
		Version:   "1.0.0",
		Commit:    "deadbeef",
		BuildDate: "2026-07-27T00:00:00Z",
		Host:      "fixture",
		Root:      root,
		Local:     probe.Local{Root: root, Runner: runner},
		Now:       func() time.Time { return fixedNow },
	})
}

func invocation(path ...string) protocol.Invocation {
	return protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     path,
		Arguments:       []string{},
		Options:         map[string]any{},
	}
}

func securityFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"etc", "etc/ssh", "root", "home", "usr", "var"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(directory)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(t, root, "etc/os-release", "ID=debian\nVERSION_ID=\"12\"\n", 0o644)
	return root
}

func writeFixture(t *testing.T, root, relative, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func hasCheck(result protocol.Result, id string, status protocol.Status) bool {
	for _, check := range result.Checks {
		if check.ID == id && check.Status == status {
			return true
		}
	}
	return false
}

func resultHasAccount(result protocol.Result, name string) bool {
	accounts, ok := result.Data["accounts"].([]accountMetadata)
	if !ok {
		return false
	}
	for _, account := range accounts {
		if account.Name == name {
			return true
		}
	}
	return false
}

func assertExitCode(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected exit code %d, got nil", want)
	}
	var failure protocol.ExitError
	if !errors.As(err, &failure) || failure.Code != want {
		t.Fatalf("error = %v, want ExitError code %d", err, want)
	}
}
