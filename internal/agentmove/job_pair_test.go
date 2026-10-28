package agentmove

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/drain"
	"github.com/blackpaw-studio/leo/internal/git/gittest"
	"github.com/blackpaw-studio/leo/internal/session"
)

// pairEndpoint is a second host on this one: every path under from is at
// the same place under to there. Verbs reach a real Receiver with paths
// mapped, and rsync and git push really run.
type pairEndpoint struct {
	recv     *Receiver
	from, to string
}

func (p *pairEndpoint) mapTo(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte(p.from), []byte(p.to))
}
func (p *pairEndpoint) mapBack(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte(p.to), []byte(p.from))
}

func (p *pairEndpoint) Run(ctx context.Context, verb string, stdin []byte) ([]byte, error) {
	out, err := p.recv.Handle(ctx, verb, p.mapTo(stdin))
	if err != nil {
		// As the daemon answers a refusal over the wire.
		return nil, &VerbError{Code: CodeOf(err), Message: err.Error()}
	}
	return p.mapBack(out), nil
}
func (p *pairEndpoint) Local() bool               { return false }
func (p *pairEndpoint) RsyncShell() string        { return "" }
func (p *pairEndpoint) Target(path string) string { return string(p.mapTo([]byte(path))) }
func (p *pairEndpoint) Name() string              { return "pair" }
func (p *pairEndpoint) Push(ws, canonical, _ string, refspecs []string) (Cmd, bool, error) {
	args := append([]string{"-C", ws, "push", "--no-verify", "--porcelain", p.Target(canonical)}, refspecs...)
	return Cmd{Name: "git", Args: args}, true, nil
}

// writeTranscript gives the agent at ws a transcript in the claude store under root.
func writeTranscript(t *testing.T, root, ws, id string) {
	t.Helper()
	gittest.Write(t, filepath.Join(root, "claude", "projects", session.ProjectSlug(ws), id+".jsonl"), "{}\n")
}

// pairHost is one side's config: a coder template on a claude env.
func pairHost(t *testing.T, root string) *config.Config {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &config.Config{
		HomePath:     filepath.Join(root, "home"),
		Environments: map[string]map[string]string{"work": {"CLAUDE_CONFIG_DIR": filepath.Join(root, "claude")}},
		Templates:    map[string]config.TemplateConfig{"coder": {Environments: []string{"work"}}},
	}
}

// newPairMover moves agents from src to dst, both on this host, with a
// real Receiver, rsync and git on the far side.
func newPairMover(t *testing.T, src, dst string, srcCfg, dstCfg *config.Config) (*Mover, *fakeAgents, *eventLog) {
	t.Helper()
	dest := &fakeAgents{envs: &configEnvs{cfg: dstCfg}, afterReceive: []agent.Record{{Name: "web", Status: "running"}}}
	var waited []time.Duration
	recv := &Receiver{
		Config: func() (*config.Config, error) { return dstCfg, nil }, Agents: dest, Hooks: &fakeHooks{},
		Runner: ExecRunner{}, Clock: instantClock{&waited}, Locks: &KeyedLocks{},
		Host: func() (Host, error) { return laptopHost(), nil }, Version: "v1", StartConfirm: DefaultStartConfirm,
		LookPath: func(name string) (string, error) {
			if name == "git" {
				return exec.LookPath(name)
			}
			return "/bin/" + name, nil
		},
	}
	log := &eventLog{}
	mv := &Mover{
		Config: func() (*config.Config, error) { return srcCfg, nil }, Agents: &fakeSource{home: srcCfg.HomePath, log: log, envs: &configEnvs{cfg: srcCfg}},
		Endpoint: func(*config.Config, string) (Endpoint, error) {
			return &pairEndpoint{recv: recv, from: src, to: dst}, nil
		},
		Runner: ExecRunner{}, Hooks: &fakeHooks{}, Prober: &fakeProber{probe: drain.Probe{Exists: true}},
		Clock: instantClock{&waited}, Host: func() (Host, error) { return studio(), nil },
	}
	return mv, dest, log
}

// A linked worktree moves between two real repositories: the destination
// ends up with the same branch, index and files; the source keeps its
// worktree and is marked moved.
func TestMoveWorktreeEndToEndLocalPair(t *testing.T) {
	testRoot := t.TempDir()
	src, dst := filepath.Join(testRoot, "a"), filepath.Join(testRoot, "b")
	canon, dstCanon, ws := filepath.Join(src, "canon"), filepath.Join(dst, "canon"), filepath.Join(src, "wt")
	gittest.Run(t, testRoot, "init", "-q", "-b", "main", canon)
	commitFile(t, canon, "a.txt", "a\n")
	gittest.Run(t, testRoot, "clone", "-q", canon, dstCanon)
	gittest.Run(t, dstCanon, "remote", "remove", "origin")
	gittest.Run(t, canon, "worktree", "add", "-q", "-b", "feat", ws)
	commitFile(t, ws, "f.txt", "f\n")
	gittest.Write(t, filepath.Join(ws, "a.txt"), "staged\n")
	gittest.Run(t, ws, "add", "a.txt")
	gittest.Write(t, filepath.Join(ws, "f.txt"), "unstaged\n")
	gittest.Write(t, filepath.Join(ws, "new.txt"), "untracked\n")
	before := snapshot(t, ws)

	srcCfg, dstCfg := pairHost(t, src), pairHost(t, dst)
	transcript := filepath.Join("projects", session.ProjectSlug(ws), "sid-1.jsonl")
	gittest.Write(t, filepath.Join(src, "claude", transcript), "{\"turn\":1}\n")
	rec := agentstore.Record{Name: "web", Template: "coder", Harness: "claude", Workspace: ws, Branch: "feat",
		CanonicalPath: canon, SessionID: "sid-1", Environments: []string{"work"}}
	if err := agentstore.Save(srcCfg.HomePath, rec); err != nil {
		t.Fatal(err)
	}

	mv, dest, log := newPairMover(t, src, dst, srcCfg, dstCfg)
	res, err := mv.Run(context.Background(), Request{Agent: "web", Mode: ModeMove, To: "pair"}, nil)
	if err != nil {
		t.Fatalf("move: %v", err)
	}

	if after := snapshot(t, filepath.Join(dst, "wt")); after != before {
		t.Fatalf("destination state:\n%s\nsource state:\n%s", after, before)
	}
	if after := snapshot(t, ws); after != before {
		t.Fatalf("source worktree changed:\n%s", after)
	}
	if got, want := gittest.Run(t, dstCanon, "rev-parse", "feat"), gittest.Run(t, ws, "rev-parse", "HEAD"); got != want {
		t.Fatalf("destination feat = %s, want %s", got, want)
	}
	for _, repo := range []string{canon, dstCanon} {
		if refs := gittest.Run(t, repo, "for-each-ref", "refs/leo-move/"); refs != "" {
			t.Fatalf("temp refs left in %s: %s", repo, refs)
		}
	}
	if !log.has("mark web pair laptop") || res.Branch != "feat" {
		t.Fatalf("events %v, result %+v", log.all(), res)
	}
	if data, err := os.ReadFile(filepath.Join(dst, "claude", transcript)); err != nil || string(data) != "{\"turn\":1}\n" {
		t.Fatalf("transcript on the destination: %q %v", data, err)
	}
	if len(dest.received) != 1 || dest.received[0].Record.Name != "web" || dest.received[0].Record.SessionID != "sid-1" {
		t.Fatalf("received = %+v", dest.received)
	}
}

// --delete must not reach what only the destination has under the
// workspace: here a worktree nested in the destination's canonical, with
// uncommitted work, while the agent moving works in the canonical itself.
func TestMoveCanonicalKeepsDestinationOnlyNestedWorktree(t *testing.T) {
	testRoot := t.TempDir()
	src, dst := filepath.Join(testRoot, "a"), filepath.Join(testRoot, "b")
	canon, dstCanon := filepath.Join(src, "canon"), filepath.Join(dst, "canon")
	gittest.Run(t, testRoot, "init", "-q", "-b", "main", canon)
	commitFile(t, canon, "a.txt", "a\n")
	gittest.Run(t, testRoot, "clone", "-q", canon, dstCanon)
	gittest.Run(t, dstCanon, "remote", "remove", "origin")
	sibling := filepath.Join(dstCanon, ".claude", "worktrees", "sibling")
	gittest.Run(t, dstCanon, "worktree", "add", "-q", "-b", "sib", sibling)
	gittest.Write(t, filepath.Join(sibling, "wip.txt"), "uncommitted\n")
	gittest.Write(t, filepath.Join(canon, "a.txt"), "edited\n")

	srcCfg, dstCfg := pairHost(t, src), pairHost(t, dst)
	rec := agentstore.Record{Name: "web", Template: "coder", Harness: "claude", Workspace: canon, SessionID: "sid-1", Environments: []string{"work"}}
	writeTranscript(t, src, canon, "sid-1")
	if err := agentstore.Save(srcCfg.HomePath, rec); err != nil {
		t.Fatal(err)
	}
	mv, _, _ := newPairMover(t, src, dst, srcCfg, dstCfg)
	if _, err := mv.Run(context.Background(), Request{Agent: "web", Mode: ModeMove, To: "pair"}, nil); err != nil {
		t.Fatalf("move: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(sibling, "wip.txt")); err != nil || string(data) != "uncommitted\n" {
		t.Fatalf("the destination's nested worktree lost its work: %q, %v", data, err)
	}
	if got := gittest.Run(t, sibling, "symbolic-ref", "HEAD"); got != "refs/heads/sib" {
		t.Fatalf("nested worktree HEAD = %s", got)
	}
	if data, err := os.ReadFile(filepath.Join(dstCanon, "a.txt")); err != nil || string(data) != "edited\n" {
		t.Fatalf("the workspace itself did not arrive: %q, %v", data, err)
	}
}

// A start that fails its confirmation and whose rollback then fails (here
// the destination cannot read its config) leaves the agent live there. The
// refusal is definite, but nothing settled, so the source stays fenced.
func TestMoveFailedRollbackKeepsSourceFenced(t *testing.T) {
	testRoot := t.TempDir()
	src, dst := filepath.Join(testRoot, "a"), filepath.Join(testRoot, "b")
	ws := filepath.Join(src, "web")
	gittest.Write(t, filepath.Join(ws, "notes.txt"), "wip\n")
	srcCfg, dstCfg := pairHost(t, src), pairHost(t, dst)
	writeTranscript(t, src, ws, "sid-1")
	if err := agentstore.Save(srcCfg.HomePath, agentstore.Record{Name: "web", Template: "coder", Harness: "claude", Workspace: ws, SessionID: "sid-1", Environments: []string{"work"}}); err != nil {
		t.Fatal(err)
	}
	mv, dest, log := newPairMover(t, src, dst, srcCfg, dstCfg)
	dest.afterReceive = []agent.Record{{Name: "web", Status: "running", Restarts: 1}}
	dest.shadowErr = errors.New("loading config: open leo.yaml: permission denied")
	_, err := mv.Run(context.Background(), Request{Agent: "web", Mode: ModeMove, To: "pair"}, nil)
	if !HasCode(err, CodeOutcomeUnknown) || strings.Contains(fmt.Sprint(err), "leo agent start web") {
		t.Fatalf("move = %v, want outcome_unknown with no start advice", err)
	}
	recs, lerr := agentstore.Load(agentstore.FilePath(srcCfg.HomePath))
	if lerr != nil {
		t.Fatal(lerr)
	}
	if recs["web"].MovePending == nil || log.has("mark ") {
		t.Fatalf("source fence = %+v (events %v), want it kept and the agent unmarked", recs["web"].MovePending, log.all())
	}
}
