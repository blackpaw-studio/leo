package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

var markerRe = regexp.MustCompile(`<!-- leo:invocation=([0-9a-f]{32}) -->`)

func extractInvocationMarker(s string) string {
	m := markerRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

type transcriptEvent struct {
	Type    string `json:"type"`
	Message struct {
		// Content is either a bare string (plain text turns, the common
		// case for leo-injected prompts) or an array of typed blocks
		// (tool_use / tool_result / typed text). Decoded lazily by
		// concatText so a string-shaped turn isn't dropped on unmarshal.
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// readLastTurn scans the transcript JSONL and returns the invocation id from
// the most recent user message that carries a marker, plus the concatenated
// text of any assistant messages following it. Returns ("", "", nil) if no
// leo-marker user message is found (human turn).
func readLastTurn(path string) (string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	var events []transcriptEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var ev transcriptEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue // skip malformed lines
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		return "", "", err
	}
	lastUserIdx := -1
	var invID string
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != "user" {
			continue
		}
		text := concatText(events[i])
		if id := extractInvocationMarker(text); id != "" {
			lastUserIdx = i
			invID = id
			break
		}
	}
	if lastUserIdx < 0 {
		return "", "", nil
	}
	var final strings.Builder
	for j := lastUserIdx + 1; j < len(events); j++ {
		if events[j].Type != "assistant" {
			continue
		}
		if final.Len() > 0 {
			final.WriteString("\n")
		}
		final.WriteString(concatText(events[j]))
	}
	return invID, final.String(), nil
}

func concatText(ev transcriptEvent) string {
	raw := ev.Message.Content
	if len(raw) == 0 {
		return ""
	}
	// String form: {"content":"...text..."}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return ""
	}
	// Array form: {"content":[{"type":"text","text":"..."}, ...]}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

type hookEnvelope struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	HookEventName  string `json:"hook_event_name"`
	CWD            string `json:"cwd"`
}

func newInternalCmd() *cobra.Command {
	parent := &cobra.Command{
		Use:    "internal",
		Hidden: true,
		Short:  "Internal subcommands (not for end users)",
	}
	parent.AddCommand(newInternalTaskReportCmd())
	return parent
}

func newInternalTaskReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "task-report",
		Hidden: true,
		Short:  "Report a Claude Code Stop hook to the leo daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			var env hookEnvelope
			if err := json.NewDecoder(os.Stdin).Decode(&env); err != nil {
				fmt.Fprintf(os.Stderr, "leo task-report: bad stdin: %v\n", err)
				return nil // never block claude
			}
			if env.TranscriptPath == "" {
				return nil
			}
			invID, final, err := readLastTurn(env.TranscriptPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "leo task-report: %v\n", err)
				return nil
			}
			if invID == "" {
				return nil // human turn, ignore
			}
			workDir := config.DefaultHome()
			// Bound the call: a wedged daemon socket must not freeze the
			// claude Stop hook (which gates claude's process exit).
			rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := daemon.ReportTask(rctx, workDir, invID, env.SessionID, final, os.Getenv("LEO_SESSION_NAME")); err != nil {
				fmt.Fprintf(os.Stderr, "leo task-report: report: %v\n", err)
			}
			return nil
		},
	}
}
