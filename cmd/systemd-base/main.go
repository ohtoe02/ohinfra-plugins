package main

import (
	"os"

	"github.com/ohtoe02/ohinfra-plugins/internal/protocol"
	"github.com/ohtoe02/ohinfra-plugins/internal/systemd"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	definition := systemd.NewDefinition(systemd.Options{
		Version: version, Commit: commit, BuildDate: buildDate,
	})
	os.Exit(protocol.Serve(definition, os.Args, os.Stdin, os.Stdout, os.Stderr))
}
