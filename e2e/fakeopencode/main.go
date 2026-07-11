// fakeopencode is a mock `opencode` CLI binary for E2E testing. It mirrors
// fakeclaude's env-driven conventions but speaks opencode's
// `run --format json` stream shape (see
// internal/harness/opencode/testdata/README.md for the captured
// real-world fixtures this mirrors).
//
// Behavior is controlled via environment variables:
//
//	FAKEOPENCODE_SCENARIO: success (default), truncated, error
//	FAKEOPENCODE_ARGLOG:   path to write received args as JSON
//	FAKEOPENCODE_ENVLOG:   path to write os.Environ() as JSON
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

const defaultSessionID = "ses_fake000000000000000000001"

func main() {
	if argLog := os.Getenv("FAKEOPENCODE_ARGLOG"); argLog != "" {
		data, err := json.Marshal(os.Args[1:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "fakeopencode: failed to marshal args: %v\n", err)
			os.Exit(2)
		}
		if err := os.WriteFile(argLog, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "fakeopencode: failed to write arg log: %v\n", err)
			os.Exit(2)
		}
	}

	if envLog := os.Getenv("FAKEOPENCODE_ENVLOG"); envLog != "" {
		data, err := json.Marshal(os.Environ())
		if err != nil {
			fmt.Fprintf(os.Stderr, "fakeopencode: failed to marshal env: %v\n", err)
			os.Exit(2)
		}
		if err := os.WriteFile(envLog, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "fakeopencode: failed to write env log: %v\n", err)
			os.Exit(2)
		}
	}

	for _, arg := range os.Args[1:] {
		if arg == "--version" {
			fmt.Println("1.17.7-fake")
			os.Exit(0)
		}
	}

	sessionID := defaultSessionID
	if id := sessionFlag(os.Args[1:]); id != "" {
		sessionID = id
	}

	scenario := os.Getenv("FAKEOPENCODE_SCENARIO")
	if scenario == "" {
		scenario = "success"
	}

	switch scenario {
	case "success":
		emitStepStart(sessionID)
		emitText(sessionID, "fake opencode done")
		emitStepFinish(sessionID)
		os.Exit(0)
	case "truncated":
		emitStepStart(sessionID)
		emitText(sessionID, "fake opencode done")
		os.Exit(0)
	case "error":
		fmt.Println("[00:00:00.000] ERROR (#fake): simulated failure")
		fmt.Printf(`{"type":"error","sessionID":%q,"error":{"name":"UnknownError","data":{"message":"fake opencode error"}}}`+"\n", sessionID)
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "fakeopencode: unknown scenario %q\n", scenario)
		os.Exit(2)
	}
}

// sessionFlag scans argv for a `-s <id>` pair and returns the id, or "" if
// no session flag is present.
func sessionFlag(args []string) string {
	for i, a := range args {
		if a == "-s" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func emitStepStart(sessionID string) {
	fmt.Printf(`{"type":"step_start","sessionID":%q,"part":{"type":"step-start","sessionID":%q}}`+"\n", sessionID, sessionID)
}

func emitText(sessionID, text string) {
	fmt.Printf(`{"type":"text","sessionID":%q,"part":{"type":"text","sessionID":%q,"text":%q}}`+"\n", sessionID, sessionID, text)
}

func emitStepFinish(sessionID string) {
	fmt.Printf(`{"type":"step_finish","sessionID":%q,"part":{"type":"step-finish","sessionID":%q,"reason":"stop"}}`+"\n", sessionID, sessionID)
}
