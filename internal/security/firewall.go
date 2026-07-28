package security

import (
	"context"
	"errors"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

type firewallProbe struct {
	Name      string
	Arguments []string
}

func collectFirewall(ctx context.Context, options Options) collection {
	probes := []firewallProbe{
		{Name: "nft", Arguments: []string{"list", "ruleset"}},
		{Name: "ufw", Arguments: []string{"status"}},
	}
	data := map[string]any{}
	errorsOut := []protocol.StructuredError{}
	usable := false
	privilegeDenied := false
	for _, item := range probes {
		output, err := options.Local.Run(ctx, probe.Command{
			Program: item.Name, Arguments: item.Arguments,
		})
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return collection{Fatal: err}
		case err == nil && output.ExitCode == 0:
			usable = true
			data[item.Name] = firewallSummary(item.Name, output.Stdout)
		case errors.Is(err, execx.ErrNotFound):
			errorsOut = append(errorsOut, structuredError(
				protocol.ErrorDependency, item.Name+"_unavailable",
				item.Name+" firewall utility is unavailable", item.Name,
			))
		case err != nil:
			errorsOut = append(errorsOut, structuredError(
				protocol.ErrorGeneral, item.Name+"_probe_failed",
				item.Name+" firewall state could not be inspected", item.Name,
			))
		case permissionDenied(output.Stderr):
			privilegeDenied = true
			errorsOut = append(errorsOut, structuredError(
				protocol.ErrorPrivilege, item.Name+"_probe_denied",
				item.Name+" firewall state requires additional privileges", item.Name,
			))
		default:
			errorsOut = append(errorsOut, structuredError(
				protocol.ErrorDependency, item.Name+"_probe_failed",
				item.Name+" firewall state is unavailable", item.Name,
			))
		}
	}
	exitCode := protocol.ExitDependency
	if privilegeDenied && !usable {
		exitCode = protocol.ExitPrivilege
	}
	checks := []protocol.Check{}
	if usable {
		checks = append(checks, protocol.Check{
			ID: "firewall.available", Status: protocol.StatusPass,
			Summary: "At least one local firewall source is available",
		})
	}
	return collection{
		Data: data, Checks: checks, Errors: errorsOut,
		Usable: usable, ExitCode: exitCode,
	}
}

func firewallSummary(name string, raw []byte) map[string]any {
	text := strings.TrimSpace(string(raw))
	lineCount := 0
	if text != "" {
		lineCount = len(strings.Split(text, "\n"))
	}
	summary := map[string]any{"available": true, "line_count": lineCount}
	if name == "ufw" {
		lower := strings.ToLower(text)
		summary["active"] = strings.Contains(lower, "status: active") &&
			!strings.Contains(lower, "status: inactive")
	}
	return summary
}

func permissionDenied(stderr []byte) bool {
	lower := strings.ToLower(string(stderr))
	return strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "operation not permitted") ||
		strings.Contains(lower, "must be root") ||
		strings.Contains(lower, "root privileges")
}
