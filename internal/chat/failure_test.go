package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestTaskFailurePostsACardToTheOwner(t *testing.T) {
	f := newFixture(t)
	f.cfg.Tasks["build"] = config.TaskConfig{Workspace: "/src/web"}
	m, err := f.svc.TaskFailure(context.Background(), TaskFailureRequest{
		Task: "build", RunID: "build-1", Attempts: 2, Error: "exit status 1", LogPath: "/home/leo/state/logs/build.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Agent != "web" || m.Kind != KindTask || m.Task != "build" || m.RunID != "build-1" || m.Direction != DirectionOut {
		t.Fatalf("card = %+v", m)
	}
	want := "**Task failed: build**\nRun `build-1` · 2 attempt(s)\n```\nexit status 1\n```\nLog: `/home/leo/state/logs/build.log`"
	if m.Text != want {
		t.Fatalf("text = %q\nwant   %q", m.Text, want)
	}
}

func TestTaskFailureWithMissingOwnerFallsBackWithANote(t *testing.T) {
	f := newFixture(t)
	f.cfg.Tasks["build"] = config.TaskConfig{Owner: "gone"}
	m, err := f.svc.TaskFailure(context.Background(), TaskFailureRequest{Task: "build", RunID: "r1", Attempts: 1, Error: "boom"})
	if err != nil {
		t.Fatal(err)
	}
	thread := f.thread(t, "assistant")
	if m.Agent != "assistant" || len(thread) != 2 || thread[1].Kind != KindSystem || !strings.Contains(thread[1].Text, "gone") {
		t.Fatalf("thread = %+v", thread)
	}
}

func TestTaskFailureErrorSummary(t *testing.T) {
	long := strings.Repeat("é", 600)
	cases := map[string]struct {
		req  TaskFailureRequest
		want func(t *testing.T, text string)
	}{
		"keeps the first 500 runes with no ellipsis": {
			req: TaskFailureRequest{Error: long},
			want: func(t *testing.T, text string) {
				if !strings.Contains(text, "```\n"+strings.Repeat("é", 500)+"\n```") {
					t.Errorf("summary not exactly 500 runes: %q", text)
				}
				if strings.Contains(text, "…") || strings.Contains(text, "...") {
					t.Errorf("unexpected ellipsis: %q", text)
				}
			},
		},
		"a triple backtick in the error still gives one code block": {
			req: TaskFailureRequest{Error: "bad ```x``` here"},
			want: func(t *testing.T, text string) {
				if !strings.Contains(text, "````\nbad ```x``` here\n````") {
					t.Errorf("fence not widened: %q", text)
				}
			},
		},
		"an empty log path omits the Log line": {
			req: TaskFailureRequest{Error: "boom"},
			want: func(t *testing.T, text string) {
				if strings.Contains(text, "Log:") {
					t.Errorf("Log line present: %q", text)
				}
			},
		},
	}
	for name, c := range cases {
		f := newFixture(t)
		f.cfg.Tasks["build"] = config.TaskConfig{Workspace: "/src/web"}
		req := c.req
		req.Task, req.RunID, req.Attempts = "build", "r1", 1
		m, err := f.svc.TaskFailure(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		c.want(t, m.Text)
	}
}

func TestTaskFailureRejectsBadRequests(t *testing.T) {
	f := newFixture(t)
	f.cfg.Tasks["build"] = config.TaskConfig{}
	cases := map[string]TaskFailureRequest{
		"unknown task":  {Task: "nope", RunID: "r", Attempts: 1},
		"empty task":    {RunID: "r", Attempts: 1},
		"empty run id":  {Task: "build", Attempts: 1},
		"zero attempts": {Task: "build", RunID: "r"},
		"bad attempts":  {Task: "build", RunID: "r", Attempts: -2},
	}
	for name, req := range cases {
		if _, err := f.svc.TaskFailure(context.Background(), req); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}
