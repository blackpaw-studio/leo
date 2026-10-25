package consult

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const worktreesDirName = "worktrees"

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func worktreeSuffix() string { return strings.TrimPrefix(newID(), "d-")[:4] }

func worktreeSlug(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		s = "dispatch"
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

func (d *Dispatcher) git(args ...string) ([]byte, error) { return execGit(d.GitCommand, args...) }

// gitWaitDelay is how long a killed git call's output pipes may stay open,
// held by a child it left behind, before execGitContext stops waiting on them.
const gitWaitDelay = time.Second

// execGit runs git through newCmd, the injectable seam tests replace.
func execGit(newCmd func(name string, args ...string) *exec.Cmd, args ...string) ([]byte, error) {
	return execGitContext(context.Background(), newCmd, args...)
}

// execGitContext runs git through newCmd and kills it once ctx is done. The
// seam builds commands without a context, so the kill is done here; the
// error then wraps ctx's.
func execGitContext(ctx context.Context, newCmd func(name string, args ...string) *exec.Cmd, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	cmd := newCmd("git", args...)
	// Callers parse the output, so it is stdout alone: a warning on stderr
	// (a broken ref, say) must never read as data. Stderr only explains a failure.
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if ctx.Done() == nil {
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return bytes.TrimSpace(stdout.Bytes()), nil
	}
	cmd.WaitDelay = gitWaitDelay
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return bytes.TrimSpace(stdout.Bytes()), nil
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-waited
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
	}
}

func (d *Dispatcher) prepareWorktree(state *runState, req Request) error {
	recorder, ok := d.recorder.(*FileRecorder)
	if !ok {
		return invalidf("worktree isolation requires persistent dispatch recording")
	}
	rootRaw, err := d.git("-C", req.Cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return invalidf("worktree isolation requires a Git repository: %v", err)
	}
	root := string(rootRaw)
	baseRaw, err := d.git("-C", root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return invalidf("worktree isolation requires an existing HEAD commit: %v", err)
	}
	base := string(baseRaw)
	path := filepath.Join(filepath.Dir(recorder.dir), worktreesDirName, state.record.ID)
	label := req.Name
	if label == "" {
		label = req.Template
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return fmt.Errorf("creating worktree directory: %w", err)
	}
	for attempts := 0; attempts < 128; attempts++ {
		suffix := d.WorktreeSuffix()
		branch := "leo/" + worktreeSlug(label) + "-" + suffix
		if _, err := d.git("check-ref-format", "--branch", branch); err != nil {
			continue
		}
		if _, err := d.git("-C", root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			continue
		}
		d.mu.Lock()
		state.record.Cwd, state.record.SourceCwd = path, req.Cwd
		state.record.RepositoryRoot, state.record.BaseCommit = root, base
		state.record.Worktree, state.record.Branch = path, branch
		state.record.WorktreeState = WorktreeCreating
		persistErr := d.persistIsolationLocked(state)
		d.mu.Unlock()
		if persistErr != nil {
			return fmt.Errorf("recording creating worktree: %w", persistErr)
		}
		if _, err := d.git("-C", root, "worktree", "add", "-b", branch, path, base); err != nil {
			if _, collision := d.git("-C", root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); collision == nil {
				continue
			}
			return fmt.Errorf("creating isolated worktree: %w", err)
		}
		d.mu.Lock()
		state.record.WorktreeState = WorktreePresent
		persistErr = d.persistIsolationLocked(state)
		d.mu.Unlock()
		if persistErr != nil {
			return fmt.Errorf("recording present worktree: %w", persistErr)
		}
		return nil
	}
	return fmt.Errorf("allocating a unique worktree branch")
}

func (d *Dispatcher) persistIsolationLocked(state *runState) error {
	h, ok := state.handle.(recordHandle)
	if !ok {
		return fmt.Errorf("recorder does not support isolation metadata")
	}
	return h.SetRecord(cloneRecord(state.record))
}

// cleanupWorktree removes id's isolated worktree when the safety rules allow
// it. A run whose writer may still be live is deferred, not kept: deferred and
// kept records are both re-evaluated on every later terminal transition (pane
// closed, release, restart) and by `leo dispatch prune`, so a postponed
// cleanup is never lost and a kept one is re-checked against the disk.
func (d *Dispatcher) cleanupWorktree(id string) Record {
	return d.cleanupWorktreeWith(id, d.git, d.git)
}

// cleanupWorktreeWith is cleanupWorktree with the git that assesses the
// worktree and the git that removes it supplied by the caller.
func (d *Dispatcher) cleanupWorktreeWith(id string, assess, mutate gitFunc) Record {
	rec, state, due := d.cleanupDue(id)
	if !due {
		return rec
	}
	return d.applyWorktreeAssessment(assess, mutate, rec, state)
}

// cleanupDue looks id up and applies cleanupPrecheck to it: it returns the
// record (deferred, when that was the decision), its live run if any, and
// whether its worktree is due an assessment.
func (d *Dispatcher) cleanupDue(id string) (Record, *runState, bool) {
	rec, state, err := d.lookup(id)
	if err != nil {
		return rec, nil, false
	}
	rec, due := d.cleanupPrecheck(rec, state)
	return rec, state, due
}

// cleanupPrecheck applies cleanupWorktree's checks that need no git: it
// returns rec unchanged, or deferred (and recorded so), when the worktree
// must not be assessed now, and reports whether the writers are known gone
// so the worktree is due its assessment.
func (d *Dispatcher) cleanupPrecheck(rec Record, state *runState) (Record, bool) {
	if rec.Isolation != "worktree" || rec.WorktreeState == WorktreeRemoved {
		return rec, false
	}
	deferCleanup := func() (Record, bool) { return d.setWorktreeState(rec, state, WorktreeDeferred), false }
	if rec.Mode == ModeInteractive {
		switch {
		case rec.PaneID != "":
			d.mu.Lock()
			rt := d.interactiveRuntime
			d.mu.Unlock()
			if rt == nil {
				return deferCleanup()
			}
			alive, certain := paneAlive(rt, rec.PaneID)
			if !certain || alive {
				return deferCleanup()
			}
		case !rec.Status.Terminal():
			return deferCleanup()
		}
		return rec, true
	}
	if !rec.Status.Terminal() {
		return rec, false
	}
	if state != nil {
		select {
		case <-state.done:
		default:
			return rec, false
		}
	}
	if state == nil || (state.headlessStarted && !d.headlessWritersGone(state)) {
		return deferCleanup()
	}
	return rec, true
}

// applyWorktreeAssessment acts on assessWorktree's verdict for a worktree
// whose writers are known to be gone. assess runs the read-only checks and
// mutate the removal.
func (d *Dispatcher) applyWorktreeAssessment(assess, mutate gitFunc, rec Record, state *runState) Record {
	keep := func() Record { return d.setWorktreeState(rec, state, WorktreeKept) }
	switch assessWorktree(assess, rec).Verdict {
	case verdictRemove:
		if err := removeWorktreeCheckout(mutate, rec); err != nil {
			return keep()
		}
		tip, _ := deleteMergedBranch(assess, mutate, rec)
		return d.setWorktreeStateTip(rec, state, WorktreeRemoved, tip)
	case verdictGone:
		if err := pruneRepositoryWorktrees(mutate, rec.RepositoryRoot); err != nil {
			return keep()
		}
		tip, _ := deleteMergedBranch(assess, mutate, rec)
		return d.setWorktreeStateTip(rec, state, WorktreeRemoved, tip)
	default:
		return keep()
	}
}

// addWorktree recreates rec's removed worktree, from its branch or, when
// cleanup deleted the branch, from the base commit it pointed at.
func (d *Dispatcher) addWorktree(rec Record) error {
	if _, err := d.git("-C", rec.RepositoryRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+rec.Branch); err == nil {
		_, err = d.git("-C", rec.RepositoryRoot, "worktree", "add", rec.Worktree, rec.Branch)
		return err
	}
	_, err := d.git("-C", rec.RepositoryRoot, "worktree", "add", "-b", rec.Branch, rec.Worktree, rec.BaseCommit)
	return err
}

func (d *Dispatcher) headlessWritersGone(state *runState) bool {
	if !state.headlessStarted {
		return true
	}
	return processGroupGone(d.ProcessCommand, state.pgid)
}

// processGroupGone reports positive confirmation that no process of pgid
// remains. An unknown group (0, 1, or the caller's own) or an unreadable
// process table is not confirmation. The daemon and prune share this check.
func processGroupGone(command func(context.Context, string, ...string) *exec.Cmd, pgid int) bool {
	if pgid <= 1 || pgid == syscall.Getpgrp() {
		return false
	}
	present, known := processGroupPresent(command, pgid)
	return known && !present
}

func (d *Dispatcher) setWorktreeState(rec Record, state *runState, disposition WorktreeState) Record {
	return d.setWorktreeStateTip(rec, state, disposition, "")
}

// setWorktreeStateTip records disposition, plus branchTip when cleanup
// deleted the branch (empty leaves BranchTip as it was).
func (d *Dispatcher) setWorktreeStateTip(rec Record, state *runState, disposition WorktreeState, branchTip string) Record {
	if state != nil {
		d.mu.Lock()
		state.record.WorktreeState = disposition
		if branchTip != "" {
			state.record.BranchTip = branchTip
		}
		rec = cloneRecord(state.record)
		if h, ok := state.handle.(recordHandle); ok {
			if err := h.SetRecord(rec); err != nil {
				state.record.WorktreeState = WorktreeKept
				state.record.Error = strings.TrimSpace(state.record.Error + "; cleanup metadata was not persisted: " + err.Error())
				rec = cloneRecord(state.record)
				if retryErr := h.SetRecord(rec); retryErr != nil {
					fmt.Fprintf(os.Stderr, "dispatch %s: recording worktree: %v\n", rec.ID, retryErr)
				}
			}
		} else {
			d.persistRecordLocked(state)
		}
		d.mu.Unlock()
		return rec
	}
	original := rec
	rec.WorktreeState = disposition
	if branchTip != "" {
		rec.BranchTip = branchTip
	}
	if recorder, ok := d.recorder.(*FileRecorder); ok {
		if err := writeRecord(recorder.dir, rec); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch %s: recording worktree: %v\n", rec.ID, err)
			original.WorktreeState = WorktreeKept
			original.Error = strings.TrimSpace(original.Error + "; cleanup metadata was not persisted: " + err.Error())
			return original
		}
	}
	return rec
}

// worktreeLister returns the worktree paths registered with a repository
// root, each resolved through symlinks.
type worktreeLister func(root string) (map[string]bool, error)

// evalSymlinks resolves a worktree path; tests count its calls.
var evalSymlinks = filepath.EvalSymlinks

// resolveListedWorktrees reads the paths out of `git worktree list
// --porcelain` output, each resolved through symlinks.
func resolveListedWorktrees(out []byte) map[string]bool {
	paths := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		listed, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if canonical, err := evalSymlinks(listed); err == nil {
			listed = canonical
		}
		paths[listed] = true
	}
	return paths
}

func (d *Dispatcher) reconcileCreating(rec Record, list worktreeLister) Record {
	if rec.WorktreeState != WorktreeCreating {
		return rec
	}
	registered, err := list(rec.RepositoryRoot)
	if err != nil {
		return d.setWorktreeState(rec, nil, WorktreeDeferred)
	}
	wantPath := rec.Worktree
	if canonical, err := evalSymlinks(rec.Worktree); err == nil {
		wantPath = canonical
	}
	if registered[wantPath] {
		return d.setWorktreeState(rec, nil, WorktreePresent)
	}
	if _, err := os.Lstat(rec.Worktree); err == nil || !os.IsNotExist(err) {
		return d.setWorktreeState(rec, nil, WorktreeDeferred)
	}
	return d.setWorktreeState(rec, nil, WorktreeRemoved)
}
