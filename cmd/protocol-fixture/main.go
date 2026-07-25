package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const desiredState = "configured\n"

func main() {
	definition := protocol.NewDefinition(
		protocol.DefinitionSpec{
			Name: "protocol-fixture", Version: "1.0.0",
			Description: "Trusted real-binary protocol conformance fixture.",
		},
		protocol.Diagnostic(
			protocol.CommandSpec{
				Path: []string{"fixture", "output"}, Use: "output",
				Short: "Emit bounded fixture output",
				Flags: []protocol.Flag{{Name: "bytes", Type: "int", Description: "Payload size", Default: 0}},
			},
			output,
		),
		protocol.Mutation(
			protocol.CommandSpec{
				Path: []string{"fixture", "apply"}, Use: "apply <state>",
				Short:     "Apply fixture state",
				Arguments: []protocol.Argument{{Name: "state", Description: "Fixture state file", Required: true}},
				Flags:     []protocol.Flag{},
			},
			protocol.CategoryOperational,
			protocol.Risk{RequiresConfirmation: true},
			plan,
			apply,
		),
	)
	os.Exit(protocol.Serve(definition, os.Args, os.Stdin, os.Stdout, os.Stderr))
}

func output(_ context.Context, invocation protocol.Invocation) (protocol.Result, error) {
	size := 0
	if raw, present := invocation.Options["bytes"]; present {
		switch value := raw.(type) {
		case int:
			size = value
		case float64:
			size = int(value)
		}
	}
	if size < 0 || size > 2<<20 {
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("fixture output size must be within 0..2097152"),
		}
	}
	return protocol.Normalize(protocol.Result{
		Command:   "fixture output",
		Status:    protocol.StatusPass,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Tool: protocol.Tool{
			Name: "protocol-fixture", Version: "1.0.0",
			GoVersion: runtime.Version(), Architecture: runtime.GOARCH,
		},
		Data: map[string]any{"payload": strings.Repeat("x", size)},
	}), nil
}

func plan(ctx context.Context, invocation protocol.Invocation) (protocol.Plan, error) {
	select {
	case <-ctx.Done():
		return protocol.Plan{}, ctx.Err()
	default:
	}
	statePath, err := fixturePath(invocation)
	if err != nil {
		return protocol.Plan{}, err
	}
	current, err := os.ReadFile(statePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return protocol.Plan{}, err
	}
	changes := []protocol.Change{}
	if string(current) != desiredState {
		changes = append(changes, protocol.Change{
			Object: statePath, Action: "write fixture state", Status: "planned",
		})
	}
	return protocol.Plan{
		CommandID: "fixture.apply",
		Summary:   "Apply the trusted protocol fixture state",
		Changes:   changes,
		Risks:     []string{"fixture state is written to the requested test path"},
	}, nil
}

func apply(
	ctx context.Context,
	invocation protocol.Invocation,
	approved protocol.Plan,
) (protocol.Result, error) {
	started := time.Now()
	statePath, err := fixturePath(invocation)
	if err != nil {
		return protocol.Result{}, err
	}
	if len(approved.Changes) != 0 {
		if err := writeState(ctx, statePath); err != nil {
			return protocol.Result{}, err
		}
	}
	changes := []protocol.Change{}
	if len(approved.Changes) != 0 {
		changes = append(changes, protocol.Change{
			Object: statePath, Action: "write fixture state", Status: "completed",
		})
	}
	return protocol.Normalize(protocol.Result{
		Command:    "fixture apply",
		Status:     protocol.StatusPass,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		DurationMS: time.Since(started).Milliseconds(),
		Tool: protocol.Tool{
			Name: "protocol-fixture", Version: "1.0.0",
			GoVersion: runtime.Version(), Architecture: runtime.GOARCH,
		},
		Data:    map[string]any{"changed": len(changes) != 0},
		Changes: changes,
	}), nil
}

func fixturePath(invocation protocol.Invocation) (string, error) {
	if len(invocation.Arguments) != 1 || invocation.Arguments[0] == "" {
		return "", protocol.ExitError{Code: protocol.ExitArguments, Err: errors.New("fixture requires one state path")}
	}
	absolute, err := filepath.Abs(invocation.Arguments[0])
	if err != nil {
		return "", protocol.ExitError{Code: protocol.ExitArguments, Err: errors.New("invalid fixture state path")}
	}
	if info, err := os.Lstat(absolute); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", protocol.ExitError{
				Code: protocol.ExitArguments, Err: errors.New("fixture state must be a regular non-symlink file"),
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return absolute, nil
}

func writeState(ctx context.Context, statePath string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	file, err := os.CreateTemp(filepath.Dir(statePath), ".protocol-fixture-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(tempPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.WriteString(desiredState); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, statePath); err != nil {
		return err
	}
	remove = false
	return nil
}
