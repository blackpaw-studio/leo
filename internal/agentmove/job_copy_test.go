package agentmove

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/git/gittest"
	"github.com/blackpaw-studio/leo/internal/session"
)

func (f *moveFixture) copyReq(name, branch string) Request {
	return Request{Agent: "web", Mode: ModeCopy, To: "laptop", Name: name, Branch: branch}
}

func (f *moveFixture) copy(t *testing.T, req Request) (Result, error) {
	t.Helper()
	return f.mv.Run(context.Background(), req, nil)
}

// execLogger runs commands for real and logs them.
type execLogger struct{ log *eventLog }

func (e execLogger) Run(ctx context.Context, c Cmd) ([]byte, error) {
	e.log.add("run %s", runLine(c))
	return ExecRunner{}.Run(ctx, c)
}

// localCopy turns the fixture into a copy on this host: verbs reach a real
// Receiver in-process and every rsync and git command really runs.
func (f *moveFixture) localCopy(t *testing.T) *fakeAgents {
	t.Helper()
	dest := &fakeAgents{
		list:         []agent.Record{{Name: "web", Status: "running", Workspace: f.ws}},
		afterReceive: []agent.Record{{Name: "web", Status: "running"}, {Name: "web-copy", Status: "running"}},
	}
	var waited []time.Duration
	recv := &Receiver{
		Config: func() (*config.Config, error) { return f.cfg, nil }, Agents: dest, Hooks: &fakeHooks{},
		Runner: ExecRunner{}, Clock: instantClock{&waited}, Locks: &KeyedLocks{},
		Host: func() (Host, error) { return studio(), nil }, Version: "v1", StartConfirm: DefaultStartConfirm,
		LookPath: func(name string) (string, error) {
			if name == "git" {
				return exec.LookPath(name)
			}
			return "/bin/" + name, nil
		},
	}
	local := NewLocalEndpoint(recv.Handle)
	f.mv.Endpoint = func(*config.Config, string) (Endpoint, error) { return local, nil }
	f.mv.Runner = execLogger{f.log}
	return dest
}

// transcript writes a two-turn conversation whose second turn is still
// running: a copy takes only the first.
func (f *moveFixture) midTurnTranscript(t *testing.T) string {
	t.Helper()
	done := `{"type":"assistant","message":{"stop_reason":"end_turn"}}`
	data := `{"type":"user"}` + "\n" + done + "\n" + `{"type":"user"}` + "\n" + `{"type":"assistant","message":{"stop_reason":"tool_use"}}` + "\n"
	gittest.Write(t, filepath.Join(f.claude, "projects", session.ProjectSlug(f.ws), "sid-1.jsonl"), data)
	return `{"type":"user"}` + "\n" + done + "\n"
}

func TestCopyNameSuffixes(t *testing.T) {
	f := newMoveFixture(t)
	f.midTurnTranscript(t)
	if err := agentstore.Save(f.home, agentstore.Record{Name: "web-copy", Workspace: "/elsewhere"}); err != nil {
		t.Fatal(err)
	}
	f.ep.answer[VerbPreflight] = func(m Manifest) ([]byte, error) {
		resp := PreflightResponse{Protocol: Protocol, Host: laptopHost(), OK: true, StagingDir: "/Users/me/.leo/state/moves/" + m.Copy.Name + "/staging"}
		if m.Copy.Name == "web-copy-2" {
			resp.OK, resp.Problems = false, []Problem{{Code: CodeNameTaken, Message: "taken"}}
		}
		return json.Marshal(resp)
	}
	res, err := f.copy(t, f.copyReq("", ""))
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	var tried []string
	for _, m := range f.ep.manifests {
		tried = append(tried, m.Copy.Name)
	}
	if strings.Join(tried, ",") != "web-copy-2,web-copy-3,web-copy-3" {
		t.Fatalf("names tried = %v, want web-copy skipped (local), -2 taken there, -3", tried)
	}
	if res.Name != "web-copy-3" || res.Workspace != f.ws+"-web-copy-3" || res.Branch != "" {
		t.Fatalf("result = %+v", res)
	}
	if _, err := f.copy(t, f.copyReq("web-copy", "")); !HasCode(err, CodeNameTaken) {
		t.Fatalf("copy onto a local name = %v, want name_taken", err)
	}
	f.ep.answer[VerbPreflight] = jsonAnswer(PreflightResponse{Protocol: Protocol, Host: laptopHost(), Problems: []Problem{{Code: CodeNameTaken, Message: "taken"}}})
	if _, err := f.copy(t, f.copyReq("mine", "")); !HasCode(err, CodeNameTaken) {
		t.Fatalf("an explicit name taken there = %v, want name_taken, not a suffix", err)
	}
}

// A copy never stops or marks the source; its workspace fills a partial
// that the commit renames.
func TestCopyWorkspacePartialRename(t *testing.T) {
	f := newMoveFixture(t)
	f.midTurnTranscript(t)
	f.ep.answer[VerbPreflight] = jsonAnswer(PreflightResponse{Protocol: Protocol, Host: laptopHost(), OK: true, StagingDir: "/Users/me/.leo/state/moves/web-copy/staging", SessionRoot: "/Users/me/.claude-work"})
	if _, err := f.copy(t, f.copyReq("", "")); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if f.log.has("stop ") || f.log.has("mark ") || !f.log.has("lock web copy") {
		t.Fatalf("events = %v", f.log.all())
	}
	if !f.log.has("run rsync -a --delete --itemize-changes --stats -e ssh -o BatchMode=yes --exclude=node_modules/ --exclude=/.worktrees/ " + f.ws + "/ u@laptop:" + f.ws + "-web-copy.leo-partial/") {
		t.Fatalf("workspace rsync does not fill the partial: %v", f.log.all())
	}
	if f.log.has("run rsync -a -R") {
		t.Fatal("a claude copy sent the source's session files; its snapshot travels in staging")
	}
	m := f.ep.manifests[len(f.ep.manifests)-1]
	if m.Mode != ModeCopy || m.Copy == nil || m.Copy.SessionID == "" || m.Copy.SessionID == "sid-1" || m.SourceSessionID != "sid-1" {
		t.Fatalf("commit manifest = %+v / %+v", m, m.Copy)
	}
}

func TestCopyMidTurnClaudeTruncates(t *testing.T) {
	f := newMoveFixture(t)
	want := f.midTurnTranscript(t)
	f.prober.probe.Busy = true
	if _, err := f.copy(t, f.copyReq("", "")); err != nil {
		t.Fatalf("copy of a busy claude agent: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(StagingDir(f.home, "web"), "harness", "claude", "sid-1.jsonl"))
	if err != nil || string(got) != want {
		t.Fatalf("staged transcript = %q %v, want the completed turn only", got, err)
	}
}

func TestCopyMidTurnCodexRefused(t *testing.T) {
	f := newMoveFixture(t)
	f.rec.Harness = "codex"
	if err := agentstore.Save(f.home, f.rec); err != nil {
		t.Fatal(err)
	}
	f.prober.probe.Busy = true
	if _, err := f.copy(t, Request{Agent: "web", Mode: ModeCopy, To: "laptop", Force: true}); !HasCode(err, CodeAgentBusy) {
		t.Fatalf("copy of a busy codex agent = %v, want agent_busy even with force", err)
	}
	if f.log.has("verb commit") || f.log.has("run ") {
		t.Fatalf("events = %v", f.log.all())
	}
}

func TestLocalCopyNoSSH(t *testing.T) {
	f := newMoveFixture(t)
	want := f.midTurnTranscript(t)
	dest := f.localCopy(t)
	res, err := f.copy(t, Request{Agent: "web", Mode: ModeCopy})
	if err != nil {
		t.Fatalf("local copy: %v", err)
	}
	for _, e := range f.log.all() {
		if strings.Contains(e, "ssh") || strings.Contains(e, " -e ") {
			t.Fatalf("a local copy used ssh: %s", e)
		}
	}
	newWS := f.ws + "-web-copy"
	if data, err := os.ReadFile(filepath.Join(newWS, "notes.txt")); err != nil || string(data) != "wip\n" {
		t.Fatalf("copied workspace: %q %v", data, err)
	}
	if _, err := os.Stat(newWS + PartialSuffix); err == nil {
		t.Fatal("partial left behind")
	}
	if len(dest.received) != 1 || !dest.received[0].Copy || dest.received[0].Record.Name != "web-copy" || dest.received[0].ForkFrom != "sid-1" {
		t.Fatalf("received = %+v", dest.received)
	}
	sid := dest.received[0].Record.SessionID
	placed, err := os.ReadFile(filepath.Join(f.claude, "projects", session.ProjectSlug(newWS), "sid-1.jsonl"))
	if err != nil || string(placed) != want || sid == "" || sid == "sid-1" {
		t.Fatalf("copy's transcript = %q %v (fork id %q)", placed, err, sid)
	}
	if res.Host != "studio" || res.To != "" || f.log.has("stop ") || f.log.has("mark ") {
		t.Fatalf("result %+v, events %v", res, f.log.all())
	}
}

func TestCopyNotWiredToPersistentTask(t *testing.T) {
	f := newMoveFixture(t)
	f.midTurnTranscript(t)
	f.cfg.Tasks = map[string]config.TaskConfig{"nightly": {Runtime: "persistent", Template: "web"}}
	f.cfg.Templates["web"] = f.cfg.Templates["coder"]
	dest := f.localCopy(t)
	res, err := f.copy(t, Request{Agent: "web", Mode: ModeCopy})
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if dest.received[0].Record.Name == "web" {
		t.Fatal("the copy took the task's agent name")
	}
	for _, n := range res.Notes {
		if n.Code == NotePersistentTaskWarned && strings.Contains(n.Message, "not wired") {
			return
		}
	}
	t.Fatalf("notes = %+v, want one saying the copy is not wired to the task", res.Notes)
}

// worktreeCopy is a local copy of "web" on a linked worktree (branch feat)
// with a staged, an unstaged and an untracked change.
func newWorktreeCopy(t *testing.T) (*moveFixture, *fakeAgents, string) {
	t.Helper()
	f, repo := newWorktreeFixture(t)
	gittest.Write(t, filepath.Join(f.ws, "new.txt"), "untracked\n")
	f.midTurnTranscript(t)
	return f, f.localCopy(t), repo
}

// stateOf is a workspace's files, index and status, without the branch.
func stateOf(t *testing.T, dir string) string {
	t.Helper()
	var keep []string
	for _, l := range strings.Split(gittest.Status(t, dir), "\n") {
		if !strings.HasPrefix(l, "# branch.head") && !strings.HasPrefix(l, "# branch.upstream") && !strings.HasPrefix(l, "# branch.ab") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n") + "\n" + gittest.Run(t, dir, "diff", "--cached") + "\n" + gittest.Run(t, dir, "diff")
}

func TestCopyWorktreeNewBranchAndPath(t *testing.T) {
	f, dest, repo := newWorktreeCopy(t)
	before := stateOf(t, f.ws)
	res, err := f.copy(t, Request{Agent: "web", Mode: ModeCopy})
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	newWS := f.ws + "-web-copy"
	if res.Branch != "feat-web-copy" || res.Workspace != newWS {
		t.Fatalf("result = %+v", res)
	}
	if got := gittest.Run(t, newWS, "symbolic-ref", "HEAD"); got != "refs/heads/feat-web-copy" {
		t.Fatalf("copy HEAD = %s", got)
	}
	if after := stateOf(t, newWS); after != before {
		t.Fatalf("copy state:\n%s\nsource state:\n%s", after, before)
	}
	if list := gittest.Run(t, repo, "worktree", "list"); !strings.Contains(list, newWS+" ") || strings.Contains(list, PartialSuffix) {
		t.Fatalf("worktrees:\n%s", list)
	}
	rec := dest.received[0].Record
	if rec.Branch != "feat-web-copy" || rec.CanonicalPath != repo || rec.Workspace != newWS {
		t.Fatalf("copy record = %+v", rec)
	}
	if refs := gittest.Run(t, repo, "for-each-ref", "refs/leo-move/"); refs != "" {
		t.Fatalf("temp refs left: %s", refs)
	}
}

func TestLocalCopyWorktreeSourceUntouched(t *testing.T) {
	f, _, _ := newWorktreeCopy(t)
	head := gittest.Run(t, f.ws, "rev-parse", "HEAD")
	index := gittest.Run(t, f.ws, "write-tree")
	status := gittest.Status(t, f.ws)
	if _, err := f.copy(t, Request{Agent: "web", Mode: ModeCopy}); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if gittest.Run(t, f.ws, "rev-parse", "HEAD") != head || gittest.Run(t, f.ws, "write-tree") != index || gittest.Status(t, f.ws) != status {
		t.Fatal("the source worktree changed")
	}
	if gittest.Run(t, f.ws, "symbolic-ref", "HEAD") != "refs/heads/feat" {
		t.Fatal("the source left its branch")
	}
}

func TestCopyWorktreeBranchExistsRefused(t *testing.T) {
	f, dest, repo := newWorktreeCopy(t)
	gittest.Run(t, repo, "branch", "taken")
	_, err := f.copy(t, Request{Agent: "web", Mode: ModeCopy, Branch: "taken"})
	if !HasCode(err, CodeBranchExists) {
		t.Fatalf("copy onto an existing branch = %v, want branch_exists", err)
	}
	if len(dest.received) != 0 || isDir(f.ws+"-web-copy") || isDir(f.ws+"-web-copy"+PartialSuffix) {
		t.Fatal("a refused copy left something behind")
	}
	// A remote copy checks this host's repository before anything is sent.
	f, _ = newWorktreeFixture(t)
	f.midTurnTranscript(t)
	gittest.Run(t, f.rec.CanonicalPath, "branch", "feat-web-copy")
	if _, err := f.copy(t, f.copyReq("", "")); !HasCode(err, CodeBranchExists) || f.log.has("verb git-prepare") {
		t.Fatalf("remote copy onto a branch this host has = %v", err)
	}
}

func TestCopyDetachedNeedsBranch(t *testing.T) {
	f, _, _ := newWorktreeCopy(t)
	gittest.Run(t, f.ws, "checkout", "-q", "--detach")
	if _, err := f.copy(t, Request{Agent: "web", Mode: ModeCopy}); !HasCode(err, CodeBadRequest) || !strings.Contains(err.Error(), "--branch") {
		t.Fatalf("copy of a detached HEAD = %v, want a --branch request", err)
	}
	if _, err := f.copy(t, Request{Agent: "web", Mode: ModeCopy, Branch: "side"}); err != nil {
		t.Fatalf("copy with --branch: %v", err)
	}
}

// A main-mode agent's copy is a linked worktree of its repository.
func TestCopyMainModeBecomesLinkedWorktree(t *testing.T) {
	f := newMoveFixture(t)
	repo := gittest.NewRepo(t)
	gittest.Run(t, repo, "worktree", "add", "-q", "-b", "side", filepath.Join(filepath.Dir(repo), "side"))
	gittest.Write(t, filepath.Join(repo, "a.txt"), "changed\n")
	f.useWorkspace(t, repo, "", "")
	f.midTurnTranscript(t)
	dest := f.localCopy(t)
	if _, err := f.copy(t, Request{Agent: "web", Mode: ModeCopy}); err != nil {
		t.Fatalf("copy: %v", err)
	}
	newWS := repo + "-web-copy"
	if _, err := os.Stat(filepath.Join(newWS, ".git")); err != nil || isDir(filepath.Join(newWS, ".git")) {
		t.Fatalf("copy is not a linked worktree (.git file): %v", err)
	}
	if gittest.Run(t, newWS, "symbolic-ref", "HEAD") != "refs/heads/main-web-copy" || stateOf(t, newWS) != stateOf(t, repo) {
		t.Fatal("copy branch or state wrong")
	}
	if rec := dest.received[0].Record; rec.Branch != "main-web-copy" || rec.CanonicalPath != repo {
		t.Fatalf("copy record = %+v", rec)
	}
}

// A main-mode superproject's copy carries its absorbed submodules: the
// modules partial lands in the new worktree's gitdir and works there, with
// attached submodule branches forked.
func TestCopyWorktreeWithSubmodulesMovesPartial(t *testing.T) {
	f := newMoveFixture(t)
	super := gittest.SuperWithSubmodules(t)
	gittest.Run(t, super, "worktree", "add", "-q", "-b", "side", filepath.Join(filepath.Dir(super), "side"))
	gittest.Run(t, filepath.Join(super, "libs/a"), "checkout", "-q", "-b", "work")
	gittest.Write(t, filepath.Join(super, "libs/a/x.txt"), "edited\n")
	f.useWorkspace(t, super, "", "")
	f.midTurnTranscript(t)
	f.localCopy(t)
	modules := treeDigest(t, filepath.Join(super, ".git", "modules"))
	if _, err := f.copy(t, Request{Agent: "web", Mode: ModeCopy}); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if treeDigest(t, filepath.Join(super, ".git", "modules")) != modules {
		t.Fatal("the source's modules dir changed")
	}
	newWS := super + "-web-copy"
	sub := filepath.Join(newWS, "libs/a")
	if got := gittest.Run(t, sub, "symbolic-ref", "HEAD"); got != "refs/heads/work-web-copy" {
		t.Fatalf("copied submodule HEAD = %s, want its branch forked", got)
	}
	if got := gittest.Run(t, sub, "status", "--porcelain"); got != "M x.txt" {
		t.Fatalf("copied submodule status = %q", got)
	}
	if gittest.Run(t, filepath.Join(super, "libs/a"), "symbolic-ref", "HEAD") != "refs/heads/work" {
		t.Fatal("source submodule left its branch")
	}
	gitDir := gittest.Run(t, newWS, "rev-parse", "--absolute-git-dir")
	if isDir(filepath.Join(gitDir, ModulesPartial)) || !isDir(filepath.Join(gitDir, "modules")) {
		t.Fatal("modules partial not renamed into place")
	}
}

// treeDigest is every file under dir with its contents, in walk order.
func treeDigest(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		b.WriteString(p + "\x00" + string(data) + "\x00")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
