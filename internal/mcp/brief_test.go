package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

const briefReply = `{"ok":true,"brief":{"goal":"Ship it","status":"testing","updated_at":"2026-11-25T02:00:00Z"}}`

func briefDaemon(t *testing.T, status int, reply string) *fakeDaemon {
	t.Helper()
	d := newFakeDaemon(func(method, path string, _ []byte) (int, string) {
		if method == "POST" && path == "/api/agent/brief" {
			return status, reply
		}
		if method == "POST" && path == "/api/agent/brief/offered" {
			return 200, `{"ok":true}`
		}
		return 404, `{"ok":false,"error":"unexpected"}`
	})
	t.Cleanup(d.close)
	return d
}

func briefRegistry(d *fakeDaemon, on bool, opts ...registryOption) *registry {
	opts = append([]registryOption{withAttentionToken("launch-tok"), withBrief(on)}, opts...)
	return newRegistry(newDaemonClient(d.port(), "agent-token"), "assistant", leotools.Permissions{}, opts...)
}

// briefCalls counts the requests that reached the brief route (listing
// tools may also ask the daemon about client tools).
func briefCalls(d *fakeDaemon) int {
	n := 0
	for _, c := range d.calls {
		if c.Path == "/api/agent/brief" {
			n++
		}
	}
	return n
}

func listedNames(r *registry) []string {
	var names []string
	for _, def := range r.list() {
		names = append(names, def.Name)
	}
	return names
}

func TestLeoBriefIsListedOnlyWhileBriefsAreOn(t *testing.T) {
	d := briefDaemon(t, 200, briefReply)
	if contains(listedNames(briefRegistry(d, false)), "leo_brief") {
		t.Error("leo_brief listed while briefs are off")
	}
	if !contains(listedNames(briefRegistry(d, true)), "leo_brief") {
		t.Error("leo_brief missing while briefs are on")
	}
}

func TestLeoBriefSchemaMatchesTheWireContract(t *testing.T) {
	d := briefDaemon(t, 200, briefReply)
	var def toolDef
	for _, c := range briefRegistry(d, true).list() {
		if c.Name == "leo_brief" {
			def = c
		}
	}
	raw, _ := json.Marshal(def.InputSchema)
	want := `{"additionalProperties":false,"properties":{"decisions":{"items":{"maxLength":140,"type":"string"},"maxItems":5,"type":"array"},"goal":{"maxLength":140,"type":"string"},"open":{"items":{"maxLength":140,"type":"string"},"maxItems":5,"type":"array"},"status":{"maxLength":140,"type":"string"}},"type":"object"}`
	if string(raw) != want {
		t.Errorf("schema = %s\nwant     %s", raw, want)
	}
	if !strings.Contains(def.Description, "Replaces the whole brief") || !strings.Contains(def.Description, "no arguments to clear") {
		t.Errorf("description = %q", def.Description)
	}
}

func TestLeoBriefSendsTheWholeBriefWithTheLaunchToken(t *testing.T) {
	d := briefDaemon(t, 200, briefReply)

	out, err := briefRegistry(d, true).call("leo_brief", json.RawMessage(
		`{"goal":"Ship it","status":"testing","decisions":["atomic writes"],"open":["review"]}`))

	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"goal":"Ship it"`) {
		t.Fatalf("result = %s", out)
	}
	path, body := lastBody(t, d)
	if path != "/api/agent/brief" || body["token"] != "launch-tok" || len(body) != 2 {
		t.Fatalf("call %s %v", path, body)
	}
	inner, _ := body["brief"].(map[string]any)
	if inner["goal"] != "Ship it" || inner["status"] != "testing" || len(inner) != 4 {
		t.Fatalf("brief = %v", inner)
	}
}

func TestLeoBriefWithNoArgumentsClears(t *testing.T) {
	d := briefDaemon(t, 200, `{"ok":true,"brief":null}`)
	out, err := briefRegistry(d, true).call("leo_brief", nil)
	if err != nil || out != "Brief cleared." {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	_, body := lastBody(t, d)
	if inner, ok := body["brief"].(map[string]any); !ok || len(inner) != 0 {
		t.Fatalf("body = %v, want an empty brief object", body)
	}
}

func TestLeoBriefRejectsBadArgumentsWithoutCallingTheDaemon(t *testing.T) {
	d := briefDaemon(t, 200, briefReply)
	reg := briefRegistry(d, true)
	for name, args := range map[string]string{
		"agent argument": `{"goal":"g","agent":"victim"}`,
		"goal not text":  `{"goal":5}`,
		"list not array": `{"open":"x"}`,
		"item not text":  `{"decisions":[1]}`,
	} {
		if _, err := reg.call("leo_brief", json.RawMessage(args)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if len(d.calls) != 0 {
		t.Fatalf("rejected calls reached the daemon: %+v", d.calls)
	}
}

func TestLeoBriefSurfacesDaemonValidationErrors(t *testing.T) {
	d := briefDaemon(t, 400, `{"ok":false,"error":"invalid brief: goal is 150 characters, at most 140"}`)
	_, err := briefRegistry(d, true).call("leo_brief", json.RawMessage(`{"goal":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "goal is 150 characters") {
		t.Fatalf("err = %v", err)
	}
}

// A harness that listed the tool before briefs were switched off (codex
// never re-lists) can still call it: the handler refuses without a request.
func TestLeoBriefRefusesWhileOff(t *testing.T) {
	d := briefDaemon(t, 200, briefReply)
	_, err := briefRegistry(d, false).call("leo_brief", json.RawMessage(`{"goal":"g"}`))
	if err == nil || !strings.Contains(err.Error(), "off") {
		t.Fatalf("err = %v, want a briefs-are-off refusal", err)
	}
	if len(d.calls) != 0 {
		t.Fatal("a refused call reached the daemon")
	}
}

func TestLeoBriefIsNotForDispatchesOrTokenlessServers(t *testing.T) {
	d := briefDaemon(t, 200, briefReply)
	dispatch := briefRegistry(d, true, withDispatchID("d-abc"))
	if contains(listedNames(dispatch), "leo_brief") {
		t.Error("a dispatch subagent was offered leo_brief")
	}
	if _, err := dispatch.call("leo_brief", json.RawMessage(`{"goal":"g"}`)); err == nil || !strings.Contains(err.Error(), "dispatch") {
		t.Errorf("dispatch err = %v", err)
	}
	bare := newRegistry(newDaemonClient(d.port(), "agent-token"), "assistant", leotools.Permissions{}, withBrief(true))
	if contains(listedNames(bare), "leo_brief") {
		t.Error("a server without a launch token was offered leo_brief")
	}
	if _, err := bare.call("leo_brief", json.RawMessage(`{"goal":"g"}`)); err == nil || !strings.Contains(err.Error(), "LEO_ATTENTION_TOKEN") {
		t.Errorf("tokenless err = %v", err)
	}
	if n := briefCalls(d); n != 0 {
		t.Fatalf("refused calls reached the daemon %d times", n)
	}
}

func TestLeoBriefIsDeniedByTemplatePermissions(t *testing.T) {
	d := briefDaemon(t, 200, briefReply)
	reg := newRegistry(newDaemonClient(d.port(), "agent-token"), "assistant",
		leotools.Permissions{DenyTools: []string{"leo_brief"}}, withAttentionToken("launch-tok"), withBrief(true))
	if contains(listedNames(reg), "leo_brief") {
		t.Fatal("a denied tool must not be listed")
	}
	if _, err := reg.call("leo_brief", json.RawMessage(`{"goal":"g"}`)); err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("err = %v", err)
	}
}

func TestListChangedWhenTheBriefSwitchFlips(t *testing.T) {
	d := newWatchDaemon(t)
	reg := newRegistry(d.client, "alfred", leotools.Permissions{}, withAttentionToken("launch-tok"), withBrief(false))
	reg.watch.after = immediate
	s := startSession(t, reg)
	s.initialize("claude-code")
	d.waitStreams(t, 1)
	if names := s.listNames(); contains(names, "leo_brief") {
		t.Fatalf("names = %v", names)
	}

	d.pushBriefConfig(true)
	if m := s.recv(); m.Method != "notifications/tools/list_changed" || len(m.ID) != 0 {
		t.Fatalf("got %+v, want list_changed", m)
	}
	if names := s.listNames(); !contains(names, "leo_brief") {
		t.Fatalf("leo_brief not offered after the flip on: %v", names)
	}

	d.pushBriefConfig(true) // not news
	d.pushBriefConfig(false)
	if m := s.recv(); m.Method != "notifications/tools/list_changed" {
		t.Fatalf("got %+v, want exactly one list_changed for the flip off", m)
	}
	if names := s.listNames(); contains(names, "leo_brief") {
		t.Fatalf("leo_brief still offered after the flip off: %v", names)
	}
	s.send("ping", nil)
	if m := s.recv(); m.Method != "" || string(m.Result) != "{}" {
		t.Fatalf("unexpected message before the ping reply: %+v", m)
	}
}

func offeredReports(d *fakeDaemon) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		if c.Path == "/api/agent/brief/offered" {
			out = append(out, c.Body)
		}
	}
	return out
}

// Listing leo_brief tells the daemon the session has it, once, which covers a
// harness that never watches for list changes. A session that does not list it
// (briefs off, or a dispatch subagent) reports nothing, and the daemon treats
// silence as "not offered".
func TestListingLeoBriefReportsItToTheDaemonOnce(t *testing.T) {
	d := briefDaemon(t, 200, briefReply)
	r := briefRegistry(d, true)
	listedNames(r)
	listedNames(r)
	got := offeredReports(d)
	if len(got) != 1 || !strings.Contains(got[0], `"offered":true`) || !strings.Contains(got[0], `"token":"launch-tok"`) {
		t.Fatalf("reports = %v, want one offered=true under the launch token", got)
	}

	for name, mk := range map[string]func(*fakeDaemon) *registry{
		"briefs off": func(d *fakeDaemon) *registry { return briefRegistry(d, false) },
		"dispatch":   func(d *fakeDaemon) *registry { return briefRegistry(d, true, withDispatchID("d-1")) },
	} {
		quiet := briefDaemon(t, 200, briefReply)
		listedNames(mk(quiet))
		if got := offeredReports(quiet); len(got) != 0 {
			t.Errorf("%s reported %v", name, got)
		}
	}
}
