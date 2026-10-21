package consult

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
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

// cleanupWorktree removes id's isolated worktree when the safety rules allow
// it. A run whose writer may still be live is deferred, not kept: deferred and
// kept records are both re-evaluated on every later terminal transition (pane
// closed, release, restart) and by `leo dispatch prune`, so a postponed
// cleanup is never lost and a kept one is re-checked against the disk.
func (d *Dispatcher) cleanupWorktree(id string) Record {
	rec, state, err := d.lookup(id)
	if err != nil || rec.Isolation != "worktree" || rec.WorktreeState == WorktreeRemoved {
		return rec
	}
	deferCleanup := func() Record { return d.setWorktreeState(rec, state, WorktreeDeferred) }
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
	} else {
		if !rec.Status.Terminal() {
			return rec
		}
		if state != nil {
			select {
			case <-state.done:
			default:
				return rec
			}
		}
		if state == nil || (state.headlessStarted && !d.headlessWritersGone(state)) {
			return deferCleanup()
		}
	}
	return d.applyWorktreeAssessment(rec, state)
}

// applyWorktreeAssessment acts on assessWorktree's verdict for a worktree
// whose writers are known to be gone.
func (d *Dispatcher) applyWorktreeAssessment(rec Record, state *runState) Record {
	keep := func() Record { return d.setWorktreeState(rec, state, WorktreeKept) }
	switch assessWorktree(d.git, rec).Verdict {
	case verdictRemove:
		if err := removeWorktreeCheckout(d.git, rec); err != nil {
			return keep()
		}
		tip, _ := deleteMergedBranch(d.git, rec)
		return d.setWorktreeStateTip(rec, state, WorktreeRemoved, tip)
	case verdictGone:
		if err := pruneRepositoryWorktrees(d.git, rec.RepositoryRoot); err != nil {
			return keep()
		}
		tip, _ := deleteMergedBranch(d.git, rec)
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
	if state.pgid <= 1 || state.pgid == syscall.Getpgrp() {
		return false
	}
	present, known := d.processGroupPresent(state.pgid)
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

func (d *Dispatcher) reconcileCreating(rec Record) Record {
	if rec.WorktreeState != WorktreeCreating {
		return rec
	}
	out, err := d.git("-C", rec.RepositoryRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return d.setWorktreeState(rec, nil, WorktreeDeferred)
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
		return d.setWorktreeState(rec, nil, WorktreeDeferred)
	}
	return d.setWorktreeState(rec, nil, WorktreeRemoved)
}
