package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ohtoe02/ohtools-plugins/internal/contractbundle"
)

func main() {
	root, err := os.Getwd()
	if err == nil {
		bundle := filepath.Join(root, "contracts", "protocol-v1")
		lock := filepath.Join(root, "contracts", "protocol-v1.lock.json")
		switch {
		case len(os.Args) == 1:
			err = contractbundle.Verify(bundle, lock)
		case len(os.Args) == 3 && os.Args[1] == "--canonical":
			err = contractbundle.VerifyAgainstCanonical(bundle, lock, os.Args[2])
		default:
			err = fmt.Errorf("usage: contractcheck [--canonical <contract-directory>]")
		}
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
