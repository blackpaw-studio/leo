package clienttools

import (
	"context"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/observe"
)

func newTestRelay(t *testing.T) (*Relay, *recordingPublisher, *countingStore) {
	t.Helper()
	pub := newRecordingPublisher()
	store := &countingStore{state: FlagState{EverRegistered: true, Tools: []string{"old_tool"}}}
	r := NewRelay(RelayOptions{
		Registry: RegistryOptions{IsBuiltin: func(n string) bool { return n == "leo_chat_post" }},
		Flag:     NewEverRegistered(store, nil),
		Publish:  pub,
	})
	return r, pub, store
}

func TestRelayRegisterPublishesChangedAndRecordsNames(t *testing.T) {
	r, pub, store := newTestRelay(t)
	id := r.Connect()
	rev, err := r.Register(id, "browser", []Tool{tool("browser_click"), tool("browser_back")})
	if err != nil || rev != 1 {
		t.Fatalf("Register: rev=%d err=%v", rev, err)
	}
	ev := pub.next(t, observe.EventClientToolsChanged).Payload.(*observe.ClientToolsChangedPayload)
	if ev.Revision != 1 || !reflect.DeepEqual(ev.Tools, []string{"browser_back", "browser_click"}) {
		t.Fatalf("changed = %+v", ev)
	}
	if got := store.state.Tools; !reflect.DeepEqual(got, []string{"browser_back", "browser_click", "old_tool"}) {
		t.Fatalf("persisted = %v", got)
	}

	// An identical second provider changes nothing, so nothing is announced.
	other := r.Connect()
	if _, err := r.Register(other, "browser", []Tool{tool("browser_click"), tool("browser_back")}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-pub.signal:
		t.Fatalf("unexpected event %s", ev.Type)
	default:
	}
}

func TestRelayListReportsFlagAndTools(t *testing.T) {
	r, _, _ := newTestRelay(t)
	if got := r.List(); !got.EverRegistered || len(got.Tools) != 0 {
		t.Fatalf("empty relay list = %+v", got)
	}
	id := r.Connect()
	if _, err := r.Register(id, "browser", []Tool{tool("browser_click")}); err != nil {
		t.Fatal(err)
	}
	if got := r.List(); got.Revision != 1 || len(got.Tools) != 1 || got.Tools[0].Group != "browser" {
		t.Fatalf("list = %+v", got)
	}
}

func TestRelayKnownNamesCoverRegisteredAndPersisted(t *testing.T) {
	r, _, _ := newTestRelay(t)
	if !r.IsKnownName("old_tool") {
		t.Fatal("a persisted name is unknown after restart")
	}
	if r.IsKnownName("browser_click") {
		t.Fatal("unregistered name is known")
	}
	id := r.Connect()
	if _, err := r.Register(id, "browser", []Tool{tool("browser_click")}); err != nil {
		t.Fatal(err)
	}
	if !r.IsKnownName("browser_click") {
		t.Fatal("registered name unknown")
	}
	r.Disconnect(id)
	if !r.IsKnownName("browser_click") {
		t.Fatal("a withdrawn name must stay known (persisted)")
	}
}

func TestRelayDisconnectWithdrawsFailsParkedCallsAndAnnounces(t *testing.T) {
	r, pub, _ := newTestRelay(t)
	id := r.Connect()
	if _, err := r.Register(id, "browser", []Tool{tool("browser_click")}); err != nil {
		t.Fatal(err)
	}
	pub.next(t, observe.EventClientToolsChanged)

	out := make(chan callOutcome, 1)
	go func() {
		res, err := r.Call(context.Background(), "alfred", "browser_click", nil)
		out <- callOutcome{res, err}
	}()
	req := pub.next(t, observe.EventClientToolRequest).Payload.(*observe.ClientToolRequestPayload).Request
	if _, err := r.Claim(req.ID, id); err != nil {
		t.Fatal(err)
	}

	r.Disconnect(id)

	o := wait(t, out)
	if o.res.OK || o.res.Error == nil || o.res.Error.Code != CodeNoClient {
		t.Fatalf("outcome = %+v", o)
	}
	ev := pub.next(t, observe.EventClientToolsChanged).Payload.(*observe.ClientToolsChangedPayload)
	if ev.Revision != 2 || len(ev.Tools) != 0 {
		t.Fatalf("changed after withdraw = %+v", ev)
	}
	if got := r.List().Tools; len(got) != 0 {
		t.Fatalf("tools survive disconnect: %+v", got)
	}
}

func TestProviderStreamAdapterConnectsAndDisconnects(t *testing.T) {
	r, _, _ := newTestRelay(t)
	p := r.Provider()
	id := p.Connect()
	if !r.ClientExists(ClientID(id)) {
		t.Fatalf("client %q not connected", id)
	}
	p.Disconnect(id)
	if r.ClientExists(ClientID(id)) {
		t.Fatal("client survives Disconnect")
	}
}
