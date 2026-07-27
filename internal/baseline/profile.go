package baseline

import (
	"errors"
	"fmt"
	"slices"
)

type Config struct {
	Profile                  string   `yaml:"profile"`
	EnabledChecks            []string `yaml:"enabled_checks"`
	DisabledChecks           []string `yaml:"disabled_checks"`
	MaxPendingPackageRecords int      `yaml:"max_pending_package_records"`
}

var compiledCheckIDs = []string{
	"supported-os",
	"time-sync",
	"filesystem-ownership",
	"kernel-controls",
	"ssh-posture",
	"package-state",
	"required-services",
}

func DefaultConfig() Config {
	return Config{
		Profile:                  "default",
		EnabledChecks:            []string{},
		DisabledChecks:           []string{},
		MaxPendingPackageRecords: 0,
	}
}

func selectedChecks(settings Config) ([]string, error) {
	if settings.Profile != "default" {
		return nil, errors.New("profile must name the compiled default profile")
	}
	if settings.MaxPendingPackageRecords < 0 || settings.MaxPendingPackageRecords > 100000 {
		return nil, errors.New("max_pending_package_records must be within 0..100000")
	}
	if err := validateCheckIDs(settings.EnabledChecks); err != nil {
		return nil, fmt.Errorf("enabled_checks: %w", err)
	}
	if err := validateCheckIDs(settings.DisabledChecks); err != nil {
		return nil, fmt.Errorf("disabled_checks: %w", err)
	}

	candidates := compiledCheckIDs
	if len(settings.EnabledChecks) > 0 {
		candidates = settings.EnabledChecks
	}
	output := make([]string, 0, len(candidates))
	for _, checkID := range compiledCheckIDs {
		if slices.Contains(candidates, checkID) && !slices.Contains(settings.DisabledChecks, checkID) {
			output = append(output, checkID)
		}
	}
	if len(output) == 0 {
		return nil, errors.New("baseline must enable at least one compiled check")
	}
	return output, nil
}

func validateCheckIDs(values []string) error {
	seen := map[string]struct{}{}
	for _, value := range values {
		if !slices.Contains(compiledCheckIDs, value) {
			return fmt.Errorf("unknown compiled check %q", value)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("duplicate compiled check %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}
