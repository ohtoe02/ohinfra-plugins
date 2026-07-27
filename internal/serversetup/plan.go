package serversetup

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

type PlanMode string

const (
	PlanApply   PlanMode = "apply"
	PlanUpgrade PlanMode = "upgrade"
)

type PlanInput struct {
	Mode           PlanMode
	Profile        Profile
	Checks         []protocol.Check
	CachedUpgrades []string
}

func BuildSetupPlan(input PlanInput) (protocol.Plan, error) {
	if input.Mode != PlanApply && input.Mode != PlanUpgrade {
		return protocol.Plan{}, errors.New("unsupported setup plan mode")
	}
	statuses := make(map[string]protocol.Status, len(input.Checks))
	checks := append([]protocol.Check(nil), input.Checks...)
	for _, check := range checks {
		if check.Status == protocol.StatusSkipped || check.Status == protocol.StatusError ||
			check.Status == protocol.StatusPartial || check.Status == protocol.StatusCancelled {
			return protocol.Plan{}, fmt.Errorf("cannot plan from incomplete check %q", check.ID)
		}
		statuses[check.ID] = check.Status
	}
	sort.SliceStable(checks, func(left, right int) bool {
		return checks[left].ID < checks[right].ID
	})

	changes := []protocol.Change{}
	cachedUpgrades := make(map[string]struct{}, len(input.CachedUpgrades))
	for _, name := range input.CachedUpgrades {
		if !contains(input.Profile.Packages, name) {
			return protocol.Plan{}, fmt.Errorf(
				"cached upgrade %q is outside the compiled profile", name,
			)
		}
		cachedUpgrades[name] = struct{}{}
	}
	for _, name := range input.Profile.Packages {
		id := "package:" + name
		if input.Mode == PlanUpgrade {
			if _, available := cachedUpgrades[name]; available {
				changes = append(changes, plannedChange(id, "upgrade-cached", nil))
			}
			continue
		}
		if needsChange(statuses[id]) {
			changes = append(changes, plannedChange(id, "install-cached", nil))
		}
	}
	for _, group := range input.Profile.Groups {
		id := "group:" + group.Name
		if needsChange(statuses[id]) {
			changes = append(changes, plannedChange(id, "create", map[string]any{
				"system": group.System,
			}))
		}
	}
	for _, user := range input.Profile.Users {
		id := "user:" + user.Name
		if needsChange(statuses[id]) {
			changes = append(changes, plannedChange(id, "create", map[string]any{
				"primary_group": user.PrimaryGroup,
				"home":          user.Home,
				"shell":         user.Shell,
				"system":        user.System,
			}))
		}
	}
	for _, directory := range input.Profile.Directories {
		id := "directory:" + directory.Path
		if needsChange(statuses[id]) {
			changes = append(changes, plannedChange(id, "ensure", map[string]any{
				"mode":  fmt.Sprintf("%#o", directory.Mode),
				"owner": directory.Owner,
				"group": directory.Group,
			}))
		}
	}
	for _, file := range input.Profile.ManagedFiles {
		id := "file:" + file.Path
		if needsChange(statuses[id]) {
			changes = append(changes, plannedChange(id, "replace", map[string]any{
				"sha256": file.SHA256,
				"mode":   fmt.Sprintf("%#o", file.Mode),
				"owner":  file.Owner,
				"group":  file.Group,
			}))
		}
	}
	for _, sysctl := range input.Profile.Sysctls {
		id := "sysctl:" + sysctl.Key
		if needsChange(statuses[id]) {
			changes = append(changes, plannedChange(id, "set", map[string]any{
				"value": sysctl.Value,
			}))
		}
	}
	for _, unit := range input.Profile.Units {
		id := "unit:" + unit
		if needsChange(statuses[id]) {
			changes = append(changes, plannedChange(id, "enable", nil))
		}
	}
	sort.SliceStable(changes, func(left, right int) bool {
		leftPriority := changePriority(changes[left].Object)
		rightPriority := changePriority(changes[right].Object)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		if changes[left].Object == changes[right].Object {
			return changes[left].Action < changes[right].Action
		}
		return changes[left].Object < changes[right].Object
	})

	summary := "Apply the exact compiled local server setup profile"
	risks := []string{"Local packages and operating-system state may change"}
	if input.Mode == PlanUpgrade {
		summary = "Upgrade only cached packages from the compiled local server setup profile"
		risks = []string{
			"Only already-cached approved packages may be upgraded",
			"No package download or distribution upgrade is permitted",
		}
	}
	return protocol.Plan{
		CommandID:            "setup." + string(input.Mode),
		Summary:              summary,
		Checks:               checks,
		Changes:              changes,
		Risks:                risks,
		RequiresRoot:         true,
		RequiresConfirmation: true,
	}, nil
}

func changePriority(object string) int {
	switch {
	case strings.HasPrefix(object, "package:"):
		return 0
	case strings.HasPrefix(object, "group:"):
		return 1
	case strings.HasPrefix(object, "user:"):
		return 2
	case strings.HasPrefix(object, "directory:"):
		return 3
	case strings.HasPrefix(object, "file:"):
		return 4
	case strings.HasPrefix(object, "sysctl:"):
		return 5
	case strings.HasPrefix(object, "unit:"):
		return 6
	default:
		return 7
	}
}

func needsChange(status protocol.Status) bool {
	switch status {
	case protocol.StatusWarning, protocol.StatusCritical:
		return true
	default:
		return false
	}
}

func plannedChange(object, action string, details map[string]any) protocol.Change {
	return protocol.Change{
		Object: object, Action: action, Status: "planned", Details: details,
	}
}

func planModeForPath(path []string) (PlanMode, error) {
	switch strings.Join(path, " ") {
	case "setup apply":
		return PlanApply, nil
	case "setup upgrade":
		return PlanUpgrade, nil
	default:
		return "", argumentError("unsupported setup mutation command")
	}
}
