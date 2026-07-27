package main

import (
	"os"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	securityplugin "github.com/ohtoe02/ohtools-plugins/internal/security"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	definition := securityplugin.NewDefinition(securityplugin.Options{
		Version: version, Commit: commit, BuildDate: buildDate,
	})
	os.Exit(protocol.Serve(definition, os.Args, os.Stdin, os.Stdout, os.Stderr))
}
