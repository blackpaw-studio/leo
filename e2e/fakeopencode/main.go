// fakeopencode is a mock `opencode` CLI binary for E2E testing. It mirrors
// fakeclaude's env-driven conventions but speaks opencode's
// `run --format json` stream shape (see
// internal/harness/opencode/testdata/README.md for the captured
// real-world fixtures this mirrors), plus a minimal `serve` mode and a
// `session list` mode used by the ServerDriver's session-id fallback.
//
// Behavior is controlled via environment variables:
//
//	FAKEOPENCODE_SCENARIO: success (default), truncated, error, no_session_id
//	FAKEOPENCODE_ARGLOG:   path to write received args as JSON
//	FAKEOPENCODE_ENVLOG:   path to write os.Environ() as JSON
//
// Subcommand dispatch (argv[0]):
//
//	serve            starts a real minimal HTTP listener on the port passed
//	                 via --port, serving GET /global/health, and blocks
//	                 until signaled (SIGTERM/SIGINT) so the "server" stays
//	                 up for the driver's health probe / Inject calls.
//	session list     emits a one-entry JSON array whose "directory" is the
//	                 process's cwd (matching the ServerDriver's --dir), for
//	                 the session-id fallback path.
//	run (default)    the existing lossy attach-stream emitter.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const defaultSessionID = "ses_fake000000000000000000001"

func main() {
	logArgsAndEnv()

	for _, arg := range os.Args[1:] {
		if arg == "--version" {
			fmt.Println("1.17.7-fake")
			os.Exit(0)
		}
	}

	switch {
	case len(os.Args) > 1 && os.Args[1] == "serve":
		runServe(os.Args[2:])
		return
	case len(os.Args) > 2 && os.Args[1] == "session" && os.Args[2] == "list":
		runSessionList()
		return
	default:
		runAttach(os.Args[1:])
	}
}

// logArgsAndEnv preserves the ARGLOG/ENVLOG contract in every mode.
func logArgsAndEnv() {
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
}

// runServe starts a real HTTP listener on the --port value and blocks until
// signaled, so ServerDriver.Start's health probe (and any Inject calls
// racing against it) see a live server.
func runServe(args []string) {
	port := flagValue(args, "--port")
	if port == "" {
		fmt.Fprintln(os.Stderr, "fakeopencode: serve requires --port")
		os.Exit(2)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"healthy":true,"version":"fake"}`)
	})
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "fakeopencode: serve failed: %v\n", err)
			os.Exit(1)
		}
	case <-sigCh:
		_ = srv.Close()
	}
}

// runSessionList emits a one-entry array whose directory is the current
// working directory (mirroring opencode's own os.Getwd()-derived report),
// so ServerDriver's dir-filter fallback resolves against the same --dir the
// caller passed via cmd.Dir.
func runSessionList() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakeopencode: session list: getwd: %v\n", err)
		os.Exit(2)
	}
	sessionID := defaultSessionID
	if id := os.Getenv("FAKEOPENCODE_SESSION_LIST_ID"); id != "" {
		sessionID = id
	}
	fmt.Printf(`[{"id":%q,"created":1,"directory":%q}]`+"\n", sessionID, cwd)
	os.Exit(0)
}

// runAttach is the existing `run --attach ...` lossy stream emitter.
func runAttach(args []string) {
	sessionID := defaultSessionID
	if id := sessionFlag(args); id != "" {
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
	case "no_session_id":
		// Simulates the lossy attach stream never surfacing a sessionID
		// (e.g. dropped step_start), forcing the ServerDriver's `session
		// list` fallback to be exercised.
		emitTextNoSession("fake opencode done")
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

// flagValue returns the value following a flag in argv, or "" if absent.
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// sessionFlag scans argv for a `-s <id>` pair and returns the id, or "" if
// no session flag is present.
func sessionFlag(args []string) string {
	return flagValue(args, "-s")
}

func emitStepStart(sessionID string) {
	fmt.Printf(`{"type":"step_start","sessionID":%q,"part":{"type":"step-start","sessionID":%q}}`+"\n", sessionID, sessionID)
}

func emitText(sessionID, text string) {
	fmt.Printf(`{"type":"text","sessionID":%q,"part":{"type":"text","sessionID":%q,"text":%q}}`+"\n", sessionID, sessionID, text)
}

// emitTextNoSession emits a text event carrying no sessionID at all,
// simulating a lossy attach stream that never surfaced one.
func emitTextNoSession(text string) {
	fmt.Printf(`{"type":"text","part":{"type":"text","text":%q}}`+"\n", text)
}

func emitStepFinish(sessionID string) {
	fmt.Printf(`{"type":"step_finish","sessionID":%q,"part":{"type":"step-finish","sessionID":%q,"reason":"stop"}}`+"\n", sessionID, sessionID)
}
