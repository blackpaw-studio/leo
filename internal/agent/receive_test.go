package agent

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/session"
)

var receiveAt = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// arriving is a record as a source host would send it, host-specific
// fields and all.
func arriving(ws string) agentstore.Record {
	return agentstore.Record{
		Name: "leo-x", Template: "coding", Harness: "claude", Workspace: ws, SessionID: "sent-session",
		Environments: []string{"base", "acct-b"}, EnvLayered: true,
		Appearance: &agentstore.Appearance{Emoji: "🦊"},
		MoveLog:    map[string]time.Time{"laptop": receiveAt.Add(-time.Hour)},
		ClaudeArgs: []string{"--evil-flag"}, Env: map[string]string{"CLAUDE_CONFIG_DIR": "/src/claude", "SRC_ONLY": "1"},
		WebPort: "1111", AttentionToken: "tok", ClaimedSession: "c", OpeningBriefID: "b", OpeningQueuedID: "q",
		NoResume: true, MovedTo: "elsewhere", WakeOnMessage: true,
	}
}

func receiveManager(t *testing.T) (*Manager, *capturingSupervisor, string, string) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	sup := &capturingSupervisor{}
	return envManager(envTestConfig(home, tws, a, b), sup), sup, home, b
}

func TestReceiveDropsHostSpecificFields(t *testing.T) {
	m, sup, home, _ := receiveManager(t)
	if err := m.Receive(context.Background(), ReceiveSpec{Record: arriving(t.TempDir()), From: "studio", At: receiveAt}); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	rec := loadRec(t, home, "leo-x")
	if containsFlag(rec.ClaudeArgs, "--evil-flag") || containsFlag(sup.spawnCall.ClaudeArgs, "--evil-flag") {
		t.Errorf("the source's launch args survived: %v", rec.ClaudeArgs)
	}
	if rec.Env["SRC_ONLY"] != "" || sup.spawnCall.Env["SRC_ONLY"] != "" {
		t.Errorf("the source's env survived: %v", rec.Env)
	}
	if rec.WebPort == "1111" || rec.AttentionToken != "" || rec.ClaimedSession != "" || rec.OpeningBriefID != "" || rec.OpeningQueuedID != "" || rec.NoResume {
		t.Errorf("host-specific state survived: %+v", rec)
	}
	if rec.MovedTo != "" || rec.Stopped || rec.WakeOnMessage {
		t.Errorf("received agent is not a plain live agent: %+v", rec)
	}
	if rec.Appearance == nil || rec.Appearance.Emoji != "🦊" {
		t.Errorf("appearance not carried: %+v", rec.Appearance)
	}
	if !rec.MoveLog["studio"].Equal(receiveAt) || rec.MoveLog["laptop"].IsZero() {
		t.Errorf("move log = %v, want studio added and laptop kept", rec.MoveLog)
	}
}

func TestReceiveRebuildsEnvFromDestConfig(t *testing.T) {
	m, sup, _, b := receiveManager(t)
	if err := m.Receive(context.Background(), ReceiveSpec{Record: arriving(t.TempDir()), From: "studio", At: receiveAt}); err != nil {
		t.Fatal(err)
	}
	env := sup.spawnCall.Env
	if env["CLAUDE_CONFIG_DIR"] != b || env["SHARED"] != "b" || env["LITERAL"] != "tmpl" {
		t.Fatalf("spawn env = %v, want this host's acct-b dir %s and template env", env, b)
	}
}

func TestReceiveResumesSentSessionID(t *testing.T) {
	m, sup, home, b := receiveManager(t)
	ws := t.TempDir()
	// The workspace's transcripts arrived with their source mtimes; another
	// session's is newer than the sent one but older than the receive.
	plantTranscript(t, b, ws, "sent-session")
	plantTranscript(t, b, ws, "older-other")
	old := time.Now().Add(-time.Hour)
	other := filepath.Join(b, "projects", session.ProjectSlug(ws), "older-other.jsonl")
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}
	if err := m.Receive(context.Background(), ReceiveSpec{Record: arriving(ws), From: "studio", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	args := sup.spawnCall.ClaudeArgs
	if i := slices.Index(args, "--resume"); i < 0 || args[i+1] != "sent-session" {
		t.Fatalf("spawn args %v must resume the sent session", args)
	}
	if rec := loadRec(t, home, "leo-x"); rec.SessionID != "sent-session" {
		t.Fatalf("SessionID = %q", rec.SessionID)
	}
}

func TestReceiveStartFailureLeavesMovedShadow(t *testing.T) {
	m, sup, home, _ := receiveManager(t)
	sup.spawnErr = errors.New("tmux exploded")
	err := m.Receive(context.Background(), ReceiveSpec{Record: arriving(t.TempDir()), From: "studio", At: receiveAt})
	if err == nil || !strings.Contains(err.Error(), "tmux exploded") {
		t.Fatalf("Receive = %v, want the start failure", err)
	}
	rec := loadRec(t, home, "leo-x")
	if !rec.Stopped || rec.MovedTo != "studio" || rec.MovedAt == nil || rec.WakeOnMessage {
		t.Fatalf("record = %+v, want a stopped shadow pointing back at studio", rec)
	}
	if !errors.Is(m.Start("leo-x"), ErrAgentMoved) {
		t.Fatal("the shadow can be started here")
	}

	// A copy that fails to start stays a stopped agent `leo agent start` recovers.
	cm, csup, chome, _ := receiveManager(t)
	csup.spawnErr = errors.New("no claude")
	copyRec := arriving(t.TempDir())
	copyRec.Name, copyRec.SessionID = "leo-x-copy", "fork-id"
	if err := cm.Receive(context.Background(), ReceiveSpec{Record: copyRec, From: "studio", At: receiveAt, Copy: true, ForkFrom: "sent-session"}); err == nil {
		t.Fatal("copy start failure not reported")
	}
	crec := loadRec(t, chome, "leo-x-copy")
	if !crec.Stopped || crec.MovedTo != "" || !strings.Contains(crec.StoppedReason, "copy start failed") {
		t.Fatalf("copy record = %+v", crec)
	}
}

func TestReceiveCopyUsesForkArgsOnce(t *testing.T) {
	m, sup, home, _ := receiveManager(t)
	rec := arriving(t.TempDir())
	rec.Name, rec.SessionID = "leo-x-copy", "fork-id"
	rec.SessionsByTemplate = map[string]string{"other": "src-archived"}
	if err := m.Receive(context.Background(), ReceiveSpec{Record: rec, From: "studio", At: receiveAt, Copy: true, ForkFrom: "sent-session"}); err != nil {
		t.Fatal(err)
	}
	args := sup.spawnCall.ClaudeArgs
	want := []string{"--resume", "sent-session", "--fork-session", "--session-id", "fork-id"}
	if len(args) < len(want) || !slices.Equal(args[len(args)-len(want):], want) {
		t.Fatalf("first launch args = %v, want ...%v", args, want)
	}
	stored := loadRec(t, home, "leo-x-copy")
	if stored.ForkFromSession != "" || stored.SessionID != "fork-id" || len(stored.MoveLog) != 0 || len(stored.SessionsByTemplate) != 0 {
		t.Fatalf("record after the fork = %+v", stored)
	}

	if err := m.Stop("leo-x-copy", StopOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start("leo-x-copy"); err != nil {
		t.Fatal(err)
	}
	args = sup.spawnCall.ClaudeArgs
	if containsFlag(args, "--fork-session") || slices.Contains(args, "sent-session") {
		t.Fatalf("second launch still forks: %v", args)
	}
	if i := slices.Index(args, "--resume"); i < 0 || args[i+1] != "fork-id" {
		t.Fatalf("second launch args = %v, want --resume fork-id", args)
	}
}

func TestReceiveNameRules(t *testing.T) {
	m, _, home, _ := receiveManager(t)
	ws := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-x", Template: "coding", Workspace: ws, Stopped: true})
	if err := m.Receive(context.Background(), ReceiveSpec{Record: arriving(ws), From: "studio", At: receiveAt}); err == nil {
		t.Fatal("Receive replaced an agent that was never moved away")
	}
	_ = agentstore.Update(home, "leo-x", func(r agentstore.Record) agentstore.Record { r.MovedTo = "studio"; return r })
	if err := m.Receive(context.Background(), ReceiveSpec{Record: arriving(ws), From: "studio", At: receiveAt}); err != nil {
		t.Fatalf("a move back onto its shadow: %v", err)
	}
	if rec := loadRec(t, home, "leo-x"); rec.MovedTo != "" || rec.Stopped {
		t.Fatalf("shadow not replaced: %+v", rec)
	}
}

// ArrivalEnv is exactly the env Receive starts the agent with, inherited
// layer included.
func TestArrivalEnvIsReceiveSpawnEnv(t *testing.T) {
	m, sup, _, _ := receiveManager(t)
	rec := arriving(t.TempDir())
	rec.InheritedEnv = map[string]string{"CLAUDE_CONFIG_DIR": "/inherited/claude"}
	spec := ReceiveSpec{Record: rec, From: "studio", At: receiveAt}
	env, err := m.ArrivalEnv(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Receive(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if env["CLAUDE_CONFIG_DIR"] != "/inherited/claude" || !maps.Equal(env, sup.spawnCall.Env) {
		t.Fatalf("ArrivalEnv = %v\nspawn env  = %v", env, sup.spawnCall.Env)
	}
}

// RuntimeEnv is what a restart of the stored agent runs with: its inherited
// layer, or a legacy record's stored env when its template is gone.
func TestRuntimeEnvIsRestartEnv(t *testing.T) {
	m, _, _, _ := receiveManager(t)
	rec := arriving(t.TempDir())
	rec.InheritedEnv = map[string]string{"CLAUDE_CONFIG_DIR": "/inherited/claude"}
	env, err := m.RuntimeEnv(rec)
	if err != nil || env["CLAUDE_CONFIG_DIR"] != "/inherited/claude" {
		t.Fatalf("RuntimeEnv = %v, %v", env, err)
	}
	legacy := agentstore.Record{Name: "old", Template: "gone", Harness: "claude", Env: map[string]string{"CLAUDE_CONFIG_DIR": "/stored"}}
	if env, err := m.RuntimeEnv(legacy); err != nil || env["CLAUDE_CONFIG_DIR"] != "/stored" {
		t.Fatalf("RuntimeEnv(legacy) = %v, %v", env, err)
	}
}
