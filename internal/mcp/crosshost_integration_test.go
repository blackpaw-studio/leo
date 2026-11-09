package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/leotools"
	"github.com/blackpaw-studio/leo/internal/mcp"
	"github.com/blackpaw-studio/leo/internal/web"
)

// Cross-host messaging end to end: two real daemons' web servers, each the
// other's api_clients receiver and client.hosts peer, with the real MCP tool
// registry as the sender. Only the final delivery into an agent's pane is
// replaced, by a capturing harness driver.

const captureHarnessName = "xhost-capture"

func init() { harness.Register(captureHarness{}) }

// delivery is one message that reached an agent's session driver.
type delivery struct{ Agent, Text string }

var (
	deliveriesMu sync.Mutex
	deliveries   = map[string][]delivery{} // keyed by receiving node
)

// captureHarness is a harness whose driver records what it is asked to inject.
// Only Name and Driver are ever called on it.
type captureHarness struct{ harness.Harness }

func (captureHarness) Name() string                  { return captureHarnessName }
func (captureHarness) Driver() harness.SessionDriver { return captureDriver{} }

type captureDriver struct{ harness.SessionDriver }

func (captureDriver) Inject(_ context.Context, h harness.SessionHandle, msg string) (*harness.Result, error) {
	deliveriesMu.Lock()
	defer deliveriesMu.Unlock()
	node := h.Workspace // the node that owns the agent (see stubAgents)
	deliveries[node] = append(deliveries[node], delivery{Agent: h.Name, Text: msg})
	return nil, nil
}

func deliveredTo(node string) []delivery {
	deliveriesMu.Lock()
	defer deliveriesMu.Unlock()
	return append([]delivery(nil), deliveries[node]...)
}

// stubAgents is the receiving node's agent service: the two methods the
// message and discovery routes use. Everything else is a nil embed.
type stubAgents struct {
	web.AgentService
	node   string
	agents map[string]string // name -> status
}

func (s stubAgents) List() []agent.Record {
	out := []agent.Record{}
	for name, status := range s.agents {
		out = append(out, agent.Record{Name: name, Status: status, Workspace: "/private/" + name})
	}
	return out
}

func (s stubAgents) ResolveHandle(name string) (string, harness.SessionHandle, bool) {
	if _, ok := s.agents[name]; !ok {
		return "", harness.SessionHandle{}, false
	}
	return captureHarnessName, harness.SessionHandle{Name: name, Workspace: s.node}, true
}

// node is one host: its web server (the receiver side) and its config file
// (the sender side's client.hosts).
type node struct {
	name       string
	ts         *httptest.Server
	port       string
	cfgPath    string
	agentToken string
}

func newNode(t *testing.T, name string) *node {
	t.Helper()
	ts := httptest.NewUnstartedServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)
	u, err := url.Parse("http://" + ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return &node{name: name, ts: ts, port: u.Port(), cfgPath: filepath.Join(t.TempDir(), "leo.yaml"), agentToken: name + "-agent-token"}
}

func (n *node) url() string { return "http://" + n.ts.Listener.Addr().String() }

type peerLink struct {
	to       *node
	token    string // what this node presents to `to`
	asClient string // this node's api_clients name on `to`
}

// writeConfig sets this node's client.hosts: one entry per peer it can message.
func (n *node) writeConfig(t *testing.T, links []peerLink) {
	t.Helper()
	var b strings.Builder
	b.WriteString("client:\n  hosts:\n")
	for _, l := range links {
		fmt.Fprintf(&b, "    %s:\n      api_url: %s\n      api_token: %s\n      api_client: %s\n", l.to.name, l.to.url(), l.token, l.asClient)
	}
	if err := os.WriteFile(n.cfgPath, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// serve starts the node's web server: clients are who may message in, agents
// what runs here.
func (n *node) serve(t *testing.T, clients []web.ClientPolicy, agents map[string]string) {
	t.Helper()
	port, err := strconv.Atoi(n.port)
	if err != nil {
		t.Fatal(err)
	}
	srv := web.New(n.cfgPath, nil, nil, nil, stubAgents{node: n.name, agents: agents}, web.Options{
		Port: port, APIToken: n.name + "-operator-token", AgentToken: n.agentToken, Clients: clients,
	})
	n.ts.Config.Handler = srv.Handler()
	n.ts.Start()
}

// wire records every request the MCP process sends to a peer.
type wire struct {
	mu   sync.Mutex
	reqs []wireReq
}

type wireReq struct{ Method, URL, Auth, Body string }

func (w *wire) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
	}
	w.mu.Lock()
	w.reqs = append(w.reqs, wireReq{r.Method, r.URL.String(), r.Header.Get("Authorization"), string(body)})
	w.mu.Unlock()
	return http.DefaultTransport.RoundTrip(r)
}

func (w *wire) all() []wireReq {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]wireReq(nil), w.reqs...)
}

const (
	tokDionysusToLaptop = "tok-dionysus-to-laptop-31f9"
	tokLaptopToDionysus = "tok-laptop-to-dionysus-8ac2"
)

// pair builds Dionysus and the laptop as peers of one another. On the laptop,
// "dionysus" may message only rocket; on Dionysus, "laptop" only scout.
func pair(t *testing.T) (dionysus, laptop *node) {
	t.Helper()
	deliveriesMu.Lock()
	deliveries = map[string][]delivery{} // each test starts with nothing delivered
	deliveriesMu.Unlock()
	dionysus, laptop = newNode(t, "dionysus"), newNode(t, "laptop")
	dionysus.writeConfig(t, []peerLink{{to: laptop, token: tokDionysusToLaptop, asClient: "dionysus"}})
	laptop.writeConfig(t, []peerLink{{to: dionysus, token: tokLaptopToDionysus, asClient: "laptop"}})
	dionysus.serve(t,
		[]web.ClientPolicy{{Name: "laptop", Token: tokLaptopToDionysus, CanMessage: []string{"scout"}, Peer: true}},
		map[string]string{"leo-scout": "running", "leo-infra": "running"})
	laptop.serve(t,
		[]web.ClientPolicy{{Name: "dionysus", Token: tokDionysusToLaptop, CanMessage: []string{"rocket"}, Peer: true}},
		map[string]string{"leo-rocket": "running", "leo-diary": "running"})
	return dionysus, laptop
}

func (n *node) mcp(process string, perms leotools.Permissions, w *wire) *mcp.TestHarness {
	var peers http.RoundTripper // stays a nil interface when no capture is wanted
	if w != nil {
		peers = w
	}
	return mcp.NewTestHarness(n.port, n.agentToken, process, perms, peers)
}

func TestCrossHost_SendReplyAndDiscovery(t *testing.T) {
	dionysus, laptop := pair(t)
	scoutWire := &wire{}
	scout := dionysus.mcp("scout", leotools.Permissions{}, scoutWire)

	t.Run("rocket@laptop is delivered with the exact request", func(t *testing.T) {
		out, err := scout.Call("leo_send_message", map[string]any{"to": "rocket@laptop", "message": "hi"})
		if err != nil || out != "Sent message to rocket@laptop" {
			t.Fatalf("send: %q, %v", out, err)
		}
		want := wireReq{
			Method: "POST",
			URL:    laptop.url() + "/web/agent/rocket/message",
			Auth:   "Bearer " + tokDionysusToLaptop,
			Body:   `{"from":"dionysus#scout","text":"hi"}`,
		}
		if got := scoutWire.all(); len(got) != 1 || got[0] != want {
			t.Fatalf("wire:\n got %+v\nwant [%+v]", got, want)
		}
		got := deliveredTo("laptop")
		if len(got) != 1 || got[0] != (delivery{Agent: "leo-rocket", Text: "[message from scout@dionysus] hi"}) {
			t.Fatalf("laptop deliveries = %+v", got)
		}
	})

	t.Run("the recipient replies to the exact sender string", func(t *testing.T) {
		rocket := laptop.mcp("rocket", leotools.Permissions{}, &wire{})
		if out, err := rocket.Call("leo_send_message", map[string]any{"to": "scout@dionysus", "message": "got it"}); err != nil {
			t.Fatalf("reply: %q, %v", out, err)
		}
		got := deliveredTo("dionysus")
		if len(got) != 1 || got[0] != (delivery{Agent: "leo-scout", Text: "[message from rocket@laptop] got it"}) {
			t.Fatalf("dionysus deliveries = %+v", got)
		}
	})

	t.Run("an agent outside can_message is denied and nothing is delivered", func(t *testing.T) {
		before := len(deliveredTo("laptop"))
		_, err := scout.Call("leo_send_message", map[string]any{"to": "diary@laptop", "message": "psst"})
		if err == nil || !strings.Contains(err.Error(), "not permitted") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), tokDionysusToLaptop) {
			t.Fatalf("error leaks the token: %v", err)
		}
		if after := len(deliveredTo("laptop")); after != before {
			t.Fatalf("a denied message was delivered")
		}
	})

	t.Run("a discovered name sends straight back, and the stored agent receives it", func(t *testing.T) {
		out, err := scout.Call("leo_list_agents", map[string]any{"host": "laptop"})
		if err != nil {
			t.Fatal(err)
		}
		var listed []struct{ Name string }
		if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed) != 1 {
			t.Fatalf("listing %q: %v", out, err)
		}
		before := len(deliveredTo("laptop"))
		if _, err := scout.Call("leo_send_message", map[string]any{"to": listed[0].Name + "@laptop", "message": "round trip"}); err != nil {
			t.Fatalf("send to discovered name %q: %v", listed[0].Name, err)
		}
		got := deliveredTo("laptop")
		if len(got) != before+1 || got[before] != (delivery{Agent: "leo-rocket", Text: "[message from scout@dionysus] round trip"}) {
			t.Fatalf("deliveries = %+v", got)
		}
	})

	t.Run("a bare name still goes to the local daemon, not a peer", func(t *testing.T) {
		before := len(scoutWire.all())
		_, _ = scout.Call("leo_send_message", map[string]any{"to": "infra", "message": "local"})
		if len(scoutWire.all()) != before {
			t.Fatal("a bare agent name reached a peer host")
		}
	})

	t.Run("listing a peer shows only what its token may message, name and status", func(t *testing.T) {
		out, err := scout.Call("leo_list_agents", map[string]any{"host": "laptop"})
		if err != nil {
			t.Fatal(err)
		}
		var got []map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("decode %q: %v", out, err)
		}
		if len(got) != 1 || len(got[0]) != 2 || got[0]["name"] != "leo-rocket" || got[0]["status"] != "running" {
			t.Fatalf("listing = %s", out)
		}
		if strings.Contains(out, "diary") || strings.Contains(out, "/private") {
			t.Fatalf("listing leaks beyond can_message: %s", out)
		}
	})
}

func TestCrossHost_FailureModes(t *testing.T) {
	t.Run("a wrong token is a 401 that never echoes the token", func(t *testing.T) {
		dionysus, laptop := pair(t)
		dionysus.writeConfig(t, []peerLink{{to: laptop, token: "not-the-token-0000", asClient: "dionysus"}})
		_, err := dionysus.mcp("scout", leotools.Permissions{}, nil).Call("leo_send_message", map[string]any{"to": "rocket@laptop", "message": "hi"})
		if err == nil || !strings.Contains(err.Error(), "rejected the api_token") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), "not-the-token-0000") || strings.Contains(err.Error(), tokDionysusToLaptop) {
			t.Fatalf("error leaks a token: %v", err)
		}
		if len(deliveredTo("laptop")) != 0 {
			t.Fatal("a rejected message was delivered")
		}
	})

	t.Run("a peer that is down is unreachable", func(t *testing.T) {
		dionysus, laptop := pair(t)
		laptop.ts.Close()
		_, err := dionysus.mcp("scout", leotools.Permissions{}, nil).Call("leo_send_message", map[string]any{"to": "rocket@laptop", "message": "hi"})
		if err == nil || !strings.Contains(err.Error(), "host laptop unreachable at "+laptop.url()) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an unknown host names it", func(t *testing.T) {
		dionysus, _ := pair(t)
		_, err := dionysus.mcp("scout", leotools.Permissions{}, nil).Call("leo_send_message", map[string]any{"to": "rocket@nowhere", "message": "hi"})
		if err == nil || !strings.Contains(err.Error(), `"nowhere"`) {
			t.Fatalf("err = %v", err)
		}
	})
}
