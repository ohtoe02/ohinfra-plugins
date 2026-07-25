package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 4 ||
		os.Args[1] != "status" ||
		os.Args[2] != "--format" ||
		os.Args[3] != "json" {
		_, _ = fmt.Fprintln(os.Stderr, "readiness entitlement fixture: unsupported invocation")
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(os.Stdout, `{"attached":true}`)
}
