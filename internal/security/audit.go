package security

import (
	"context"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func executeAudit(ctx context.Context, options Options) (protocol.Result, error) {
	sections := []struct {
		Name    string
		Collect func(context.Context, Options) collection
	}{
		{Name: "accounts", Collect: collectAccounts},
		{Name: "ssh", Collect: collectSSH},
		{Name: "firewall", Collect: collectFirewall},
	}
	combined := collection{
		Data: map[string]any{},
	}
	for _, section := range sections {
		collected := section.Collect(ctx, options)
		if collected.Fatal != nil {
			return protocol.Result{}, collected.Fatal
		}
		combined.Data[section.Name] = collected.Data
		combined.Checks = append(combined.Checks, collected.Checks...)
		combined.Errors = append(combined.Errors, collected.Errors...)
		if collected.Usable {
			combined.Usable = true
		}
	}
	if len(combined.Errors) > 0 {
		for index := range combined.Checks {
			if combined.Checks[index].Status == protocol.StatusWarning {
				combined.Checks[index].Status = protocol.StatusPartial
			}
		}
	}
	return buildResult("security audit", options, combined), nil
}
