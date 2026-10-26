package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

const (
	// listChangedMethod is the MCP notification that tells a client to re-list tools.
	listChangedMethod = "notifications/tools/list_changed"

	// clientToolsCoalesce batches a burst of client_tools.changed events (an
	// app registering several groups) into one refetch and one notification.
	clientToolsCoalesce = time.Second

	watchBackoffMin = time.Second
	watchBackoffMax = 30 * time.Second
)

// clientToolsWatch is the list_changed machinery of one MCP session: the
// writer for notifications and the context the watcher lives under.
// runWith attaches it; a registry used without runWith never watches.
type clientToolsWatch struct {
	mu      sync.Mutex
	ctx     context.Context
	wg      *sync.WaitGroup
	notify  func(method string)
	started bool
	// after is the timer behind coalescing and backoff; tests inject one that fires at once.
	after func(time.Duration) <-chan time.Time
}

func (w *clientToolsWatch) attach(ctx context.Context, wg *sync.WaitGroup, notify func(string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ctx, w.wg, w.notify = ctx, wg, notify
}

func (w *clientToolsWatch) timer(d time.Duration) <-chan time.Time {
	if w.after != nil {
		return w.after(d)
	}
	return time.After(d)
}

// startWatch begins watching the daemon for client-tool changes once the
// session's client is known to honor list_changed. A client that does not
// gets the fixed fallback tools instead, which never change, so there is
// nothing to watch.
func (r *registry) startWatch() {
	w := &r.watch
	w.mu.Lock()
	defer w.mu.Unlock()
	if r.clientTools == nil || w.started || w.notify == nil || !r.isNativeClient() {
		return
	}
	w.started = true
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		r.clientTools.watch(w.ctx, w.timer, func() { w.notify(listChangedMethod) })
	}()
}

// watching reports whether this session sends list_changed: a client that
// honors it, served by a daemon, under a runWith that can write to it.
func (r *registry) watching() bool {
	w := &r.watch
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.started
}

// watch follows the daemon's event stream until ctx ends, calling changed
// whenever the client-tools revision differs from the one the harness last
// listed. A dropped stream is retried with backoff; each (re)connect refetches
// on hello, so a change missed while disconnected is still seen.
func (v *clientToolsView) watch(ctx context.Context, after func(time.Duration) <-chan time.Time, changed func()) {
	trigger := make(chan struct{}, 1)
	poke := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-trigger:
			}
			select {
			case <-ctx.Done():
				return
			case <-after(clientToolsCoalesce):
			}
			list, err := v.fetch(ctx)
			if err == nil && v.markChanged(list.Revision) {
				changed()
			}
		}
	}()

	backoff := watchBackoffMin
	for {
		connected := v.streamEvents(ctx, poke)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = watchBackoffMin
		}
		select {
		case <-ctx.Done():
			return
		case <-after(backoff):
		}
		backoff = min(backoff*2, watchBackoffMax)
	}
}

// markChanged records rev as the one the harness is now told about and
// reports whether that is news: it is not when the harness has not listed yet
// (its first list will see rev) or already has it.
func (v *clientToolsView) markChanged(rev uint64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.listed || rev == v.listedRev {
		return false
	}
	v.listedRev = rev
	return true
}

// streamEvents reads one SSE connection to the daemon, calling poke on hello
// and on every client_tools.changed; every other event is ignored. It reports
// whether the stream got as far as hello, and returns when it ends or ctx does.
func (v *clientToolsView) streamEvents(ctx context.Context, poke func()) (connected bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.client.baseURL+"/api/v1/events", nil)
	if err != nil {
		return false
	}
	v.client.setAuth(req)
	req.Header.Set("Accept", "text/event-stream")
	// The stream is open-ended: no client-wide timeout, only ctx.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}

	rd := bufio.NewReader(resp.Body)
	var event string
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return connected
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case line == "":
			switch event {
			case "hello":
				connected = true
				poke()
			case "client_tools.changed":
				poke()
			}
			event = ""
		}
	}
}

// fallbackListed reports whether the fixed fallback tools are listed. The
// daemon's ever_registered flag is sampled once, at the first successful
// listing, so the list a non-honoring client sees never changes mid-session
// (an app registering for the first time needs an agent restart to appear).
func (r *registry) fallbackListed(ctx context.Context) bool {
	r.clientMu.Lock()
	sampled, on := r.fallbackSampled, r.fallbackOn
	r.clientMu.Unlock()
	if sampled {
		return on
	}
	list, err := r.clientTools.fetch(ctx)
	if err != nil {
		return false
	}
	r.clientMu.Lock()
	defer r.clientMu.Unlock()
	if !r.fallbackSampled {
		r.fallbackSampled, r.fallbackOn = true, list.EverRegistered
	}
	return r.fallbackOn
}

// fallbackDefs are leo_app_tools and leo_app_call, minus any the template denies.
func (r *registry) fallbackDefs() []toolDef {
	defs := []toolDef{{
		Name:        leotools.AppToolsTool,
		Description: "List the tools connected apps (such as the Leo app's browser) currently offer this agent, with their input schemas. Call leo_app_call to use one.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, {
		Name:        leotools.AppCallTool,
		Description: "Call a tool offered by a connected app, by name, with arguments matching its schema from leo_app_tools. Results may include images.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool": map[string]any{"type": "string", "description": "Tool name from leo_app_tools."},
				"args": map[string]any{"type": "object", "description": "Arguments for the tool."},
			},
			"required": []string{"tool"},
		},
	}}
	out := defs[:0]
	for _, d := range defs {
		if !r.perms.DeniesTool(d.Name) {
			out = append(out, d)
		}
	}
	return out
}

// callFallback serves leo_app_tools and leo_app_call; ok is false for any
// other tool name.
func (r *registry) callFallback(ctx context.Context, name string, raw json.RawMessage) (toolOutcome, bool) {
	if r.clientTools == nil || (name != leotools.AppToolsTool && name != leotools.AppCallTool) {
		return toolOutcome{}, false
	}
	if r.perms.DeniesTool(name) {
		return textOutcome(fmt.Sprintf("tool %q is not permitted for this agent", name), true), true
	}
	if name == leotools.AppToolsTool {
		return r.appTools(ctx), true
	}
	return r.appCall(ctx, raw), true
}

func (r *registry) appTools(ctx context.Context) toolOutcome {
	list, err := r.clientTools.fetch(ctx)
	if err != nil {
		return textOutcome(err.Error(), true)
	}
	type entry struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	entries := make([]entry, 0, len(list.Tools))
	for _, t := range list.Tools {
		if !r.perms.DeniesTool(t.Name) {
			entries = append(entries, entry{t.Name, t.Description, t.InputSchema})
		}
	}
	out, err := json.Marshal(map[string]any{"tools": entries})
	if err != nil {
		return textOutcome(err.Error(), true)
	}
	return textOutcome(string(out), false)
}

func (r *registry) appCall(ctx context.Context, raw json.RawMessage) toolOutcome {
	var args struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return textOutcome(fmt.Sprintf("invalid arguments: %v", err), true)
		}
	}
	if args.Tool == "" {
		return textOutcome(`missing required argument "tool"`, true)
	}
	return r.clientTools.call(ctx, args.Tool, args.Args)
}
