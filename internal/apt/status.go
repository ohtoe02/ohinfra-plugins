package apt

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const dpkgStatusLimit = 32 << 20

func collectStatus(
	local probe.Local,
) (map[string]any, []protocol.Check, []protocol.StructuredError, error) {
	encoded, err := local.Read("var/lib/dpkg/status", dpkgStatusLimit)
	if err != nil {
		return nil, nil, nil, err
	}
	installed, nonInstalled := parseDpkgStatus(encoded)
	pending, pendingErr := countRegularFiles(local, "var/lib/dpkg/updates", nil, 1024)

	status := protocol.StatusPass
	summary := "The local dpkg database is consistent"
	if nonInstalled > 0 || pending > 0 {
		status = protocol.StatusWarning
		summary = "The local dpkg database contains incomplete state"
	}
	checks := []protocol.Check{{
		ID: "apt:dpkg-status", Status: status, Summary: summary,
		Details: map[string]any{
			"installed_packages":     installed,
			"non_installed_records":  nonInstalled,
			"pending_update_records": pending,
		},
	}}
	structuredErrors := []protocol.StructuredError{}
	if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
		structuredErrors = append(structuredErrors, protocol.StructuredError{
			Kind: protocol.ErrorGeneral, Code: "dpkg_updates_unavailable",
			Message: "Unable to inspect local dpkg update records",
		})
	}
	return map[string]any{
		"installed_packages":     installed,
		"non_installed_records":  nonInstalled,
		"pending_update_records": pending,
	}, checks, structuredErrors, nil
}

func parseDpkgStatus(encoded []byte) (int, int) {
	installed := 0
	nonInstalled := 0
	scanner := bufio.NewScanner(bytes.NewReader(encoded))
	scanner.Buffer(make([]byte, 1024), dpkgStatusLimit)
	hasPackage := false
	state := ""
	flush := func() {
		if !hasPackage {
			return
		}
		if state == "install ok installed" {
			installed++
		} else {
			nonInstalled++
		}
		hasPackage = false
		state = ""
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if value, ok := strings.CutPrefix(line, "Package:"); ok {
			hasPackage = strings.TrimSpace(value) != ""
		}
		if value, ok := strings.CutPrefix(line, "Status:"); ok {
			state = strings.TrimSpace(value)
		}
	}
	flush()
	return installed, nonInstalled
}
