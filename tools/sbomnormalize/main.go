package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("sbomnormalize", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	createdText := flags.String("created", "", "stable RFC3339 creation time")
	namespace := flags.String("namespace", "", "stable SPDX document namespace")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *createdText == "" || *namespace == "" {
		return errors.New("sbomnormalize requires --created and --namespace")
	}
	created, err := time.Parse(time.RFC3339, *createdText)
	if err != nil {
		return fmt.Errorf("parse created timestamp: %w", err)
	}
	encoded, err := io.ReadAll(io.LimitReader(input, 32<<20+1))
	if err != nil {
		return err
	}
	if len(encoded) > 32<<20 {
		return errors.New("SPDX input exceeds 32 MiB")
	}
	normalized, err := normalizeSPDX(encoded, created, *namespace)
	if err != nil {
		return err
	}
	_, err = output.Write(normalized)
	return err
}

func normalizeSPDX(encoded []byte, created time.Time, namespace string) ([]byte, error) {
	if created.IsZero() {
		return nil, errors.New("SPDX creation time must not be zero")
	}
	parsedNamespace, err := url.Parse(namespace)
	if err != nil || parsedNamespace.Scheme != "https" || parsedNamespace.Host == "" ||
		parsedNamespace.User != nil || parsedNamespace.RawQuery != "" ||
		parsedNamespace.Fragment != "" {
		return nil, errors.New(
			"SPDX document namespace must use credential-free HTTPS without query or fragment",
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode SPDX JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode SPDX JSON: multiple JSON documents")
		}
		return nil, fmt.Errorf("decode SPDX JSON: %w", err)
	}
	if version, ok := document["spdxVersion"].(string); !ok || version != "SPDX-2.3" {
		return nil, errors.New("SPDX document must declare spdxVersion 2.3")
	}
	if current, ok := document["documentNamespace"].(string); !ok || strings.TrimSpace(current) == "" {
		return nil, errors.New("SPDX documentNamespace is required")
	}
	creationInfo, ok := document["creationInfo"].(map[string]any)
	if !ok {
		return nil, errors.New("SPDX creationInfo object is required")
	}
	if current, ok := creationInfo["created"].(string); !ok || strings.TrimSpace(current) == "" {
		return nil, errors.New("SPDX creationInfo.created is required")
	}
	document["documentNamespace"] = namespace
	creationInfo["created"] = created.UTC().Format(time.RFC3339)
	normalized, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(normalized, '\n'), nil
}
