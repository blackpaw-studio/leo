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
	for _, tool := range []string{"leo_chat_post", "leo_chat_ask", "leo_chat_history"} {
		_, err := reg.call(tool, json.RawMessage(`{"text":"x","question":"q","options":["a"]}`))
		if err == nil || !strings.Contains(err.Error(), "leo_dispatch subagent") {
			t.Errorf("%s: err = %v", tool, err)
		}
	}
	if len(d.calls) != 0 {
		t.Fatalf("a refused call reached the daemon: %+v", d.calls)
	}
}

func TestChatAskNormalizesOptionsAndDefaultsTimeout(t *testing.T) {
	d := chatDaemon(t, `{"ok":true,"data":{"value":"y","label":"Yes","timed_out":false}}`)
	reg := newRegistry(newDaemonClient(d.port(), ""), "assistant", leotools.Permissions{})
	out, err := reg.call("leo_chat_ask", json.RawMessage(`{"question":"Ship?","options":["y",{"value":"n","label":"No"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"value":"y","label":"Yes","timed_out":false}` {
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

func TestChatAskHTTPDeadlineOutlivesTheAsk(t *testing.T) {
	if got := chatAskHTTPTimeout(time.Hour); got <= time.Hour {
		t.Fatalf("HTTP timeout %s must exceed the ask's own", got)
	}
	if leoToolCeiling := chatAskHTTPTimeout(maxChatAskTimeout); leoToolCeiling > leomcp.ToolTimeout {
		t.Fatalf("a maximal ask (%s HTTP) outlives the harness tool timeout %s", leoToolCeiling, leomcp.ToolTimeout)
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
