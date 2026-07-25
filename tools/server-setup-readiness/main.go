package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/serversetupreadiness"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (returnErr error) {
	if len(os.Args) == 3 && os.Args[1] == "binary-smoke" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		report, err := serversetupreadiness.RunBinarySmoke(ctx, os.Args[2])
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	if len(os.Args) == 5 && os.Args[1] == "verify-report" {
		content, err := os.ReadFile(os.Args[2])
		if err != nil {
			return err
		}
		if len(content) == 0 || len(content) > 1<<20 {
			return errors.New("readiness report has an invalid size")
		}
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.DisallowUnknownFields()
		var report serversetupreadiness.Report
		if err := decoder.Decode(&report); err != nil {
			return err
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return errors.New("readiness report has trailing JSON")
		}
		return serversetupreadiness.ValidateReport(report, os.Args[3], os.Args[4])
	}
	if len(os.Args) != 1 {
		return errors.New(
			"usage: server-setup-readiness [binary-smoke <plugin> | " +
				"verify-report <path> <id> <version>]",
		)
	}
	osRelease, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("/tmp", "ohtools-server-setup-readiness-")
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, os.RemoveAll(root))
	}()
	report, err := serversetupreadiness.Run(context.Background(), osRelease, root)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
