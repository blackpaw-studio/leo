package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/chat"
	"github.com/blackpaw-studio/leo/internal/devices"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// chatDeliveries records what the fake deliverer was handed.
type chatDeliveries struct {
	mu      sync.Mutex
	prompts []string
}

func (d *chatDeliveries) Deliver(_ context.Context, _, prompt, _ string, _ func()) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prompts = append(d.prompts, prompt)
	return "claude", nil
}

// eventTap forwards chat events to a channel.
type eventTap struct{ ch chan observe.Event }

func (e eventTap) Publish(ev observe.Event) { e.ch <- ev }

type chatServer struct {
	s       *Server
	deliver *chatDeliveries
	events  chan observe.Event
	svc     *mockAgentService
}

// newChatServer is a token-split server (auth exercised for real) with chat
// wired to a fake deliverer and a tiny upload cap.
func newChatServer(t *testing.T) chatServer {
	t.Helper()
	s := newTokenSplitServer(t)
	svc := s.agentSvc.(*mockAgentService)
	svc.records = []agent.Record{{Name: "assistant", Workspace: "/ws"}}
	store, err := chat.OpenStore(filepath.Join(t.TempDir(), "chat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cs := chatServer{s: s, deliver: &chatDeliveries{}, events: make(chan observe.Event, 64), svc: svc}
	s.chat = chat.NewService(chat.Options{
		Store:   store,
		Files:   chat.Files{Root: t.TempDir(), MaxBytes: 64},
		Deliver: cs.deliver,
		Publish: eventTap{cs.events},
		Agents:  s.chatAgents,
		Config:  s.loadConfig,
	})
	t.Cleanup(s.chat.Close)
	return cs
}

func chatRequest(t *testing.T, s *Server, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Host = testHost
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	return w
}

func decodeChatData(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	var env struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decoding %s: %v", w.Body.String(), err)
	}
	if !env.OK {
		t.Fatalf("not ok: %s", env.Error)
	}
	if v != nil {
		if err := json.Unmarshal(env.Data, v); err != nil {
			t.Fatalf("decoding data %s: %v", env.Data, err)
		}
	}
}

func TestChatAppRoutesTakeOperatorOrDeviceTokensOnly(t *testing.T) {
	cs := newChatServer(t)
	store := devices.NewStore(filepath.Join(t.TempDir(), "state"), nil)
	cs.s.devices = store
	code, _, err := store.NewPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	_, deviceToken, err := store.Pair(code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token string
		want  int
	}{{"", http.StatusUnauthorized}, {testAgentToken, http.StatusForbidden}, {testAPIToken, http.StatusOK}, {deviceToken, http.StatusOK}} {
		if w := chatRequest(t, cs.s, http.MethodGet, "/api/v1/chat/threads", tc.token, ""); w.Code != tc.want {
			t.Errorf("token %q: status %d, want %d (%s)", tc.token, w.Code, tc.want, w.Body)
		}
	}
}

func TestChatSendThenPage(t *testing.T) {
	cs := newChatServer(t)
	w := chatRequest(t, cs.s, http.MethodPost, "/api/v1/chat/assistant/messages", testAPIToken, `{"text":"hello"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("send = %d %s", w.Code, w.Body)
	}
	var sent chat.Message
	decodeChatData(t, w, &sent)
	if sent.State != chat.StatePending || sent.Text != "hello" {
		t.Fatalf("sent = %+v", sent)
	}
	cs.s.chat.Flush()
	if len(cs.deliver.prompts) != 1 || cs.deliver.prompts[0] != "[Leo Chat] hello" {
		t.Fatalf("delivered %q", cs.deliver.prompts)
	}
	w = chatRequest(t, cs.s, http.MethodGet, "/api/v1/chat/assistant/messages?limit=10", testAPIToken, "")
	var page chat.PageResult
	decodeChatData(t, w, &page)
	if len(page.Messages) != 1 || page.Messages[0].ID != sent.ID || page.Messages[0].State != chat.StateDelivered || page.HasMore {
		t.Fatalf("page = %+v", page)
	}
	if !strings.Contains(w.Body.String(), `"has_more":false`) {
		t.Fatalf("page body = %s", w.Body)
	}
	if w := chatRequest(t, cs.s, http.MethodPost, "/api/v1/chat/ghost/messages", testAPIToken, `{"text":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", w.Code)
	}
	if w := chatRequest(t, cs.s, http.MethodGet, "/api/v1/chat/assistant/messages?limit=x", testAPIToken, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad limit = %d", w.Code)
	}
	if w := chatRequest(t, cs.s, http.MethodPost, "/api/v1/chat/assistant/read", testAPIToken, `{"up_to":"`+sent.ID+`"}`); w.Code != http.StatusOK {
		t.Fatalf("read = %d %s", w.Code, w.Body)
	}
}

func multipartUpload(t *testing.T, agentName, filename, contentType string, body []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if agentName != "" {
		_ = mw.WriteField("agent", agentName)
	}
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{`form-data; name="file"; filename="` + filename + `"`}
	h["Content-Type"] = []string{contentType}
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(body)
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func upload(t *testing.T, s *Server, agentName, filename, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	buf, ct := multipartUpload(t, agentName, filename, contentType, body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/uploads", buf)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+testAPIToken)
	req.Host = testHost
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	return w
}

func TestChatUploadVoiceAndServeWithRange(t *testing.T) {
	cs := newChatServer(t)
	w := upload(t, cs.s, "", "../../note.m4a", "audio/mp4", []byte("0123456789"))
	if w.Code != http.StatusOK {
		t.Fatalf("upload = %d %s", w.Code, w.Body)
	}
	var att chat.Attachment
	decodeChatData(t, w, &att)
	if att.Filename != "note.m4a" || att.MIME != "audio/mp4" || att.Bytes != 10 {
		t.Fatalf("attachment = %+v", att)
	}
	w = chatRequest(t, cs.s, http.MethodPost, "/api/v1/chat/assistant/messages", testAPIToken, `{"attachment_ids":["`+att.ID+`"]}`)
	var sent chat.Message
	decodeChatData(t, w, &sent)
	if sent.Kind != chat.KindVoice || len(sent.Attachments) != 1 {
		t.Fatalf("voice message = %+v", sent)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/chat/attachments/"+att.ID, nil)
	req.Header.Set("Authorization", "Bearer "+testAPIToken)
	req.Header.Set("Range", "bytes=2-4")
	req.Host = testHost
	rec := httptest.NewRecorder()
	cs.s.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "234" || rec.Header().Get("Content-Type") != "audio/mp4" {
		t.Fatalf("range = %d %q %s", rec.Code, rec.Body, rec.Header())
	}
	if w := chatRequest(t, cs.s, http.MethodGet, "/api/v1/chat/attachments/01AAAAAAAAAAAAAAAAAAAAAAAA", testAPIToken, ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown attachment = %d", w.Code)
	}
}

func TestChatUploadRejections(t *testing.T) {
	cs := newChatServer(t)
	if w := upload(t, cs.s, "ghost", "a.txt", "text/plain", []byte("x")); w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", w.Code)
	}
	if w := upload(t, cs.s, "assistant", "big.bin", "application/octet-stream", bytes.Repeat([]byte("x"), 65)); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over cap = %d %s", w.Code, w.Body)
	}
}

func TestChatUploadBodyMayExceedTheGlobalCap(t *testing.T) {
	var got int64
	h := bodySizeMiddleware(4, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		got = n
	}), bodyLimit{path: chatUploadPath, max: 16})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, chatUploadPath, strings.NewReader("0123456789")))
	if got != 10 {
		t.Fatalf("upload read %d bytes, want 10", got)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/chat/assistant/messages", strings.NewReader("0123456789")))
	if got != 4 {
		t.Fatalf("other routes keep the global cap: read %d", got)
	}
}

func waitEvent(t *testing.T, events chan observe.Event, kind string) chat.Message {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if p, ok := ev.Payload.(*chat.MessagePayload); ok && p.Message.Kind == kind {
				return p.Message
			}
		case <-timeout:
			t.Fatalf("no %s message published", kind)
		}
	}
}

func TestChatAskAnsweredOverHTTP(t *testing.T) {
	cs := newChatServer(t)
	type result struct {
		code int
		body string
	}
	done := make(chan result, 1)
	go func() {
		w := chatRequest(t, cs.s, http.MethodPost, "/api/agent/assistant/chat/ask", testAgentToken, `{"question":"Deploy?","options":[{"value":"y","label":"Yes"},{"value":"n","label":"No"}],"timeout_s":60}`)
		done <- result{w.Code, w.Body.String()}
	}()
	ask := waitEvent(t, cs.events, chat.KindAsk)
	if w := chatRequest(t, cs.s, http.MethodPost, "/api/v1/chat/assistant/messages/"+ask.ID+"/answer", testAPIToken, `{"value":"nope"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad answer = %d", w.Code)
	}
	if w := chatRequest(t, cs.s, http.MethodPost, "/api/v1/chat/assistant/messages/"+ask.ID+"/answer", testAPIToken, `{"value":"y"}`); w.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", w.Code, w.Body)
	}
	got := <-done
	if got.code != http.StatusOK || !strings.Contains(got.body, `"value":"y"`) || !strings.Contains(got.body, `"label":"Yes"`) {
		t.Fatalf("ask = %d %s", got.code, got.body)
	}
	if w := chatRequest(t, cs.s, http.MethodPost, "/api/v1/chat/assistant/messages/"+ask.ID+"/answer", testAPIToken, `{"value":"n"}`); w.Code != http.StatusConflict {
		t.Fatalf("second answer = %d", w.Code)
	}
}

func TestChatToolRoutes(t *testing.T) {
	cs := newChatServer(t)
	if w := chatRequest(t, cs.s, http.MethodPost, "/api/agent/assistant/chat/post", testAgentToken, `{"text":"hi","dispatch_id":"d-1"}`); w.Code != http.StatusForbidden {
		t.Fatalf("dispatch subagent post = %d", w.Code)
	}
	w := chatRequest(t, cs.s, http.MethodPost, "/api/agent/assistant/chat/post", testAgentToken, `{"text":"build is green"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("post = %d %s", w.Code, w.Body)
	}
	w = chatRequest(t, cs.s, http.MethodPost, "/api/agent/assistant/chat/history", testAgentToken, `{"limit":5}`)
	var hist chat.PageResult
	decodeChatData(t, w, &hist)
	if len(hist.Messages) != 1 || hist.Messages[0].Text != "build is green" {
		t.Fatalf("history = %+v", hist)
	}
	if w := chatRequest(t, cs.s, http.MethodPost, "/api/agent/ghost/chat/post", testAgentToken, `{"text":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown caller = %d %s", w.Code, w.Body)
	}
}

func TestAgentPermissionDecidedFromChat(t *testing.T) {
	cs := newChatServer(t)
	cs.svc.handles = map[string]resolvedHandle{"assistant": {harnessName: "claude"}}
	cs.s.attention = observe.NewAttentionStore(nil)
	cs.s.attention.RegisterToken("tok-a", "assistant")
	done := make(chan string, 1)
	go func() {
		w := chatRequest(t, cs.s, http.MethodPost, "/api/agent/permission", testAgentToken,
			`{"token":"tok-a","payload":{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"make deploy"}}}`)
		done <- w.Body.String()
	}()
	perm := waitEvent(t, cs.events, chat.KindPermission)
	if perm.Permission == nil || perm.Permission.Summary != "make deploy" {
		t.Fatalf("permission = %+v", perm)
	}
	if w := chatRequest(t, cs.s, http.MethodPost, "/api/v1/chat/assistant/messages/"+perm.ID+"/answer", testAPIToken, `{"value":"deny"}`); w.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", w.Code, w.Body)
	}
	if got := <-done; !strings.Contains(got, `"behavior":"deny"`) {
		t.Fatalf("hook reply = %s", got)
	}
	// An unknown token gets no decision at once.
	w := chatRequest(t, cs.s, http.MethodPost, "/api/agent/permission", testAgentToken, `{"token":"nope","payload":{"tool_name":"Bash"}}`)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "behavior") {
		t.Fatalf("unknown token = %d %s", w.Code, w.Body)
	}
}

func TestChatSocketHandlerServesTheAppRoutes(t *testing.T) {
	cs := newChatServer(t)
	w := httptest.NewRecorder()
	cs.s.ChatHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/chat/assistant/messages", strings.NewReader(`{"text":"from the mac"}`)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("socket send = %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	cs.s.ChatHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/chat/threads", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `{"threads":[{"agent":"assistant","last_message":{`) {
		t.Fatalf("socket threads = %d %s", w.Code, w.Body)
	}
}

func TestChatDisabledAnswers503(t *testing.T) {
	s := newTokenSplitServer(t)
	if w := chatRequest(t, s, http.MethodGet, "/api/v1/chat/threads", testAPIToken, ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestChatDelivererStartsAStoppedAgentThenInjects(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	var injected []string
	s.injectPrompt = func(_ context.Context, session, body string) error {
		injected = append(injected, session+"|"+body)
		return nil
	}
	started := false
	h, err := chatDeliverer{s}.Deliver(context.Background(), "web", "[Leo Chat] hi", "chat-1", func() { started = true })
	if err != nil {
		t.Fatal(err)
	}
	if !started || !svc.startCalled || svc.startName != "web" {
		t.Fatalf("starting=%v startCalled=%v name=%q", started, svc.startCalled, svc.startName)
	}
	if h != "claude" || len(injected) != 1 || injected[0] != "leo-web|[Leo Chat] hi" {
		t.Fatalf("harness %q injected %q", h, injected)
	}
}

// An agent started by someone else between the live check and Start is not
// a failure: the prompt still goes in once it is ready.
func TestChatDelivererAgentStartedMeanwhileStillDelivers(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.startErr = fmt.Errorf("%w: %q", agent.ErrAgentAlreadyRunning, "web")
	var injected []string
	s.injectPrompt = func(_ context.Context, session, body string) error {
		injected = append(injected, session+"|"+body)
		return nil
	}
	h, err := chatDeliverer{s}.Deliver(context.Background(), "web", "[Leo Chat] hi", "chat-1", func() {})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if h != "claude" || len(injected) != 1 || injected[0] != "leo-web|[Leo Chat] hi" {
		t.Fatalf("harness %q injected %q", h, injected)
	}
}

func TestChatDelivererLiveAgentUsesPeerInbox(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.resolvePeerSocket = func(context.Context, string) (string, error) { return "/tmp/peer.sock", nil }
	var got string
	s.deliverPeer = func(_ context.Context, _ string, text string) error {
		got = text
		return nil
	}
	if _, err := (chatDeliverer{s}).Deliver(context.Background(), "assistant", "[Leo Chat] live", "chat-1", func() { t.Error("a live agent is not started") }); err != nil {
		t.Fatal(err)
	}
	if got != "[Leo Chat] live" {
		t.Fatalf("peer delivery %q", got)
	}
}

// A bridged agent gets the prompt under the chat message's ref, the id its
// outbox keeps it by across a daemon restart.
func TestChatDelivererBridgedAgentDeliversUnderTheRef(t *testing.T) {
	s, _ := newTestServer(t)
	b := newBridgedAgent(t, s, "assistant", true)
	done := make(chan error, 1)
	go func() {
		_, err := chatDeliverer{s}.Deliver(context.Background(), "assistant", "[Leo Chat] hi", "chat-01ABC", func() {})
		done <- err
	}()
	cmd := b.next()
	if cmd.ID != "chat-01ABC" || cmd.Text != "[Leo Chat] hi" || !cmd.AsUser {
		t.Fatalf("deliver = %+v", cmd)
	}
	b.ack(cmd, true, "")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestChatAgentNamedAttachmentsKeepsItsThread(t *testing.T) {
	cs := newChatServer(t)
	cs.svc.records = append(cs.svc.records, agent.Record{Name: "attachments", Workspace: "/ws2"})
	if w := chatRequest(t, cs.s, http.MethodPost, "/api/v1/chat/attachments/messages", testAPIToken, `{"text":"hi"}`); w.Code != http.StatusAccepted {
		t.Fatalf("send = %d %s", w.Code, w.Body)
	}
	w := chatRequest(t, cs.s, http.MethodGet, "/api/v1/chat/attachments/messages", testAPIToken, "")
	var page chat.PageResult
	decodeChatData(t, w, &page)
	if len(page.Messages) != 1 || page.Messages[0].Agent != "attachments" {
		t.Fatalf("page = %+v", page)
	}
	// Attachment downloads share the pattern and still resolve.
	var att chat.Attachment
	decodeChatData(t, upload(t, cs.s, "attachments", "a.txt", "text/plain", []byte("x")), &att)
	if w := chatRequest(t, cs.s, http.MethodGet, "/api/v1/chat/attachments/"+att.ID, testAPIToken, ""); w.Code != http.StatusOK || w.Body.String() != "x" {
		t.Fatalf("attachment GET = %d %q", w.Code, w.Body)
	}
}
