// fakeclaude is a mock claude CLI binary for E2E testing.
// Behavior is controlled via environment variables:
//
//	FAKECLAUDE_SCENARIO: success (default), error, timeout
//	FAKECLAUDE_ARGLOG:   path to write received args as JSON
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	scenario := os.Getenv("FAKECLAUDE_SCENARIO")
	if scenario == "" {
		scenario = "success"
	}

	// Log args if requested
	if argLog := os.Getenv("FAKECLAUDE_ARGLOG"); argLog != "" {
		data, err := json.Marshal(os.Args[1:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "fakeclaude: failed to marshal args: %v\n", err)
			os.Exit(2)
		}
		if err := os.WriteFile(argLog, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "fakeclaude: failed to write arg log: %v\n", err)
			os.Exit(2)
		}
	}

	// Handle --version flag (used by prereq checks)
	for _, arg := range os.Args[1:] {
		if arg == "--version" {
			fmt.Println("claude 1.0.0-fake")
			os.Exit(0)
		}
	}

	switch scenario {
	case "success":
		fmt.Println("Task completed successfully.")
		os.Exit(0)
	case "error":
		fmt.Fprintln(os.Stderr, "fakeclaude: simulated error")
		os.Exit(1)
	case "timeout":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "fakeclaude: unknown scenario %q\n", scenario)
		os.Exit(2)
	}
}
