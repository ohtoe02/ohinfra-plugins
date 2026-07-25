package serversetup

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	Name        = "server-setup-base"
	Description = "Idempotent Debian and Ubuntu server setup checks, apply, and upgrade operations."
	ConfigPath  = "/etc/ohtools/plugins/server-setup-base.yaml"
)

type Item string

const (
	ItemPackages        Item = "packages"
	ItemUsers           Item = "users"
	ItemShellHistory    Item = "shell-history"
	ItemCronPermissions Item = "cron-permissions"
	ItemSSH             Item = "ssh"
	ItemFail2Ban        Item = "fail2ban"
	ItemTimeSync        Item = "time-sync"
	ItemSecurityUpdates Item = "security-updates"
	ItemLogging         Item = "logging"
	ItemAuditd          Item = "auditd"
	ItemFirewall        Item = "firewall"
	ItemSysctl          Item = "sysctl"
	ItemZabbix          Item = "zabbix"
)

var itemOrder = []Item{
	ItemPackages,
	ItemUsers,
	ItemShellHistory,
	ItemCronPermissions,
	ItemTimeSync,
	ItemSecurityUpdates,
	ItemLogging,
	ItemAuditd,
	ItemSysctl,
	ItemFirewall,
	ItemSSH,
	ItemFail2Ban,
	ItemZabbix,
}

var itemName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type Platform struct {
	ID                      string `json:"id"`
	Version                 string `json:"version"`
	RequiresExtendedSupport bool   `json:"requires_extended_support"`
}

type Administrator struct {
	Name                 string   `yaml:"name"`
	Groups               []string `yaml:"groups"`
	AuthorizedKeySources []string `yaml:"authorized_key_sources"`
}

type ZabbixConfig struct {
	Enabled                 bool   `yaml:"enabled"`
	Server                  string `yaml:"server"`
	Hostname                string `yaml:"hostname"`
	RepositoryPackageURL    string `yaml:"repository_package_url"`
	RepositoryPackageSize   int64  `yaml:"repository_package_size"`
	RepositoryPackageSHA256 string `yaml:"repository_package_sha256"`
}

type Config struct {
	EnabledItems   []string        `yaml:"enabled_items"`
	Packages       []string        `yaml:"packages"`
	Administrators []Administrator `yaml:"administrators"`
	SSHPort        int             `yaml:"ssh_port"`
	ManageFirewall bool            `yaml:"manage_firewall"`
	Zabbix         *ZabbixConfig   `yaml:"zabbix"`
}

func DefaultConfig() Config {
	return Config{
		EnabledItems: []string{
			string(ItemPackages),
			string(ItemShellHistory),
			string(ItemCronPermissions),
			string(ItemSSH),
			string(ItemFail2Ban),
			string(ItemTimeSync),
			string(ItemSecurityUpdates),
			string(ItemLogging),
			string(ItemAuditd),
			string(ItemSysctl),
		},
		Packages:       []string{},
		Administrators: []Administrator{},
		SSHPort:        22,
	}
}

func ResolvePlatform(content []byte) (Platform, error) {
	if len(content) == 0 || len(content) > 64<<10 {
		return Platform{}, errors.New("os-release must contain 1..65536 bytes")
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "ID" && key != "VERSION_ID" {
			continue
		}
		if _, duplicate := values[key]; duplicate {
			return Platform{}, fmt.Errorf("os-release contains duplicate %s", key)
		}
		values[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	if err := scanner.Err(); err != nil {
		return Platform{}, fmt.Errorf("read os-release: %w", err)
	}
	id, version := values["ID"], values["VERSION_ID"]
	supported := map[string]map[string]bool{
		"debian": {
			"10": true, "11": false, "12": false, "13": false,
		},
		"ubuntu": {
			"20.04": true, "22.04": false, "24.04": false,
		},
	}
	versions, found := supported[id]
	requiresExtendedSupport, supportedVersion := versions[version]
	if !found || !supportedVersion && !containsVersion(versions, version) {
		return Platform{}, fmt.Errorf("unsupported operating system %s %s", id, version)
	}
	return Platform{
		ID: id, Version: version, RequiresExtendedSupport: requiresExtendedSupport,
	}, nil
}

func containsVersion(versions map[string]bool, version string) bool {
	_, found := versions[version]
	return found
}

func ExpandItems(requested []string, config Config) ([]Item, error) {
	if len(requested) == 0 {
		requested = append([]string(nil), config.EnabledItems...)
		if len(config.Administrators) > 0 {
			requested = append(requested, string(ItemUsers))
		}
		if config.ManageFirewall {
			requested = append(requested, string(ItemFirewall))
		}
		if config.Zabbix != nil && config.Zabbix.Enabled {
			requested = append(requested, string(ItemZabbix))
		}
	}
	selected := map[Item]bool{}
	var add func(Item) error
	add = func(item Item) error {
		if selected[item] {
			return nil
		}
		switch item {
		case ItemPackages:
		case ItemUsers:
			if err := add(ItemPackages); err != nil {
				return err
			}
		case ItemShellHistory:
			if err := add(ItemUsers); err != nil {
				return err
			}
		case ItemCronPermissions, ItemTimeSync, ItemSecurityUpdates, ItemLogging,
			ItemAuditd, ItemSysctl, ItemFirewall, ItemZabbix:
			if err := add(ItemPackages); err != nil {
				return err
			}
		case ItemSSH:
			if err := add(ItemUsers); err != nil {
				return err
			}
			if config.ManageFirewall {
				if err := add(ItemFirewall); err != nil {
					return err
				}
			}
		case ItemFail2Ban:
			if err := add(ItemSSH); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown setup item %q", item)
		}
		selected[item] = true
		return nil
	}
	for _, value := range requested {
		if !itemName.MatchString(value) {
			return nil, fmt.Errorf("invalid setup item %q", value)
		}
		if err := add(Item(value)); err != nil {
			return nil, err
		}
	}
	output := make([]Item, 0, len(selected))
	for _, item := range itemOrder {
		if selected[item] {
			output = append(output, item)
		}
	}
	return output, nil
}
