// Command computer-use serves two desktop tools over the Model Context
// Protocol: AX, app-scoped accessibility observation and action, and GUI,
// full-desktop screenshots and absolute-pixel input. On Linux it drives the
// signed-in desktop session through an embedded Python AT-SPI2 and X11
// provider.
//
// Run "computer-use mcp" to start the stdio server, or "computer-use call"
// to invoke a tool once from the command line.
package main

import (
	"errors"
	"fmt"
	"os"
)

// version is set at release build time from the repository VERSION file.
var version = "dev"

func main() {
	serverVersion = version
	err := runCLI(os.Args[1:], os.Stdout)
	shutdownPlatformRuntime()
	if err != nil {
		var exit *exitError
		if !errors.As(err, &exit) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(exitStatusOf(err))
	}
}
