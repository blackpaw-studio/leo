package observe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func wireFixture(t *testing.T, name string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "clienttools", "testdata", "wire", name))
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return v
}

func assertMatchesFixture(t *testing.T, name string, payload any) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if want := wireFixture(t, name); !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %s\nwant %v", name, b, want)
	}
}

func TestClientToolEventsEncodeGoldenWire(t *testing.T) {
	at := time.Date(2026, 10, 26, 15, 21, 1, 0, time.UTC)
	created := time.Date(2026, 10, 26, 15, 21, 2, 123_000_000, time.UTC)
	const reqID = "ctr_01JC8Z4A5B6C7D8E9F0G1H2J3K"
	request := ClientToolRequest{
		ID: reqID, Agent: "alfred", Group: "browser", Tool: "browser_click",
		Args: json.RawMessage(`{"ref":"e12"}`), Owner: "ct_01JC8Z3K4M5N6P7Q8R9S0T1V2W", TimeoutS: 30, CreatedAt: created,
	}

	t.Run("changed", func(t *testing.T) {
		assertMatchesFixture(t, "event-client-tools-changed.json", ClientToolsChangedPayload{
			Meta: Meta{Seq: 42, At: at}, Revision: 7, Tools: []string{"browser_click", "browser_navigate", "browser_snapshot"},
		})
	})
	t.Run("request", func(t *testing.T) {
		assertMatchesFixture(t, "event-client-tool-request.json", ClientToolRequestPayload{
			Meta: Meta{Seq: 43, At: created}, Request: request,
		})
	})
	t.Run("request redacted", func(t *testing.T) {
		assertMatchesFixture(t, "event-client-tool-request-redacted.json",
			ClientToolRequestPayload{Meta: Meta{Seq: 43, At: created}, Request: request}.Redacted())
	})
	t.Run("cancelled", func(t *testing.T) {
		assertMatchesFixture(t, "event-client-tool-cancelled.json", ClientToolCancelledPayload{
			Meta: Meta{Seq: 44, At: time.Date(2026, 10, 26, 15, 21, 32, 130_000_000, time.UTC)},
			ID:   reqID, Agent: "alfred", Reason: CancelTimeout,
		})
	})
	t.Run("cancelled redacted", func(t *testing.T) {
		assertMatchesFixture(t, "event-client-tool-cancelled-redacted.json", ClientToolCancelledPayload{
			Meta: Meta{Seq: 44, At: time.Date(2026, 10, 26, 15, 21, 32, 130_000_000, time.UTC)},
			ID:   reqID, Agent: "alfred", Reason: CancelTimeout,
		}.Redacted())
	})
	t.Run("hello", func(t *testing.T) {
		assertMatchesFixture(t, "hello-provider.json", HelloPayload{
			Meta: Meta{Seq: 41, At: time.Date(2026, 10, 26, 15, 21, 0, 0, time.UTC)}, Version: 1,
			ServerTime: time.Date(2026, 10, 26, 15, 21, 0, 0, time.UTC), BootID: "b7f3c2a91d4e5f60",
			Features: Features(), ClientID: "ct_01JC8Z3K4M5N6P7Q8R9S0T1V2W",
		})
	})
}

func TestHelloOmitsClientIDOnPlainStreams(t *testing.T) {
	b, err := json.Marshal(HelloPayload{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["client_id"]; ok {
		t.Fatalf("plain hello carries client_id: %s", b)
	}
}
