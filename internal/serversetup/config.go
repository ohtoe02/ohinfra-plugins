package serversetup

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	pluginconfig "github.com/ohtoe02/ohtools-plugins/internal/config"
)

var (
	accountName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]{0,127}$`)
)

func LoadConfig(path string, required bool) (Config, error) {
	if path == "" {
		path = ConfigPath
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if required {
			return Config{}, fmt.Errorf("server setup mutations require %s", path)
		}
		return DefaultConfig(), nil
	}
	config, err := pluginconfig.Load(path, DefaultConfig())
	if err != nil {
		return Config{}, err
	}
	if err := ValidateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func ValidateConfig(config Config) error {
	if config.SSHPort < 1 || config.SSHPort > 65535 {
		return errors.New("ssh_port must be within 1..65535")
	}
	seenPackages := map[string]struct{}{}
	for _, name := range config.Packages {
		if !packageName.MatchString(name) || strings.HasPrefix(name, "-") {
			return fmt.Errorf("invalid package %q", name)
		}
		if _, duplicate := seenPackages[name]; duplicate {
			return fmt.Errorf("duplicate package %q", name)
		}
		seenPackages[name] = struct{}{}
	}
	seenUsers := map[string]struct{}{}
	for _, administrator := range config.Administrators {
		if !accountName.MatchString(administrator.Name) {
			return fmt.Errorf("invalid administrator %q", administrator.Name)
		}
		if _, duplicate := seenUsers[administrator.Name]; duplicate {
			return fmt.Errorf("duplicate administrator %q", administrator.Name)
		}
		seenUsers[administrator.Name] = struct{}{}
		for _, group := range administrator.Groups {
			if !accountName.MatchString(group) {
				return fmt.Errorf("invalid group %q", group)
			}
		}
		for _, source := range administrator.AuthorizedKeySources {
			if !path.IsAbs(source) || path.Clean(source) != source ||
				!strings.HasPrefix(source, "/etc/ohtools/plugins/keys/") {
				return fmt.Errorf("unsafe authorized key source %q", source)
			}
		}
	}
	for _, value := range config.EnabledItems {
		if _, err := ExpandItems([]string{value}, config); err != nil {
			return err
		}
	}
	if config.Zabbix != nil && config.Zabbix.Enabled {
		if strings.TrimSpace(config.Zabbix.Server) == "" ||
			(net.ParseIP(config.Zabbix.Server) == nil && !validHostname(config.Zabbix.Server)) {
			return errors.New("zabbix.server must be an IP address or hostname")
		}
		if !validHostname(config.Zabbix.Hostname) {
			return errors.New("zabbix.hostname is invalid")
		}
		repositoryURL, err := url.Parse(config.Zabbix.RepositoryPackageURL)
		if err != nil || repositoryURL.Scheme != "https" ||
			repositoryURL.Hostname() != "repo.zabbix.com" ||
			repositoryURL.Port() != "" || repositoryURL.User != nil ||
			repositoryURL.RawQuery != "" || repositoryURL.Fragment != "" ||
			!strings.HasSuffix(repositoryURL.Path, ".deb") {
			return errors.New("zabbix.repository_package_url must be a credential-free pinned repo.zabbix.com HTTPS .deb URL")
		}
		if config.Zabbix.RepositoryPackageSize < 1 ||
			config.Zabbix.RepositoryPackageSize > 50<<20 {
			return errors.New("zabbix.repository_package_size must be within 1..52428800 bytes")
		}
		if len(config.Zabbix.RepositoryPackageSHA256) != 64 ||
			strings.ToLower(config.Zabbix.RepositoryPackageSHA256) !=
				config.Zabbix.RepositoryPackageSHA256 {
			return errors.New("zabbix.repository_package_sha256 must be lowercase SHA-256")
		}
		if _, err := hex.DecodeString(config.Zabbix.RepositoryPackageSHA256); err != nil {
			return errors.New("zabbix.repository_package_sha256 must be lowercase SHA-256")
		}
	}
	selected, err := ExpandItems(nil, config)
	if err != nil {
		return err
	}
	if slices.Contains(selected, ItemSSH) {
		hasTrustedKeySource := false
		for _, administrator := range config.Administrators {
			if len(administrator.AuthorizedKeySources) > 0 {
				hasTrustedKeySource = true
				break
			}
		}
		if !hasTrustedKeySource {
			return errors.New(
				"ssh hardening requires at least one administrator with an authorized key source",
			)
		}
		if config.SSHPort != 22 && !config.ManageFirewall {
			return errors.New(
				"a non-default ssh_port requires manage_firewall so the new port is opened before reload",
			)
		}
	}
	return nil
}

func validHostname(value string) bool {
	if value == "" || len(value) > 253 || strings.HasPrefix(value, "-") ||
		strings.HasSuffix(value, "-") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func enabled(config Config, item Item) bool {
	return slices.Contains(config.EnabledItems, string(item))
}
