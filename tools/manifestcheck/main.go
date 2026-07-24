package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ohtoe02/ohinfra-plugins/internal/protocol"
)

func main() {
	decoder := json.NewDecoder(os.Stdin)
	decoder.DisallowUnknownFields()
	var manifest protocol.Manifest
	if err := decoder.Decode(&manifest); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := protocol.ValidateManifest(manifest); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
