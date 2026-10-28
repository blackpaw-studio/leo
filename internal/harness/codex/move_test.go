package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCodexSessionFilesFindsRolloutByID(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"sessions/2026/10/26/rollout-2026-10-26T10-00-00-aaa.jsonl",
		"sessions/2026/10/27/rollout-2026-10-27T09-00-00-bbb.jsonl.zst",
		"sessions/2026/10/27/rollout-2026-10-27T09-30-00-ccc.jsonl",
		"sessions/2026/10/27/notes-aaa.jsonl",
		"history.jsonl",
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Codex{}.SessionFiles(root, "/ws", []string{"aaa", "bbb", "zzz"})
	if err != nil {
		t.Fatalf("SessionFiles: %v", err)
	}
	want := []string{
		"sessions/2026/10/26/rollout-2026-10-26T10-00-00-aaa.jsonl",
		"sessions/2026/10/27/rollout-2026-10-27T09-00-00-bbb.jsonl.zst",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SessionFiles = %v, want %v", got, want)
	}
	if got := (Codex{}).SessionRoot(map[string]string{"CODEX_HOME": "/c"}); got != "/c" {
		t.Fatalf("SessionRoot = %q, want /c", got)
	}
}

func TestCodexForkArgs(t *testing.T) {
	got := Codex{}.ForkArgs([]string{"resume", "old", "-m", "gpt"}, "src", "ignored")
	if want := []string{"fork", "src", "-m", "gpt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ForkArgs = %v, want %v", got, want)
	}
}

func TestCodexRefreshStripsForkAfterDiscovery(t *testing.T) {
	fork := []string{"fork", "S", "a", "b"}
	if got := refreshSessionArgs(fork, "N"); !reflect.DeepEqual(got, []string{"resume", "N", "a", "b"}) {
		t.Fatalf("refresh after discovery = %v", got)
	}
	// Until the fork's own id is known, the fork stays pending.
	if got := refreshSessionArgs(fork, ""); !reflect.DeepEqual(got, fork) {
		t.Fatalf("refresh before discovery = %v, want the pending fork", got)
	}
	// A fork that quick-exits is poisoned like a resume.
	if got, _ := recoverQuickExitArgs(fork); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("recover = %v", got)
	}
}

func TestCodexTranscriptFindsRolloutByID(t *testing.T) {
	root := t.TempDir()
	if _, ok := (Codex{}).Transcript(root, "", "aaa"); ok {
		t.Fatal("found a rollout in an empty store")
	}
	p := filepath.Join(root, "sessions/2026/10/26/rollout-2026-10-26T10-00-00-aaa.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := (Codex{}).Transcript(root, "", "aaa"); !ok {
		t.Fatal("rollout not found")
	}
	if _, ok := (Codex{}).Transcript(root, "", "bbb"); ok {
		t.Fatal("found another session's rollout")
	}
}
