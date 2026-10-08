package main

import (
	"os"

	"github.com/blackpaw-studio/leo/internal/cli"
	"github.com/blackpaw-studio/leo/internal/prompt"
)

func main() {
	if err := cli.Execute(); err != nil {
		// An empty message means the failure was already reported (a remote
		// leo's own stderr), so only the exit status is left to pass on.
		if msg := err.Error(); msg != "" {
			prompt.Err.Fprintf(os.Stderr, "Error: %s\n", msg)
		}
		os.Exit(cli.ExitCode(err))
	}
}
