// Package follow pulls a lead host's shared config over ssh for a follower
// and keeps it fresh: Puller does one pull, Loop runs them on a schedule.
// See docs/configuration/follow-lead.md.
package follow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/remote"
)

const (
	// pullTimeout bounds one export call, connection included.
	pullTimeout = 30 * time.Second
	// connectTimeout is ssh's own connect timeout, so an unreachable lead
	// fails fast instead of eating the whole pullTimeout.
	connectTimeout = "10"
	// sshUnreachableExit is ssh's exit status for its own failures.
	sshUnreachableExit = 255
	// maxStderrDetail bounds the stderr carried into an error message.
	maxStderrDetail = 300
)

// ErrNotFollowing is Pull's answer when the config has no follow block.
var ErrNotFollowing = errors.New("this host does not follow a lead (no follow: block)")

// Runner runs one external command and returns its output; ExecRunner is the
// real one. A failed run's error should expose ExitCode() when it has one.
type Runner interface {
	Run(ctx context.Context, name string, args []string) (stdout, stderr []byte, err error)
}

// Puller pulls the lead's export once per Pull.
type Puller struct {
	// LoadConfig loads the follower's config fresh for every pull, so a
	// follow.host or client.hosts edit applies to the next one.
	LoadConfig func() (*config.Config, error)
	Runner     Runner
	Now        func() time.Time
}

// Outcome reports a successful pull.
type Outcome struct {
	// Changed is true when the lead's sections or revision differ from the
	// previous cache (or there was none): the config must be reloaded.
	Changed bool
	Cache   config.FollowCache
}

// Pull runs `leo config export --follow` on the lead and, on success,
// atomically replaces the cache. Every attempt is recorded in the status
// file; a failure leaves the cache untouched and returns a
// *config.FollowError.
func (p *Puller) Pull(ctx context.Context) (Outcome, error) {
	cfg, err := p.LoadConfig()
	if err != nil {
		return Outcome{}, fmt.Errorf("loading config: %w", err)
	}
	if !cfg.IsFollower() {
		return Outcome{}, ErrNotFollowing
	}
	state := cfg.StatePath()
	res, err := cfg.ResolveHost(cfg.Follow.Host)
	if err != nil {
		return Outcome{}, err
	}
	now := p.Now()
	out, pullErr := p.pull(ctx, cfg, res, now)
	status := config.FollowStatus{LastAttempt: now}
	var fe *config.FollowError
	if errors.As(pullErr, &fe) {
		status.LastError, status.LastErrorCode = fe.Error(), fe.Code
	} else if pullErr != nil {
		status.LastError = pullErr.Error()
	}
	if err := config.WriteFollowStatus(state, status); err != nil && pullErr == nil {
		return out, err
	}
	return out, pullErr
}

func (p *Puller) pull(ctx context.Context, cfg *config.Config, res config.HostResolution, now time.Time) (Outcome, error) {
	data, err := p.export(ctx, res)
	if err != nil {
		return Outcome{}, err
	}
	exp, err := config.ParseFollowExport(data)
	if err != nil {
		return Outcome{}, err
	}
	state := cfg.StatePath()
	self, err := config.HomeID(state)
	if err != nil {
		return Outcome{}, err
	}
	if exp.HomeID == self {
		return Outcome{}, &config.FollowError{Code: config.FollowSelfFollow, Detail: fmt.Sprintf("host %q is this leo home", res.Name)}
	}
	fc := config.FollowCache{
		Host: res.Name, SSH: res.Host.SSH, PulledAt: now,
		LeadRevision: exp.Revision, LeadVersion: exp.LeoVersion, LeadHomeID: exp.HomeID,
		Digest: exp.Digest(), Export: data,
	}
	prev, _ := config.ReadFollowCache(state)
	changed := prev == nil || prev.Host != fc.Host || prev.Digest != fc.Digest || prev.LeadRevision != fc.LeadRevision
	if err := config.WriteFollowCache(state, fc); err != nil {
		return Outcome{}, err
	}
	return Outcome{Changed: changed, Cache: fc}, nil
}

// export runs the export on the lead and classifies a failure.
func (p *Puller) export(ctx context.Context, res config.HostResolution) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	stdout, stderr, err := p.Runner.Run(ctx, "ssh", leadArgv(res, "config", "export", "--follow"))
	if err == nil {
		return stdout, nil
	}
	full, detail := string(stderr), stderrDetail(stderr)
	switch {
	case errors.Is(err, errExportTooLarge):
		return nil, &config.FollowError{Code: config.FollowMalformed, Detail: "export " + err.Error()}
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
		return nil, &config.FollowError{Code: config.FollowUnreachable, Detail: fmt.Sprintf("ssh to %s timed out after %s", res.Name, pullTimeout)}
	case exitCode(err) == sshUnreachableExit:
		return nil, &config.FollowError{Code: config.FollowUnreachable, Detail: orElse(detail, err.Error())}
	case strings.Contains(full, string(config.FollowLeadIsFollower)+":"):
		return nil, &config.FollowError{Code: config.FollowLeadIsFollower, Detail: afterCode(full, config.FollowLeadIsFollower)}
	case strings.Contains(full, string(config.FollowLeadConfigInvalid)+":"):
		return nil, &config.FollowError{Code: config.FollowLeadConfigInvalid, Detail: afterCode(full, config.FollowLeadConfigInvalid)}
	case isUnknownCommand(full):
		return nil, &config.FollowError{Code: config.FollowLeadTooOld, Detail: p.leadVersion(ctx, res)}
	}
	return nil, &config.FollowError{Code: config.FollowUnreachable, Detail: fmt.Sprintf("export on %s failed: %s", res.Name, orElse(detail, err.Error()))}
}

// leadVersion asks the lead's leo for its version over the same connection.
func (p *Puller) leadVersion(ctx context.Context, res config.HostResolution) string {
	stdout, _, err := p.Runner.Run(ctx, "ssh", leadArgv(res, "version"))
	fields := strings.Fields(string(stdout))
	if err != nil || len(fields) == 0 {
		return "unknown version"
	}
	return fields[len(fields)-1]
}

// leadArgv is the ssh argv (without "ssh") that runs `<leo> <leoArgs...>`
// on the lead: BatchMode so a launchd daemon fails instead of prompting, the
// host's ssh_args, the shared ControlMaster socket, and a short connect
// timeout. It matches remote.SSHArgv(res, true, "-o", "ConnectTimeout=10",
// tail...) from feat/agent-move; swap that in when the branches meet.
func leadArgv(res config.HostResolution, leoArgs ...string) []string {
	args := []string{"-o", "BatchMode=yes", res.Host.SSH}
	args = append(args, res.Host.SSHArgs...)
	if res.ControlPath != "" && os.MkdirAll(filepath.Dir(res.ControlPath), 0o700) == nil {
		args = append(args, "-o", "ControlMaster=auto", "-o", "ControlPath="+res.ControlPath)
	}
	args = append(args, "-o", "ConnectTimeout="+connectTimeout, remote.LeoWord(res.Host.RemoteLeoPath()))
	return append(args, leoArgs...)
}

func isUnknownCommand(stderr string) bool {
	return strings.Contains(stderr, `unknown command "export"`) || strings.Contains(stderr, "unknown flag: --follow")
}

func exitCode(err error) int {
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return -1
}

// stderrDetail is the last line of stderr, trimmed and bounded, with the
// CLI's "Error: " prefix dropped.
func stderrDetail(stderr []byte) string {
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	last := strings.TrimPrefix(strings.TrimSpace(lines[len(lines)-1]), "Error: ")
	if len(last) > maxStderrDetail {
		last = last[:maxStderrDetail] + "…"
	}
	return last
}

// afterCode is the text after "<code>:" in stderr, bounded.
func afterCode(stderr string, code config.FollowErrorCode) string {
	_, rest, _ := strings.Cut(stderr, string(code)+":")
	rest = strings.Join(strings.Fields(rest), " ")
	if len(rest) > maxStderrDetail {
		rest = rest[:maxStderrDetail] + "…"
	}
	return rest
}

func orElse(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
