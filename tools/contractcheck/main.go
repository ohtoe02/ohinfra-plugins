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
		err = contractbundle.Verify(
			filepath.Join(root, "contracts", "protocol-v1"),
			filepath.Join(root, "contracts", "protocol-v1.lock.json"),
		)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
