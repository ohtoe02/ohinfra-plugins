package monitoring

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
	"time"

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
		{"monitoring", "status"},
		{"monitoring", "inventory"},
		{"monitoring", "config"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command paths = %#v, want %#v", got, want)
	}
	if err := protocol.ValidateManifest(definition.Manifest); err != nil {
		t.Fatalf("manifest is invalid: %v", err)
	}

	plan, err := definition.Plan(context.Background(), invocation("monitoring", "status"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 || plan.RequiresRoot || plan.RequiresConfirmation {
		t.Fatalf("read-only plan can mutate: %#v", plan)
	}
}

func TestInventoryUsesCompiledProductsAndOnlyFixedLocalProbes(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://alice:proxy-secret@attacker.invalid")
	t.Setenv("PROMETHEUS_URL", "https://alice:api-secret@attacker.invalid")

	root := t.TempDir()
	writeFixture(t, root, "var/lib/dpkg/status", `Package: prometheus
Status: install ok installed
Version: 2.45.0

Package: unrelated
Status: install ok installed
Version: 1
`)
	writeFixture(t, root, "proc/42/comm", "prometheus\n")

	var calls []execx.Spec
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			switch spec.Program {
			case "systemctl":
				if reflect.DeepEqual(spec.Arguments, []string{
					"show", "prometheus.service",
					"--property=LoadState,ActiveState,SubState", "--no-pager",
				}) {
					return execx.Output{
						Stdout: []byte("LoadState=loaded\nActiveState=active\nSubState=running\n"),
					}, nil
				}
				return execx.Output{
					Stdout: []byte("LoadState=not-found\nActiveState=inactive\nSubState=dead\n"),
				}, nil
			case "ss":
				return execx.Output{
					Stdout: []byte("tcp LISTEN 0 4096 127.0.0.1:9090 0.0.0.0:*\n"),
				}, nil
			default:
				t.Fatalf("unexpected program %q", spec.Program)
				return execx.Output{}, nil
			}
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("monitoring", "inventory"))
	if err != nil {
		t.Fatal(err)
	}

	products, ok := result.Data["products"].([]ProductInventory)
	if !ok || len(products) != 6 {
		t.Fatalf("products = %#v, want six compiled products", result.Data["products"])
	}
	wantIDs := []string{
		"alloy", "grafana-agent", "node-exporter", "prometheus", "telegraf", "zabbix-agent",
	}
	gotIDs := make([]string, 0, len(products))
	for _, product := range products {
		gotIDs = append(gotIDs, product.ID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("product order = %#v, want %#v", gotIDs, wantIDs)
	}
	var prometheus ProductInventory
	for _, product := range products {
		if product.ID == "prometheus" {
			prometheus = product
		}
	}
	if !prometheus.Installed || prometheus.PackageVersion != "2.45.0" ||
		prometheus.ProcessCount != 1 || prometheus.UnitState != "active/running" ||
		!reflect.DeepEqual(prometheus.ListeningPorts, []int{9090}) {
		t.Fatalf("Prometheus inventory = %#v", prometheus)
	}
	if len(calls) != 8 {
		t.Fatalf("probe calls = %#v, want seven systemctl and one ss call", calls)
	}
	for index, spec := range calls {
		if len(spec.Environment) != 0 || len(spec.Stdin) != 0 ||
			spec.StdoutLimit <= 0 || spec.StderrLimit <= 0 {
			t.Fatalf("unsafe probe %d: %#v", index, spec)
		}
	}
	wantListenerProbe := execx.Spec{
		Program: "ss", Arguments: []string{"-H", "-lntu"},
		StdoutLimit: listenerOutputLimit, StderrLimit: commandErrorLimit,
	}
	if !reflect.DeepEqual(calls[0], wantListenerProbe) {
		t.Fatalf("listener probe = %#v, want %#v", calls[0], wantListenerProbe)
	}
	if result.Status != protocol.StatusPass {
		t.Fatalf("inventory status = %q, want pass", result.Status)
	}
	missingInfo := 0
	for _, check := range result.Checks {
		if strings.HasSuffix(check.ID, ".missing") && check.Status == protocol.StatusInfo {
			missingInfo++
		}
	}
	if missingInfo != 5 {
		t.Fatalf("missing product checks = %#v, want five informational checks", result.Checks)
	}
	text := resultText(t, result)
	for _, secret := range []string{
		"proxy-secret", "api-secret", "alice", "attacker.invalid", "HTTP_PROXY", "PROMETHEUS_URL",
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("inventory leaks %q: %s", secret, text)
		}
	}
}

func TestInventorySupportsClassicZabbixAgentUnit(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "var/lib/dpkg/status", `Package: zabbix-agent
Status: install ok installed
Version: 1:6.0.14+dfsg-1
`)
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program == "ss" {
				return execx.Output{}, nil
			}
			if spec.Arguments[1] == "zabbix-agent.service" {
				return execx.Output{
					Stdout: []byte("LoadState=loaded\nActiveState=active\nSubState=running\n"),
				}, nil
			}
			return execx.Output{Stdout: []byte("LoadState=not-found\n")}, nil
		}),
	})
	result, err := definition.Execute(
		context.Background(), invocation("monitoring", "inventory"),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, product := range result.Data["products"].([]ProductInventory) {
		if product.ID == "zabbix-agent" {
			if product.PackageVersion != "1:6.0.14+dfsg-1" ||
				product.UnitState != "active/running" {
				t.Fatalf("classic Zabbix Agent inventory = %#v", product)
			}
			return
		}
	}
	t.Fatal("Zabbix Agent is missing from inventory")
}

func TestStatusAggregatesDuplicateConfigIssuesByProductAndCode(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "etc/zabbix/zabbix_agent2.conf",
		"malformed agent2 cloud-key-secret\n")
	writeFixture(t, root, "etc/zabbix/zabbix_agentd.conf",
		"malformed classic snmp-community-secret\n")
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program == "ss" {
				return execx.Output{}, nil
			}
			return execx.Output{Stdout: []byte("LoadState=not-found\n")}, nil
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("monitoring", "status"))
	if err != nil {
		t.Fatal(err)
	}
	checkCount := 0
	for _, check := range result.Checks {
		if check.ID == "monitoring.zabbix-agent.malformed_config" {
			checkCount++
		}
	}
	errorCount := 0
	for _, issue := range result.Errors {
		if issue.Code == "malformed_config" &&
			issue.Details["product"] == "zabbix-agent" {
			errorCount++
		}
	}
	if checkCount != 1 || errorCount != 1 {
		t.Fatalf("duplicate aggregation = checks %d errors %d; result %#v",
			checkCount, errorCount, result)
	}
	text := resultText(t, result)
	for _, secret := range []string{"cloud-key-secret", "snmp-community-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("aggregated issue leaks %q: %s", secret, text)
		}
	}
}

func TestStatusIsPartialForMalformedInstalledConfigWithoutLeakingSecrets(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "var/lib/dpkg/status", `Package: prometheus
Status: install ok installed
Version: 2.45.0
`)
	writeFixture(t, root, "etc/prometheus/prometheus.yml", `
not valid yaml
bearer_token: bearer-status-secret
basic_auth: alice:basic-status-secret
remote_write: https://cloud-user:remote-status-secret@example.invalid/api
`)
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "ss":
			return execx.Output{}, nil
		case "systemctl":
			if spec.Arguments[1] == "prometheus.service" {
				return execx.Output{
					Stdout: []byte("LoadState=loaded\nActiveState=active\nSubState=running\n"),
				}, nil
			}
			return execx.Output{Stdout: []byte("LoadState=not-found\n")}, nil
		default:
			t.Fatalf("unexpected program: %q", spec.Program)
			return execx.Output{}, nil
		}
	})
	definition := NewDefinition(Options{Root: root, Runner: runner})
	result, err := definition.Execute(context.Background(), invocation("monitoring", "status"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPartial {
		t.Fatalf("status = %q, want partial: %#v", result.Status, result)
	}
	if len(result.Errors) == 0 || result.Errors[0].Code != "malformed_config" {
		t.Fatalf("errors = %#v, want malformed config", result.Errors)
	}
	configs, ok := result.Data["configs"].([]ConfigMetadata)
	if !ok || len(configs) != 1 || configs[0].ProductID != "prometheus" ||
		configs[0].Valid {
		t.Fatalf("configs = %#v, want invalid Prometheus metadata", result.Data["configs"])
	}
	text := resultText(t, result)
	for _, forbidden := range []string{
		"bearer-status-secret", "basic-status-secret", "remote-status-secret",
		"alice", "cloud-user", "example.invalid", "bearer_token", "basic_auth", "remote_write",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("status leaks %q: %s", forbidden, text)
		}
	}
}

func TestInventoryRejectsUntrustedSystemdOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output execx.Output
	}{
		{
			name: "nonzero exit",
			output: execx.Output{
				ExitCode: 1, Stderr: []byte("bearer_token=unit-secret"),
			},
		},
		{
			name: "truncated",
			output: execx.Output{
				Stdout:          []byte("LoadState=loaded\nActiveState=active\nSubState=running\n"),
				StdoutTruncated: true,
			},
		},
		{
			name: "malformed",
			output: execx.Output{
				Stdout: []byte("LoadState loaded\nActiveState=active\nSubState=running\n"),
			},
		},
		{
			name: "duplicate",
			output: execx.Output{
				Stdout: []byte(
					"LoadState=loaded\nActiveState=active\n" +
						"ActiveState=inactive\nSubState=running\n",
				),
			},
		},
		{
			name: "unexpected",
			output: execx.Output{
				Stdout: []byte(
					"LoadState=loaded\nActiveState=active\nSubState=running\n" +
						"Environment=BEARER_TOKEN=unit-secret\n",
				),
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			definition := NewDefinition(Options{
				Root: t.TempDir(),
				Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
					if spec.Program == "ss" {
						return execx.Output{}, nil
					}
					if spec.Arguments[1] == "prometheus.service" {
						return test.output, nil
					}
					return execx.Output{Stdout: []byte("LoadState=not-found\n")}, nil
				}),
			})
			result, err := definition.Execute(
				context.Background(), invocation("monitoring", "inventory"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != protocol.StatusPartial {
				t.Fatalf("status = %q, want partial: %#v", result.Status, result)
			}
			found := false
			for _, issue := range result.Errors {
				if issue.Code == "unit_inventory" &&
					issue.Details["product"] == "prometheus" {
					found = true
				}
			}
			if !found {
				t.Fatalf("errors = %#v, want Prometheus unit issue", result.Errors)
			}
			if strings.Contains(resultText(t, result), "unit-secret") {
				t.Fatalf("result leaked raw systemd output: %s", resultText(t, result))
			}
		})
	}
}

func TestInventoryRejectsSymlinkedAndOversizedProcInventories(t *testing.T) {
	t.Parallel()

	safeRunner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		if spec.Program == "ss" {
			return execx.Output{}, nil
		}
		return execx.Output{Stdout: []byte("LoadState=not-found\n")}, nil
	})

	t.Run("symlinked proc", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		outside := t.TempDir()
		writeFixture(t, outside, "99/comm", "prometheus\n")
		if err := os.Symlink(outside, filepath.Join(root, "proc")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		definition := NewDefinition(Options{Root: root, Runner: safeRunner})
		result, err := definition.Execute(
			context.Background(), invocation("monitoring", "inventory"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != protocol.StatusPartial ||
			!hasErrorCode(result, "process_inventory") {
			t.Fatalf("result = %#v, want partial process inventory", result)
		}
		if strings.Contains(resultText(t, result), "prometheus") {
			for _, product := range result.Data["products"].([]ProductInventory) {
				if product.ID == "prometheus" && product.ProcessCount != 0 {
					t.Fatalf("symlinked proc content was followed: %#v", product)
				}
			}
		}
	})

	t.Run("bounded proc", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		proc := filepath.Join(root, "proc")
		if err := os.Mkdir(proc, 0o755); err != nil {
			t.Fatal(err)
		}
		for index := 1; index <= maxProcessEntries+1; index++ {
			if err := os.Mkdir(filepath.Join(proc, strconv.Itoa(index)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		definition := NewDefinition(Options{Root: root, Runner: safeRunner})
		result, err := definition.Execute(
			context.Background(), invocation("monitoring", "inventory"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != protocol.StatusPartial ||
			!hasErrorCode(result, "process_inventory") {
			t.Fatalf("result = %#v, want bounded partial process inventory", result)
		}
	})
}

func TestInventoryRejectsUntrustedListenerOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output execx.Output
	}{
		{
			name: "nonzero exit",
			output: execx.Output{
				ExitCode: 1, Stderr: []byte("community=listener-secret"),
			},
		},
		{
			name: "truncated",
			output: execx.Output{
				Stdout:          []byte("tcp LISTEN 0 4096 127.0.0.1:9090 0.0.0.0:*\n"),
				StdoutTruncated: true,
			},
		},
		{
			name: "malformed",
			output: execx.Output{
				Stdout: []byte("tcp LISTEN malformed listener-secret\n"),
			},
		},
		{
			name: "unexpected protocol",
			output: execx.Output{
				Stdout: []byte("http LISTEN 0 4096 127.0.0.1:9090 0.0.0.0:*\n"),
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			definition := NewDefinition(Options{
				Root: t.TempDir(),
				Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
					if spec.Program == "ss" {
						return test.output, nil
					}
					return execx.Output{Stdout: []byte("LoadState=not-found\n")}, nil
				}),
			})
			result, err := definition.Execute(
				context.Background(), invocation("monitoring", "inventory"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != protocol.StatusPartial ||
				!hasErrorCode(result, "listener_inventory") {
				t.Fatalf("result = %#v, want partial listener inventory", result)
			}
			if strings.Contains(resultText(t, result), "listener-secret") {
				t.Fatalf("result leaked raw listener output: %s", resultText(t, result))
			}
		})
	}
}

func TestConfigReturnsOnlyBoundedMetadataAndOmitsCredentialClasses(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "etc/prometheus/prometheus.yml", `
global:
  scrape_interval: 15s
remote_write:
  - url: https://cloud-user:remote-secret@example.invalid/api
    bearer_token: bearer-secret
    basic_auth:
      username: alice
      password: basic-secret
`)
	writeFixture(t, root, "etc/alloy/config.alloy", `
prometheus.remote_write "cloud" {
  endpoint {
    url = "https://alloy-user:alloy-secret@example.invalid/api"
  }
}
`)
	writeFixture(t, root, "etc/grafana-agent.yaml", `
integrations:
  snmp:
    community: snmp-community-secret
`)
	writeFixture(t, root, "etc/default/prometheus-node-exporter",
		`ARGS="--web.listen-address=127.0.0.1:9100"`+"\n")
	writeFixture(t, root, "etc/telegraf/telegraf.conf", `
[[outputs.cloud]]
  token = "cloud-key-secret"
`)
	writeFixture(t, root, "etc/zabbix/zabbix_agent2.conf", `
Server=127.0.0.1
TLSPSKIdentity=cloud-identity-secret
TLSPSKFile=/etc/zabbix/cloud-key-secret
`)

	called := false
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			called = true
			return execx.Output{}, nil
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("monitoring", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("config metadata inspection invoked an external command")
	}
	if result.Status != protocol.StatusPass {
		t.Fatalf("config status = %q, want pass: %#v", result.Status, result)
	}
	configs, ok := result.Data["configs"].([]ConfigMetadata)
	if !ok || len(configs) != 6 {
		t.Fatalf("configs = %#v, want six metadata records", result.Data["configs"])
	}
	for _, config := range configs {
		if !config.Valid || config.SizeBytes <= 0 || !strings.HasPrefix(config.Path, "/etc/") {
			t.Fatalf("invalid config metadata: %#v", config)
		}
	}
	text := resultText(t, result)
	for _, forbidden := range []string{
		"remote-secret", "bearer-secret", "basic-secret", "alloy-secret",
		"snmp-community-secret", "cloud-key-secret", "cloud-identity-secret",
		"alice", "cloud-user", "alloy-user", "example.invalid", "community",
		"remote_write", "bearer_token", "basic_auth", "TLSPSK",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("config leaks %q: %s", forbidden, text)
		}
	}
}

func TestConfigRejectsSymlinkAndOversizeWithoutReadingTarget(t *testing.T) {
	t.Parallel()

	t.Run("symlink", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.yml")
		if err := os.WriteFile(outside, []byte("bearer_token: outside-secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "etc", "prometheus")
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(path, "prometheus.yml")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		definition := NewDefinition(Options{Root: root})
		result, err := definition.Execute(context.Background(), invocation("monitoring", "config"))
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != protocol.StatusPartial || !hasErrorCode(result, "unsafe_config") {
			t.Fatalf("result = %#v, want unsafe config partial", result)
		}
		if strings.Contains(resultText(t, result), "outside-secret") {
			t.Fatalf("symlink target leaked: %s", resultText(t, result))
		}
	})

	t.Run("oversize", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeFixture(t, root, "etc/telegraf/telegraf.conf",
			strings.Repeat("x", configFileLimit+1))
		definition := NewDefinition(Options{Root: root})
		result, err := definition.Execute(context.Background(), invocation("monitoring", "config"))
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != protocol.StatusPartial || !hasErrorCode(result, "unsafe_config") {
			t.Fatalf("result = %#v, want bounded config partial", result)
		}
	})
}

func TestCommandsRejectCallerInputAndPropagateCancellation(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{Root: t.TempDir()})
	for _, path := range [][]string{
		{"monitoring", "status"},
		{"monitoring", "inventory"},
		{"monitoring", "config"},
	} {
		badArguments := invocation(path...)
		badArguments.Arguments = []string{"https://alice:secret@attacker.invalid"}
		_, err := definition.Execute(context.Background(), badArguments)
		assertExitCode(t, err, protocol.ExitArguments)

		badOptions := invocation(path...)
		badOptions.Options = map[string]any{"target": "attacker.invalid"}
		_, err = definition.Execute(context.Background(), badOptions)
		assertExitCode(t, err, protocol.ExitArguments)
	}

	called := false
	cancelledDefinition := NewDefinition(Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			called = true
			return execx.Output{}, nil
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cancelledDefinition.Execute(ctx, invocation("monitoring", "status"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execute error = %v, want context cancellation", err)
	}
	if called {
		t.Fatal("runner called after cancellation")
	}
}

func TestInventoryIsDeterministic(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "var/lib/dpkg/status", `Package: telegraf
Status: install ok installed
Version: 1.30.0

Package: prometheus
Status: install ok installed
Version: 2.45.0
`)
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		if spec.Program == "ss" {
			return execx.Output{
				Stdout: []byte(
					"tcp LISTEN 0 4096 127.0.0.1:9090 0.0.0.0:*\n" +
						"tcp LISTEN 0 4096 127.0.0.1:9273 0.0.0.0:*\n",
				),
			}, nil
		}
		if spec.Arguments[1] == "prometheus.service" ||
			spec.Arguments[1] == "telegraf.service" {
			return execx.Output{
				Stdout: []byte("LoadState=loaded\nActiveState=active\nSubState=running\n"),
			}, nil
		}
		return execx.Output{Stdout: []byte("LoadState=not-found\n")}, nil
	})
	now := time.Date(2026, 7, 27, 18, 0, 0, 0, time.UTC)
	definition := NewDefinition(Options{
		Root: root, Runner: runner, Host: "monitoring-host", Now: func() time.Time { return now },
	})
	first, err := definition.Execute(context.Background(), invocation("monitoring", "inventory"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := definition.Execute(context.Background(), invocation("monitoring", "inventory"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("inventory is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
}

func hasErrorCode(result protocol.Result, code string) bool {
	for _, issue := range result.Errors {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func invocation(path ...string) protocol.Invocation {
	return protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "monitoring-test",
		CommandPath:     path,
		Arguments:       []string{},
		Options:         map[string]any{},
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

func assertExitCode(t *testing.T, err error, code int) {
	t.Helper()
	var failure protocol.ExitError
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("execute error = %v, want exit %d", err, code)
	}
}
