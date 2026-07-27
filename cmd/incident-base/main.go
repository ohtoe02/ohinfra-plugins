package main

import (
	"os"

	"github.com/ohtoe02/ohtools-plugins/internal/incident"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	definition := incident.NewDefinition(incident.Options{
		Version: version, Commit: commit, BuildDate: buildDate,
	})
	os.Exit(protocol.Serve(definition, os.Args, os.Stdin, os.Stdout, os.Stderr))
}
