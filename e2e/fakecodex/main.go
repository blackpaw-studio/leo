// fakecodex is a mock `codex` CLI binary for E2E testing. It mirrors
// fakeclaude's env-driven conventions but speaks codex's `exec --json`
// stream shape (see internal/harness/codex/testdata/README.md for the
// captured real-world fixtures this mirrors).
//
// Behavior is controlled via environment variables:
//
//	FAKECODEX_SCENARIO: success (default), error, stale_resume
//	FAKECODEX_ARGLOG:   path to write received args as JSON
//	FAKECODEX_ENVLOG:   path to write os.Environ() as JSON
//
// stale_resume: a call whose argv contains "resume" exits 1 with EMPTY
// stdout (the "no rollout found" shape TurnDriver treats as a stale thread);
// a fresh call (no "resume") succeeds normally. Mirrors the real codex CLI's
// behavior when asked to resume a thread whose rollout file has vanished.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	if argLog := os.Getenv("FAKECODEX_ARGLOG"); argLog != "" {
		data, err := json.Marshal(os.Args[1:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "fakecodex: failed to marshal args: %v\n", err)
			os.Exit(2)
		}
		if err := os.WriteFile(argLog, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "fakecodex: failed to write arg log: %v\n", err)
			os.Exit(2)
		}
	}

	if envLog := os.Getenv("FAKECODEX_ENVLOG"); envLog != "" {
		data, err := json.Marshal(os.Environ())
		if err != nil {
			fmt.Fprintf(os.Stderr, "fakecodex: failed to marshal env: %v\n", err)
			os.Exit(2)
		}
		if err := os.WriteFile(envLog, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "fakecodex: failed to write env log: %v\n", err)
			os.Exit(2)
		}
	}

	for _, arg := range os.Args[1:] {
		if arg == "--version" {
			fmt.Println("codex-cli 0.144.1-fake")
			os.Exit(0)
		}
	}

	threadID := "thread_fake_1"
	if id := resumeID(os.Args[1:]); id != "" {
		threadID = id
	}

	scenario := os.Getenv("FAKECODEX_SCENARIO")
	if scenario == "" {
		scenario = "success"
	}

	switch scenario {
	case "stale_resume":
		if resumeID(os.Args[1:]) != "" {
			// Stale thread: exit 1 with no stdout at all (the "no rollout
			// found" shape TurnDriver's stale-thread detection depends on).
			os.Exit(1)
		}
		fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n", threadID)
		fmt.Println(`{"type":"turn.started"}`)
		fmt.Println(`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"fake codex fresh after stale"}}`)
		fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`)
		os.Exit(0)
	case "success":
		fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n", threadID)
		fmt.Println(`{"type":"turn.started"}`)
		fmt.Println(`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"fake codex done"}}`)
		fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`)
		os.Exit(0)
	case "error":
		fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n", threadID)
		fmt.Println(`{"type":"error","message":"fake model_not_found error"}`)
		fmt.Println(`{"type":"turn.failed","error":{"message":"fake model_not_found error"}}`)
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "fakecodex: unknown scenario %q\n", scenario)
		os.Exit(2)
	}
}

// resumeID scans argv for a `resume <id>` pair and returns the id, or ""
// if no resume subcommand is present.
func resumeID(args []string) string {
	for i, a := range args {
		if a == "resume" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
