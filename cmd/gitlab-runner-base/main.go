package main

import (
	"os"

	"github.com/ohtoe02/ohtools-plugins/internal/gitlabrunner"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	definition := gitlabrunner.NewDefinition(gitlabrunner.Options{
		Version: version, Commit: commit, BuildDate: buildDate,
	})
	os.Exit(protocol.Serve(definition, os.Args, os.Stdin, os.Stdout, os.Stderr))
}
