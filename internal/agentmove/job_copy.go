package agentmove

import (
	"context"
	"fmt"
	"slices"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/git"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/session"
)

// maxCopyNames bounds how many default names (<agent>-copy, -copy-2, ...)
// a copy tries.
const maxCopyNames = 10

// snapshotCopier reports whether a copy of a takes its session as of the
// last completed turn: such a copy may run mid-turn, sends no session files
// and pins the fork's id up front.
func snapshotCopier(a harness.SessionMover) bool {
	s, ok := a.(harness.SnapshotCopier)
	return ok && s.SnapshotCopy()
}

// validateCopy is P0 for a copy: the harness must fork, a git workspace
// needs a branch to fork from, and the candidate names are picked.
func (t *transfer) validateCopy(ctx context.Context, records map[string]agentstore.Record) error {
	if _, ok := t.adapter.(harness.Forker); !ok {
		return problemf(CodeHarnessUnsupported, "%s agents cannot fork their session, so they cannot be copied", orClaude(t.rec.Harness))
	}
	if t.git.IsRepo {
		head, err := git.HeadState(ctx, t.rec.Workspace)
		if err != nil {
			return problemf(CodeGitUnsupported, "reading HEAD: %v", err)
		}
		t.curBranch = head.Branch()
		if t.curBranch == "" && t.req.Branch == "" {
			return problemf(CodeBadRequest, "%s's HEAD is detached; name the copy's branch with --branch", t.rec.Name)
		}
	}
	if snapshotCopier(t.adapter) {
		t.forkID = session.NewID()
	}
	if t.req.Name != "" {
		if _, taken := records[t.req.Name]; taken {
			return problemf(CodeNameTaken, "an agent named %s already exists here", t.req.Name)
		}
		t.names = []string{t.req.Name}
		return nil
	}
	base := t.rec.Name + "-copy"
	for i := 1; len(t.names) < maxCopyNames; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		if _, taken := records[name]; !taken {
			t.names = append(t.names, name)
		}
	}
	return nil
}

// askName is the first preflight. A copy tries its candidate names in turn
// while the destination reports nothing but the name taken.
func (t *transfer) askName(ctx context.Context) error {
	if !t.isCopy() {
		return t.ask(ctx)
	}
	for i, name := range t.names {
		if err := t.setCopy(name); err != nil {
			return err
		}
		if err := t.ask(ctx); err != nil {
			return err
		}
		if t.resp.OK || i == len(t.names)-1 || !onlyNameTaken(t.resp.Problems) {
			return nil
		}
	}
	return nil
}

func onlyNameTaken(ps []Problem) bool {
	return len(ps) > 0 && !slices.ContainsFunc(ps, func(p Problem) bool { return p.Code != CodeNameTaken })
}

// setCopy names the copy: workspace <workspace>-<name>, and in git the
// branch <current>-<name> unless --branch names one.
func (t *transfer) setCopy(name string) error {
	if err := ValidateRef(name); err != nil {
		return err
	}
	branch := ""
	if t.git.IsRepo {
		branch = t.req.Branch
		if branch == "" {
			branch = t.curBranch + "-" + name
		}
		if err := ValidateRef(branch); err != nil {
			return err
		}
	}
	ws := t.rec.Workspace + "-" + name
	if err := ValidatePath(ws); err != nil {
		return err
	}
	t.m.Copy = &CopySpec{Name: name, Workspace: ws, Branch: branch, SessionID: t.forkID}
	return nil
}

// checkCopyBranch refuses a copy branch this host's repository already has.
// A local git-mode copy shares the canonical, where G3 tells a re-run's own
// partial from a clash.
func (t *transfer) checkCopyBranch(ctx context.Context) error {
	b := t.m.Copy.Branch
	if b == "" || (t.ep.Local() && t.gitMode()) {
		return nil
	}
	sha, err := git.RefSHA(ctx, t.rec.Workspace, "refs/heads/"+b)
	if err != nil {
		return err
	}
	if sha != "" {
		return problemf(CodeBranchExists, "branch %s already exists here; pick another with --branch", b)
	}
	return nil
}

func (t *transfer) checkCopyIdle(ctx context.Context) error {
	if snapshotCopier(t.adapter) {
		return nil
	}
	t.p.Stage(StageIdle)
	h := orClaude(t.rec.Harness)
	pr, err := t.mv.Prober.Probe(ctx, t.rec.Name)
	switch {
	case err != nil:
		return problemf(CodeAgentBusy, "could not tell whether %s is mid-turn (%v); a %s agent can only be copied between turns", t.rec.Name, err, h)
	case pr.Busy:
		return problemf(CodeAgentBusy, "%s is mid-turn and a %s agent can only be copied between turns; try again once the turn finishes", t.rec.Name, h)
	}
	return nil
}

// sendCopy transfers and commits without touching the source agent.
func (t *transfer) sendCopy(ctx context.Context) (Result, error) {
	if err := t.transfer(ctx); err != nil {
		return Result{}, err
	}
	if err := t.commit(ctx); err != nil {
		return Result{}, err
	}
	return t.result(), nil
}
