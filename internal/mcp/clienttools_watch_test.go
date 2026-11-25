package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// immediate fires every timer at once, so coalescing and backoff cost nothing.
func immediate(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

// watchDaemon is a fake daemon with a controllable client-tools revision and
// an SSE stream the test pushes events into.
type watchDaemon struct {
	mu       sync.Mutex
	revision uint64
	ever     bool
	tools    []string
	events   chan string // raw SSE frames to push
	streams  int
	lists    int
	// briefOn is the daemon's brief switch as /api/agent/brief/config answers
	// it, and briefStatus its status (0 means 200); briefAsks counts the asks.
	briefOn     bool
	briefStatus int
	briefAsks   int
	// offers are the /api/agent/brief/offered reports, in order; offerStatus
	// is the status the route answers (0 means 200).
	offers      []offerReport
	offerStatus int
	drop        chan struct{} // a send ends the open event stream
	srv         *httptest.Server
	client      *daemonClient
}

func newWatchDaemon(t *testing.T) *watchDaemon {
	t.Helper()
	d := &watchDaemon{revision: 3, ever: true, tools: []string{"browser_click"}, events: make(chan string, 16), drop: make(chan struct{}, 1)}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/events":
			d.mu.Lock()
			d.streams++
			d.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fl := w.(http.Flusher)
			_, _ = io.WriteString(w, "event: hello\ndata: {\"seq\":1}\n\n")
			fl.Flush()
			for {
				select {
				case frame := <-d.events:
					_, _ = io.WriteString(w, frame)
					fl.Flush()
				case <-d.drop:
					return
				case <-r.Context().Done():
					return
				}
			}
		case r.URL.Path == "/api/agent/brief/config":
			d.mu.Lock()
			d.briefAsks++
			status, on := d.briefStatus, d.briefOn
			d.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"ok":false,"error":"boom"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"ok":true,"data":{"enabled":%v}}`, on)
		case r.URL.Path == "/api/agent/brief/offered":
			var body offerReport
			_ = json.NewDecoder(r.Body).Decode(&body)
			d.mu.Lock()
			status := d.offerStatus
			if status == 0 {
				d.offers = append(d.offers, body)
			}
			d.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"ok":false,"error":"boom"}`)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true}`)
		case strings.HasSuffix(r.URL.Path, "/client-tools"):
			d.mu.Lock()
			defer d.mu.Unlock()
			d.lists++
			tools := make([]string, 0, len(d.tools))
			for _, n := range d.tools {
				tools = append(tools, fmt.Sprintf(`{"group":"browser","name":%q,"description":"d","input_schema":{"type":"object"},"timeout_s":30}`, n))
			}
			_, _ = fmt.Fprintf(w, `{"ok":true,"data":{"revision":%d,"ever_registered":%v,"tools":[%s]}}`, d.revision, d.ever, strings.Join(tools, ","))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(d.srv.Close)
	d.client = newDaemonClient(strings.TrimPrefix(d.srv.URL, "http://127.0.0.1:"), "tok")
	return d
}

// waitFor polls the fake daemon until cond holds.
func (d *watchDaemon) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		d.mu.Lock()
		ok := cond()
		d.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (d *watchDaemon) waitLists(t *testing.T, n int) {
	t.Helper()
	d.waitFor(t, fmt.Sprintf("%d client-tools fetches", n), func() bool { return d.lists >= n })
}

func (d *watchDaemon) waitStreams(t *testing.T, n int) {
	t.Helper()
	d.waitFor(t, fmt.Sprintf("%d event streams", n), func() bool { return d.streams >= n })
}

func (d *watchDaemon) setRevision(rev uint64, tools ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.revision, d.tools = rev, tools
}

func (d *watchDaemon) pushChanged(rev uint64) {
	d.events <- fmt.Sprintf("event: client_tools.changed\ndata: {\"seq\":9,\"revision\":%d,\"tools\":[]}\n\n", rev)
}

func (d *watchDaemon) pushBriefConfig(enabled bool) {
	d.events <- fmt.Sprintf("event: brief_config_changed\ndata: {\"seq\":9,\"at\":\"2026-11-25T02:00:00Z\",\"enabled\":%v}\n\n", enabled)
}

// session runs the server over pipes and lets a test speak MCP to it.
type session struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *bufio.Reader
	done   chan error
	nextID int
}

func startSession(t *testing.T, reg *registry) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &session{t: t, in: inW, out: bufio.NewReader(outR), done: make(chan error, 1)}
	go func() {
		err := runWith(inR, outW, reg)
		_ = outW.Close()
		s.done <- err
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop after stdin closed")
		}
	})
	return s
}

type wireMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
}

func (s *session) send(method string, params any) {
	s.t.Helper()
	s.nextID++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": s.nextID, "method": method, "params": params})
	if _, err := s.in.Write(append(b, '\n')); err != nil {
		s.t.Fatal(err)
	}
}

func (s *session) notify(method string) {
	s.t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if _, err := s.in.Write(append(b, '\n')); err != nil {
		s.t.Fatal(err)
	}
}

func (s *session) recv() wireMsg {
	s.t.Helper()
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := s.out.ReadBytes('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			s.t.Fatalf("recv: %v", r.err)
		}
		var m wireMsg
		if err := json.Unmarshal(bytes.TrimSpace(r.line), &m); err != nil {
			s.t.Fatalf("recv %q: %v", r.line, err)
		}
		return m
	case <-time.After(5 * time.Second):
		s.t.Fatal("timed out waiting for a message")
		return wireMsg{}
	}
}

func (s *session) initialize(clientName string) json.RawMessage {
	s.t.Helper()
	s.send("initialize", map[string]any{"protocolVersion": "2025-06-18", "clientInfo": map[string]any{"name": clientName, "version": "1"}})
	m := s.recv()
	s.notify("notifications/initialized")
	return m.Result
}

func (s *session) listNames() []string {
	s.t.Helper()
	s.send("tools/list", map[string]any{})
	m := s.recv()
	var r struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	if err := json.Unmarshal(m.Result, &r); err != nil {
		s.t.Fatal(err)
	}
	names := make([]string, 0, len(r.Tools))
	for _, tl := range r.Tools {
		names = append(names, tl.Name)
	}
	return names
}

func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func registryFor(d *watchDaemon) *registry {
	reg := newRegistry(d.client, "alfred", leotools.Permissions{})
	reg.watch.after = immediate
	return reg
}

func TestListChangedEmittedOnRevisionChange(t *testing.T) {
	d := newWatchDaemon(t)
	s := startSession(t, registryFor(d))

	var init struct {
		Capabilities struct {
			Tools struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(s.initialize("claude-code"), &init); err != nil || !init.Capabilities.Tools.ListChanged {
		t.Fatalf("a honoring client must be told listChanged (%v): %+v", err, init)
	}
	if names := s.listNames(); !contains(names, "browser_click") {
		t.Fatalf("names = %v", names)
	}

	// The same revision is not a change.
	d.pushChanged(3)
	// A new revision is: exactly one notification.
	d.setRevision(4, "browser_click", "browser_back")
	d.pushChanged(4)

	if m := s.recv(); m.Method != "notifications/tools/list_changed" || len(m.ID) != 0 {
		t.Fatalf("got %+v, want list_changed", m)
	}
	if names := s.listNames(); !contains(names, "browser_back") {
		t.Fatalf("relist missed the new tool: %v", names)
	}
}

func TestListChangedNotSentBeforeFirstList(t *testing.T) {
	d := newWatchDaemon(t)
	s := startSession(t, registryFor(d))
	s.initialize("claude-code")
	d.waitLists(t, 1) // hello's refetch

	// A change before the harness ever listed is invisible to it: nothing to refresh.
	d.setRevision(10, "browser_click")
	d.pushChanged(10)
	d.waitLists(t, 2)
	if names := s.listNames(); !contains(names, "browser_click") {
		t.Fatalf("names = %v", names)
	}

	// The list already showed revision 10, so repeating it is not news.
	d.pushChanged(10)
	d.waitLists(t, 4)
	s.send("ping", nil)
	if m := s.recv(); m.Method != "" || string(m.Result) != "{}" {
		t.Fatalf("unexpected message before the ping reply: %+v", m)
	}
}

func TestWatcherReconnectsAfterTheStreamDrops(t *testing.T) {
	d := newWatchDaemon(t)
	reg := registryFor(d)
	s := startSession(t, reg)
	s.initialize("claude-code")
	d.waitStreams(t, 1)
	s.listNames()

	// Close every connection; the watcher must reconnect (after its backoff) and refetch on hello.
	d.setRevision(5, "browser_click", "browser_new")
	d.srv.CloseClientConnections()
	d.waitStreams(t, 2)
	if m := s.recv(); m.Method != "notifications/tools/list_changed" {
		t.Fatalf("got %+v, want list_changed after reconnect", m)
	}
}

func TestNonHonoringClientGetsFixedFallbackList(t *testing.T) {
	d := newWatchDaemon(t)
	s := startSession(t, registryFor(d))
	var init struct {
		Capabilities struct {
			Tools map[string]any `json:"tools"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(s.initialize("codex-mcp-client"), &init); err != nil {
		t.Fatal(err)
	}
	if init.Capabilities.Tools["listChanged"] == true {
		t.Fatal("a non-honoring client must not be promised list_changed")
	}
	names := s.listNames()
	if !contains(names, "leo_app_tools") || !contains(names, "leo_app_call") {
		t.Fatalf("fallback tools missing: %v", names)
	}
	if contains(names, "browser_click") {
		t.Fatalf("native client tool listed for a non-honoring client: %v", names)
	}

	// The list never changes, however the daemon's set does, and no notification is sent.
	d.setRevision(4, "browser_click", "browser_back")
	d.pushChanged(4)
	if again := s.listNames(); strings.Join(again, ",") != strings.Join(names, ",") {
		t.Fatalf("fallback list changed mid-session: %v -> %v", names, again)
	}
	d.mu.Lock()
	streams := d.streams
	d.mu.Unlock()
	if streams != 0 {
		t.Fatalf("a non-honoring client opened %d watch streams", streams)
	}
}

func TestUnknownClientInfoUsesFallback(t *testing.T) {
	d := newWatchDaemon(t)
	s := startSession(t, registryFor(d))
	s.initialize("some-new-harness")
	names := s.listNames()
	if !contains(names, "leo_app_call") || contains(names, "browser_click") {
		t.Fatalf("unknown client names = %v", names)
	}
}

func TestFallbackHiddenUntilEverRegistered(t *testing.T) {
	d := newWatchDaemon(t)
	d.ever = false
	d.setRevision(0)
	s := startSession(t, registryFor(d))
	s.initialize("codex-mcp-client")
	names := s.listNames()
	if contains(names, "leo_app_tools") || contains(names, "leo_app_call") {
		t.Fatalf("fallback tools listed on a daemon no app ever registered with: %v", names)
	}

	// Sampled once: registration later in the session does not add them
	// (that first-registration case needs an agent restart).
	d.mu.Lock()
	d.ever = true
	d.mu.Unlock()
	if again := s.listNames(); contains(again, "leo_app_tools") {
		t.Fatalf("fallback tools appeared mid-session: %v", again)
	}

	// A new session samples the flag afresh.
	s2 := startSession(t, registryFor(d))
	s2.initialize("codex-mcp-client")
	if names := s2.listNames(); !contains(names, "leo_app_tools") {
		t.Fatalf("a fresh session missed the fallback tools: %v", names)
	}
}

func TestAppToolsListsRegisteredTools(t *testing.T) {
	d := newWatchDaemon(t)
	d.setRevision(3, "browser_click")
	reg := registryFor(d)
	reg.setClientName("codex-mcp-client")
	out := reg.callTool(context.Background(), "leo_app_tools", json.RawMessage(`{}`))
	if out.IsError {
		t.Fatalf("leo_app_tools failed: %s", out.Content)
	}
	text := outcomeText(t, out)
	if !strings.Contains(text, "browser_click") || !strings.Contains(text, `"input_schema"`) {
		t.Fatalf("leo_app_tools text = %s", text)
	}
}

func TestAppCallMatchesNativeCallContent(t *testing.T) {
	cd, client := newClientToolsDaemon(t)
	content := `[{"type":"text","text":"clicked e12"},{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}]`
	cd.callBody = `{"ok":true,"data":{"ok":true,"content":` + content + `}}`

	native := nativeRegistry(client, leotools.Permissions{})
	viaNative := native.callTool(context.Background(), "browser_click", json.RawMessage(`{"ref":"e12"}`))

	fallback := newRegistry(client, "alfred", leotools.Permissions{})
	fallback.setClientName("codex-mcp-client")
	viaApp := fallback.callTool(context.Background(), "leo_app_call", json.RawMessage(`{"tool":"browser_click","args":{"ref":"e12"}}`))

	if !bytes.Equal(viaApp.Content, viaNative.Content) || viaApp.IsError != viaNative.IsError {
		t.Fatalf("leo_app_call = %s, native = %s", viaApp.Content, viaNative.Content)
	}
	args, _ := cd.lastCall["args"].(map[string]any)
	if cd.lastCall["tool"] != "browser_click" || args["ref"] != "e12" {
		t.Fatalf("daemon saw %v", cd.lastCall)
	}
}

func TestAppCallRequiresTool(t *testing.T) {
	_, client := newClientToolsDaemon(t)
	reg := newRegistry(client, "alfred", leotools.Permissions{})
	reg.setClientName("codex-mcp-client")
	out := reg.callTool(context.Background(), "leo_app_call", json.RawMessage(`{}`))
	if !out.IsError || !strings.Contains(outcomeText(t, out), "tool") {
		t.Fatalf("outcome = %s", out.Content)
	}
}

func TestDeniedFallbackToolsAreHidden(t *testing.T) {
	d := newWatchDaemon(t)
	reg := newRegistry(d.client, "alfred", leotools.Permissions{DenyTools: []string{"leo_app_call"}})
	reg.setClientName("codex-mcp-client")
	names := toolNames(reg)
	if contains(names, "leo_app_call") || !contains(names, "leo_app_tools") {
		t.Fatalf("names = %v", names)
	}
}

func TestListChangedIgnoresRevisionResetWhenToolsMatch(t *testing.T) {
	d := newWatchDaemon(t)
	d.setRevision(7, "browser_click")
	s := startSession(t, registryFor(d))
	s.initialize("claude-code")
	if names := s.listNames(); !contains(names, "browser_click") {
		t.Fatalf("names = %v", names)
	}

	// The daemon restarted: its revision counts from zero again, but the apps
	// re-registered the same tools, so the harness has nothing to refresh.
	d.setRevision(1, "browser_click")
	d.pushChanged(1)
	d.waitLists(t, 3) // hello's refetch, the list, this change's refetch
	s.send("ping", nil)
	if m := s.recv(); m.Method != "" || string(m.Result) != "{}" {
		t.Fatalf("unexpected message before the ping reply: %+v", m)
	}

	// A different set under the very same revision number is news.
	d.setRevision(1, "browser_click", "browser_back")
	d.pushChanged(1)
	if m := s.recv(); m.Method != listChangedMethod {
		t.Fatalf("got %+v, want list_changed", m)
	}
}

func TestWatchWaitsForItsCoalescingGoroutineOnShutdown(t *testing.T) {
	d := newWatchDaemon(t)
	view := newClientToolsView(d.client, "alfred", leotools.Permissions{})
	if _, err := view.fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	view.defs(context.Background()) // the harness has listed revision 3
	d.setRevision(4, "browser_click", "browser_back")

	entered, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		view.watch(ctx, &wg, immediate, func() {
			close(entered)
			<-release // a notification mid-write when the session ends
		})
	}()
	<-entered
	cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		t.Fatal("the session finished while a notification was still being sent")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not stop")
	}
}

// offerReport is the body an MCP server reports what it offers with.
type offerReport struct {
	Token   string `json:"token"`
	Offered bool   `json:"offered"`
}

// reports is a copy of the offered reports so far.
func (d *watchDaemon) reports() []offerReport {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]offerReport(nil), d.offers...)
}

func (d *watchDaemon) setBrief(on bool, status int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.briefOn, d.briefStatus = on, status
}

func (d *watchDaemon) waitBriefAsks(t *testing.T, n int) {
	t.Helper()
	d.waitFor(t, fmt.Sprintf("%d brief config asks", n), func() bool { return d.briefAsks >= n })
}

func briefWatchSession(t *testing.T, d *watchDaemon, launchedOn bool) *session {
	t.Helper()
	reg := newRegistry(d.client, "alfred", leotools.Permissions{}, withAttentionToken("launch-tok"), withBrief(launchedOn))
	reg.watch.after = immediate
	s := startSession(t, reg)
	s.initialize("claude-code")
	return s
}

// A toggle made while the stream was down (or before it first connected, in
// the daemon's reload window) is not replayed: the switch is asked for on
// every hello, and a difference re-lists the tools.
func TestBriefSwitchMissedBeforeTheFirstConnectionIsReconciledOnHello(t *testing.T) {
	d := newWatchDaemon(t)
	d.setBrief(true, 0)
	s := briefWatchSession(t, d, false) // launched while briefs were off

	if m := s.recv(); m.Method != "notifications/tools/list_changed" || len(m.ID) != 0 {
		t.Fatalf("got %+v, want list_changed once the switch was reconciled", m)
	}
	if names := s.listNames(); !contains(names, "leo_brief") {
		t.Fatalf("leo_brief not offered after reconciling an on switch: %v", names)
	}
}

func TestBriefSwitchMissedWhileDisconnectedIsReconciledOnReconnect(t *testing.T) {
	d := newWatchDaemon(t)
	s := briefWatchSession(t, d, false)
	d.waitStreams(t, 1)
	d.waitBriefAsks(t, 1)
	s.send("ping", nil)
	if m := s.recv(); m.Method != "" || string(m.Result) != "{}" {
		t.Fatalf("a matching switch produced %+v before the ping reply", m)
	}

	d.setBrief(true, 0) // flipped while no stream was listening
	d.drop <- struct{}{}
	d.waitStreams(t, 2)

	if m := s.recv(); m.Method != "notifications/tools/list_changed" {
		t.Fatalf("got %+v, want list_changed after the reconnect", m)
	}
	if names := s.listNames(); !contains(names, "leo_brief") {
		t.Fatalf("leo_brief not offered after the reconnect: %v", names)
	}

	d.setBrief(false, 0)
	d.drop <- struct{}{}
	d.waitStreams(t, 3)
	if m := s.recv(); m.Method != "notifications/tools/list_changed" {
		t.Fatalf("got %+v, want list_changed for the flip off", m)
	}
	if names := s.listNames(); contains(names, "leo_brief") {
		t.Fatalf("leo_brief still offered after the flip off: %v", names)
	}
}

// A daemon that cannot answer leaves the switch as it was: the handler
// refusal is the backstop, and the next hello asks again.
func TestBriefSwitchReconcileFailureLeavesTheListAlone(t *testing.T) {
	d := newWatchDaemon(t)
	d.setBrief(true, http.StatusInternalServerError)
	s := briefWatchSession(t, d, false)
	d.waitBriefAsks(t, 1)

	s.send("ping", nil)
	if m := s.recv(); m.Method != "" || string(m.Result) != "{}" {
		t.Fatalf("a failed ask produced %+v before the ping reply", m)
	}
	if names := s.listNames(); contains(names, "leo_brief") {
		t.Fatalf("leo_brief offered after a failed ask: %v", names)
	}
}

// The daemon injects brief guidance only for a session whose MCP server says it
// offers leo_brief. That report must reach the daemon before the harness is
// told to re-list, so the first prompt after a flip already has the tool.
func TestBriefFlipIsReportedToTheDaemonBeforeTheHarnessRelists(t *testing.T) {
	d := newWatchDaemon(t)
	s := briefWatchSession(t, d, false)
	d.waitStreams(t, 1)
	d.waitBriefAsks(t, 1)
	if got := d.reports(); len(got) != 0 {
		t.Fatalf("reported %v while briefs were off and never offered", got)
	}

	d.setBrief(true, 0)
	d.pushBriefConfig(true)
	if m := s.recv(); m.Method != "notifications/tools/list_changed" {
		t.Fatalf("got %+v, want list_changed", m)
	}
	if got := d.reports(); len(got) != 1 || got[0] != (offerReport{Token: "launch-tok", Offered: true}) {
		t.Fatalf("reports at list_changed = %v, want one offered=true under the launch token", got)
	}

	d.setBrief(false, 0)
	d.pushBriefConfig(false)
	if m := s.recv(); m.Method != "notifications/tools/list_changed" {
		t.Fatalf("got %+v, want list_changed for the flip off", m)
	}
	if got := d.reports(); len(got) != 2 || got[1].Offered {
		t.Fatalf("reports after the flip off = %v, want offered=false last", got)
	}
}

// A daemon that restarts forgets what each session offers, so every
// (re)connection reports again while the tool is on.
func TestBriefOfferedIsReportedAgainOnReconnect(t *testing.T) {
	d := newWatchDaemon(t)
	d.setBrief(true, 0)
	s := briefWatchSession(t, d, true) // launched with briefs on
	d.waitStreams(t, 1)
	d.waitFor(t, "the first report", func() bool { return len(d.offers) >= 1 })
	s.send("ping", nil)
	_ = s.recv()

	d.drop <- struct{}{}
	d.waitStreams(t, 2)
	d.waitFor(t, "a report after the reconnect", func() bool { return len(d.offers) >= 2 })
}
