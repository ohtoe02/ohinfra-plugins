package baseline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/platform"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

type checkOutcome struct {
	check protocol.Check
	err   *protocol.StructuredError
	fatal error
}

func runChecks(
	ctx context.Context,
	local probe.Local,
	info platform.Info,
	settings Config,
	checkIDs []string,
) ([]protocol.Check, []protocol.StructuredError, error) {
	checks := make([]protocol.Check, 0, len(checkIDs))
	structuredErrors := []protocol.StructuredError{}
	for _, checkID := range checkIDs {
		var outcome checkOutcome
		switch checkID {
		case "supported-os":
			outcome = supportedOSCheck(info)
		case "time-sync":
			outcome = timeSyncCheck(ctx, local)
		case "filesystem-ownership":
			outcome = filesystemOwnershipCheck(local)
		case "kernel-controls":
			outcome = kernelControlsCheck(local)
		case "ssh-posture":
			outcome = sshPostureCheck(local)
		case "package-state":
			outcome = packageStateCheck(local, settings.MaxPendingPackageRecords)
		case "required-services":
			outcome = requiredServicesCheck(ctx, local)
		}
		if outcome.fatal != nil {
			return nil, nil, outcome.fatal
		}
		checks = append(checks, outcome.check)
		if outcome.err != nil {
			structuredErrors = append(structuredErrors, *outcome.err)
		}
	}
	return checks, structuredErrors, nil
}

func supportedOSCheck(info platform.Info) checkOutcome {
	return checkOutcome{check: protocol.Check{
		ID: "baseline:supported-os", Status: protocol.StatusPass,
		Summary: fmt.Sprintf("A compiled baseline supports %s %s", info.ID, info.VersionID),
	}}
}

func timeSyncCheck(ctx context.Context, local probe.Local) checkOutcome {
	output, err := local.Run(ctx, probe.Command{
		Program:     "timedatectl",
		Arguments:   []string{"show", "--property=NTPSynchronized", "--value"},
		StdoutLimit: 64 << 10, StderrLimit: 64 << 10,
	})
	if err != nil {
		return unavailableCheck("time-sync", "timedatectl", err)
	}
	synced := output.ExitCode == 0 && strings.EqualFold(strings.TrimSpace(string(output.Stdout)), "yes")
	status, summary := protocol.StatusPass, "System time synchronization is active"
	if !synced {
		status, summary = protocol.StatusWarning, "System time synchronization is not active"
	}
	return checkOutcome{check: protocol.Check{
		ID: "baseline:time-sync", Status: status, Summary: summary,
	}}
}

func filesystemOwnershipCheck(local probe.Local) checkOutcome {
	for _, relative := range []string{"etc/passwd", "etc/shadow"} {
		exists, err := local.Exists(relative)
		if err != nil || !exists {
			return localInspectionError("filesystem-ownership", "Required account files are unavailable")
		}
		info, err := os.Lstat(filepath.Join(local.Root, filepath.FromSlash(relative)))
		if err != nil || !trustedAccountFile(local.Root, info) {
			return checkOutcome{check: protocol.Check{
				ID: "baseline:filesystem-ownership", Status: protocol.StatusCritical,
				Summary: "Required account files have unsafe ownership or permissions",
			}}
		}
	}
	return checkOutcome{check: protocol.Check{
		ID: "baseline:filesystem-ownership", Status: protocol.StatusPass,
		Summary: "Required account files have trusted ownership and permissions",
	}}
}

func kernelControlsCheck(local probe.Local) checkOutcome {
	expected := map[string]string{
		"proc/sys/kernel/randomize_va_space":   "2",
		"proc/sys/net/ipv4/conf/all/rp_filter": "1",
	}
	for relative, want := range expected {
		encoded, err := local.Read(relative, 64)
		if err != nil {
			return localInspectionError("kernel-controls", "Required kernel controls are unavailable")
		}
		if strings.TrimSpace(string(encoded)) != want {
			return checkOutcome{check: protocol.Check{
				ID: "baseline:kernel-controls", Status: protocol.StatusWarning,
				Summary: "One or more compiled kernel controls differ from the baseline",
			}}
		}
	}
	return checkOutcome{check: protocol.Check{
		ID: "baseline:kernel-controls", Status: protocol.StatusPass,
		Summary: "Compiled kernel controls match the baseline",
	}}
}

func sshPostureCheck(local probe.Local) checkOutcome {
	encoded, err := local.Read("etc/ssh/sshd_config", 1<<20)
	if err != nil {
		return localInspectionError("ssh-posture", "OpenSSH server configuration is unavailable")
	}
	values := map[string]string{}
	for _, raw := range strings.Split(string(encoded), "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			values[strings.ToLower(fields[0])] = strings.ToLower(fields[1])
		}
	}
	secureRoot := values["permitrootlogin"] == "no" || values["permitrootlogin"] == "prohibit-password"
	if !secureRoot || values["passwordauthentication"] != "no" {
		return checkOutcome{check: protocol.Check{
			ID: "baseline:ssh-posture", Status: protocol.StatusWarning,
			Summary: "OpenSSH settings differ from the compiled baseline",
		}}
	}
	return checkOutcome{check: protocol.Check{
		ID: "baseline:ssh-posture", Status: protocol.StatusPass,
		Summary: "OpenSSH settings match the compiled baseline",
	}}
}

func packageStateCheck(local probe.Local, maximum int) checkOutcome {
	encoded, err := local.Read("var/lib/dpkg/status", 32<<20)
	if err != nil {
		return localInspectionError("package-state", "The local dpkg database is unavailable")
	}
	pending := 0
	for _, line := range strings.Split(string(encoded), "\n") {
		if value, ok := strings.CutPrefix(line, "Status:"); ok &&
			strings.TrimSpace(value) != "install ok installed" {
			pending++
		}
	}
	status, summary := protocol.StatusPass, "The local package database matches the baseline"
	if pending > maximum {
		status, summary = protocol.StatusWarning, "The local package database contains incomplete records"
	}
	return checkOutcome{check: protocol.Check{
		ID: "baseline:package-state", Status: status, Summary: summary,
		Details: map[string]any{"pending_records": pending, "maximum": maximum},
	}}
}

func requiredServicesCheck(ctx context.Context, local probe.Local) checkOutcome {
	for _, service := range []string{"ssh", "cron"} {
		output, err := local.Run(ctx, probe.Command{
			Program: "systemctl", Arguments: []string{"is-active", "--", service},
			StdoutLimit: 64 << 10, StderrLimit: 64 << 10,
		})
		if err != nil {
			return unavailableCheck("required-services", "systemctl", err)
		}
		if output.ExitCode != 0 || strings.TrimSpace(string(output.Stdout)) != "active" {
			return checkOutcome{check: protocol.Check{
				ID: "baseline:required-services", Status: protocol.StatusWarning,
				Summary: "One or more compiled required services are inactive",
			}}
		}
	}
	return checkOutcome{check: protocol.Check{
		ID: "baseline:required-services", Status: protocol.StatusPass,
		Summary: "Compiled required services are active",
	}}
}

func unavailableCheck(checkID, dependency string, err error) checkOutcome {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return checkOutcome{fatal: err}
	}
	kind := protocol.ErrorGeneral
	code := "baseline_probe_failed"
	if errors.Is(err, execx.ErrNotFound) {
		kind = protocol.ErrorDependency
		code = "missing_dependency"
	}
	structuredError := protocol.StructuredError{
		Kind: kind, Code: code,
		Message:    fmt.Sprintf("Unable to run the compiled %s probe", checkID),
		Dependency: dependency,
	}
	return checkOutcome{
		check: protocol.Check{
			ID: "baseline:" + checkID, Status: protocol.StatusSkipped,
			Summary: "The compiled check could not be completed",
		},
		err: &structuredError,
	}
}

func localInspectionError(checkID, summary string) checkOutcome {
	structuredError := protocol.StructuredError{
		Kind: protocol.ErrorGeneral, Code: "local_probe_failed",
		Message: summary,
	}
	return checkOutcome{
		check: protocol.Check{
			ID: "baseline:" + checkID, Status: protocol.StatusSkipped,
			Summary: "The compiled local check could not be completed",
		},
		err: &structuredError,
	}
}
