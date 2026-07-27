package kafka

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
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestDefinitionDeclaresOnlyApprovedReadOnlyCommands(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{Version: "1.0.0", Root: t.TempDir()})
	got := make([][]string, 0, len(definition.Manifest.Commands))
	for _, command := range definition.Manifest.Commands {
		got = append(got, command.Path)
		if command.Category != protocol.CategoryDiagnostic ||
			command.RequiresRoot || command.RequiresForce ||
			command.RequiresConfirmation || command.SupportsDryRun {
			t.Fatalf("command has mutation requirements: %#v", command)
		}
	}
	want := [][]string{
		{"kafka", "status"},
		{"kafka", "config"},
		{"kafka", "storage"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command paths = %#v, want %#v", got, want)
	}
	if err := protocol.ValidateManifest(definition.Manifest); err != nil {
		t.Fatalf("manifest is invalid: %v", err)
	}

	plan, err := definition.Plan(context.Background(), invocation("kafka", "status"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 || plan.RequiresRoot || plan.RequiresConfirmation {
		t.Fatalf("read-only plan can mutate: %#v", plan)
	}
}

func TestDefinitionRejectsUnknownArgumentsAndOptions(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{Root: t.TempDir()})
	tests := []protocol.Invocation{
		func() protocol.Invocation {
			value := invocation("kafka", "status")
			value.Arguments = []string{"--bootstrap-server=attacker.invalid:9092"}
			return value
		}(),
		func() protocol.Invocation {
			value := invocation("kafka", "config")
			value.Options["bootstrap-server"] = "attacker.invalid:9092"
			return value
		}(),
		func() protocol.Invocation {
			value := invocation("kafka", "storage")
			value.Options["command"] = "sh -c id"
			return value
		}(),
	}
	for _, request := range tests {
		_, err := definition.Execute(context.Background(), request)
		var failure protocol.ExitError
		if !errors.As(err, &failure) || failure.Code != protocol.ExitArguments {
			t.Fatalf("execute(%#v) error = %v, want arguments exit 2", request, err)
		}
	}
}

func TestStatusUsesOnlyLocalPackageProcessAndUnitMetadata(t *testing.T) {
	t.Setenv("KAFKA_OPTS", "-Djava.security.auth.login.config=/tmp/attacker-jaas.conf")
	t.Setenv("KAFKA_BOOTSTRAP_SERVERS", "attacker.invalid:9092")
	t.Setenv("SASL_JAAS_CONFIG", "password=caller-secret")

	root := t.TempDir()
	writeFixture(t, root, "var/lib/dpkg/status", `Package: kafka
Status: install ok installed
Version: 3.8.1-1

Package: unrelated
Status: install ok installed
Version: 1
`)
	writeFixture(t, root, "proc/4242/comm", "java\n")
	writeFixture(t, root, "proc/4242/cmdline",
		"java\x00-Xmx1g\x00kafka.Kafka\x00/etc/kafka/server.properties\x00")

	var calls []execx.Spec
	definition := NewDefinition(Options{
		Version: "1.0.0", Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			return execx.Output{
				Stdout: []byte("LoadState=loaded\nActiveState=active\nSubState=running\n"),
			}, nil
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("kafka", "status"))
	if err != nil {
		t.Fatal(err)
	}

	wantCalls := []execx.Spec{{
		Program: "systemctl",
		Arguments: []string{
			"show", "--no-pager", "--property=LoadState,ActiveState,SubState",
			"--", "kafka.service",
		},
		StdoutLimit: 32 << 10, StderrLimit: 16 << 10,
	}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("probe calls = %#v, want fixed local systemctl call %#v", calls, wantCalls)
	}
	if result.Data["installed"] != true ||
		result.Data["package_count"] != 1 ||
		result.Data["process_count"] != 1 ||
		result.Data["systemd_active_state"] != "active" {
		t.Fatalf("status data = %#v", result.Data)
	}
	text := resultText(t, result)
	for _, forbidden := range []string{
		"caller-secret", "attacker.invalid", "attacker-jaas.conf",
		"bootstrap", "SASL_JAAS_CONFIG", "/etc/kafka/server.properties",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("status leaks forbidden value %q: %s", forbidden, text)
		}
	}
}

func TestStatusRejectsUnsafeProcessEntriesAndBoundsInventory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "proc/100/comm", "java\n")
	writeFixture(t, root, "proc/100/cmdline", "java\x00kafka.Kafka\x00")
	if err := os.MkdirAll(filepath.Join(root, "proc", "101"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("java\x00kafka.Kafka\x00token=outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "proc", "101", "cmdline")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	writeFixture(t, root, "proc/101/comm", "java\n")

	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{}, execx.ErrNotFound
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("kafka", "status"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Data["process_count"] != 1 {
		t.Fatalf("process count = %#v, want one confined process", result.Data["process_count"])
	}
	if strings.Contains(resultText(t, result), "outside-secret") {
		t.Fatal("status leaked a symlink target")
	}
}

func TestStatusPropagatesCancellationBeforeProbes(t *testing.T) {
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
	_, err := definition.Execute(ctx, invocation("kafka", "status"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execute error = %v, want context cancellation", err)
	}
	if called {
		t.Fatal("runner called after cancellation")
	}
}

func TestStatusInstalledUsesAnyLocalInstallationSignal(t *testing.T) {
	t.Parallel()

	t.Run("process only", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "proc/42/comm", "java\n")
		writeFixture(t, root, "proc/42/cmdline", "java\x00kafka.Kafka\x00")
		definition := NewDefinition(Options{
			Root: root,
			Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
				return execx.Output{}, execx.ErrNotFound
			}),
		})
		result, err := definition.Execute(context.Background(), invocation("kafka", "status"))
		if err != nil {
			t.Fatal(err)
		}
		if result.Data["installed"] != true {
			t.Fatalf("process-only installed = %#v, want true", result.Data["installed"])
		}
	})

	t.Run("loaded unit only", func(t *testing.T) {
		definition := NewDefinition(Options{
			Root: t.TempDir(),
			Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
				return execx.Output{
					Stdout: []byte("LoadState=loaded\nActiveState=inactive\nSubState=dead\n"),
				}, nil
			}),
		})
		result, err := definition.Execute(context.Background(), invocation("kafka", "status"))
		if err != nil {
			t.Fatal(err)
		}
		if result.Data["installed"] != true {
			t.Fatalf("unit-only installed = %#v, want true", result.Data["installed"])
		}
	})
}

func TestStatusRejectsMalformedPackageMetadataWithoutLeakingIt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "var", "lib", "dpkg", "status")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{
		'P', 'a', 'c', 'k', 'a', 'g', 'e', ':', ' ', 'k', 'a', 'f', 'k', 'a', '\n',
		'V', 'e', 'r', 's', 'i', 'o', 'n', ':', ' ', 0xff, '\n',
	}, 0o600); err != nil {
		t.Fatal(err)
	}
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{}, execx.ErrNotFound
		}),
	})
	_, err := definition.Execute(context.Background(), invocation("kafka", "status"))
	assertExitCode(t, err, protocol.ExitDependency)
	if err != nil && strings.Contains(err.Error(), "\ufffd") {
		t.Fatalf("status error leaks malformed package metadata: %v", err)
	}
}

func TestStatusOmitsSensitivePackageVersionValues(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "var/lib/dpkg/status", `Package: kafka
Status: install ok installed
Version: 3.8.1-token-secret
`)
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{}, execx.ErrNotFound
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("kafka", "status"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Data["package_count"] != 0 {
		t.Fatalf("sensitive package version was accepted: %#v", result.Data["packages"])
	}
	if strings.Contains(resultText(t, result), "token-secret") {
		t.Fatalf("sensitive package version leaked: %s", resultText(t, result))
	}
}

func TestStatusBoundsProcessDirectoryTraversal(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	proc := filepath.Join(root, "proc")
	if err := os.MkdirAll(proc, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= maxProcesses+1; index++ {
		if err := os.Mkdir(filepath.Join(proc, strconv.Itoa(index)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	local := probe.Local{Root: root}
	if _, err := collectProcesses(context.Background(), local, root); err == nil {
		t.Fatal("oversized process inventory was accepted")
	}
}

func TestConfigReturnsOnlyAllowlistedPropertiesFromFixedLocalFiles(t *testing.T) {
	t.Setenv("KAFKA_OPTS", "sasl.jaas.config=password=caller-secret")

	root := t.TempDir()
	writeFixture(t, root, "etc/kafka/server.properties", `
node.id=2
broker.id=2
process.roles=broker,controller
num.partitions=12
default.replication.factor=3
min.insync.replicas=2
auto.create.topics.enable=false
log.dirs=/var/lib/kafka,/srv/kafka
listeners=SASL_SSL://attacker.invalid:9092
advertised.listeners=SASL_SSL://user:uri-secret@attacker.invalid:9092
sasl.jaas.config=org.example.Login required password="jaas-secret";
sasl.password=sasl-secret
ssl.keystore.password=keystore-secret
ssl.truststore.password=truststore-secret
token.auth.secret=token-secret
`)

	called := false
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			called = true
			return execx.Output{}, nil
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("kafka", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("config inspection invoked an external program")
	}
	files, ok := result.Data["files"].([]ConfigFile)
	if !ok || len(files) != 1 {
		t.Fatalf("config files = %#v", result.Data["files"])
	}
	want := map[string]string{
		"auto.create.topics.enable":  "false",
		"broker.id":                  "2",
		"default.replication.factor": "3",
		"min.insync.replicas":        "2",
		"node.id":                    "2",
		"num.partitions":             "12",
		"process.roles":              "broker,controller",
	}
	if files[0].Path != "/etc/kafka/server.properties" ||
		!reflect.DeepEqual(files[0].Settings, want) {
		t.Fatalf("safe config = %#v, want path and settings %#v", files[0], want)
	}
	text := resultText(t, result)
	for _, forbidden := range []string{
		"caller-secret", "uri-secret", "attacker.invalid", "user",
		"jaas-secret", "sasl-secret", "keystore-secret", "truststore-secret",
		"token-secret", "listeners", "advertised", "sasl", "keystore",
		"truststore", "token.auth", "/var/lib/kafka", "/srv/kafka",
	} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Fatalf("config leaks forbidden value %q: %s", forbidden, text)
		}
	}
}

func TestConfigRejectsUnsafeOversizedAndMalformedProperties(t *testing.T) {
	t.Parallel()

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(root, "outside.properties")
		if err := os.WriteFile(outside, []byte("sasl.password=outside-secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		configDir := filepath.Join(root, "etc", "kafka")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(configDir, "server.properties")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		definition := NewDefinition(Options{Root: root})
		_, err := definition.Execute(context.Background(), invocation("kafka", "config"))
		assertExitCode(t, err, protocol.ExitDependency)
		if strings.Contains(err.Error(), "outside-secret") {
			t.Fatalf("error leaks symlink target content: %v", err)
		}
	})

	t.Run("oversize", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "etc/kafka/server.properties",
			strings.Repeat("x", int(configFileLimit)+1))
		definition := NewDefinition(Options{Root: root})
		_, err := definition.Execute(context.Background(), invocation("kafka", "config"))
		assertExitCode(t, err, protocol.ExitDependency)
	})

	t.Run("malformed", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "etc/kafka/server.properties",
			"node.id=1\nsasl.jaas.config=super-secret\nmalformed-secret-line\n")
		definition := NewDefinition(Options{Root: root})
		_, err := definition.Execute(context.Background(), invocation("kafka", "config"))
		assertExitCode(t, err, protocol.ExitDependency)
		if strings.Contains(err.Error(), "super-secret") ||
			strings.Contains(err.Error(), "malformed-secret-line") {
			t.Fatalf("error leaks malformed config: %v", err)
		}
	})
}

func TestConfigIsDeterministicAndPropagatesCancellation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "opt/kafka/config/server.properties",
		"process.roles=controller,broker\nnode.id=7\n")
	writeFixture(t, root, "etc/kafka/server.properties",
		"node.id=2\nprocess.roles=broker\n")
	fixed := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	definition := NewDefinition(Options{Root: root, Now: func() time.Time { return fixed }})
	first, err := definition.Execute(context.Background(), invocation("kafka", "config"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := definition.Execute(context.Background(), invocation("kafka", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if resultText(t, first) != resultText(t, second) {
		t.Fatalf("config result is not deterministic:\n%s\n%s", resultText(t, first), resultText(t, second))
	}
	files := first.Data["files"].([]ConfigFile)
	if len(files) != 2 ||
		files[0].Path != "/etc/kafka/server.properties" ||
		files[1].Path != "/opt/kafka/config/server.properties" ||
		files[1].Settings["process.roles"] != "broker,controller" {
		t.Fatalf("config ordering = %#v", files)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = definition.Execute(ctx, invocation("kafka", "config"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execute error = %v, want context cancellation", err)
	}
}

func TestStorageUsesOnlyFixedLocalInfoCommandAndBoundedLogDirectories(t *testing.T) {
	t.Setenv("KAFKA_OPTS", "-Djava.security.auth.login.config=/tmp/caller-secret.conf")
	t.Setenv("KAFKA_BOOTSTRAP_SERVERS", "attacker.invalid:9092")
	t.Setenv("SASL_JAAS_CONFIG", "password=caller-jaas-secret")

	root := t.TempDir()
	writeFixture(t, root, "etc/kafka/server.properties", `
node.id=2
log.dirs=/var/lib/kafka-a,/srv/kafka-b
sasl.jaas.config=password=properties-jaas-secret
ssl.keystore.password=keystore-secret
`)
	mkdirFixture(t, root, "var/lib/kafka-a")
	mkdirFixture(t, root, "srv/kafka-b")
	writeFixture(t, root, "var/lib/kafka-a/000000.log", "segment")

	var calls []execx.Spec
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			return execx.Output{Stdout: []byte(
				"Found log directory:\n" +
					"  /var/lib/kafka-a\n" +
					"Found metadata: {cluster.id=safe-id, node.id=2, version=1}\n",
			)}, nil
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("kafka", "storage"))
	if err != nil {
		t.Fatal(err)
	}
	want := []execx.Spec{{
		Program: "kafka-storage.sh",
		Arguments: []string{
			"info", "--config", "/etc/kafka/server.properties",
		},
		StdoutLimit: storageOutputLimit, StderrLimit: 32 << 10,
	}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("storage calls = %#v, want fixed local call %#v", calls, want)
	}
	directories, ok := result.Data["log_directories"].([]LogDirectory)
	if !ok || len(directories) != 2 ||
		directories[0].Path != "/srv/kafka-b" ||
		directories[1].Path != "/var/lib/kafka-a" ||
		directories[1].EntryCount != 1 {
		t.Fatalf("log directories = %#v", result.Data["log_directories"])
	}
	if result.Data["storage_tool_available"] != true ||
		result.Data["metadata_records"] != 1 {
		t.Fatalf("storage metadata = %#v", result.Data)
	}
	text := resultText(t, result)
	for _, forbidden := range []string{
		"caller-secret", "caller-jaas-secret", "attacker.invalid",
		"properties-jaas-secret", "keystore-secret", "SASL_JAAS_CONFIG",
		"bootstrap-server", "safe-id",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("storage leaks forbidden value %q: %s", forbidden, text)
		}
	}
}

func TestStorageFallsBackOnlyBetweenFixedLocalToolNames(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "etc/kafka/server.properties", "log.dirs=/var/lib/kafka\n")
	mkdirFixture(t, root, "var/lib/kafka")
	var calls []execx.Spec
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			if spec.Program == "kafka-storage.sh" {
				return execx.Output{}, execx.ErrNotFound
			}
			return execx.Output{Stdout: []byte("Found log directory:\n  /var/lib/kafka\n")}, nil
		}),
	})
	_, err := definition.Execute(context.Background(), invocation("kafka", "storage"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Program != "kafka-storage.sh" ||
		calls[1].Program != "kafka-storage" ||
		!reflect.DeepEqual(calls[0].Arguments, calls[1].Arguments) {
		t.Fatalf("storage fallback calls = %#v", calls)
	}
	for _, call := range calls {
		joined := strings.Join(call.Arguments, " ")
		for _, forbidden := range []string{
			"bootstrap", "--command-config", "attacker", "socket", "broker",
		} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("storage command contains forbidden %q: %#v", forbidden, call)
			}
		}
	}
}

func TestStorageRejectsUnsafeAndUnboundedLogDirectories(t *testing.T) {
	t.Parallel()

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "etc/kafka/server.properties", "log.dirs=/var/lib/kafka\n")
		mkdirFixture(t, root, "outside")
		if err := os.MkdirAll(filepath.Join(root, "var", "lib"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "outside"),
			filepath.Join(root, "var", "lib", "kafka")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		definition := NewDefinition(Options{Root: root})
		_, err := definition.Execute(context.Background(), invocation("kafka", "storage"))
		assertExitCode(t, err, protocol.ExitDependency)
	})

	t.Run("traversal", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "etc/kafka/server.properties",
			"log.dirs=/var/lib/kafka/../../../outside-secret\n")
		definition := NewDefinition(Options{Root: root})
		_, err := definition.Execute(context.Background(), invocation("kafka", "storage"))
		assertExitCode(t, err, protocol.ExitDependency)
		if strings.Contains(err.Error(), "outside-secret") {
			t.Fatalf("error leaks unsafe log path: %v", err)
		}
	})

	t.Run("too many", func(t *testing.T) {
		root := t.TempDir()
		values := make([]string, maxLogDirectories+1)
		for index := range values {
			values[index] = "/var/lib/kafka-" + string(rune('a'+index))
		}
		writeFixture(t, root, "etc/kafka/server.properties",
			"log.dirs="+strings.Join(values, ",")+"\n")
		definition := NewDefinition(Options{Root: root})
		_, err := definition.Execute(context.Background(), invocation("kafka", "storage"))
		assertExitCode(t, err, protocol.ExitDependency)
	})

	t.Run("too many directory entries", func(t *testing.T) {
		root := t.TempDir()
		directory := filepath.Join(root, "var", "lib", "kafka")
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		for index := 0; index <= maxLogDirEntries; index++ {
			name := filepath.Join(directory, "entry-"+strconv.Itoa(index))
			if err := os.WriteFile(name, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := inspectDirectory(context.Background(), root, "/var/lib/kafka"); err == nil {
			t.Fatal("oversized log directory inventory was accepted")
		}
	})

	t.Run("conflicting properties", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "etc/kafka/server.properties",
			"log.dir=/var/lib/kafka-a\nlog.dirs=/var/lib/kafka-b\n")
		mkdirFixture(t, root, "var/lib/kafka-a")
		mkdirFixture(t, root, "var/lib/kafka-b")
		definition := NewDefinition(Options{
			Root: root,
			Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
				return execx.Output{Stdout: []byte(
					"Found log directory:\n  /var/lib/kafka-b\n",
				)}, nil
			}),
		})
		_, err := definition.Execute(context.Background(), invocation("kafka", "storage"))
		assertExitCode(t, err, protocol.ExitDependency)
	})

	t.Run("sensitive path", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "etc/kafka/server.properties",
			"log.dirs=/var/lib/token-secret\n")
		mkdirFixture(t, root, "var/lib/token-secret")
		definition := NewDefinition(Options{
			Root: root,
			Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
				return execx.Output{Stdout: []byte(
					"Found log directory:\n  /var/lib/token-secret\n",
				)}, nil
			}),
		})
		_, err := definition.Execute(context.Background(), invocation("kafka", "storage"))
		assertExitCode(t, err, protocol.ExitDependency)
		if strings.Contains(err.Error(), "token-secret") {
			t.Fatalf("error leaks sensitive log path: %v", err)
		}
	})
}

func TestInspectDirectoryRequestsOnlyLimitPlusOneEntries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mkdirFixture(t, root, "var/lib/kafka")
	reader := &recordingDirectoryReader{
		entries: make([]os.DirEntry, maxLogDirEntries+1),
	}
	_, err := inspectDirectoryWithOpen(
		context.Background(),
		root,
		"/var/lib/kafka",
		func(target string) (directoryReader, error) {
			info, err := os.Stat(target)
			reader.info = info
			return reader, err
		},
	)
	if err == nil {
		t.Fatal("oversized directory inventory was accepted")
	}
	if reader.requested != maxLogDirEntries+1 {
		t.Fatalf("ReadDir requested %d entries, want %d", reader.requested, maxLogDirEntries+1)
	}
	if !reader.closed {
		t.Fatal("directory reader was not closed")
	}
}

type recordingDirectoryReader struct {
	entries   []os.DirEntry
	info      os.FileInfo
	requested int
	closed    bool
}

func (reader *recordingDirectoryReader) ReadDir(count int) ([]os.DirEntry, error) {
	reader.requested = count
	return reader.entries, nil
}

func (reader *recordingDirectoryReader) Stat() (os.FileInfo, error) {
	return reader.info, nil
}

func (reader *recordingDirectoryReader) Close() error {
	reader.closed = true
	return nil
}

func TestStorageSanitizesMalformedOutputAndPropagatesCancellation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "etc/kafka/server.properties", "log.dirs=/var/lib/kafka\n")
	mkdirFixture(t, root, "var/lib/kafka")
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{
				Stdout: []byte("malformed password=storage-output-secret\n"),
			}, nil
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("kafka", "storage"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Data["storage_tool_available"] != false {
		t.Fatalf("malformed tool availability = %#v", result.Data)
	}
	if strings.Contains(resultText(t, result), "storage-output-secret") {
		t.Fatalf("malformed output leaked: %s", resultText(t, result))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = definition.Execute(ctx, invocation("kafka", "storage"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execute error = %v, want context cancellation", err)
	}
}

func invocation(path ...string) protocol.Invocation {
	return protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "kafka-test",
		CommandPath:     path,
		Arguments:       []string{},
		Options:         map[string]any{},
	}
}

func mkdirFixture(t *testing.T, root, relative string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(relative)), 0o755); err != nil {
		t.Fatal(err)
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
