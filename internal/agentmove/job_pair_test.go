package agentmove

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	return p.mapBack(out), err
}
func (p *pairEndpoint) Local() bool               { return false }
func (p *pairEndpoint) RsyncShell() string        { return "" }
func (p *pairEndpoint) Target(path string) string { return string(p.mapTo([]byte(path))) }
func (p *pairEndpoint) Name() string              { return "pair" }
func (p *pairEndpoint) Push(ws, canonical, _ string, refspecs []string) (Cmd, bool, error) {
	args := append([]string{"-C", ws, "push", "--no-verify", "--porcelain", p.Target(canonical)}, refspecs...)
	return Cmd{Name: "git", Args: args}, true, nil
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
		Config: func() (*config.Config, error) { return srcCfg, nil }, Agents: &fakeSource{log: log, envs: &configEnvs{cfg: srcCfg}},
		Endpoint: func(*config.Config, string) (Endpoint, error) {
			return &pairEndpoint{recv: recv, from: src, to: dst}, nil
		},
		Runner: ExecRunner{}, Hooks: &fakeHooks{}, Prober: &fakeProber{probe: drain.Probe{Exists: true}},
		Clock: instantClock{&waited}, Host: func() (Host, error) { return studio(), nil },
	}
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
