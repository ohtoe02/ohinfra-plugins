package serversetup

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
)

func TestSystemBackendStagesActivatesAndVerifiesOwnedFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	var calls []execx.Spec
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			return execx.Output{}, nil
		}),
	}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemSysctl},
	}
	observation, err := backend.Observe(context.Background(), ItemSysctl, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("missing managed sysctl file was reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemSysctl, profile); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), ItemSysctl, profile); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "kernel.kptr_restrict = 2") {
		t.Fatalf("managed content = %q", content)
	}
	if len(calls) != 1 || calls[0].Program != "sysctl" ||
		!reflect.DeepEqual(calls[0].Arguments, []string{"--system"}) {
		t.Fatalf("runner calls = %#v", calls)
	}
}

func TestSystemBackendRestoresOwnedFileWhenActivationFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# previous\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{ExitCode: 1, Stderr: []byte("rejected")}, nil
		}),
	}
	err := backend.Apply(context.Background(), ItemSysctl, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemSysctl},
	})
	if err == nil {
		t.Fatal("activation failure was ignored")
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "# previous\n" {
		t.Fatalf("rollback content = %q", content)
	}
}

func TestSystemBackendRollsBackWhenPostVerificationFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := []byte("# previous\n")
	if err := os.WriteFile(target, previous, 0o644); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program == "sysctl" {
				if err := os.WriteFile(target, []byte("# rejected effective state\n"), 0o644); err != nil {
					return execx.Output{}, err
				}
			}
			return execx.Output{}, nil
		}),
	}
	err := backend.Apply(context.Background(), ItemSysctl, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemSysctl},
	})
	if err == nil {
		t.Fatal("post-verification drift was ignored")
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(content, previous) {
		t.Fatalf("post-verification rollback content = %q", content)
	}
}

func TestSystemBackendFailsClosedOnStaleManagedTransactionArtifact(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{Root: root, Runner: successfulRunner()}
	managed, err := backend.desiredFile(ItemSysctl, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, managed.Content, managed.Mode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".rollback", []byte("# previous\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = backend.Observe(context.Background(), ItemSysctl, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
	})
	if err == nil {
		t.Fatal("stale managed transaction artifact was ignored")
	}
}

func TestSystemBackendRefusesSymlinkedManagedFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	backend := SystemBackend{Root: root, Runner: successfulRunner()}
	_, err := backend.Observe(context.Background(), ItemSysctl, Profile{
		Platform: Platform{ID: "debian", Version: "12"}, Config: DefaultConfig(),
	})
	if err == nil {
		t.Fatal("symlinked managed file was accepted")
	}
}

func TestSystemBackendUsesDirectPackageArgv(t *testing.T) {
	t.Parallel()

	var calls []execx.Spec
	backend := SystemBackend{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Packages = []string{"curl"}
	profile := Profile{
		Platform: Platform{ID: "ubuntu", Version: "22.04"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemSSH},
	}
	if err := backend.Apply(context.Background(), ItemPackages, profile); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].Program != "apt-get" || !reflect.DeepEqual(calls[0].Arguments, []string{"update"}) {
		t.Fatalf("apt update = %#v", calls[0])
	}
	install := calls[1]
	if install.Program != "apt-get" || !reflect.DeepEqual(install.Arguments,
		[]string{"install", "-y", "--no-install-recommends", "--", "curl", "openssh-server", "sudo"}) {
		t.Fatalf("apt install = %#v", install)
	}
	if len(install.Environment) == 0 || install.Environment["DEBIAN_FRONTEND"] != "noninteractive" {
		t.Fatalf("apt environment = %#v", install.Environment)
	}
}

func TestSystemBackendVerifiesPinnedZabbixRepositoryPackage(t *testing.T) {
	t.Parallel()

	content := []byte("repo-pkg")
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	var calls []execx.Spec
	backend := SystemBackend{
		Root: t.TempDir(),
		HTTPClient: httpDoerFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Scheme != "https" || request.URL.Host != "repo.zabbix.com" {
				t.Fatalf("request URL = %s", request.URL)
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: int64(len(content)),
				Body:          io.NopCloser(strings.NewReader(string(content))),
				Header:        make(http.Header),
			}, nil
		}),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Zabbix = &ZabbixConfig{
		Enabled: true, Server: "192.0.2.10", Hostname: "web-01",
		RepositoryPackageURL:    "https://repo.zabbix.com/release.deb",
		RepositoryPackageSize:   int64(len(content)),
		RepositoryPackageSHA256: digest,
	}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemZabbix},
	}
	if err := backend.Apply(context.Background(), ItemPackages, profile); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || calls[0].Program != "dpkg-query" ||
		calls[1].Program != "dpkg" ||
		!reflect.DeepEqual(calls[1].Arguments[:2], []string{"--install", "--"}) ||
		calls[2].Program != "apt-get" || calls[3].Program != "apt-get" {
		t.Fatalf("calls = %#v", calls)
	}

	config.Zabbix.RepositoryPackageSHA256 = strings.Repeat("0", 64)
	calls = nil
	if err := backend.Apply(context.Background(), ItemPackages, Profile{
		Platform: profile.Platform, Config: config, Items: profile.Items,
	}); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
	if len(calls) != 1 || calls[0].Program != "dpkg-query" {
		t.Fatalf("commands ran after digest mismatch: %#v", calls)
	}
}

func TestSystemBackendDoesNotReinstallPinnedZabbixRepositoryPackage(t *testing.T) {
	t.Parallel()

	downloads := 0
	var calls []execx.Spec
	backend := SystemBackend{
		Root: t.TempDir(),
		HTTPClient: httpDoerFunc(func(*http.Request) (*http.Response, error) {
			downloads++
			return nil, errors.New("unexpected download")
		}),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			if spec.Program == "dpkg-query" {
				return execx.Output{
					Stdout: []byte("zabbix-release\tinstall ok installed\n"),
				}, nil
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Zabbix = &ZabbixConfig{
		Enabled: true, Server: "192.0.2.10", Hostname: "web-01",
		RepositoryPackageURL:    "https://repo.zabbix.com/release.deb",
		RepositoryPackageSize:   8,
		RepositoryPackageSHA256: strings.Repeat("0", 64),
	}
	if err := backend.Apply(context.Background(), ItemPackages, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemZabbix},
	}); err != nil {
		t.Fatal(err)
	}
	if downloads != 0 || len(calls) != 3 ||
		calls[0].Program != "dpkg-query" ||
		calls[1].Program != "apt-get" ||
		calls[2].Program != "apt-get" {
		t.Fatalf("downloads=%d calls=%#v", downloads, calls)
	}
}

func TestSystemBackendTreatsMissingPackagesAsDrift(t *testing.T) {
	t.Parallel()

	backend := SystemBackend{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program != "dpkg-query" {
				return execx.Output{}, errors.New("unexpected program")
			}
			return execx.Output{
				ExitCode: 1,
				Stdout:   []byte("curl\tinstall ok installed\n"),
			}, nil
		}),
	}
	config := DefaultConfig()
	config.Packages = []string{"curl", "vim"}
	observation, err := backend.Observe(context.Background(), ItemPackages, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged ||
		!reflect.DeepEqual(observation.Details["missing"], []string{"vim"}) {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestSystemBackendCreatesMissingAdministratorWithDirectArgv(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	passwd := filepath.Join(root, "etc", "passwd")
	if err := os.MkdirAll(filepath.Dir(passwd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwd, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []execx.Spec
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			if spec.Program == "id" {
				return execx.Output{ExitCode: 1}, nil
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{Name: "operator", Groups: []string{"sudo", "adm"}}}
	if err := backend.Apply(context.Background(), ItemUsers, Profile{
		Platform: Platform{ID: "ubuntu", Version: "22.04"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers},
	}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Program != "id" || calls[1].Program != "useradd" {
		t.Fatalf("calls = %#v", calls)
	}
	if !reflect.DeepEqual(calls[1].Arguments,
		[]string{"--create-home", "--shell", "/bin/bash", "--groups", "adm,sudo", "--", "operator"}) {
		t.Fatalf("useradd argv = %#v", calls[1].Arguments)
	}
}

func TestSystemBackendRepairsOnlyMissingAdministratorGroups(t *testing.T) {
	t.Parallel()

	groups := []string{"operator", "sudo"}
	var calls []execx.Spec
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "home", "operator"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			switch {
			case spec.Program == "id" && containsArgument(spec.Arguments, "-u"):
				return execx.Output{Stdout: []byte("1000\n")}, nil
			case spec.Program == "id" && containsArgument(spec.Arguments, "-nG"):
				return execx.Output{Stdout: []byte(strings.Join(groups, " ") + "\n")}, nil
			case spec.Program == "getent":
				return execx.Output{Stdout: []byte(
					"operator:x:1000:1000:Operator:/home/operator:/bin/bash\n",
				)}, nil
			case spec.Program == "usermod":
				groups = append(groups, "adm")
				return execx.Output{}, nil
			default:
				return execx.Output{}, errors.New("unexpected command")
			}
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{
		Name: "operator", Groups: []string{"sudo", "adm"},
	}}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers},
	}
	observation, err := backend.Observe(context.Background(), ItemUsers, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("missing administrator group was reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemUsers, profile); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), ItemUsers, profile); err != nil {
		t.Fatal(err)
	}
	var usermodCalls []execx.Spec
	for _, call := range calls {
		if call.Program == "usermod" {
			usermodCalls = append(usermodCalls, call)
		}
	}
	if len(usermodCalls) != 1 || !reflect.DeepEqual(
		usermodCalls[0].Arguments,
		[]string{"--append", "--groups", "adm", "--", "operator"},
	) {
		t.Fatalf("usermod calls = %#v", usermodCalls)
	}
}

func TestSystemBackendObservesAndInstallsAuthorizedKeysUnderInjectedRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "etc", "ohtools", "plugins", "keys", "operator.pub")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	key := validEd25519PublicKey("operator@example")
	if err := os.WriteFile(source, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "home", "operator"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program == "getent" {
				return execx.Output{Stdout: []byte(
					"operator:x:1000:1000:Operator:/home/operator:/bin/bash\n",
				)}, nil
			}
			if spec.Program == "id" && containsArgument(spec.Arguments, "-u") {
				return execx.Output{Stdout: []byte("1000\n")}, nil
			}
			if spec.Program == "id" {
				return execx.Output{Stdout: []byte("operator sudo\n")}, nil
			}
			return execx.Output{}, errors.New("unexpected command")
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{
		Name: "operator", Groups: []string{"sudo"},
		AuthorizedKeySources: []string{
			"/etc/ohtools/plugins/keys/operator.pub",
		},
	}}
	profile := Profile{
		Platform: Platform{ID: "ubuntu", Version: "24.04"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers},
	}
	observation, err := backend.Observe(context.Background(), ItemUsers, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("missing authorized key was reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemUsers, profile); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), ItemUsers, profile); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "home", "operator", ".ssh", "authorized_keys")
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != key+"\n" {
		t.Fatalf("authorized_keys = %q", content)
	}
}

func TestSystemBackendRejectsMalformedAuthorizedKeyBeforeSSHHardening(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "etc", "ohtools", "plugins", "keys", "operator.pub")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("ssh-ed25519 not-base64 operator\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "home", "operator"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if output, ok := usableAdministratorOutput(spec); ok {
				return output, nil
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{
		Name: "operator", Groups: []string{"sudo"},
		AuthorizedKeySources: []string{"/etc/ohtools/plugins/keys/operator.pub"},
	}}
	_, err := backend.Observe(context.Background(), ItemUsers, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemSSH},
	})
	if err == nil {
		t.Fatal("malformed SSH public key was accepted")
	}
}

func TestSystemBackendRejectsAdministratorWithNonLoginShell(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	key := validEd25519PublicKey("operator@example")
	source := filepath.Join(root, "etc", "ohtools", "plugins", "keys", "operator.pub")
	target := filepath.Join(root, "home", "operator", ".ssh", "authorized_keys")
	for _, directory := range []string{filepath.Dir(source), filepath.Dir(target)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(source, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch {
			case spec.Program == "id" && containsArgument(spec.Arguments, "-u"):
				return execx.Output{Stdout: []byte("1000\n")}, nil
			case spec.Program == "id":
				return execx.Output{Stdout: []byte("operator sudo\n")}, nil
			case spec.Program == "getent":
				return execx.Output{Stdout: []byte(
					"operator:x:1000:1000:Operator:/home/operator:/usr/sbin/nologin\n",
				)}, nil
			default:
				return execx.Output{}, nil
			}
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{
		Name: "operator", Groups: []string{"sudo"},
		AuthorizedKeySources: []string{"/etc/ohtools/plugins/keys/operator.pub"},
	}}
	_, err := backend.Observe(context.Background(), ItemUsers, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemSSH},
	})
	if err == nil {
		t.Fatal("administrator with non-login shell was accepted")
	}
}

func TestSystemBackendRejectsAdministratorWithMissingHomeDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch {
			case spec.Program == "id" && containsArgument(spec.Arguments, "-u"):
				return execx.Output{Stdout: []byte("1000\n")}, nil
			case spec.Program == "getent":
				return execx.Output{Stdout: []byte(
					"operator:x:1000:1000:Operator:/home/operator:/bin/bash\n",
				)}, nil
			default:
				return execx.Output{}, nil
			}
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{Name: "operator"}}
	_, err := backend.Observe(context.Background(), ItemUsers, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers},
	})
	if err == nil {
		t.Fatal("administrator with a missing home directory was accepted")
	}
}

func TestFirewallObservationRejectsWrongActivePort(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch {
			case spec.Program == "systemctl":
				return execx.Output{}, nil
			case spec.Program == "nft" && containsArgument(spec.Arguments, "list"):
				return execx.Output{Stdout: []byte(
					"table inet ohtools_server_setup { chain input { type filter hook input priority 0; policy accept; tcp dport 22 accept; } }\n",
				)}, nil
			default:
				return execx.Output{}, nil
			}
		}),
	}
	config := DefaultConfig()
	config.SSHPort = 2222
	config.ManageFirewall = true
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemFirewall},
	}
	for _, managed := range mustFirewallFiles(t, backend, profile) {
		target := backend.path(managed.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, managed.Content, managed.Mode); err != nil {
			t.Fatal(err)
		}
	}
	observation, err := backend.Observe(context.Background(), ItemFirewall, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("firewall with the wrong active SSH port was accepted")
	}
}

func TestFirewallRollbackRestoresActiveRulesAndEnablement(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	oldConfig := DefaultConfig()
	oldConfig.ManageFirewall = true
	oldProfile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   oldConfig,
		Items:    []Item{ItemPackages, ItemFirewall},
	}
	enabled := false
	activeRules := append([]byte(nil), mustFirewallFiles(
		t,
		SystemBackend{Root: root},
		oldProfile,
	)[0].Content...)
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch {
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-enabled"):
				if enabled {
					return execx.Output{}, nil
				}
				return execx.Output{ExitCode: 1}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "enable"):
				enabled = true
				return execx.Output{}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "disable"):
				enabled = false
				return execx.Output{}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "restart"):
				activeRules = nil
				return execx.Output{ExitCode: 1, Stderr: []byte("restart failed")}, nil
			case spec.Program == "systemctl":
				return execx.Output{}, nil
			case spec.Program == "nft" && containsArgument(spec.Arguments, "list"):
				if len(activeRules) == 0 {
					return execx.Output{ExitCode: 1}, nil
				}
				return execx.Output{Stdout: append([]byte(nil), activeRules...)}, nil
			case spec.Program == "nft" && containsArgument(spec.Arguments, "-c"):
				return execx.Output{}, nil
			case spec.Program == "nft" && containsArgument(spec.Arguments, "-f"):
				if len(spec.Stdin) > 0 {
					activeRules = append([]byte(nil), spec.Stdin...)
					return execx.Output{}, nil
				}
				return execx.Output{}, errors.New("nft restore path is missing")
			case spec.Program == "nft" && containsArgument(spec.Arguments, "delete"):
				activeRules = nil
				return execx.Output{}, nil
			default:
				return execx.Output{}, nil
			}
		}),
	}
	for _, managed := range mustFirewallFiles(t, backend, oldProfile) {
		target := backend.path(managed.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, managed.Content, managed.Mode); err != nil {
			t.Fatal(err)
		}
	}
	newConfig := oldConfig
	newConfig.SSHPort = 2222
	err := backend.Apply(context.Background(), ItemFirewall, Profile{
		Platform: oldProfile.Platform,
		Config:   newConfig,
		Items:    oldProfile.Items,
	})
	if err == nil {
		t.Fatal("firewall restart failure was ignored")
	}
	if enabled {
		t.Fatal("firewall service enablement was not rolled back")
	}
	if !firewallRulesEqual(activeRules, mustFirewallFiles(t, backend, oldProfile)[0].Content) {
		t.Fatalf("active firewall rules were not restored: %q", activeRules)
	}
}

func mustFirewallFiles(t *testing.T, backend SystemBackend, profile Profile) []managedFile {
	t.Helper()
	files, err := backend.firewallFiles(profile)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func validEd25519PublicKey(comment string) string {
	return validEd25519PublicKeyWithSeed(comment, 1)
}

func validEd25519PublicKeyWithSeed(comment string, seed byte) string {
	keyType := []byte("ssh-ed25519")
	public := make([]byte, 32)
	for index := range public {
		public[index] = byte(index) + seed
	}
	blob := make([]byte, 0, 4+len(keyType)+4+len(public))
	blob = binary.BigEndian.AppendUint32(blob, uint32(len(keyType)))
	blob = append(blob, keyType...)
	blob = binary.BigEndian.AppendUint32(blob, uint32(len(public)))
	blob = append(blob, public...)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " " + comment
}

func prepareUsableSSHAdministrator(t *testing.T, root string) Administrator {
	t.Helper()
	key := validEd25519PublicKey("operator@example")
	source := filepath.Join(root, "etc", "ohtools", "plugins", "keys", "operator.pub")
	target := filepath.Join(root, "home", "operator", ".ssh", "authorized_keys")
	for _, directory := range []string{filepath.Dir(source), filepath.Dir(target)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{source, target} {
		if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Administrator{
		Name: "operator", Groups: []string{"sudo"},
		AuthorizedKeySources: []string{"/etc/ohtools/plugins/keys/operator.pub"},
	}
}

func usableAdministratorOutput(spec execx.Spec) (execx.Output, bool) {
	switch {
	case spec.Program == "id" && containsArgument(spec.Arguments, "-u"):
		return execx.Output{Stdout: []byte("1000\n")}, true
	case spec.Program == "id":
		return execx.Output{Stdout: []byte("operator sudo\n")}, true
	case spec.Program == "getent":
		return execx.Output{Stdout: []byte(
			"operator:x:1000:1000:Operator:/home/operator:/bin/bash\n",
		)}, true
	default:
		return execx.Output{}, false
	}
}

func TestSystemBackendChecksLegacyEntitlementWithoutMutation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{Root: root, Runner: successfulRunner()}
	if err := backend.Entitled(context.Background(), Platform{
		ID: "debian", Version: "10", RequiresExtendedSupport: true,
	}); err == nil {
		t.Fatal("missing Debian ELTS source was accepted")
	}
	source := filepath.Join(root, "etc", "apt", "sources.list.d", "elts.list")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("deb https://deb.freexian.com/extended-lts buster-lts main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := backend.Entitled(context.Background(), Platform{
		ID: "debian", Version: "10", RequiresExtendedSupport: true,
	}); err != nil {
		t.Fatal(err)
	}

	ubuntu := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program != "pro" {
				return execx.Output{}, errors.New("unexpected program")
			}
			return execx.Output{Stdout: []byte(`{"attached":true}`)}, nil
		}),
	}
	if err := ubuntu.Entitled(context.Background(), Platform{
		ID: "ubuntu", Version: "20.04", RequiresExtendedSupport: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSystemBackendAddsEarlySSHIncludeAndVerifiesEffectiveState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mainConfig := filepath.Join(root, "etc", "ssh", "sshd_config")
	if err := os.MkdirAll(filepath.Dir(mainConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		mainConfig,
		[]byte("# vendor configuration\nUsePAM yes\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	var calls []execx.Spec
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			if output, ok := usableAdministratorOutput(spec); ok {
				return output, nil
			}
			if spec.Program == "sshd" && containsArgument(spec.Arguments, "-T") {
				return execx.Output{Stdout: []byte(
					"port 2222\n" +
						"permitrootlogin prohibit-password\n" +
						"passwordauthentication no\n" +
						"kbdinteractiveauthentication no\n" +
						"pubkeyauthentication yes\n",
				)}, nil
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.SSHPort = 2222
	config.ManageFirewall = true
	config.Administrators = []Administrator{prepareUsableSSHAdministrator(t, root)}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "10"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemFirewall, ItemSSH},
	}
	observation, err := backend.Observe(context.Background(), ItemSSH, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("missing SSH include and drop-in were reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemSSH, profile); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), ItemSSH, profile); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(mainConfig)
	if err != nil {
		t.Fatal(err)
	}
	const include = "Include /etc/ssh/sshd_config.d/*.conf\n"
	if !strings.HasPrefix(string(content), include) ||
		strings.Count(string(content), include) != 1 {
		t.Fatalf("sshd_config = %q", content)
	}
	if len(calls) < 3 {
		t.Fatalf("expected syntax, reload, and effective-state checks; calls=%#v", calls)
	}
}

func TestSystemBackendBlocksSSHHardeningUntilAdministratorKeyIsUsable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mainConfig := filepath.Join(root, "etc", "ssh", "sshd_config")
	if err := os.MkdirAll(filepath.Dir(mainConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainConfig, []byte("# vendor configuration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program == "id" {
				return execx.Output{ExitCode: 1}, nil
			}
			if spec.Program == "sshd" && containsArgument(spec.Arguments, "-T") {
				return execx.Output{Stdout: []byte(
					"port 22\n" +
						"permitrootlogin prohibit-password\n" +
						"passwordauthentication no\n" +
						"kbdinteractiveauthentication no\n" +
						"pubkeyauthentication yes\n",
				)}, nil
			}
			if spec.Program == "systemctl" {
				reloads++
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{
		Name: "operator",
		AuthorizedKeySources: []string{
			"/etc/ohtools/plugins/keys/operator.pub",
		},
	}}
	err := backend.Apply(context.Background(), ItemSSH, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemSSH},
	})
	if err == nil {
		t.Fatal("SSH hardening accepted an unavailable administrator key")
	}
	if reloads != 0 {
		t.Fatalf("SSH was reloaded before administrator access validation: %d", reloads)
	}
}

func TestSystemBackendRollsBackSSHIncludeWhenActivationFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mainConfig := filepath.Join(root, "etc", "ssh", "sshd_config")
	if err := os.MkdirAll(filepath.Dir(mainConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("# vendor configuration\n")
	if err := os.WriteFile(mainConfig, original, 0o600); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if output, ok := usableAdministratorOutput(spec); ok {
				return output, nil
			}
			if spec.Program == "sshd" && containsArgument(spec.Arguments, "-T") {
				return execx.Output{Stdout: []byte(
					"port 22\n" +
						"permitrootlogin prohibit-password\n" +
						"passwordauthentication no\n" +
						"kbdinteractiveauthentication no\n" +
						"pubkeyauthentication yes\n",
				)}, nil
			}
			if spec.Program == "systemctl" {
				return execx.Output{ExitCode: 1, Stderr: []byte("reload rejected")}, nil
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{prepareUsableSSHAdministrator(t, root)}
	err := backend.Apply(context.Background(), ItemSSH, Profile{
		Platform: Platform{ID: "debian", Version: "10"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemSSH},
	})
	if err == nil {
		t.Fatal("SSH activation failure was ignored")
	}
	content, readErr := os.ReadFile(mainConfig)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(content, original) {
		t.Fatalf("main SSH configuration was not rolled back: %q", content)
	}
}

func TestSystemBackendBlocksForeignSSHConflictBeforeReload(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mainConfig := filepath.Join(root, "etc", "ssh", "sshd_config")
	if err := os.MkdirAll(filepath.Dir(mainConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		mainConfig,
		[]byte(sshIncludeDirective+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "etc", "ssh", "sshd_config.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	previousDropIn := []byte("# previous managed state\n")
	if err := os.WriteFile(target, previousDropIn, 0o600); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if output, ok := usableAdministratorOutput(spec); ok {
				return output, nil
			}
			if spec.Program == "sshd" && containsArgument(spec.Arguments, "-T") {
				return execx.Output{Stdout: []byte(
					"port 22\n" +
						"permitrootlogin yes\n" +
						"passwordauthentication yes\n" +
						"kbdinteractiveauthentication yes\n" +
						"pubkeyauthentication yes\n",
				)}, nil
			}
			if spec.Program == "systemctl" {
				reloads++
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{prepareUsableSSHAdministrator(t, root)}
	err := backend.Apply(context.Background(), ItemSSH, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemSSH},
	})
	if err == nil {
		t.Fatal("foreign effective SSH conflict was accepted")
	}
	if reloads != 0 {
		t.Fatalf("sshd reloaded before effective-state validation: %d", reloads)
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(content, previousDropIn) {
		t.Fatalf("managed drop-in was not restored after conflict: %q", content)
	}
}

func TestSystemBackendPersistsFirewallWithoutSecondUpdate(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	enabled := false
	active := false
	daemonReloads := 0
	enableCalls := 0
	restartCalls := 0
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch {
			case spec.Program == "nft" && containsArgument(spec.Arguments, "-c"):
				return execx.Output{}, nil
			case spec.Program == "nft" && containsArgument(spec.Arguments, "list"):
				if active {
					return execx.Output{Stdout: []byte(
						"table inet ohtools_server_setup {\n chain input { type filter hook input priority 0; policy accept; tcp dport 22 accept; }\n}\n",
					)}, nil
				}
				return execx.Output{ExitCode: 1}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-enabled"):
				if enabled {
					return execx.Output{}, nil
				}
				return execx.Output{ExitCode: 1}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "daemon-reload"):
				daemonReloads++
				return execx.Output{}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "enable"):
				enableCalls++
				enabled = true
				return execx.Output{}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "restart"):
				restartCalls++
				active = true
				return execx.Output{}, nil
			default:
				return execx.Output{}, errors.New("unexpected command")
			}
		}),
	}
	config := DefaultConfig()
	config.ManageFirewall = true
	profile := Profile{
		Platform: Platform{ID: "ubuntu", Version: "24.04"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemFirewall},
	}
	observation, err := backend.Observe(context.Background(), ItemFirewall, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("missing persistent firewall unit was reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemFirewall, profile); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), ItemFirewall, profile); err != nil {
		t.Fatal(err)
	}
	second, err := backend.Observe(context.Background(), ItemFirewall, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Converged || daemonReloads != 1 || enableCalls != 1 ||
		restartCalls != 1 {
		t.Fatalf(
			"second observation=%#v daemonReloads=%d enableCalls=%d restartCalls=%d",
			second,
			daemonReloads,
			enableCalls,
			restartCalls,
		)
	}
	unit := filepath.Join(
		root,
		"etc",
		"systemd",
		"system",
		"ohtools-server-setup-firewall.service",
	)
	content, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		string(content),
		"ExecStart=/usr/sbin/nft -f /etc/nftables.d/ohtools-server-setup.nft",
	) {
		t.Fatalf("firewall unit = %q", content)
	}
}

func successfulRunner() execx.Runner {
	return execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
		return execx.Output{}, nil
	})
}

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (function httpDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}
