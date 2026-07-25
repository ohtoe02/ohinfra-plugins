package main

import (
	"os"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/serversetup"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	os.Exit(protocol.Serve(buildDefinition(), os.Args, os.Stdin, os.Stdout, os.Stderr))
}

func buildDefinition() protocol.Definition {
	return serversetup.NewDefinition(serversetup.Options{
		Version: version, Commit: commit, BuildDate: buildDate,
	})
}
