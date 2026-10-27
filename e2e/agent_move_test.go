//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentmove"
	"github.com/blackpaw-studio/leo/internal/session"
)

// The agent-move e2e runs two daemons on this machine, host-a and host-b,
// each with its own leo home, tmux server and move host name. `ssh` is
// e2e/testdata/fakessh, which runs the remote command in the peer's home, so
// rsync and git really transfer, onto the same paths. That makes the
// workspace a self-sync: these tests cover the control plane (preflight,
// stop, hooks, git stages, commit, moved_to, move-back) and a full local
// copy.

const moveTimeout = 60 * time.Second

// moveHost is one daemon of a movePair.
type moveHost struct {
	name, home, cfg, socket string
	// env is added to the daemon's and every CLI call's environment.
	env []string
	svc *exec.Cmd
	out *bytes.Buffer
}

type movePair struct {
	root, ws, claudeDir, sshLog, hookLog string
	a, b                                 *moveHost
}

func newMovePair(t *testing.T) *movePair {
	t.Helper()
	if _, err := os.Stat(faketmux); err != nil {
		t.Skip("tmux wrapper unavailable")
	}
	// /tmp is a symlink on macOS; git reports resolved paths.
	root, err := filepath.EvalSymlinks(mkTempE2EDir(t, "leo-e2e-move-*"))
	if err != nil {
		t.Fatal(err)
	}
	p := &movePair{
		root: root, ws: filepath.Join(root, "work"), claudeDir: filepath.Join(root, "claude"),
		sshLog: filepath.Join(root, "ssh.log"), hookLog: filepath.Join(root, "hooks.log"),
	}
	bin, hooks := filepath.Join(root, "bin"), filepath.Join(root, "hooks")
	for _, d := range []string{p.ws, p.claudeDir, bin, hooks} {
		mustMkdir(t, d)
	}
	shim, err := os.ReadFile(filepath.Join(findRepoRoot(), "e2e", "testdata", "fakessh"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(bin, "ssh"), string(shim), 0o755)
	mustWrite(t, filepath.Join(hooks, "pre"), "#!/bin/sh\n"+
		`echo "pre_send $LEO_MOVE_MODE $LEO_MOVE_FROM $LEO_MOVE_TO $LEO_AGENT" >>`+p.hookLog+"\n"+
		`echo carried >"$LEO_MOVE_DIR/token"`+"\n", 0o755)
	mustWrite(t, filepath.Join(hooks, "post"), "#!/bin/sh\n"+
		`echo "post_receive $LEO_MOVE_MODE $LEO_MOVE_FROM $LEO_MOVE_TO $LEO_AGENT $(cat "$LEO_MOVE_DIR/token")" >>`+p.hookLog+"\n", 0o755)

	peers := filepath.Join(root, "peers")
	p.a = p.newHost(t, "host-a", "host-b", bin, peers, hooks)
	p.b = p.newHost(t, "host-b", "host-a", bin, peers, hooks)
	mustWrite(t, peers, fmt.Sprintf("host-a %s\nhost-b %s\n", p.a.home, p.b.home), 0o600)
	p.a.start(t)
	p.b.start(t)
	return p
}

func (p *movePair) newHost(t *testing.T, name, peer, bin, peers, hooks string) *moveHost {
	t.Helper()
	h := &moveHost{name: name, home: filepath.Join(p.root, name), socket: fmt.Sprintf("leo-e2e-%s-%d", name, time.Now().UnixNano())}
	h.cfg = filepath.Join(h.home, "leo.yaml")
	mustMkdir(t, h.home)
	h.env = []string{
		"PATH=" + bin + ":" + filepath.Dir(fakeclaude) + ":" + os.Getenv("PATH"),
		"FAKECLAUDE_TMUX_SOCKET=" + h.socket,
		agentmove.HostnameEnv + "=" + name,
		"FAKESSH_PEERS=" + peers,
		"FAKESSH_LOG=" + p.sshLog,
	}
	// What a remote command run "on" this host sees (fakessh loads it).
	mustWrite(t, filepath.Join(h.home, "fakessh.env"), strings.Join(h.env[1:3], "\n")+"\n", 0o600)
	mustWrite(t, h.cfg, fmt.Sprintf(`defaults:
  model: sonnet
client:
  default_host: localhost
  hosts:
    %s:
      ssh: %s
      leo_path: %s
move:
  hooks:
    pre_send: [%s]
    post_receive: [%s]
templates:
  mv:
    workspace: %s
    env:
      CLAUDE_CONFIG_DIR: %s
`, peer, peer, leoBin, filepath.Join(hooks, "pre"), filepath.Join(hooks, "post"), p.ws, p.claudeDir), 0o600)
	t.Cleanup(func() { killMoveTmux(h.socket) })
	return h
}

func (h *moveHost) start(t *testing.T) {
	t.Helper()
	cmd := exec.Command(leoBin, "service", "--supervised", "-c", h.cfg)
	cmd.Dir = h.home
	cmd.Env = append(os.Environ(), h.env...)
	h.out = &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = h.out, h.out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", h.name, err)
	}
	h.svc = cmd
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("%s service output:\n%s", h.name, h.out)
		}
	})
	waitUntil(t, 30*time.Second, h.name+" daemon ready", func() bool {
		_, _, code := h.leo(t, "agent", "list", "--json")
		return code == 0
	})
}

func (h *moveHost) leo(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return runLeo(t, h.home, h.env, append(args, "-c", h.cfg)...)
}

func (h *moveHost) mustLeo(t *testing.T, args ...string) string {
	t.Helper()
	out, errOut, code := h.leo(t, args...)
	if code != 0 {
		t.Fatalf("%s: leo %s exited %d\nstdout: %s\nstderr: %s", h.name, strings.Join(args, " "), code, out, errOut)
	}
	return out
}

// record is the agent named name on h, or nil.
func (h *moveHost) record(t *testing.T, name string) *agent.Record {
	t.Helper()
	var recs []agent.Record
	if err := json.Unmarshal([]byte(h.mustLeo(t, "agent", "list", "--json")), &recs); err != nil {
		t.Fatalf("%s agent list: %v", h.name, err)
	}
	for i := range recs {
		if recs[i].Name == name {
			return &recs[i]
		}
	}
	return nil
}

func (h *moveHost) waitRunning(t *testing.T, name string) {
	t.Helper()
	waitUntil(t, moveTimeout, name+" running on "+h.name, func() bool {
		r := h.record(t, name)
		return r != nil && r.Status == "running"
	})
}

// spawnWorktree spawns a worktree agent of template mv on branch.
func (h *moveHost) spawnWorktree(t *testing.T, branch string) string {
	t.Helper()
	h.mustLeo(t, "agent", "spawn", "mv", "--repo", "e2e/repo", "--worktree", branch, "--base", "main")
	name := "leo-mv-e2e-repo-" + branch
	h.waitRunning(t, name)
	return name
}

func killMoveTmux(socket string) {
	real, err := exec.LookPath("tmux")
	if err != nil {
		return
	}
	_ = exec.Command(real, "-L", socket, "kill-server").Run()
	_ = os.Remove(tmuxSocketPath(os.Getenv("TMUX_TMPDIR"), os.Getuid(), socket))
}

func waitUntil(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Leo Test", "GIT_AUTHOR_EMAIL=leo@example.test",
		"GIT_COMMITTER_NAME=Leo Test", "GIT_COMMITTER_EMAIL=leo@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// initRepo makes the template's canonical clone, ws/repo, of a local bare
// origin with one commit on main, plus (withSub) a submodule at libs/sub
// from another local repo.
func (p *movePair) initRepo(t *testing.T, withSub bool) string {
	t.Helper()
	seed := filepath.Join(p.root, "seed")
	mustMkdir(t, seed)
	gitIn(t, seed, "init", "-q", "-b", "main")
	mustWrite(t, filepath.Join(seed, "README.md"), "base\n", 0o600)
	mustWrite(t, filepath.Join(seed, ".gitignore"), "*.log\n", 0o600)
	gitIn(t, seed, "add", ".")
	gitIn(t, seed, "commit", "-q", "-m", "base")
	if withSub {
		src := filepath.Join(p.root, "subsrc")
		mustMkdir(t, src)
		gitIn(t, src, "init", "-q", "-b", "main")
		mustWrite(t, filepath.Join(src, "lib.txt"), "lib\n", 0o600)
		gitIn(t, src, "add", ".")
		gitIn(t, src, "commit", "-q", "-m", "lib")
		gitIn(t, seed, "submodule", "add", "-q", src, "libs/sub")
		gitIn(t, seed, "commit", "-q", "-m", "add sub")
	}
	origin := filepath.Join(p.root, "origin.git")
	gitIn(t, p.root, "clone", "-q", "--bare", seed, origin)
	canonical := filepath.Join(p.ws, "repo")
	gitIn(t, p.ws, "clone", "-q", origin, canonical)
	return canonical
}

// dirty gives a worktree an unpushed commit, a staged hunk, an unstaged
// edit, an untracked file and an ignored one.
func dirty(t *testing.T, wt string) {
	t.Helper()
	mustWrite(t, filepath.Join(wt, "committed.txt"), "c\n", 0o600)
	gitIn(t, wt, "add", "committed.txt")
	gitIn(t, wt, "commit", "-q", "-m", "local work")
	mustWrite(t, filepath.Join(wt, "README.md"), "base\nstaged\n", 0o600)
	gitIn(t, wt, "add", "README.md")
	mustWrite(t, filepath.Join(wt, "README.md"), "base\nstaged\nunstaged\n", 0o600)
	mustWrite(t, filepath.Join(wt, "untracked.txt"), "u\n", 0o600)
	mustWrite(t, filepath.Join(wt, "build.log"), "ignored\n", 0o600)
}

// gitState is a worktree's HEAD, branch, index and working-file status.
func gitState(t *testing.T, wt string) string {
	t.Helper()
	return gitIn(t, wt, "rev-parse", "HEAD") + gitIn(t, wt, "status", "--porcelain=v2", "--branch") + gitIn(t, wt, "diff", "--cached")
}

// assertSSHLog checks what crossed fakessh: a git push into canonical, and
// never the manifest, which goes over stdin.
func (p *movePair) assertSSHLog(t *testing.T, canonical string) {
	t.Helper()
	data, err := os.ReadFile(p.sshLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if !strings.Contains(log, " receive-pack '"+canonical+"'\n") {
		t.Errorf("no git push into %s over ssh:\n%s", canonical, log)
	}
	if strings.Contains(log, "{") || strings.Contains(log, `"agent"`) {
		t.Errorf("manifest bytes on the ssh argv:\n%s", log)
	}
}

func (p *movePair) hookLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(p.hookLog)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// A plain (non-git) workspace. Both daemons see the same path, so a file in
// it makes it the destination's own business and preflight refuses before
// anything stops; emptied, it counts as absent and the move runs through.
func TestAgentMove(t *testing.T) {
	p := newMovePair(t)
	p.a.mustLeo(t, "agent", "spawn", "mv")
	p.a.waitRunning(t, "mv")
	occupant := filepath.Join(p.ws, "notes.txt")
	mustWrite(t, occupant, "n\n", 0o600)

	out, errOut, code := p.a.leo(t, "agent", "move", "mv", "--to", "host-b")
	if code == 0 || !strings.Contains(errOut, agentmove.CodeWorkspaceExists) {
		t.Fatalf("move onto an occupied path: exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if r := p.a.record(t, "mv"); r == nil || r.Status != "running" || r.MovedTo != "" {
		t.Fatalf("source after a refused move = %+v, want it running and unmarked", r)
	}
	if r := p.b.record(t, "mv"); r != nil {
		t.Fatalf("destination holds %+v after a refusal", r)
	}
	if _, err := os.Stat(p.hookLog); err == nil {
		t.Fatalf("a hook ran for a refused move")
	}

	if err := os.Remove(occupant); err != nil {
		t.Fatal(err)
	}
	out = p.a.mustLeo(t, "agent", "move", "mv", "--to", "host-b")
	if !strings.Contains(out, "moved mv to host-b") || !strings.Contains(out, "leo --host host-b agent attach mv") {
		t.Errorf("move output:\n%s", out)
	}
	p.b.waitRunning(t, "mv")
	if r := p.a.record(t, "mv"); r == nil || r.MovedTo != "host-b" {
		t.Fatalf("source record after the move = %+v, want moved_to host-b", r)
	}
	if want := []string{"pre_send move host-a host-b mv", "post_receive move host-a host-b mv carried"}; strings.Join(p.hookLines(t), "\n") != strings.Join(want, "\n") {
		t.Errorf("hooks ran as:\n%s", strings.Join(p.hookLines(t), "\n"))
	}
	if _, errOut, code := p.a.leo(t, "agent", "start", "mv"); code == 0 || !strings.Contains(errOut, "host-b") {
		t.Fatalf("starting the moved-away agent: exit %d, stderr %s", code, errOut)
	}
	if data, err := os.ReadFile(p.sshLog); err != nil || strings.Contains(string(data), "{") {
		t.Errorf("ssh log = %q, %v; want argv without manifest bytes", data, err)
	}

	p.b.mustLeo(t, "agent", "move", "mv", "--to", "host-a")
	p.a.waitRunning(t, "mv")
	if r := p.a.record(t, "mv"); r.MovedTo != "" {
		t.Errorf("source after the move back = %+v, want it unmarked", r)
	}
	if r := p.b.record(t, "mv"); r == nil || r.MovedTo != "host-a" {
		t.Errorf("destination after the move back = %+v, want moved_to host-a", r)
	}
}

func TestAgentMoveWorktree(t *testing.T) {
	p := newMovePair(t)
	canonical := p.initRepo(t, false)
	name := p.a.spawnWorktree(t, "move-wt")
	sib := p.a.spawnWorktree(t, "move-sib")
	wt := filepath.Join(p.ws, ".worktrees", "repo", "move-wt")
	sibWT := filepath.Join(p.ws, ".worktrees", "repo", "move-sib")
	dirty(t, wt)
	mustWrite(t, filepath.Join(sibWT, "sib.txt"), "s\n", 0o600)
	before, sibBefore := gitState(t, wt), gitState(t, sibWT)

	out := p.a.mustLeo(t, "agent", "move", name, "--to", "host-b")
	if !strings.Contains(out, "moved "+name+" to host-b") {
		t.Errorf("move output:\n%s", out)
	}
	p.b.waitRunning(t, name)
	if r := p.a.record(t, name); r == nil || r.MovedTo != "host-b" {
		t.Fatalf("source record after the move = %+v, want moved_to host-b", r)
	}
	if got := gitState(t, wt); got != before {
		t.Errorf("worktree state changed:\nbefore:\n%s\nafter:\n%s", before, got)
	}
	if got := gitState(t, sibWT); got != sibBefore {
		t.Errorf("sibling worktree changed:\nbefore:\n%s\nafter:\n%s", sibBefore, got)
	}
	if r := p.a.record(t, sib); r == nil || r.Status != "running" {
		t.Errorf("sibling agent = %+v, want it still running", r)
	}
	if list := gitIn(t, canonical, "worktree", "list"); !strings.Contains(list, wt) || !strings.Contains(list, sibWT) {
		t.Errorf("worktree list lost an entry:\n%s", list)
	}
	if refs := gitIn(t, canonical, "for-each-ref", "refs/leo-move/"); refs != "" {
		t.Errorf("temp refs left behind:\n%s", refs)
	}
	p.assertSSHLog(t, canonical)
	wantHooks := []string{
		"pre_send move host-a host-b " + name,
		"post_receive move host-a host-b " + name + " carried",
	}
	if got := p.hookLines(t); strings.Join(got, "\n") != strings.Join(wantHooks, "\n") {
		t.Errorf("hooks ran as:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantHooks, "\n"))
	}

	if _, errOut, code := p.a.leo(t, "agent", "start", name); code == 0 || !strings.Contains(errOut, "host-b") {
		t.Fatalf("starting the moved-away agent: exit %d, stderr %s", code, errOut)
	}

	p.b.mustLeo(t, "agent", "move", name, "--to", "host-a")
	p.a.waitRunning(t, name)
	if r := p.a.record(t, name); r.MovedTo != "" {
		t.Errorf("source after the move back = %+v, want it unmarked", r)
	}
	if r := p.b.record(t, name); r == nil || r.MovedTo != "host-a" {
		t.Errorf("destination after the move back = %+v, want moved_to host-a", r)
	}
	if got := gitState(t, wt); got != before {
		t.Errorf("worktree state changed after the round trip:\nbefore:\n%s\nafter:\n%s", before, got)
	}
}

func TestAgentMoveWorktreeSubmodule(t *testing.T) {
	p := newMovePair(t)
	canonical := p.initRepo(t, true)
	name := p.a.spawnWorktree(t, "move-wt")
	wt := filepath.Join(p.ws, ".worktrees", "repo", "move-wt")
	gitIn(t, wt, "submodule", "update", "--init", "-q")
	sub := filepath.Join(wt, "libs", "sub")
	mustWrite(t, filepath.Join(sub, "lib.txt"), "lib\nlocal\n", 0o600)
	gitIn(t, sub, "commit", "-q", "-am", "unpushed sub work")
	mustWrite(t, filepath.Join(sub, "wip.txt"), "w\n", 0o600)
	before, subBefore := gitState(t, wt), gitState(t, sub)

	p.a.mustLeo(t, "agent", "move", name, "--to", "host-b")
	p.b.waitRunning(t, name)
	if r := p.a.record(t, name); r == nil || r.MovedTo != "host-b" {
		t.Fatalf("source record after the move = %+v", r)
	}
	if got := gitState(t, wt); got != before {
		t.Errorf("superproject state changed:\nbefore:\n%s\nafter:\n%s", before, got)
	}
	if got := gitState(t, sub); got != subBefore {
		t.Errorf("submodule state changed:\nbefore:\n%s\nafter:\n%s", subBefore, got)
	}
	p.assertSSHLog(t, canonical)
}

func TestAgentCopyWorktreeLocal(t *testing.T) {
	p := newMovePair(t)
	canonical := p.initRepo(t, true)
	name := p.a.spawnWorktree(t, "move-wt")
	wt := filepath.Join(p.ws, ".worktrees", "repo", "move-wt")
	gitIn(t, wt, "submodule", "update", "--init", "-q")
	dirty(t, wt)
	sid := pollAgentstoreSessionID(t, p.a.home, name, moveTimeout)
	transcript := filepath.Join(p.claudeDir, "projects", session.ProjectSlug(wt), sid+".jsonl")
	mustMkdir(t, filepath.Dir(transcript))
	mustWrite(t, transcript, `{"type":"user","message":{"content":"hi"}}`+"\n"+
		`{"type":"assistant","message":{"stop_reason":"end_turn"}}`+"\n"+
		`{"type":"user","message":{"content":"mid-turn"}}`+"\n", 0o600)
	before := gitState(t, wt)

	out := p.a.mustLeo(t, "agent", "copy", name, "--name", "cp")
	cpWT := wt + "-cp"
	if !strings.Contains(out, "copied to cp here") {
		t.Errorf("copy output:\n%s", out)
	}
	p.a.waitRunning(t, "cp")
	src, cp := p.a.record(t, name), p.a.record(t, "cp")
	if src == nil || src.Status != "running" || src.MovedTo != "" {
		t.Errorf("source after the copy = %+v, want it running and unmarked", src)
	}
	if cp.Workspace != cpWT || cp.Branch != "move-wt-cp" {
		t.Errorf("copy = %+v, want workspace %s on move-wt-cp", cp, cpWT)
	}
	if got := gitState(t, wt); got != before {
		t.Errorf("source worktree changed:\nbefore:\n%s\nafter:\n%s", before, got)
	}
	if list := gitIn(t, canonical, "worktree", "list"); !strings.Contains(list, wt+" ") || !strings.Contains(list, cpWT+" ") {
		t.Errorf("worktree list lacks the source or the copy:\n%s", list)
	}
	if got := strings.TrimSpace(gitIn(t, cpWT, "rev-parse", "--abbrev-ref", "HEAD")); got != "move-wt-cp" {
		t.Errorf("copy HEAD = %s", got)
	}
	if got, want := gitIn(t, cpWT, "status", "--porcelain=v2"), gitIn(t, wt, "status", "--porcelain=v2"); got != want {
		t.Errorf("copy's working state differs:\ncopy:\n%s\nsource:\n%s", got, want)
	}
	if data, err := os.ReadFile(filepath.Join(cpWT, "libs", "sub", "lib.txt")); err != nil || string(data) != "lib\n" {
		t.Errorf("copy's submodule = %q, %v", data, err)
	}
	cpSID := pollAgentstoreSessionID(t, p.a.home, "cp", moveTimeout)
	if cpSID == sid {
		t.Errorf("the copy shares the source's session %s", sid)
	}
	forked, err := os.ReadFile(filepath.Join(p.claudeDir, "projects", session.ProjectSlug(cpWT), sid+".jsonl"))
	if err != nil || strings.Contains(string(forked), "mid-turn") || !strings.Contains(string(forked), "end_turn") {
		t.Errorf("copy's transcript = %q, %v; want it cut at the last completed turn", forked, err)
	}
	if _, err := os.Stat(p.sshLog); err == nil {
		t.Errorf("a local copy used ssh")
	}
	if got := p.hookLines(t); len(got) != 2 || !strings.HasPrefix(got[0], "pre_send copy host-a host-a") || !strings.HasPrefix(got[1], "post_receive copy host-a host-a cp carried") {
		t.Errorf("hooks ran as:\n%s", strings.Join(got, "\n"))
	}
}
