package consult

import (
	"bytes"
	"fmt"
	"os"
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

func (d *Dispatcher) git(args ...string) ([]byte, error) {
	cmd := d.GitCommand("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return bytes.TrimSpace(out), nil
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

func (d *Dispatcher) cleanupWorktree(id string) Record {
	rec, state, err := d.lookup(id)
	if err != nil || rec.Isolation != "worktree" || rec.WorktreeState == WorktreeRemoved || rec.WorktreeState == WorktreeKept {
		return rec
	}
	if rec.Mode == ModeInteractive {
		if rec.PaneID == "" {
			return d.setWorktreeState(rec, state, WorktreeKept)
		}
		d.mu.Lock()
		rt := d.interactiveRuntime
		d.mu.Unlock()
		if rt == nil {
			return d.setWorktreeState(rec, state, WorktreeKept)
		}
		alive, certain := paneAlive(rt, rec.PaneID)
		if !certain || alive {
			return d.setWorktreeState(rec, state, WorktreeKept)
		}
	}
	if rec.Mode != ModeInteractive && state != nil && !headlessProcessGroupGone(state) {
		return d.setWorktreeState(rec, state, WorktreeKept)
	}
	keep := func() Record { return d.setWorktreeState(rec, state, WorktreeKept) }
	if rec.Worktree == "" || rec.RepositoryRoot == "" || rec.BaseCommit == "" || rec.Branch == "" {
		return keep()
	}
	branch, err := d.git("-C", rec.Worktree, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || string(branch) != rec.Branch {
		return keep()
	}
	head, err := d.git("-C", rec.Worktree, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || string(head) != rec.BaseCommit {
		return keep()
	}
	status, err := d.git("-C", rec.Worktree, "status", "--porcelain")
	if err != nil || len(status) != 0 {
		return keep()
	}
	commits, err := d.git("-C", rec.Worktree, "rev-list", rec.BaseCommit+"..HEAD")
	if err != nil || len(commits) != 0 {
		return keep()
	}
	if _, err := d.git("-C", rec.RepositoryRoot, "worktree", "remove", rec.Worktree); err != nil {
		return keep()
	}
	return d.setWorktreeState(rec, state, WorktreeRemoved)
}

// processGroupGone treats every error other than ESRCH as uncertainty. A
// cleanup that cannot prove the group is gone must keep its worktree. This
// only contains descendants that remain in the harness process group: a
// deliberate setsid-style daemon escape is not portably discoverable here.
func processGroupGone(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	return syscall.Kill(-pgid, 0) == syscall.ESRCH
}

func waitProcessGroupGone(pgid int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for !processGroupGone(pgid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return processGroupGone(pgid)
}

func headlessProcessGroupGone(state *runState) bool {
	return state.pgidGone
}

func (d *Dispatcher) setWorktreeState(rec Record, state *runState, disposition WorktreeState) Record {
	original := rec
	rec.WorktreeState = disposition
	if recorder, ok := d.recorder.(*FileRecorder); ok {
		if err := writeRecord(recorder.dir, rec); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch %s: recording worktree: %v\n", rec.ID, err)
			original.WorktreeState = WorktreeKept
			original.Error = strings.TrimSpace(original.Error + "; cleanup metadata was not persisted: " + err.Error())
			if state != nil {
				d.mu.Lock()
				state.record = cloneRecord(original)
				d.mu.Unlock()
			}
			return original
		}
	}
	if state != nil {
		d.mu.Lock()
		state.record.WorktreeState = disposition
		rec = cloneRecord(state.record)
		d.mu.Unlock()
	}
	return rec
}

func (d *Dispatcher) reconcileCreating(rec Record) Record {
	if rec.WorktreeState != WorktreeCreating {
		return rec
	}
	out, err := d.git("-C", rec.RepositoryRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return d.setWorktreeState(rec, nil, WorktreeKept)
	}
	wantPath := rec.Worktree
	if canonical, err := filepath.EvalSymlinks(rec.Worktree); err == nil {
		wantPath = canonical
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		listed := strings.TrimPrefix(line, "worktree ")
		if canonical, err := filepath.EvalSymlinks(listed); err == nil {
			listed = canonical
		}
		if listed == wantPath {
			return d.setWorktreeState(rec, nil, WorktreePresent)
		}
	}
	if _, err := os.Lstat(rec.Worktree); err == nil || !os.IsNotExist(err) {
		return d.setWorktreeState(rec, nil, WorktreeKept)
	}
	return d.setWorktreeState(rec, nil, WorktreeRemoved)
}
