package consult

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

// gitProbeTimeout bounds each git call the confinement check makes, so a hung
// repository can only fail the Git-worktree rule, never stall a dispatch.
const gitProbeTimeout = 5 * time.Second

// CallerHome is where a root caller lives: the workspace its cwd must stay in
// and the harness it runs under (which decides its scratch directory).
type CallerHome struct {
	Workspace string
	Harness   string
}

// confinementDeps are the filesystem, git and record lookups the checker
// needs, injected so the rule table tests without a live daemon.
type confinementDeps struct {
	evalSymlinks func(string) (string, error)
	// gitCommonDir and gitTopLevel take a real (symlink-resolved) directory
	// and return git's absolute answer for it.
	gitCommonDir func(dir string) (string, error)
	gitTopLevel  func(dir string) (string, error)
	// worktreeOwner returns the root caller of the isolated dispatch with
	// this id, and false when no such isolated dispatch exists.
	worktreeOwner func(id string) (caller string, ok bool)
	// tmpDir is the base directory of the harness scratch space ($TMPDIR
	// equivalent claude uses); uid is the daemon's user id.
	tmpDir string
	uid    int
}

type confinementInput struct {
	Cwd          string
	RootCaller   string
	Home         CallerHome
	WorktreesDir string
}

type confinementVerdict struct {
	Allowed bool
	// Rule names the rule that allowed the cwd; empty when refused.
	Rule string
	// Roots describes every allowed root, for refusal and warning messages.
	Roots []string
}

// within reports whether p is root or lies beneath it, by path components.
func within(p, root string) bool {
	if root == string(filepath.Separator) {
		return filepath.IsAbs(p)
	}
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// claudeScratchKey is how claude names a workspace's directory under its temp
// dir: s.replace(/[^a-zA-Z0-9]/g, "-") in JS, i.e. one '-' per UTF-16 code
// unit outside [A-Za-z0-9].
func claudeScratchKey(workspace string) string {
	var b strings.Builder
	for _, r := range workspace {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r > 0xFFFF:
			// JS strings are UTF-16: a supplementary-plane character is a
			// surrogate pair, two code units, each replaced.
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// confinementRoot is one place a cwd may be: a description for messages, the
// rule name reported when it allows, and the membership test.
type confinementRoot struct {
	desc, rule string
	contains   func(cwd string) bool
}

// checkConfinement decides whether a dispatch's requested cwd stays inside
// what its root caller may reach. Roots that cannot be resolved are dropped,
// and a cwd that cannot be resolved is refused.
func checkConfinement(ctx context.Context, deps confinementDeps, in confinementInput) confinementVerdict {
	ws, err := deps.evalSymlinks(in.Home.Workspace)
	if err != nil {
		return confinementVerdict{Roots: []string{in.Home.Workspace}}
	}
	roots := confinementRoots(deps, in, ws)
	v := confinementVerdict{}
	for _, r := range roots {
		v.Roots = append(v.Roots, r.desc)
	}
	cwd, err := deps.evalSymlinks(in.Cwd)
	if err != nil {
		return v
	}
	for _, r := range roots {
		if r.contains(cwd) {
			v.Allowed, v.Rule = true, r.rule
			break
		}
	}
	return v
}

func confinementRoots(deps confinementDeps, in confinementInput, ws string) []confinementRoot {
	roots := []confinementRoot{{
		desc: "workspace " + ws, rule: "workspace",
		contains: func(cwd string) bool { return within(cwd, ws) },
	}}
	if common, ok := workspaceRepoCommonDir(deps, ws); ok {
		roots = append(roots, confinementRoot{
			desc: "Git worktrees of " + common, rule: "git worktree of the workspace repository",
			contains: func(cwd string) bool {
				got, err := resolvedProbe(deps, deps.gitCommonDir, cwd)
				return err == nil && got == common
			},
		})
	}
	if dir, err := deps.evalSymlinks(in.WorktreesDir); err == nil {
		roots = append(roots, confinementRoot{
			desc: fmt.Sprintf("dispatch worktrees under %s started by %s", dir, in.RootCaller), rule: "dispatch worktree of the same caller",
			contains: func(cwd string) bool { return ownedWorktree(deps, dir, in.RootCaller, cwd) },
		})
	}
	if in.Home.Harness == "" || in.Home.Harness == config.DefaultHarnessName {
		roots = append(roots, scratchRoots(deps, in.Home.Workspace, ws)...)
	}
	return roots
}

// workspaceRepoCommonDir returns the resolved Git common dir of the workspace's
// repository, only when the repository's top level and its common dir both lie
// inside the workspace: a workspace nested in a larger repo must not grant the
// whole repo, and an agent that forges .git metadata (a gitdir file or a
// commondir pointing elsewhere) must not make a foreign repo its own.
func workspaceRepoCommonDir(deps confinementDeps, ws string) (string, bool) {
	top, err := resolvedProbe(deps, deps.gitTopLevel, ws)
	if err != nil || !within(top, ws) {
		return "", false
	}
	common, err := resolvedProbe(deps, deps.gitCommonDir, ws)
	if err != nil || !within(common, ws) {
		return "", false
	}
	return common, true
}

// resolvedProbe runs a git probe on dir and resolves symlinks in its answer.
func resolvedProbe(deps confinementDeps, probe func(string) (string, error), dir string) (string, error) {
	out, err := probe(dir)
	if err != nil {
		return "", err
	}
	return deps.evalSymlinks(out)
}

// ownedWorktree reports whether cwd is inside <worktrees>/<id> for an isolated
// dispatch whose root caller is caller.
func ownedWorktree(deps confinementDeps, worktrees, caller, cwd string) bool {
	if !within(cwd, worktrees) || cwd == worktrees {
		return false
	}
	id, _, _ := strings.Cut(strings.TrimPrefix(cwd, worktrees+string(filepath.Separator)), string(filepath.Separator))
	if !ValidDispatchID(id) {
		return false
	}
	owner, ok := deps.worktreeOwner(id)
	return ok && owner == caller
}

// scratchRoots are claude's per-workspace temp directories, keyed by the
// workspace path as configured and as resolved. A root is the literal
// <base>/claude-<uid>/<key> beneath the resolved temp base; it is dropped
// unless every component is a real directory, so a symlink an agent plants in
// the scratch space can never make another workspace a root.
func scratchRoots(deps confinementDeps, rawWorkspace, ws string) []confinementRoot {
	base, err := deps.evalSymlinks(deps.tmpDir)
	if err != nil {
		return nil
	}
	var roots []confinementRoot
	seen := map[string]bool{}
	for _, key := range []string{claudeScratchKey(rawWorkspace), claudeScratchKey(ws)} {
		if seen[key] {
			continue
		}
		seen[key] = true
		dir := filepath.Join(base, fmt.Sprintf("claude-%d", deps.uid), key)
		if !realDirChain(base, dir) {
			continue
		}
		roots = append(roots, confinementRoot{
			desc: "claude scratch directory " + dir, rule: "harness scratch directory",
			contains: func(cwd string) bool { return within(cwd, dir) },
		})
	}
	return roots
}

// realDirChain reports whether every component of dir below base is a
// directory and not a symlink.
func realDirChain(base, dir string) bool {
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	cur := base
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}

// gitProbes returns the production git probes, run through newCmd (the
// dispatcher's git seam) with a bounded timeout.
func gitProbes(newCmd func(name string, args ...string) *exec.Cmd) (commonDir, topLevel func(dir string) (string, error)) {
	run := func(dir string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), gitProbeTimeout)
		defer cancel()
		out, err := execGitContext(ctx, newCmd, append([]string{"-C", dir}, args...)...)
		if err != nil {
			return "", fmt.Errorf("probing %s: %w", dir, err)
		}
		return string(out), nil
	}
	return func(dir string) (string, error) {
			return run(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		},
		func(dir string) (string, error) { return run(dir, "rev-parse", "--show-toplevel") }
}

// confineCwd applies defaults.dispatch_confinement to a dispatch's requested
// cwd (the source cwd, before any worktree isolation). rootCaller is the root
// of the dispatch tree. It returns a warning to attach in warn mode and an
// invalid-request error in enforce mode.
func (d *Dispatcher) confineCwd(ctx context.Context, cfg *config.Config, rootCaller, cwd string) (string, error) {
	mode := cfg.DispatchConfinement()
	if mode == config.ConfinementOff || rootCaller == "" {
		return "", nil
	}
	d.mu.Lock()
	resolver := d.callerWorkspaces
	d.mu.Unlock()
	var home CallerHome
	resolved := false
	if resolver != nil {
		home, resolved = resolver.CallerWorkspace(cfg, rootCaller)
	}
	if !resolved {
		// API clients (leoterm-ios, peers) are identified by config and have
		// no workspace; anything else named but unresolvable fails closed.
		if _, ok := cfg.APIClients[rootCaller]; ok {
			return "", nil
		}
		if mode == config.ConfinementEnforce {
			return "", invalidf("caller %q has no resolvable workspace, so cwd %s cannot be confined (defaults.dispatch_confinement=enforce)", rootCaller, cwd)
		}
		return fmt.Sprintf("dispatch caller %q has no resolvable workspace, so cwd %s cannot be confined; defaults.dispatch_confinement=enforce would refuse it", rootCaller, cwd), nil
	}
	verdict := checkConfinement(ctx, d.confinementDeps(), confinementInput{
		Cwd: cwd, RootCaller: rootCaller, Home: home, WorktreesDir: d.worktreesDir(),
	})
	if verdict.Allowed {
		return "", nil
	}
	roots := strings.Join(verdict.Roots, "; ")
	if mode == config.ConfinementEnforce {
		return "", invalidf("cwd %s is outside caller %q's workspace %s; allowed roots: %s", cwd, rootCaller, home.Workspace, roots)
	}
	return fmt.Sprintf("dispatch cwd %s is outside caller %q's workspace %s (allowed roots: %s); defaults.dispatch_confinement=enforce would refuse it", cwd, rootCaller, home.Workspace, roots), nil
}

// worktreesDir is where isolated dispatches' worktrees live; empty without a
// file recorder, which also means no isolated dispatch exists to match.
func (d *Dispatcher) worktreesDir() string {
	recorder, ok := d.recorder.(*FileRecorder)
	if !ok {
		return ""
	}
	return filepath.Join(filepath.Dir(recorder.dir), worktreesDirName)
}

func (d *Dispatcher) confinementDeps() confinementDeps {
	common, top := gitProbes(d.GitCommand)
	tmp := os.Getenv("CLAUDE_CODE_TMPDIR")
	if tmp == "" {
		tmp = "/tmp"
	}
	return confinementDeps{
		evalSymlinks: filepath.EvalSymlinks,
		gitCommonDir: common,
		gitTopLevel:  top,
		worktreeOwner: func(id string) (string, bool) {
			rec, _, err := d.lookup(id)
			if err != nil || rec.Isolation != "worktree" {
				return "", false
			}
			return rec.Caller, true
		},
		tmpDir: tmp,
		uid:    os.Getuid(),
	}
}

// SetCallerWorkspaces installs the resolver that maps a root caller to its
// workspace; without one every named caller is unresolvable.
func (d *Dispatcher) SetCallerWorkspaces(r CallerWorkspaces) {
	d.mu.Lock()
	d.callerWorkspaces = r
	d.mu.Unlock()
}
