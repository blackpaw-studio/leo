// fakeclaude is a mock claude CLI binary for E2E testing.
// Behavior is controlled via environment variables:
//
//	FAKECLAUDE_SCENARIO: success (default), error, timeout
//	FAKECLAUDE_ARGLOG:   path to write received args as JSON
//	FAKECLAUDE_ENVLOG:   path to write os.Environ() as JSON (verifies env passthrough)
//
// Any invocation without a `-p` flag enters an interactive REPL mode instead
// of the one-shot scenario switch — mirroring real claude, which only runs
// one-shot when given `-p`; without it, it starts an interactive session and
// stays resident. This is what makes fakeclaude usable as the spawned binary
// for agent/session tmux tests (leo never passes `-p` when launching an
// agent). The REPL accepts two flags for finer control, both optional:
//
//	--transcript-path <path>  append JSONL transcript events here
//	--resume <id>             echo "resumed: <id>" on first line of output
//
// --interactive is still accepted (and implied) for backward compatibility
// with callers that passed it explicitly.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	// Args/env logging happens for every invocation so existing e2e tests
	// can verify how the binary was launched, including the interactive one.
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

	if envLog := os.Getenv("FAKECLAUDE_ENVLOG"); envLog != "" {
		data, err := json.Marshal(os.Environ())
		if err != nil {
			fmt.Fprintf(os.Stderr, "fakeclaude: failed to marshal env: %v\n", err)
			os.Exit(2)
		}
		if err := os.WriteFile(envLog, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "fakeclaude: failed to write env log: %v\n", err)
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

	// Interactive mode is now the default whenever no `-p` flag is present —
	// matching real claude's actual behavior (no `-p` means an interactive
	// session, not a one-shot run). --interactive is still accepted as an
	// explicit (now redundant) marker for backward compatibility. We parse a
	// small flag set scoped to the interactive surface so the existing -p
	// scenario path is untouched. ContinueOnError + ignoring unknown flags
	// keeps us tolerant of any other args the caller threads through.
	if !hasFlag(os.Args[1:], "-p") {
		interactiveFlags := flag.NewFlagSet("fakeclaude-interactive", flag.ContinueOnError)
		interactiveFlags.SetOutput(&bytes.Buffer{}) // swallow flag-parsing noise
		// Registered so Parse tolerates an explicit --interactive appearing
		// anywhere in argv, even though it's no longer load-bearing (absence
		// of -p is now sufficient on its own).
		interactiveFlags.Bool("interactive", false, "interactive mode marker (accepted, ignored)")
		transcriptPathFlag := interactiveFlags.String("transcript-path", "", "JSONL transcript path")
		resumeFlag := interactiveFlags.String("resume", "", "resume session id")

		// Pre-filter args down to the flags we care about so unrelated flags
		// (e.g. --model, --append-system-prompt) don't trip the parser.
		filtered := filterKnownFlags(os.Args[1:], []string{"interactive", "transcript-path", "resume"})
		_ = interactiveFlags.Parse(filtered)

		runInteractive(*transcriptPathFlag, *resumeFlag)
		return
	}

	scenario := os.Getenv("FAKECLAUDE_SCENARIO")
	if scenario == "" {
		scenario = "success"
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

// hasFlag reports whether name (e.g. "--interactive") appears in args.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

// filterKnownFlags returns only the args that correspond to the supplied
// flag names (bare or `=value` form), plus the value tokens that follow a
// bare flag. Unknown flags and their values are dropped. This lets us run
// flag.Parse on a permissive call-site without erroring on extra args.
func filterKnownFlags(args []string, known []string) []string {
	knownSet := make(map[string]struct{}, len(known))
	for _, k := range known {
		knownSet[k] = struct{}{}
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name := strings.TrimLeft(a, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
		}
		if _, ok := knownSet[name]; !ok {
			// Skip the value too if this flag takes one and isn't = form.
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
			continue
		}
		out = append(out, a)
		// Carry the value token through if it was supplied separately.
		if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			out = append(out, args[i+1])
			i++
		}
	}
	return out
}

// runInteractive reads stdin line-by-line and echoes a deterministic
// FAKE-REPLY for each blank-line-terminated submission. JSONL transcript
// events are appended to transcriptPath when non-empty.
func runInteractive(transcriptPath, resumeID string) {
	if resumeID != "" {
		fmt.Printf("resumed: %s\n", resumeID)
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var buf bytes.Buffer
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if buf.Len() == 0 {
				continue
			}
			emitSubmission(transcriptPath, &buf)
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	// Drain any trailing buffer on EOF.
	if buf.Len() > 0 {
		emitSubmission(transcriptPath, &buf)
	}
}

func emitSubmission(transcriptPath string, buf *bytes.Buffer) {
	submission := strings.TrimSpace(buf.String())
	buf.Reset()
	writeTranscriptUser(transcriptPath, submission)
	reply := "FAKE-REPLY: " + truncate(submission, 80)
	writeTranscriptAssistant(transcriptPath, reply)
	fmt.Println(reply)
}

func writeTranscriptUser(path, text string) {
	if path == "" {
		return
	}
	ev := map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	}
	appendJSONL(path, ev)
}

func writeTranscriptAssistant(path, text string) {
	if path == "" {
		return
	}
	ev := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	}
	appendJSONL(path, ev)
}

func appendJSONL(path string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(raw)
	_, _ = f.Write([]byte("\n"))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
