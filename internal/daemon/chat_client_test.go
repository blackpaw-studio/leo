package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReportTaskFailurePostsTheCard(t *testing.T) {
	var gotMethod, gotPath string
	var got TaskFailureRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"id":"m1","agent":"assistant"}}`))
	}))
	defer srv.Close()

	want := TaskFailureRequest{Task: "build", RunID: "build-1", Attempts: 2, Error: "exit status 1", LogPath: "/state/logs/build.log"}
	if err := ReportTaskFailureHTTP(context.Background(), srv.URL, want); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/chat/task-failure" || got != want {
		t.Fatalf("%s %s %+v", gotMethod, gotPath, got)
	}
}

func TestReportTaskFailureSurfacesTheDaemonError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false,"error":"Leo Chat is unavailable: the web server is not running"}`))
	}))
	defer srv.Close()

	err := ReportTaskFailureHTTP(context.Background(), srv.URL, TaskFailureRequest{Task: "build", RunID: "r", Attempts: 1})
	if err == nil || !strings.Contains(err.Error(), "Leo Chat is unavailable") {
		t.Fatalf("err = %v", err)
	}
}
