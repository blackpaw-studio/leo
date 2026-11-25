package opencode

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type pluginRequest struct {
	Via     string            `json:"via"`  // "socket" (the daemon's unix socket) or "tcp"
	Path    string            `json:"path"` // socket requests
	URL     string            `json:"url"`  // tcp requests
	Headers map[string]string `json:"headers"`
	Body    struct {
		Token   string            `json:"token"`
		PID     *int              `json:"pid"` // must never be sent: the daemon trusts only the kernel's peer pid
		Payload map[string]string `json:"payload"`
	} `json:"body"`
}

type pluginContextRun struct {
	Posts    []pluginRequest `json:"posts"`
	Contexts []pluginRequest `json:"contexts"`
	Timeouts []int           `json:"timeouts"` // the deadlines asked for on context requests
	Rejected []struct {
		Message int      `json:"message"`
		Missing []string `json:"missing"`
	} `json:"rejected"` // injected parts opencode would refuse to save
	Parts [][]struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		Synthetic bool   `json:"synthetic"`
	} `json:"parts"`
}

func runContextScenario(t *testing.T, scenario string) pluginContextRun {
	t.Helper()
	out := runTurnPlugin(t, scenario, "LEO_ATTENTION_TOKEN=tok-1", "LEO_WEB_PORT=8399", "LEO_API_TOKEN=api-1", "LEO_DISPATCH_ID=")
	var run pluginContextRun
	if err := json.Unmarshal(out, &run); err != nil {
		t.Fatalf("harness output is not JSON: %v\n%s", err, out)
	}
	return run
}

// asked lists the event/source/session of every context request.
func (r pluginContextRun) asked() []string {
	var asked []string
	for _, c := range r.Contexts {
		p := c.Body.Payload
		asked = append(asked, p["hook_event_name"]+"/"+p["source"]+"/"+p["session_id"])
	}
	return asked
}

// injected is the text of the synthetic part message i ended with ("" when
// none was added).
func (r pluginContextRun) injected(t *testing.T, i int) string {
	t.Helper()
	var added []string
	for _, p := range r.Parts[i] {
		if p.Synthetic {
			if p.Type != "text" {
				t.Fatalf("message %d: synthetic part type %q, want text", i, p.Type)
			}
			added = append(added, p.Text)
		}
	}
	if len(added) > 1 {
		t.Fatalf("message %d: %d synthetic parts, want at most one: %q", i, len(added), added)
	}
	return strings.Join(added, "")
}

// The plugin tells a session its context the way claude's SessionStart and
// UserPromptSubmit hooks do: a synthesized SessionStart (startup) before the
// session's first message, then UserPromptSubmit on every message, the
// daemon's reply appended to the message as one synthetic text part.
func TestOpencodeTurnPluginInjectsContextFromTheDaemon(t *testing.T) {
	run := runContextScenario(t, "ctx-startup-then-prompts")
	wantAsked := []string{"SessionStart/startup/ses_1", "UserPromptSubmit//ses_1", "UserPromptSubmit//ses_1"}
	if got := run.asked(); !reflect.DeepEqual(got, wantAsked) {
		t.Fatalf("asked %q, want %q", got, wantAsked)
	}
	for i, c := range run.Contexts {
		if c.Via != "socket" || c.Path != "/api/agent/context" || c.Body.Token != "tok-1" {
			t.Errorf("request %d via %s path %q token=%q, want the daemon socket", i, c.Via, c.Path, c.Body.Token)
		}
	}
	if got, want := run.injected(t, 0), "ctx:SessionStart/startup\n\nctx:UserPromptSubmit"; got != want {
		t.Errorf("first message injected %q, want %q", got, want)
	}
	if got, want := run.injected(t, 1), "ctx:UserPromptSubmit"; got != want {
		t.Errorf("second message injected %q, want %q", got, want)
	}
	// The turn hook the daemon already receives carries the user's words only.
	if len(run.Posts) != 2 {
		t.Fatalf("posted %d hooks, want 2 (one per message)", len(run.Posts))
	}
	for i, post := range run.Posts {
		if post.Body.Payload["hook_event_name"] != "UserPromptSubmit" || post.Body.Payload["prompt"] != "hello" {
			t.Errorf("hook %d = %v, want UserPromptSubmit with the user's text alone", i, post.Body.Payload)
		}
	}
}

func TestOpencodeTurnPluginRestatesContextAfterACompaction(t *testing.T) {
	run := runContextScenario(t, "ctx-compaction")
	want := []string{
		"SessionStart/startup/ses_1", "UserPromptSubmit//ses_1",
		"SessionStart/compact/ses_1", "UserPromptSubmit//ses_1",
		"UserPromptSubmit//ses_1",
	}
	if got := run.asked(); !reflect.DeepEqual(got, want) {
		t.Fatalf("asked %q, want %q", got, want)
	}
	if got, w := run.injected(t, 1), "ctx:SessionStart/compact\n\nctx:UserPromptSubmit"; got != w {
		t.Errorf("message after the compaction injected %q, want %q", got, w)
	}
	if got, w := run.injected(t, 2), "ctx:UserPromptSubmit"; got != w {
		t.Errorf("a later message injected %q, want %q", got, w)
	}
}

func TestOpencodeTurnPluginStartsEverySessionOnce(t *testing.T) {
	run := runContextScenario(t, "ctx-two-sessions")
	want := []string{"SessionStart/startup/ses_1", "UserPromptSubmit//ses_1", "SessionStart/startup/ses_2", "UserPromptSubmit//ses_2"}
	if got := run.asked(); !reflect.DeepEqual(got, want) {
		t.Fatalf("asked %q, want %q", got, want)
	}
}

// A daemon with nothing to say, down, or erroring leaves the message
// untouched and the turn is still reported.
func TestOpencodeTurnPluginAddsNothingWithoutContext(t *testing.T) {
	for _, scenario := range []string{"ctx-empty", "ctx-daemon-down", "ctx-daemon-error"} {
		t.Run(scenario, func(t *testing.T) {
			run := runContextScenario(t, scenario)
			if got := run.injected(t, 0); got != "" {
				t.Errorf("injected %q, want nothing", got)
			}
			if len(run.Parts[0]) != 1 {
				t.Errorf("message parts = %+v, want the user's part alone", run.Parts[0])
			}
			if len(run.Posts) != 1 || run.Posts[0].Body.Payload["hook_event_name"] != "UserPromptSubmit" {
				t.Errorf("posts = %+v, want the UserPromptSubmit hook still reported", run.Posts)
			}
		})
	}
}

// The daemon decides whether a session may rebind from the process the kernel
// says connected, which only a unix-socket connection reveals: the plugin
// sends its context requests over LEO_SOCKET and claims no pid of its own.
func TestOpencodeTurnPluginAsksForContextOverTheSocketWithoutAPid(t *testing.T) {
	run := runContextScenario(t, "ctx-startup-then-prompts")
	if len(run.Contexts) == 0 {
		t.Fatal("no context requests")
	}
	for i, c := range run.Contexts {
		if c.Via != "socket" {
			t.Errorf("request %d went over %s, want the unix socket", i, c.Via)
		}
		if c.Body.PID != nil {
			t.Errorf("request %d claimed pid %d, want none", i, *c.Body.PID)
		}
	}
}

// With no socket to reach (not named, or nobody listening) the plugin still
// asks over TCP, where the daemon serves only a session already bound.
func TestOpencodeTurnPluginFallsBackToTCPWithoutAReachableSocket(t *testing.T) {
	for _, scenario := range []string{"ctx-tcp-only", "ctx-socket-missing"} {
		t.Run(scenario, func(t *testing.T) {
			run := runContextScenario(t, scenario)
			if got, want := run.asked(), []string{"SessionStart/startup/ses_1", "UserPromptSubmit//ses_1"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("asked %q, want %q", got, want)
			}
			for i, c := range run.Contexts {
				if c.Via != "tcp" || c.URL != "http://127.0.0.1:8399/api/agent/context" || c.Headers["Authorization"] != "Bearer api-1" || c.Body.PID != nil {
					t.Errorf("request %d = %+v, want a pid-less authenticated TCP request", i, c)
				}
			}
			if got := run.injected(t, 0); got == "" {
				t.Error("nothing injected over the TCP fallback")
			}
		})
	}
}

// One message waits at most one deadline for the daemon, however many
// requests it makes: a message that needs a SessionStart and a
// UserPromptSubmit shares one 3 s budget instead of owing two.
func TestOpencodeTurnPluginSharesOneDeadlinePerMessage(t *testing.T) {
	run := runContextScenario(t, "ctx-daemon-slow")
	if want := []int{3000}; !reflect.DeepEqual(run.Timeouts, want) {
		t.Errorf("deadlines asked for = %v, want exactly %v for the one message", run.Timeouts, want)
	}
	if got := run.asked(); !reflect.DeepEqual(got, []string{"SessionStart/startup/ses_1"}) {
		t.Errorf("asked %q, want only the SessionStart: its timeout used the message's budget, so no second request", got)
	}
	if got := run.injected(t, 0); got != "" {
		t.Errorf("injected %q from a daemon that never answered", got)
	}
	if len(run.Posts) != 1 {
		t.Errorf("posted %d hooks, want the turn still reported", len(run.Posts))
	}
}

func TestOpencodeTurnPluginStartsOneDeadlinePerMessage(t *testing.T) {
	run := runContextScenario(t, "ctx-startup-then-prompts")
	if want := []int{3000, 3000}; !reflect.DeepEqual(run.Timeouts, want) {
		t.Errorf("deadlines = %v, want one per message (%v)", run.Timeouts, want)
	}
}

// opencode saves every part a chat.message hook adds, and refuses (failing the
// whole turn) one without its own id, sessionID and messageID.
func TestOpencodeTurnPluginInjectedPartsCarryTheKeysOpencodeSaves(t *testing.T) {
	run := runContextScenario(t, "ctx-startup-then-prompts")
	if run.injected(t, 0) == "" || run.injected(t, 1) == "" {
		t.Fatal("nothing injected, so nothing to check")
	}
	if len(run.Rejected) != 0 {
		t.Fatalf("opencode would refuse the injected parts: %+v", run.Rejected)
	}
}
