package cli

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// contextDaemon answers /api/agent/context with reply, recording the request.
func contextDaemon(t *testing.T, status int, reply string) (port string, got *capturedReport) {
	t.Helper()
	got = &capturedReport{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*got = capturedReport{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	_, port, _ = net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	return port, got
}

func runContext(t *testing.T, payload string) (stdout, stderr string) {
	t.Helper()
	cmd := newDispatchContextCmd()
	var out, errOut bytes.Buffer
	cmd.SetIn(strings.NewReader(payload))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("context hook must never fail: %v", err)
	}
	return out.String(), errOut.String()
}

const (
	sessionStartInput = `{"hook_event_name":"SessionStart","source":"startup","session_id":"s1"}`
	promptInput       = `{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"hi"}`
)

func TestDispatchContextPrintsAdditionalContext(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"session start", sessionStartInput,
			`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"use <roles> & \"more\""}}` + "\n"},
		{"prompt submit", promptInput,
			`{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"use <roles> & \"more\""}}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearReportEnv(t)
			port, got := contextDaemon(t, http.StatusOK, `{"context":"use <roles> & \"more\""}`)
			t.Setenv("LEO_ATTENTION_TOKEN", "launch-tok")
			t.Setenv("LEO_WEB_PORT", port)
			t.Setenv("LEO_API_TOKEN", "agent-token")

			out, _ := runContext(t, tc.input)

			if out != tc.want {
				t.Fatalf("stdout = %q, want %q", out, tc.want)
			}
			if got.path != "/api/agent/context" || got.auth != "Bearer agent-token" {
				t.Fatalf("request = %+v", *got)
			}
			if !strings.Contains(string(got.body), `"token":"launch-tok"`) || !strings.Contains(string(got.body), tc.input) {
				t.Fatalf("body = %s, want the token and the hook payload", got.body)
			}
		})
	}
}

func TestDispatchContextPrintsNothingWhenThereIsNothingToSay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		status int
		reply  string
		input  string
	}{
		{"empty context", nil, http.StatusOK, `{"context":""}`, promptInput},
		{"daemon error", nil, http.StatusInternalServerError, `oops`, promptInput},
		{"malformed reply", nil, http.StatusOK, `not json`, promptInput},
		{"a dispatch run", map[string]string{"LEO_DISPATCH_ID": "d-1"}, http.StatusOK, `{"context":"x"}`, promptInput},
		{"an event that takes no context", nil, http.StatusOK, `{"context":"x"}`, `{"hook_event_name":"Stop","session_id":"s1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearReportEnv(t)
			port, _ := contextDaemon(t, tc.status, tc.reply)
			t.Setenv("LEO_ATTENTION_TOKEN", "launch-tok")
			t.Setenv("LEO_WEB_PORT", port)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			if out, _ := runContext(t, tc.input); out != "" {
				t.Fatalf("stdout = %q, want nothing", out)
			}
		})
	}
}

func TestDispatchContextOutsideALeoAgentIsSilent(t *testing.T) {
	clearReportEnv(t)

	if out, errOut := runContext(t, promptInput); out != "" || errOut != "" {
		t.Fatalf("stdout %q stderr %q, want silence outside a leo agent", out, errOut)
	}
}

func TestDispatchContextFailsOpenWhenTheDaemonIsDownOrSlow(t *testing.T) {
	clearReportEnv(t)
	t.Setenv("LEO_ATTENTION_TOKEN", "launch-tok")

	t.Run("down", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		_, port, _ := net.SplitHostPort(listener.Addr().String())
		if err := listener.Close(); err != nil { // nothing listens here now
			t.Fatal(err)
		}
		t.Setenv("LEO_WEB_PORT", port)

		out, errOut := runContext(t, promptInput)

		if out != "" || strings.Count(strings.TrimSpace(errOut), "\n") != 0 || errOut == "" {
			t.Fatalf("stdout %q stderr %q, want nothing on stdout and one stderr line", out, errOut)
		}
	})

	t.Run("slow", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		t.Cleanup(func() { close(release); srv.Close() })
		_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
		t.Setenv("LEO_WEB_PORT", port)
		prev := contextHookTimeout
		contextHookTimeout = 50 * time.Millisecond
		t.Cleanup(func() { contextHookTimeout = prev })

		out, errOut := runContext(t, promptInput)

		if out != "" || errOut == "" {
			t.Fatalf("stdout %q stderr %q, want nothing on stdout and a stderr line", out, errOut)
		}
	})
}

// contextSocketDaemon answers /api/agent/context on a unix socket, recording
// the request.
func contextSocketDaemon(t *testing.T, reply string) (sockPath string, got *capturedReport) {
	t.Helper()
	got = &capturedReport{}
	dir, err := os.MkdirTemp("", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath = filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*got = capturedReport{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sockPath, got
}

// The daemon decides who may rebind a session from who the kernel says
// connected, which only a unix socket connection can say: the relay uses the
// socket the launch names, and sends no pid of its own to be believed.
func TestDispatchContextCallsTheDaemonSocketWhenTheLaunchNamesOne(t *testing.T) {
	clearReportEnv(t)
	sock, onSocket := contextSocketDaemon(t, `{"context":"from the socket"}`)
	tcpPort, onTCP := contextDaemon(t, http.StatusOK, `{"context":"from tcp"}`)
	t.Setenv("LEO_ATTENTION_TOKEN", "launch-tok")
	t.Setenv("LEO_WEB_PORT", tcpPort)
	t.Setenv("LEO_SOCKET", sock)

	out, _ := runContext(t, sessionStartInput)

	if !strings.Contains(out, "from the socket") {
		t.Fatalf("stdout = %q, want the socket daemon's context", out)
	}
	if onSocket.path != "/api/agent/context" || !strings.Contains(string(onSocket.body), `"token":"launch-tok"`) || !strings.Contains(string(onSocket.body), sessionStartInput) {
		t.Fatalf("socket request = %+v", *onSocket)
	}
	if strings.Contains(string(onSocket.body), `"pid"`) {
		t.Fatalf("body = %s, want no pid (the kernel says who connected)", onSocket.body)
	}
	if onTCP.path != "" {
		t.Fatalf("tcp daemon was called (%+v) although the socket answered", *onTCP)
	}
}

// With the socket unusable the relay still delivers context over TCP: the
// daemon then cannot tell who is calling, so it only serves the session the
// agent is already bound to.
func TestDispatchContextFallsBackToTCPWhenTheSocketIsUnusable(t *testing.T) {
	clearReportEnv(t)
	tcpPort, onTCP := contextDaemon(t, http.StatusOK, `{"context":"from tcp"}`)
	t.Setenv("LEO_ATTENTION_TOKEN", "launch-tok")
	t.Setenv("LEO_WEB_PORT", tcpPort)
	t.Setenv("LEO_SOCKET", filepath.Join(t.TempDir(), "gone.sock"))

	out, _ := runContext(t, sessionStartInput)

	if !strings.Contains(out, "from tcp") || onTCP.path != "/api/agent/context" {
		t.Fatalf("stdout = %q, tcp request = %+v; want the TCP fallback", out, *onTCP)
	}
}

func TestDispatchContextWorksOnTheSocketAloneWithoutAWebPort(t *testing.T) {
	clearReportEnv(t)
	sock, _ := contextSocketDaemon(t, `{"context":"only socket"}`)
	t.Setenv("LEO_ATTENTION_TOKEN", "launch-tok")
	t.Setenv("LEO_SOCKET", sock)

	if out, _ := runContext(t, promptInput); !strings.Contains(out, "only socket") {
		t.Fatalf("stdout = %q, want the socket daemon's context", out)
	}
}

// A daemon that answers with an error is not retried elsewhere.
func TestDispatchContextDoesNotFallBackFromADaemonThatAnswered(t *testing.T) {
	clearReportEnv(t)
	dir, _ := os.MkdirTemp("", "ctx")
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	tcpPort, onTCP := contextDaemon(t, http.StatusOK, `{"context":"from tcp"}`)
	t.Setenv("LEO_ATTENTION_TOKEN", "launch-tok")
	t.Setenv("LEO_WEB_PORT", tcpPort)
	t.Setenv("LEO_SOCKET", sock)

	out, errOut := runContext(t, sessionStartInput)

	if out != "" || onTCP.path != "" || !strings.Contains(errOut, "500") {
		t.Fatalf("stdout = %q, stderr = %q, tcp = %+v; want nothing printed, a logged 500 and no TCP retry", out, errOut, *onTCP)
	}
}
