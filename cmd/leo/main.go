package main

import (
	"os"

	"github.com/blackpaw-studio/leo/internal/cli"
	"github.com/blackpaw-studio/leo/internal/prompt"
)

func main() {
	if err := cli.Execute(); err != nil {
		prompt.Err.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}
