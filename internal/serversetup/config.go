package serversetup

import (
	"errors"
	"fmt"

	pluginconfig "github.com/ohtoe02/ohtools-plugins/internal/config"
	"github.com/ohtoe02/ohtools-plugins/internal/platform"
)

const (
	minInspectionBytes int64 = 4 << 10
	maxManagedBytes    int64 = 8 << 20
	maxAccountBytes    int64 = 1 << 20
)

type Config struct {
	Profile               string `yaml:"profile"`
	ManagedFileLimitBytes int64  `yaml:"managed_file_limit_bytes"`
	AccountFileLimitBytes int64  `yaml:"account_file_limit_bytes"`
}

func DefaultConfig() Config {
	return Config{
		Profile:               "auto",
		ManagedFileLimitBytes: 1 << 20,
		AccountFileLimitBytes: 256 << 10,
	}
}

func LoadConfig(path string) (Config, error) {
	settings, err := pluginconfig.Load(path, DefaultConfig())
	if err != nil {
		return Config{}, fmt.Errorf("load server-setup-base config: %w", err)
	}
	if err := ValidateConfig(settings); err != nil {
		return Config{}, err
	}
	return settings, nil
}

func ValidateConfig(settings Config) error {
	if settings.Profile == "" {
		return errors.New("server-setup-base config profile must not be empty")
	}
	if settings.ManagedFileLimitBytes < minInspectionBytes ||
		settings.ManagedFileLimitBytes > maxManagedBytes {
		return fmt.Errorf(
			"server-setup-base config managed_file_limit_bytes must be within %d..%d",
			minInspectionBytes, maxManagedBytes,
		)
	}
	if settings.AccountFileLimitBytes < minInspectionBytes ||
		settings.AccountFileLimitBytes > maxAccountBytes {
		return fmt.Errorf(
			"server-setup-base config account_file_limit_bytes must be within %d..%d",
			minInspectionBytes, maxAccountBytes,
		)
	}
	return nil
}

func ResolveProfile(settings Config, detected platform.Info) (Profile, error) {
	if err := ValidateConfig(settings); err != nil {
		return Profile{}, err
	}
	if settings.Profile == "auto" {
		return ProfileFor(detected)
	}

	profile, err := ProfileByID(settings.Profile)
	if err != nil {
		return Profile{}, fmt.Errorf("server-setup-base config: %w", err)
	}
	if profile.PlatformID != detected.ID || profile.VersionID != detected.VersionID ||
		!detected.Supported {
		return Profile{}, fmt.Errorf(
			"server-setup-base config profile %q does not match detected platform %s %s",
			settings.Profile, detected.ID, detected.VersionID,
		)
	}
	return profile, nil
}
