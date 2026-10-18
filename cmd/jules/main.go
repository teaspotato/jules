// Command jules is a terminal client for Google Jules.
package main

import (
	"os"

	"github.com/teaspotato/jules/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
