package main

import (
	"os"

	"github.com/blackpaw-studio/leo/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
