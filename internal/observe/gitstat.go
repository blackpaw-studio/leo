package observe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// shortstatRe matches git's one summary line:
// " 3 files changed, 10 insertions(+), 2 deletions(-)" where the insertions
// and deletions clauses are each omitted when zero.
var shortstatRe = regexp.MustCompile(`^(\d+) files? changed(?:, (\d+) insertions?\(\+\))?(?:, (\d+) deletions?\(-\))?$`)

// ParseShortstat reads the output of `git diff --shortstat`: empty output is
// a clean tree. Anything else that is not one summary line is an error.
func ParseShortstat(out string) (GitStats, error) {
	line := strings.TrimSpace(out)
	if line == "" {
		return GitStats{}, nil
	}
	m := shortstatRe.FindStringSubmatch(line)
	if m == nil {
		return GitStats{}, fmt.Errorf("unrecognised shortstat output %q", truncateRunes(line, 80))
	}
	var stats GitStats
	for i, dst := range []*int64{&stats.Added, &stats.Removed} {
		digits := m[i+2] // groups 2 and 3; group 1 is the file count
		if digits == "" {
			continue
		}
		n, err := strconv.ParseInt(digits, 10, 64)
		if err != nil {
			return GitStats{}, fmt.Errorf("shortstat count %q: %w", digits, err)
		}
		*dst = n
	}
	return stats, nil
}

// GitStatsTimeout bounds one git call.
const GitStatsTimeout = 5 * time.Second

// gitWaitDelay bounds how long a killed git's output pipes may linger.
const gitWaitDelay = 2 * time.Second

// CommandRunner runs one command. exitCode is the command's own exit status;
// err is only for a command that could not run or finish (not found, killed
// by the context).
type CommandRunner interface {
	Run(ctx context.Context, env []string, argv ...string) (stdout []byte, exitCode int, err error)
}

// ExecCommandRunner runs commands with os/exec, directly (never through a
// shell), in env.
type ExecCommandRunner struct{}

// Run implements CommandRunner.
func (ExecCommandRunner) Run(ctx context.Context, env []string, argv ...string) ([]byte, int, error) {
	if len(argv) == 0 {
		return nil, 0, errors.New("no command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- argv is built by GitStatsFor, never from a shell string
	cmd.Env = env
	cmd.WaitDelay = gitWaitDelay
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && ctx.Err() == nil {
		return out, exitErr.ExitCode(), nil
	}
	return out, 0, err
}

// GitStatsFor reads the uncommitted change size of workspace ws: one
// `git diff HEAD --shortstat`, without taking locks or starting the
// filesystem monitor, within GitStatsTimeout. ok=false with no error means
// there is nothing to report (not a repository, or no commit yet); an error
// is a failure worth logging.
func GitStatsFor(ctx context.Context, r CommandRunner, ws string) (stats GitStats, ok bool, err error) {
	if !filepath.IsAbs(ws) {
		return GitStats{}, false, fmt.Errorf("git stats: workspace %q is not an absolute path", ws)
	}
	ctx, cancel := context.WithTimeout(ctx, GitStatsTimeout)
	defer cancel()
	env := append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, exit, err := r.Run(ctx, env, "git", "-C", ws, "-c", "core.fsmonitor=false", "diff", "HEAD", "--shortstat")
	if err != nil {
		return GitStats{}, false, fmt.Errorf("git stats in %s: %w", ws, err)
	}
	if exit != 0 {
		return GitStats{}, false, nil
	}
	stats, err = ParseShortstat(string(out))
	if err != nil {
		return GitStats{}, false, fmt.Errorf("git stats in %s: %w", ws, err)
	}
	return stats, true, nil
}
