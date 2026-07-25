package serversetup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigRequiresTrustedMutationProfile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "server-setup-base.yaml")
	if _, err := LoadConfig(path, true); err == nil {
		t.Fatal("missing mutation config was accepted")
	}
	if err := os.WriteFile(path, []byte("unknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path, true); err == nil {
		t.Fatal("unknown config key was accepted")
	}
}

func TestConfigRejectsPasswordsCommandsAndUnsafeIdentifiers(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"password": "administrators:\n  - name: operator\n    password: secret\n",
		"command":  "zabbix:\n  enabled: true\n  command: curl bad\n",
		"user":     "administrators:\n  - name: ../root\n",
		"package":  "packages:\n  - --option-smuggling\n",
		"ssh-port": "ssh_port: 70000\n",
	} {
		name, content := name, content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "server-setup-base.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path, true); err == nil {
				t.Fatalf("unsafe config was accepted:\n%s", content)
			}
		})
	}
}

func TestLoadConfigAcceptsConstrainedProfile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "server-setup-base.yaml")
	content := []byte(`
packages: [curl, vim]
administrators:
  - name: operator
    groups: [sudo]
    authorized_key_sources:
      - /etc/ohtools/plugins/keys/operator.pub
ssh_port: 2222
manage_firewall: true
zabbix:
  enabled: true
  server: 192.0.2.10
  hostname: web-01
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if config.SSHPort != 2222 || !config.ManageFirewall || len(config.Administrators) != 1 ||
		config.Zabbix == nil || !config.Zabbix.Enabled {
		t.Fatalf("config = %#v", config)
	}
}
