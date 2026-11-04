// Command stremio-cliuwu is a terminal Stremio client. The program itself lives
// in internal/ui; this file only carries the build-time version.
package main

import (
	"os"

	"stremio-cliuwu/internal/ui"
)

// version is overwritten at build time via
// -ldflags="-X main.version=…". "dev" is what an untagged local build
// reports.
var version = "dev"

func main() {
	ui.Run(os.Args[1:], version)
}
