package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
)

func TestDispatchRunTargetAgentSendsNoCwd(t *testing.T) {
	body, err := runDispatchCommand(t, false, "--target-agent", "leo")
	if err != nil {
		t.Fatal(err)
	}
	if body["target_agent"] != "leo" {
		t.Fatalf("body = %#v, want target_agent leo", body)
	}
	if cwd, set := body["cwd"]; set {
		t.Fatalf("body carries cwd %v alongside target_agent", cwd)
	}
}

func TestDispatchRunTargetAgentRefusesCwdAndIsolation(t *testing.T) {
	for _, extra := range [][]string{{"--cwd", "/tmp"}, {"--isolation", "worktree"}} {
		body, err := runDispatchCommand(t, false, append([]string{"--target-agent", "leo"}, extra...)...)
		if err == nil || !strings.Contains(err.Error(), "target-agent") {
			t.Fatalf("%v: err = %v, want a local refusal naming --target-agent", extra, err)
		}
		if body != nil {
			t.Fatalf("%v: refused run still posted %#v", extra, body)
		}
	}
}

func TestDispatchListAndShowRenderTheReadGrant(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "dispatches")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := consult.Record{ID: "d-grant", Template: "scout", Harness: "claude", Status: consult.StatusRunning, StartedAt: time.Now(),
		TargetAgent: "leo", TargetGrant: "template:marketing", TargetWorkspaces: []string{"/ws/leo"}}
	writeTestRecord(t, dir, rec)
	var out bytes.Buffer
	if err := listConsults(state, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "TARGET") || !strings.Contains(out.String(), "leo (grant template:marketing)") {
		t.Fatalf("list = %s", out.String())
	}
	out.Reset()
	if err := encodeDispatchShow(&out, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	var shown map[string]any
	if err := json.Unmarshal(out.Bytes(), &shown); err != nil {
		t.Fatal(err)
	}
	if shown["target_agent"] != "leo" || shown["target_grant"] != "template:marketing" {
		t.Fatalf("show = %s", out.String())
	}
}
