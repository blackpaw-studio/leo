package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withTestServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	original := apiBaseURL
	apiBaseURL = server.URL + "/bot"
	t.Cleanup(func() { apiBaseURL = original })

	return server.URL
}

func TestSendMessage(t *testing.T) {
	var gotPath string
	var gotParams url.Values

	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		r.ParseForm()
		gotParams = r.PostForm
		w.WriteHeader(http.StatusOK)
	})

	err := SendMessage("test-token", "123", "hello world", 0)
	if err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}

	if !strings.Contains(gotPath, "test-token/sendMessage") {
		t.Errorf("request path = %q, want to contain test-token/sendMessage", gotPath)
	}

	if got := gotParams.Get("chat_id"); got != "123" {
		t.Errorf("chat_id = %q, want %q", got, "123")
	}

	if got := gotParams.Get("text"); got != "hello world" {
		t.Errorf("text = %q, want %q", got, "hello world")
	}

	if got := gotParams.Get("parse_mode"); got != "Markdown" {
		t.Errorf("parse_mode = %q, want %q", got, "Markdown")
	}
}

func TestSendMessageWithTopic(t *testing.T) {
	var gotParams url.Values

	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotParams = r.PostForm
		w.WriteHeader(http.StatusOK)
	})

	err := SendMessage("test-token", "123", "hello", 42)
	if err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}

	if got := gotParams.Get("message_thread_id"); got != "42" {
		t.Errorf("message_thread_id = %q, want %q", got, "42")
	}
}

func TestSendMessageNoTopic(t *testing.T) {
	var gotParams url.Values

	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotParams = r.PostForm
		w.WriteHeader(http.StatusOK)
	})

	err := SendMessage("test-token", "123", "hello", 0)
	if err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}

	if got := gotParams.Get("message_thread_id"); got != "" {
		t.Errorf("message_thread_id = %q, want empty", got)
	}
}

func TestSendMessageAPIError(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"description":"Bad Request"}`))
	})

	err := SendMessage("test-token", "123", "hello", 0)
	if err == nil {
		t.Fatal("expected error for 400 response")
	}

	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %q, want to contain status code 400", err.Error())
	}
}

func TestSendMessageNetworkError(t *testing.T) {
	original := apiBaseURL
	apiBaseURL = "http://127.0.0.1:1/bot"
	defer func() { apiBaseURL = original }()

	err := SendMessage("test-token", "123", "hello", 0)
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestPollChatID(t *testing.T) {
	calls := 0
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		resp := updateResponse{
			OK: true,
			Result: []struct {
				Message struct {
					Chat struct {
						ID   int64  `json:"id"`
						Type string `json:"type"`
					} `json:"chat"`
					Text string `json:"text"`
				} `json:"message"`
			}{
				{
					Message: struct {
						Chat struct {
							ID   int64  `json:"id"`
							Type string `json:"type"`
						} `json:"chat"`
						Text string `json:"text"`
					}{
						Chat: struct {
							ID   int64  `json:"id"`
							Type string `json:"type"`
						}{ID: 12345, Type: "private"},
						Text: "hello",
					},
				},
			},
		}
		data, _ := json.Marshal(resp)
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	})

	chatID, err := PollChatID("test-token", 5*time.Second)
	if err != nil {
		t.Fatalf("PollChatID() error: %v", err)
	}

	if chatID != "12345" {
		t.Errorf("chatID = %q, want %q", chatID, "12345")
	}
}

func TestPollChatIDTimeout(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := updateResponse{OK: true}
		data, _ := json.Marshal(resp)
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	})

	_, err := PollChatID("test-token", 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}

	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want to contain 'timed out'", err.Error())
	}
}

func TestPollChatIDCtxCancellation(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := updateResponse{OK: true}
		data, _ := json.Marshal(resp)
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	})

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately
	cancel()

	_, err := PollChatIDCtx(ctx, "test-token")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want to contain 'timed out'", err.Error())
	}
}

func TestFetchTopics(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "getUpdates") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"ok": true,
			"result": [
				{"message": {"chat": {"id": -100999, "type": "supergroup"}, "message_thread_id": 3, "text": "hello"}},
				{"message": {"chat": {"id": -100999, "type": "supergroup"}, "message_thread_id": 7, "text": "world"}},
				{"message": {"chat": {"id": -100999, "type": "supergroup"}, "message_thread_id": 3, "text": "again"}},
				{"message": {"chat": {"id": 12345, "type": "private"}, "text": "dm"}}
			]
		}`))
	})

	topics, err := FetchTopics(context.Background(), "test-token", "-100999")
	if err != nil {
		t.Fatalf("FetchTopics() error: %v", err)
	}

	if len(topics) != 2 {
		t.Fatalf("got %d topics, want 2", len(topics))
	}

	// Should be sorted by ID
	if topics[0].ID != 3 || topics[1].ID != 7 {
		t.Errorf("topics = %v, want IDs [3, 7]", topics)
	}
}

func TestFetchTopicsWithNames(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"ok": true,
			"result": [
				{"message": {"chat": {"id": -100999, "type": "supergroup"}, "message_thread_id": 5, "forum_topic_created": {"name": "Chat"}, "text": ""}},
				{"message": {"chat": {"id": -100999, "type": "supergroup"}, "message_thread_id": 5, "text": "hello"}}
			]
		}`))
	})

	topics, err := FetchTopics(context.Background(), "test-token", "-100999")
	if err != nil {
		t.Fatalf("FetchTopics() error: %v", err)
	}

	if len(topics) != 1 {
		t.Fatalf("got %d topics, want 1", len(topics))
	}

	if topics[0].Name != "Chat" {
		t.Errorf("topic name = %q, want %q", topics[0].Name, "Chat")
	}
}

func TestFetchTopicsEmpty(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": true, "result": []}`))
	})

	topics, err := FetchTopics(context.Background(), "test-token", "-100999")
	if err != nil {
		t.Fatalf("FetchTopics() error: %v", err)
	}

	if len(topics) != 0 {
		t.Errorf("got %d topics, want 0", len(topics))
	}
}

func TestWriteAndReadTopicCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "topics.json")
	topics := []Topic{{ID: 1, Name: "General"}, {ID: 5, Name: "News"}}

	if err := WriteTopicCache(path, topics); err != nil {
		t.Fatal(err)
	}

	loaded, err := ReadTopicCache(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(loaded) != 2 {
		t.Fatalf("len = %d, want 2", len(loaded))
	}
	if loaded[0].Name != "General" || loaded[1].Name != "News" {
		t.Errorf("topics = %v", loaded)
	}
}

func TestReadTopicCacheNotFound(t *testing.T) {
	_, err := ReadTopicCache("/nonexistent/path")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestReadTopicCacheInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "topics.json")
	os.WriteFile(path, []byte("not json"), 0644)

	_, err := ReadTopicCache(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestSortTopics(t *testing.T) {
	topics := []Topic{
		{ID: 5, Name: "Five"},
		{ID: 1, Name: "One"},
		{ID: 3, Name: "Three"},
	}
	sortTopics(topics)

	if topics[0].ID != 1 || topics[1].ID != 3 || topics[2].ID != 5 {
		t.Errorf("sort order wrong: %v", topics)
	}
}

func TestSortTopicsEmpty(t *testing.T) {
	sortTopics(nil)       // should not panic
	sortTopics([]Topic{}) // should not panic
}

func TestSortTopicsSingleElement(t *testing.T) {
	topics := []Topic{{ID: 1, Name: "One"}}
	sortTopics(topics)
	if topics[0].ID != 1 {
		t.Error("single element sort failed")
	}
}

func TestFetchTopicsAPIError(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": false, "result": []}`))
	})

	_, err := FetchTopics(context.Background(), "test-token", "-100999")
	if err == nil {
		t.Error("expected error when API returns ok=false")
	}
	if !strings.Contains(err.Error(), "ok=false") {
		t.Errorf("error = %q, want to contain 'ok=false'", err.Error())
	}
}

func TestSendMessageParsesAPIDescription(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`))
	})

	err := SendMessage("test-token", "123", "hello", 0)
	if err == nil {
		t.Fatal("expected error")
	}
	// Should extract the description, not dump raw JSON
	if !strings.Contains(err.Error(), "bot was blocked by the user") {
		t.Errorf("error = %q, want parsed description", err.Error())
	}
}
