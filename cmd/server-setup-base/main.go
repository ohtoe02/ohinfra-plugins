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
	definition := serversetup.NewDefinition(serversetup.Options{
		Version: version, Commit: commit, BuildDate: buildDate,
	})
	os.Exit(protocol.Serve(definition, os.Args, os.Stdin, os.Stdout, os.Stderr))
}
