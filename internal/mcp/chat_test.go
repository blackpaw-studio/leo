package mcp

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/leomcp"
	"github.com/blackpaw-studio/leo/internal/leotools"
)

func chatDaemon(t *testing.T, reply string) *fakeDaemon {
	t.Helper()
	d := newFakeDaemon(func(method, path string, _ []byte) (int, string) {
		if method == "POST" && strings.Contains(path, "/chat/") {
			return 200, reply
		}
		return 404, `{"ok":false,"error":"unexpected"}`
	})
	t.Cleanup(d.close)
	return d
}

func lastBody(t *testing.T, d *fakeDaemon) (string, map[string]any) {
	t.Helper()
	if len(d.calls) == 0 {
		t.Fatal("no daemon call")
	}
	c := d.calls[len(d.calls)-1]
	var body map[string]any
	if err := json.Unmarshal([]byte(c.Body), &body); err != nil {
		t.Fatalf("body %q: %v", c.Body, err)
	}
	return c.Path, body
}

func TestChatPostFromAnAgentPostsToItsThread(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{"id":"01X","agent":"assistant"}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{})
	out, err := reg.call("leo_chat_post", json.RawMessage(`{"text":"done","files":["/abs/r.pdf","rel/notes.md"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"id":"01X","agent":"assistant"}` {
		t.Fatalf("result = %s", out)
	}
	path, body := lastBody(t, d)
	if path != "/api/agent/assistant/chat/post" || body["text"] != "done" {
		t.Fatalf("call %s %v", path, body)
	}
	files := body["files"].([]any)
	if files[0] != "/abs/r.pdf" || !filepath.IsAbs(files[1].(string)) || !strings.HasSuffix(files[1].(string), "rel/notes.md") {
		t.Fatalf("files = %v; relative paths resolve against the workspace", files)
	}
	if _, ok := body["task"]; ok {
		t.Fatalf("an agent call carries no task: %v", body)
	}
}

func TestChatPostFromATaskRunCarriesTaskAndRun(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{"id":"01X","agent":"assistant"}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "task:daily", leotools.Permissions{}, withTaskRun("daily", "run-7"))
	if _, err := reg.call("leo_chat_post", json.RawMessage(`{"text":"report"}`)); err != nil {
		t.Fatal(err)
	}
	path, body := lastBody(t, d)
	if path != "/api/agent/task:daily/chat/post" || body["task"] != "daily" || body["run_id"] != "run-7" {
		t.Fatalf("call %s %v", path, body)
	}
}

func TestChatToolsRefuseDispatchSubagents(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{}, withDispatchID("d-1"))
	for _, tool := range []string{"leo_chat_post", "leo_chat_ask", "leo_chat_history", "leo_chat_schedule", "leo_chat_schedule_list", "leo_chat_schedule_cancel"} {
		_, err := reg.call(tool, json.RawMessage(`{"text":"x","question":"q","options":["a"],"at":"2026-10-14T09:00:00Z","id":"01X"}`))
		if err == nil || !strings.Contains(err.Error(), "leo_dispatch subagent") {
			t.Errorf("%s: err = %v", tool, err)
		}
	}
	if len(d.calls) != 0 {
		t.Fatalf("a refused call reached the daemon: %+v", d.calls)
	}
}

func TestChatAskNormalizesOptionsAndDefaultsTimeout(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{"values":["y"],"labels":["Yes"],"text":"","timed_out":false}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{})
	out, err := reg.call("leo_chat_ask", json.RawMessage(`{"question":"Ship?","options":["y",{"value":"n","label":"No"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"values":["y"],"labels":["Yes"],"text":"","timed_out":false}` {
		t.Fatalf("result = %s", out)
	}
	path, body := lastBody(t, d)
	opts := body["options"].([]any)
	if path != "/api/agent/assistant/chat/ask" || body["timeout_s"] != float64(300) || len(opts) != 2 {
		t.Fatalf("call %s %v", path, body)
	}
	if o := opts[0].(map[string]any); o["value"] != "y" || o["label"] != "y" {
		t.Fatalf("string option = %v", o)
	}
	if o := opts[1].(map[string]any); o["value"] != "n" || o["label"] != "No" {
		t.Fatalf("object option = %v", o)
	}
}

func TestChatAskPassesATypedInput(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{"values":["a"],"labels":["A"],"text":"","timed_out":false}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{})
	_, err := reg.call("leo_chat_ask", json.RawMessage(`{"question":"Which?","input":{"type":"checkboxes","options":["a",{"value":"b","label":"B"}],"min":1,"max":2,"allow_text":true,"placeholder":"other"}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, body := lastBody(t, d)
	in, _ := body["input"].(map[string]any)
	opts, _ := in["options"].([]any)
	if body["options"] != nil || in["type"] != "checkboxes" || in["min"] != float64(1) || in["max"] != float64(2) || in["allow_text"] != true || in["placeholder"] != "other" || len(opts) != 2 {
		t.Fatalf("body = %v", body)
	}
	if o := opts[0].(map[string]any); o["value"] != "a" || o["label"] != "a" {
		t.Fatalf("string option = %v", o)
	}
	if _, err := reg.call("leo_chat_ask", json.RawMessage(`{"question":"Why?","input":{"type":"text","placeholder":"because"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, body = lastBody(t, d); body["input"].(map[string]any)["type"] != "text" {
		t.Fatalf("text body = %v", body)
	}
}

func TestChatAskRejectsBadInputLocally(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{})
	for name, args := range map[string]string{
		"options and input":   `{"question":"q","options":["a"],"input":{"type":"buttons","options":["a"]}}`,
		"input not an object": `{"question":"q","input":"text"}`,
		"no type":             `{"question":"q","input":{"options":["a"]}}`,
		"unknown type":        `{"question":"q","input":{"type":"slider","options":["a"]}}`,
		"min not an integer":  `{"question":"q","input":{"type":"checkboxes","options":["a"],"min":"1"}}`,
		"allow_text not bool": `{"question":"q","input":{"type":"radio","options":["a"],"allow_text":"yes"}}`,
		"bad option":          `{"question":"q","input":{"type":"radio","options":[7]}}`,
		"max zero":            `{"question":"q","input":{"type":"checkboxes","options":["a"],"max":0}}`,
		"resume with input":   `{"message_id":"01ASK","input":{"type":"text"}}`,
	} {
		if _, err := reg.call("leo_chat_ask", json.RawMessage(args)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(d.calls) != 0 {
		t.Fatalf("invalid asks reached the daemon: %+v", d.calls)
	}
}

func TestChatAskRejectsBadArgumentsLocally(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{})
	many := `["1","2","3","4","5","6","7","8","9","10","11","12","13"]`
	for name, args := range map[string]string{
		"no question":   `{"options":["a"]}`,
		"no options":    `{"question":"q","options":[]}`,
		"13 options":    `{"question":"q","options":` + many + `}`,
		"bad option":    `{"question":"q","options":[7]}`,
		"timeout > 1h":  `{"question":"q","options":["a"],"timeout_s":3601}`,
		"timeout <= 0":  `{"question":"q","options":["a"],"timeout_s":0}`,
		"empty value":   `{"question":"q","options":[{"value":""}]}`,
		"timeout float": `{"question":"q","options":["a"],"timeout_s":"soon"}`,
	} {
		if _, err := reg.call("leo_chat_ask", json.RawMessage(args)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(d.calls) != 0 {
		t.Fatalf("invalid asks reached the daemon: %+v", d.calls)
	}
}

// Leo's MCP ceiling stays at the consult deadline's: a long ask waits in
// calls that each fit under it, and the ask's own deadline lives in the
// daemon.
func TestChatAskWaitsInCallsUnderTheToolCeiling(t *testing.T) {
	if leomcp.ToolTimeout != 32*time.Minute {
		t.Fatalf("leo's MCP tool ceiling = %s; a long ask must not raise it", leomcp.ToolTimeout)
	}
	if got := chatAskHTTPTimeout(chatAskCallWindow); got >= leomcp.ToolTimeout {
		t.Fatalf("one ask call (%s HTTP) is not under the harness tool ceiling %s", got, leomcp.ToolTimeout)
	}
	d := chatDaemon(t, `{"ok":true,"data":{"values":[],"labels":[],"text":"","timed_out":false,"pending":true,"message_id":"01ASK"}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{})
	out, err := reg.call("leo_chat_ask", json.RawMessage(`{"question":"Ship?","options":["y"],"timeout_s":3600}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"pending":true`) || !strings.Contains(out, `"message_id":"01ASK"`) {
		t.Fatalf("result = %s", out)
	}
	_, body := lastBody(t, d)
	if body["timeout_s"] != float64(3600) || body["wait_s"] != chatAskCallWindow.Seconds() {
		t.Fatalf("long ask body = %v", body)
	}
	if _, err := reg.call("leo_chat_ask", json.RawMessage(`{"question":"Quick?","options":["y"],"timeout_s":60}`)); err != nil {
		t.Fatal(err)
	}
	if _, body = lastBody(t, d); body["wait_s"] != float64(60) {
		t.Fatalf("short ask waits its whole timeout in one call: %v", body)
	}
}

func TestChatAskResumesByMessageID(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{"values":["y"],"labels":["y"],"text":"","timed_out":false,"message_id":"01ASK"}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{})
	if _, err := reg.call("leo_chat_ask", json.RawMessage(`{"message_id":"01ASK"}`)); err != nil {
		t.Fatal(err)
	}
	path, body := lastBody(t, d)
	if path != "/api/agent/assistant/chat/ask" || body["message_id"] != "01ASK" || body["wait_s"] != chatAskCallWindow.Seconds() || body["question"] != nil {
		t.Fatalf("resume call %s %v", path, body)
	}
	if _, err := reg.call("leo_chat_ask", json.RawMessage(`{"message_id":"01ASK","question":"again?","options":["y"]}`)); err == nil {
		t.Fatal("a resume that also asks a question was accepted")
	}
}

func TestChatHistoryPassesPaging(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{"messages":[{"id":"01A","text":"hi"}],"has_more":false}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{})
	out, err := reg.call("leo_chat_history", json.RawMessage(`{"limit":20,"before":"01B","task":"daily"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"01A"`) {
		t.Fatalf("result = %s", out)
	}
	path, body := lastBody(t, d)
	if path != "/api/agent/assistant/chat/history" || body["limit"] != float64(20) || body["before"] != "01B" || body["task_filter"] != "daily" {
		t.Fatalf("call %s %v", path, body)
	}
	if _, err := reg.call("leo_chat_history", json.RawMessage(`{"limit":201}`)); err == nil {
		t.Fatal("limit over 200 accepted")
	}
}

func TestChatScheduleToolsCallTheDaemon(t *testing.T) {
	cases := []struct {
		name     string
		opts     []registryOption
		caller   string
		tool     string
		args     string
		reply    string
		wantPath string
		wantBody map[string]any
	}{
		{"schedule", nil, "assistant", "leo_chat_schedule", `{"text":"stand up","at":"2026-10-14T09:00:00-04:00"}`,
			`{"ok":true,"data":{"id":"01X","at":"2026-10-14T09:00:00-04:00"}}`,
			"/api/agent/assistant/chat/schedule", map[string]any{"text": "stand up", "at": "2026-10-14T09:00:00-04:00"}},
		{"schedule from a task run", []registryOption{withTaskRun("daily", "run-7")}, "task:daily", "leo_chat_schedule", `{"text":"check","at":"2026-10-14T09:00:00Z"}`,
			`{"ok":true,"data":{"id":"01Y","at":"2026-10-14T05:00:00-04:00"}}`,
			"/api/agent/task:daily/chat/schedule", map[string]any{"text": "check", "at": "2026-10-14T09:00:00Z", "task": "daily", "run_id": "run-7"}},
		{"list", nil, "assistant", "leo_chat_schedule_list", `{}`,
			`{"ok":true,"data":[{"id":"01X","at":"2026-10-14T09:00:00-04:00","text":"stand up"}]}`,
			"/api/agent/assistant/chat/schedule/list", map[string]any{}},
		{"list from a task run", []registryOption{withTaskRun("daily", "run-7")}, "task:daily", "leo_chat_schedule_list", `{}`,
			`{"ok":true,"data":[]}`,
			"/api/agent/task:daily/chat/schedule/list", map[string]any{"task": "daily", "run_id": "run-7"}},
		{"cancel", nil, "assistant", "leo_chat_schedule_cancel", `{"id":"01X"}`,
			`{"ok":true,"data":{"id":"01X","cancelled":true}}`,
			"/api/agent/assistant/chat/schedule/cancel", map[string]any{"id": "01X"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := chatDaemon(t, c.reply)
			reg := newRegistry(newDaemonClient(d.port(), ""), c.caller, leotools.Permissions{}, c.opts...)
			out, err := reg.call(c.tool, json.RawMessage(c.args))
			if err != nil {
				t.Fatal(err)
			}
			var want struct{ Data json.RawMessage }
			if err := json.Unmarshal([]byte(c.reply), &want); err != nil {
				t.Fatal(err)
			}
			if out != string(want.Data) {
				t.Fatalf("result = %s, want the daemon's data verbatim %s", out, want.Data)
			}
			path, body := lastBody(t, d)
			if path != c.wantPath || len(body) != len(c.wantBody) {
				t.Fatalf("call %s %v, want %s %v", path, body, c.wantPath, c.wantBody)
			}
			for k, v := range c.wantBody {
				if body[k] != v {
					t.Fatalf("body[%s] = %v, want %v (%v)", k, body[k], v, body)
				}
			}
		})
	}
}

func TestChatScheduleToolsRejectMissingArgumentsLocally(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{})
	for name, c := range map[string]struct{ tool, args string }{
		"schedule without text":      {"leo_chat_schedule", `{"at":"2026-10-14T09:00:00Z"}`},
		"schedule without at":        {"leo_chat_schedule", `{"text":"x"}`},
		"schedule with a numeric at": {"leo_chat_schedule", `{"text":"x","at":5}`},
		"cancel without id":          {"leo_chat_schedule_cancel", `{}`},
		"cancel with a numeric id":   {"leo_chat_schedule_cancel", `{"id":5}`},
	} {
		if _, err := reg.call(c.tool, json.RawMessage(c.args)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(d.calls) != 0 {
		t.Fatalf("a bad call reached the daemon: %+v", d.calls)
	}
}
