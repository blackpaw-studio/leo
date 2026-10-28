package claude

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/session"
)

func touch(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeSessionFilesIncludesProjectDirAndFileHistory(t *testing.T) {
	root := t.TempDir()
	ws := "/Users/me/.leo/agents/x"
	slug := session.ProjectSlug(ws)
	touch(t, filepath.Join(root, "projects", slug, "a.jsonl"), "{}\n")
	touch(t, filepath.Join(root, "projects", slug, "memory", "m.md"), "m")
	touch(t, filepath.Join(root, "file-history", "a", "f1"), "1")
	touch(t, filepath.Join(root, "file-history", "b", "f1"), "1")
	touch(t, filepath.Join(root, "file-history", "other", "f1"), "1")
	touch(t, filepath.Join(root, "session-env", "a", "env"), "x")
	touch(t, filepath.Join(root, "todos", "a.json"), "[]")

	got, err := Claude{}.SessionFiles(root, ws, []string{"a", "b", "missing"})
	if err != nil {
		t.Fatalf("SessionFiles: %v", err)
	}
	want := []string{"projects/" + slug + "/", "file-history/a/", "file-history/b/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SessionFiles = %v, want %v", got, want)
	}

	if got, _ := (Claude{}).SessionFiles(t.TempDir(), ws, []string{"a"}); len(got) != 0 {
		t.Fatalf("SessionFiles on an empty root = %v, want none", got)
	}
}

func TestClaudeTruncateAtLastEndTurn(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"thinking"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text"}]}}`,
		`{"type":"system","subtype":"turn_duration"}`,
		`{"type":"user","message":{"role":"user","content":"next"}}`,
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use"}}`,
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	got, ok := TruncateAtLastEndTurn(data)
	if !ok {
		t.Fatal("no completed turn found")
	}
	if want := strings.Join(lines[:5], "\n") + "\n"; string(got) != want {
		t.Fatalf("truncated =\n%s\nwant\n%s", got, want)
	}
	if _, ok := TruncateAtLastEndTurn([]byte(lines[0] + "\n" + lines[1] + "\n")); ok {
		t.Fatal("a transcript with no completed turn reported one")
	}
}

func TestClaudeForkArgs(t *testing.T) {
	in := []string{"--model", "opus", "--session-id", "old", "--resume", "x", "--fork-session"}
	got := Claude{}.ForkArgs(in, "src", "new")
	want := []string{"--model", "opus", "--resume", "src", "--fork-session", "--session-id", "new"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ForkArgs = %v, want %v", got, want)
	}
	if in[3] != "old" {
		t.Fatal("ForkArgs mutated its input")
	}
}

// A copy stages the source transcript as of its last completed turn, with
// its sidecar dir and memory, and the destination places them under the new
// workspace's project dir, seeding memory only where there is none.
func TestClaudeCopyStagesAndPlacesUnderNewSlug(t *testing.T) {
	srcRoot, dstRoot, stage := t.TempDir(), t.TempDir(), t.TempDir()
	srcWS, newWS := "/Users/me/ws", "/Users/me/ws-copy"
	srcDir := filepath.Join(srcRoot, "projects", session.ProjectSlug(srcWS))
	touch(t, filepath.Join(srcDir, "s1.jsonl"),
		`{"type":"assistant","message":{"stop_reason":"end_turn"}}`+"\n"+`{"type":"user","message":{"content":"mid"}}`+"\n")
	touch(t, filepath.Join(srcDir, "s1", "subagents", "a.jsonl"), "{}\n")
	touch(t, filepath.Join(srcDir, "memory", "MEMORY.md"), "remember")

	c := Claude{}
	src := harness.MoveContext{Env: map[string]string{"CLAUDE_CONFIG_DIR": srcRoot}, Workspace: srcWS, SessionIDs: []string{"s1"}, Copy: true}
	if err := c.PrepareSend(context.Background(), src, stage); err != nil {
		t.Fatalf("PrepareSend: %v", err)
	}
	dst := harness.MoveContext{Env: map[string]string{"CLAUDE_CONFIG_DIR": dstRoot}, Workspace: newWS, SessionIDs: []string{"s1"}, Copy: true}
	if err := c.FinishReceive(context.Background(), dst, stage); err != nil {
		t.Fatalf("FinishReceive: %v", err)
	}
	newDir := filepath.Join(dstRoot, "projects", session.ProjectSlug(newWS))
	got, err := os.ReadFile(filepath.Join(newDir, "s1.jsonl"))
	if err != nil || string(got) != `{"type":"assistant","message":{"stop_reason":"end_turn"}}`+"\n" {
		t.Fatalf("placed transcript = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(newDir, "s1", "subagents", "a.jsonl")); err != nil {
		t.Fatalf("sidecar not placed: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(newDir, "memory", "MEMORY.md")); string(got) != "remember" {
		t.Fatalf("memory not seeded: %q", got)
	}

	// A second receive never overwrites the copy's own memory.
	touch(t, filepath.Join(newDir, "memory", "MEMORY.md"), "the copy's own")
	if err := c.FinishReceive(context.Background(), dst, stage); err != nil {
		t.Fatalf("FinishReceive again: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(newDir, "memory", "MEMORY.md")); string(got) != "the copy's own" {
		t.Fatalf("memory overwritten: %q", got)
	}

	// A move stages nothing: its files travel by SessionFiles.
	moveStage := t.TempDir()
	src.Copy = false
	if err := c.PrepareSend(context.Background(), src, moveStage); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(moveStage); len(entries) != 0 {
		t.Fatalf("move staged %v", entries)
	}
}

func TestClaudeTranscriptFindsOnlyThatSession(t *testing.T) {
	root, ws := t.TempDir(), "/Users/me/ws"
	touch(t, filepath.Join(root, "projects", session.ProjectSlug(ws), "other.jsonl"), "{}\n")
	if path, ok := (Claude{}).Transcript(root, ws, "sid"); ok || !strings.HasSuffix(path, "sid.jsonl") {
		t.Fatalf("Transcript = %q, %v; another session's file must not count", path, ok)
	}
	touch(t, filepath.Join(root, "projects", session.ProjectSlug(ws), "sid.jsonl"), "{}\n")
	if _, ok := (Claude{}).Transcript(root, ws, "sid"); !ok {
		t.Fatal("transcript not found")
	}
}
