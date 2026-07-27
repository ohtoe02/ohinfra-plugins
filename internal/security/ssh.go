package security

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const sshConfigLimit int64 = 256 << 10

var safeSSHDirectives = map[string]struct{}{
	"challengeresponseauthentication": {},
	"kbdinteractiveauthentication":    {},
	"logingracetime":                  {},
	"maxauthtries":                    {},
	"passwordauthentication":          {},
	"permitrootlogin":                 {},
	"pubkeyauthentication":            {},
	"usepam":                          {},
	"x11forwarding":                   {},
}

func collectSSH(ctx context.Context, options Options) collection {
	data := map[string]any{}
	errorsOut := []protocol.StructuredError{}
	usable := false

	directives, configured, configErrors := readSSHConfiguration(options)
	errorsOut = append(errorsOut, configErrors...)
	if configured {
		data["configured"] = true
		data["configured_directives"] = directives
		usable = true
	} else {
		data["configured"] = false
		errorsOut = append(errorsOut, structuredError(
			protocol.ErrorDependency, "sshd_config_unavailable",
			"SSH server configuration is unavailable", "sshd_config",
		))
	}

	output, err := options.Local.Run(ctx, probe.Command{
		Program: "sshd",
		Arguments: []string{
			"-T", "-C", "user=root,host=localhost,addr=127.0.0.1",
		},
	})
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return collection{Fatal: err}
	case err == nil && output.ExitCode == 0:
		data["effective_available"] = true
		data["effective_directives"] = parseSafeSSHDirectives(
			strings.Split(strings.TrimSpace(string(output.Stdout)), "\n"),
		)
		usable = true
	case errors.Is(err, execx.ErrNotFound):
		data["effective_available"] = false
		errorsOut = append(errorsOut, structuredError(
			protocol.ErrorDependency, "sshd_unavailable",
			"SSH server executable is unavailable", "sshd",
		))
	case err != nil:
		data["effective_available"] = false
		errorsOut = append(errorsOut, structuredError(
			protocol.ErrorGeneral, "sshd_probe_failed",
			"SSH effective configuration could not be inspected", "sshd",
		))
	default:
		data["effective_available"] = false
		kind := protocol.ErrorGeneral
		code := "sshd_probe_failed"
		if permissionDenied(output.Stderr) {
			kind = protocol.ErrorPrivilege
			code = "sshd_probe_denied"
		}
		errorsOut = append(errorsOut, structuredError(
			kind, code, "SSH effective configuration could not be inspected", "sshd",
		))
	}
	return collection{
		Data: data, Checks: sshChecks(data), Errors: errorsOut,
		Usable: usable, ExitCode: protocol.ExitDependency,
	}
}

func readSSHConfiguration(
	options Options,
) (map[string]string, bool, []protocol.StructuredError) {
	directives := map[string]string{}
	configured := false
	errorsOut := []protocol.StructuredError{}
	if lines, err := options.Local.Lines("etc/ssh/sshd_config", sshConfigLimit); err == nil {
		mergeSSHDirectives(directives, parseSafeSSHDirectives(lines))
		configured = true
	}

	directory := filepath.Join(options.Root, "etc", "ssh", "sshd_config.d")
	info, err := os.Lstat(directory)
	if err != nil {
		return directives, configured, errorsOut
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return directives, configured, append(errorsOut, structuredError(
			protocol.ErrorGeneral, "sshd_dropins_unsafe",
			"SSH configuration drop-in directory is not a safe local directory", "sshd_config",
		))
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return directives, configured, append(errorsOut, structuredError(
			protocol.ErrorPrivilege, "sshd_dropins_unavailable",
			"SSH configuration drop-ins are unavailable", "sshd_config",
		))
	}
	if len(entries) > 128 {
		return directives, configured, append(errorsOut, structuredError(
			protocol.ErrorGeneral, "sshd_dropins_exceeded",
			"SSH configuration drop-in count exceeds the safety limit", "sshd_config",
		))
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".conf" || entry.Type()&os.ModeSymlink != 0 ||
			!entry.Type().IsRegular() {
			continue
		}
		relative := filepath.ToSlash(filepath.Join("etc", "ssh", "sshd_config.d", entry.Name()))
		lines, err := options.Local.Lines(relative, sshConfigLimit)
		if err != nil {
			errorsOut = append(errorsOut, structuredError(
				protocol.ErrorGeneral, "sshd_dropin_unavailable",
				"An SSH configuration drop-in could not be inspected", "sshd_config",
			))
			continue
		}
		mergeSSHDirectives(directives, parseSafeSSHDirectives(lines))
		configured = true
	}
	return directives, configured, errorsOut
}

func mergeSSHDirectives(destination, source map[string]string) {
	for key, value := range source {
		destination[key] = value
	}
}

func parseSafeSSHDirectives(lines []string) map[string]string {
	values := map[string]string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.ToLower(fields[0])
		if _, allowed := safeSSHDirectives[key]; !allowed {
			continue
		}
		values[key] = fields[1]
	}
	return values
}

func sshChecks(data map[string]any) []protocol.Check {
	directives, _ := data["effective_directives"].(map[string]string)
	if directives == nil {
		directives, _ = data["configured_directives"].(map[string]string)
	}
	checks := []protocol.Check{}
	if strings.EqualFold(directives["permitrootlogin"], "yes") {
		checks = append(checks, protocol.Check{
			ID: "ssh.root-login", Status: protocol.StatusWarning,
			Summary: "SSH root login is enabled",
		})
	}
	if strings.EqualFold(directives["passwordauthentication"], "yes") {
		checks = append(checks, protocol.Check{
			ID: "ssh.password-authentication", Status: protocol.StatusWarning,
			Summary: "SSH password authentication is enabled",
		})
	}
	if len(checks) == 0 && len(directives) > 0 {
		checks = append(checks, protocol.Check{
			ID: "ssh.posture", Status: protocol.StatusPass,
			Summary: "No enabled high-risk SSH authentication setting was detected",
		})
	}
	return checks
}
