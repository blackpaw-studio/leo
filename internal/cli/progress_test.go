package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type progressCall struct {
	path, auth string
	body       map[string]any
}

// progressDaemon answers POST /api/agent/progress with status and body, and
// records what it was sent.
func progressDaemon(t *testing.T, status int, reply string) (port string, last func() progressCall) {
	t.Helper()
	var got progressCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = progressCall{path: r.URL.Path, auth: r.Header.Get("Authorization")}
		_ = json.Unmarshal(raw, &got.body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	return p, func() progressCall { return got }
}

func insideAgent(t *testing.T, port string) {
	t.Helper()
	clearReportEnv(t)
	t.Setenv("LEO_ATTENTION_TOKEN", "launch-tok")
	t.Setenv("LEO_WEB_PORT", port)
	t.Setenv("LEO_API_TOKEN", "agent-token")
}

func runProgress(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newProgressCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

const okReply = `{"ok":true,"task":{"id":"build","title":"Xcode build","state":"running"}}`

func TestProgressCmdMapsFlagsToTheRequest(t *testing.T) {
	port, last := progressDaemon(t, http.StatusOK, okReply)
	insideAgent(t, port)

	if _, _, err := runProgress(t, "build", "--title", "Xcode build", "--percent", "62.5", "--status", "Compiling 4/9", "--eta", "90s"); err != nil {
		t.Fatalf("progress: %v", err)
	}

	call := last()
	if call.path != "/api/agent/progress" || call.auth != "Bearer agent-token" {
		t.Fatalf("path=%q auth=%q", call.path, call.auth)
	}
	want := map[string]any{"token": "launch-tok", "id": "build", "title": "Xcode build", "percent": 62.5, "status": "Compiling 4/9", "eta_seconds": 90.0}
	if len(call.body) != len(want) {
		t.Fatalf("body = %v, want exactly %v", call.body, want)
	}
	for k, v := range want {
		if call.body[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, call.body[k], v)
		}
	}
}

func TestProgressCmdOmitsUnsetFlags(t *testing.T) {
	port, last := progressDaemon(t, http.StatusOK, okReply)
	insideAgent(t, port)

	if _, _, err := runProgress(t, "build", "--percent", "0"); err != nil {
		t.Fatal(err)
	}

	body := last().body
	if _, has := body["title"]; has {
		t.Errorf("unset --title leaked into the body: %v", body)
	}
	if p, has := body["percent"]; !has || p != 0.0 {
		t.Errorf("an explicit --percent 0 must be sent: %v", body)
	}
}

func TestProgressCmdStateAndModeFlags(t *testing.T) {
	for _, tc := range []struct {
		flag  string
		key   string
		value any
	}{
		{"--indeterminate", "indeterminate", true},
		{"--done", "state", "succeeded"},
		{"--fail", "state", "failed"},
		{"--cancel", "state", "cancelled"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			port, last := progressDaemon(t, http.StatusOK, okReply)
			insideAgent(t, port)

			if _, _, err := runProgress(t, "build", tc.flag); err != nil {
				t.Fatal(err)
			}
			if got := last().body[tc.key]; got != tc.value {
				t.Fatalf("body[%s] = %v, want %v", tc.key, got, tc.value)
			}
		})
	}
}

func TestProgressCmdRejectsConflictingFlags(t *testing.T) {
	port, last := progressDaemon(t, http.StatusOK, okReply)
	insideAgent(t, port)
	for _, args := range [][]string{
		{"build", "--percent", "5", "--indeterminate"},
		{"build", "--done", "--fail"},
		{"build", "--done", "--cancel"},
		{"build", "--eta", "soon"},
		{"build", "--eta", "-5s"},
		{},
		{"a", "b"},
	} {
		if _, _, err := runProgress(t, args...); err == nil {
			t.Errorf("args %v: want an error", args)
		}
	}
	if last().path != "" {
		t.Fatal("invalid invocations must not reach the daemon")
	}
}

func TestProgressCmdOutsideAnAgentExitsTwo(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"no token": {"LEO_WEB_PORT": "8370"},
		"no port":  {"LEO_ATTENTION_TOKEN": "tok"},
	} {
		t.Run(name, func(t *testing.T) {
			clearReportEnv(t)
			for k, v := range env {
				t.Setenv(k, v)
			}

			_, _, err := runProgress(t, "build", "--percent", "1")

			if ExitCode(err) != 2 || !strings.Contains(err.Error(), "not inside a Leo agent") {
				t.Fatalf("err = %v (exit %d)", err, ExitCode(err))
			}
		})
	}
}

func TestProgressCmdSurfacesTheDaemonError(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusConflict} {
		port, _ := progressDaemon(t, status, `{"ok":false,"error":"too many running tasks (max 8); finish one first"}`)
		insideAgent(t, port)

		_, _, err := runProgress(t, "build", "--title", "T")

		if err == nil || !strings.Contains(err.Error(), "too many running tasks") || ExitCode(err) != 1 {
			t.Fatalf("status %d: err = %v (exit %d)", status, err, ExitCode(err))
		}
	}
}

func TestProgressCmdRejectsABadPort(t *testing.T) {
	insideAgent(t, "not-a-port")
	if _, _, err := runProgress(t, "build", "--percent", "1"); err == nil || !strings.Contains(err.Error(), "LEO_WEB_PORT") {
		t.Fatalf("err = %v", err)
	}
}

// A dispatch pane sits in its caller's tmux session, so it can still see the
// caller's token; it must not report progress as the caller.
func TestProgressCmdRefusesADispatchSubagent(t *testing.T) {
	port, last := progressDaemon(t, http.StatusOK, okReply)
	insideAgent(t, port)
	t.Setenv("LEO_DISPATCH_ID", "d-abc")

	_, _, err := runProgress(t, "build", "--percent", "1")

	if err == nil || !strings.Contains(err.Error(), "dispatch") || last().path != "" {
		t.Fatalf("err = %v, daemon called = %v", err, last().path != "")
	}
}
