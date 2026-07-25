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
  repository_package_url: https://repo.zabbix.com/zabbix/7.0/debian/pool/main/z/zabbix-release/zabbix-release_latest_7.0+debian12_all.deb
  repository_package_size: 8
  repository_package_sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
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

func TestConfigRequiresPinnedZabbixRepositoryPackage(t *testing.T) {
	t.Parallel()

	base := `
zabbix:
  enabled: true
  server: 192.0.2.10
  hostname: web-01
`
	for name, fields := range map[string]string{
		"missing": "",
		"http": `
  repository_package_url: http://repo.zabbix.com/release.deb
  repository_package_size: 8
  repository_package_sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`,
		"credentials": `
  repository_package_url: https://token@repo.zabbix.com/release.deb
  repository_package_size: 8
  repository_package_sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`,
		"host": `
  repository_package_url: https://example.com/release.deb
  repository_package_size: 8
  repository_package_sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`,
		"digest": `
  repository_package_url: https://repo.zabbix.com/release.deb
  repository_package_size: 8
  repository_package_sha256: bad
`,
	} {
		name, fields := name, fields
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "server-setup-base.yaml")
			if err := os.WriteFile(path, []byte(base+fields), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path, true); err == nil {
				t.Fatalf("unsafe Zabbix repository configuration %q was accepted", name)
			}
		})
	}
}

func TestConfigBlocksUnsafeSSHHardeningProfiles(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"no-admin-key": `
enabled_items: [ssh]
ssh_port: 22
`,
		"new-port-without-firewall": `
enabled_items: [ssh]
administrators:
  - name: operator
    authorized_key_sources:
      - /etc/ohtools/plugins/keys/operator.pub
ssh_port: 2222
manage_firewall: false
`,
	} {
		name, content := name, content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "server-setup-base.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path, true); err == nil {
				t.Fatalf("unsafe SSH profile %q was accepted", name)
			}
		})
	}
}
