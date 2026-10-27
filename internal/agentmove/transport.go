package agentmove

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/remote"
)

// Endpoint is the destination of a transfer.
type Endpoint interface {
	// Run invokes `leo agent move-receive <verb>` on the destination with
	// stdin (the manifest, as JSON) and returns its stdout. User data never
	// reaches argv.
	Run(ctx context.Context, verb string, stdin []byte) ([]byte, error)
	// Local reports whether the destination is this host.
	Local() bool
	// RsyncShell is the rsync -e value, "" for a local endpoint.
	RsyncShell() string
	// Target renders an absolute destination path as an rsync target.
	Target(path string) string
	// Name is the client.hosts key ("" for local).
	Name() string
}

// Handler serves move-receive verbs in-process, for the local endpoint.
type Handler func(ctx context.Context, verb string, stdin []byte) ([]byte, error)

// LocalEndpoint is this host as a destination (a copy with no --to): verbs
// go straight to the daemon's own handler and rsync copies locally.
type LocalEndpoint struct{ handle Handler }

// NewLocalEndpoint returns an endpoint that serves verbs with handle.
func NewLocalEndpoint(handle Handler) *LocalEndpoint { return &LocalEndpoint{handle: handle} }

func (l *LocalEndpoint) Run(ctx context.Context, verb string, stdin []byte) ([]byte, error) {
	return l.handle(ctx, verb, stdin)
}
func (l *LocalEndpoint) Local() bool               { return true }
func (l *LocalEndpoint) RsyncShell() string        { return "" }
func (l *LocalEndpoint) Target(path string) string { return path }
func (l *LocalEndpoint) Name() string              { return "" }

// SSHEndpoint reaches a client.hosts entry over ssh.
type SSHEndpoint struct {
	res    config.HostResolution
	runner Runner
	shell  string
}

// NewSSHEndpoint validates res for use on rsync's -e and in GIT_SSH_COMMAND
// (both split it into words, and git runs it through sh -c) and returns its
// endpoint.
func NewSSHEndpoint(res config.HostResolution, runner Runner) (*SSHEndpoint, error) {
	shell, err := GitSSHCommand(res)
	if err != nil {
		return nil, err
	}
	return &SSHEndpoint{res: res, runner: runner, shell: shell}, nil
}

func (s *SSHEndpoint) Run(ctx context.Context, verb string, stdin []byte) ([]byte, error) {
	// leo_path stays unquoted so the remote shell expands its $HOME.
	argv := remote.SSHArgv(s.res, true, s.res.Host.RemoteLeoPath(), "agent", "move-receive", verb)
	return s.runner.Run(ctx, Cmd{Name: "ssh", Args: argv, Stdin: stdin})
}
func (s *SSHEndpoint) Local() bool               { return false }
func (s *SSHEndpoint) RsyncShell() string        { return s.shell }
func (s *SSHEndpoint) Target(path string) string { return s.res.Host.SSH + ":" + path }
func (s *SSHEndpoint) Name() string              { return s.res.Name }

// Resolution is the host the endpoint reaches.
func (s *SSHEndpoint) Resolution() config.HostResolution { return s.res }

// shellToken is what every word of the ssh command line may contain: it is
// split on whitespace by rsync and run through sh -c by git.
var shellToken = regexp.MustCompile(`^[A-Za-z0-9._@%+=,:/-]+$`)

// GitSSHCommand is `ssh -o BatchMode=yes [ssh_args] [control opts]` for
// GIT_SSH_COMMAND and rsync -e. Every word, and the host, must be a plain
// token; anything else is refused as unsafe.
func GitSSHCommand(res config.HostResolution) (string, error) {
	if !shellToken.MatchString(res.Host.SSH) {
		return "", fmt.Errorf("unsafe ssh host %q for a move (allowed: %s)", res.Host.SSH, shellToken)
	}
	for _, w := range res.Host.SSHArgs {
		if !shellToken.MatchString(w) {
			return "", fmt.Errorf("unsafe ssh_args entry %q for a move: each must be a plain token (allowed: %s)", w, shellToken)
		}
	}
	if res.ControlPath != "" && !shellToken.MatchString(res.ControlPath) {
		return "", fmt.Errorf("unsafe ssh control path %q for a move (allowed: %s)", res.ControlPath, shellToken)
	}
	words := append([]string{"ssh"}, remote.SSHArgv(res, true)...)
	// SSHArgv puts the host right after BatchMode; the command form leaves
	// it to rsync/git to append.
	words = append(words[:3], words[4:]...)
	return strings.Join(words, " "), nil
}

// PushCmd is G2's `git push` of the move refs into the destination's
// canonical, through the same ssh connection rsync uses. Refspecs are argv
// elements and never reach a shell.
func PushCmd(res config.HostResolution, ws, canonical, gitPath string, refspecs []string) (Cmd, error) {
	shell, err := GitSSHCommand(res)
	if err != nil {
		return Cmd{}, err
	}
	for _, p := range []string{ws, canonical, gitPath} {
		if err := ValidatePath(p); err != nil {
			return Cmd{}, err
		}
	}
	args := []string{"-C", ws, "push", "--no-verify", "--porcelain",
		"--receive-pack=" + gitPath + " receive-pack", res.Host.SSH + ":" + canonical}
	return Cmd{
		Name: "git",
		Args: append(args, refspecs...),
		Env:  []string{"GIT_SSH_COMMAND=" + shell, "GIT_SSH_VARIANT=ssh"},
	}, nil
}

// refBad matches what check-ref-format forbids anywhere in a ref, plus shell
// metacharacters.
var refBad = regexp.MustCompile("[\\x00-\\x20\\x7f~^:?*\\[\\\\$`;|&<>'\"(){}!#]")

// ValidateRef checks name (an agent or branch name used in a ref) against
// git's check-ref-format rules plus no leading '-' and no shell
// metacharacters. A failure is unsafe_ref.
func ValidateRef(name string) error {
	bad := func(why string) error { return problemf(CodeUnsafeRef, "unsafe ref %q: %s", name, why) }
	switch {
	case name == "" || name == "@":
		return bad("empty or @")
	case strings.HasPrefix(name, "-"):
		return bad("starts with -")
	case strings.Contains(name, ".."), strings.Contains(name, "@{"):
		return bad("contains .. or @{")
	case refBad.MatchString(name):
		return bad("contains a forbidden character")
	case strings.HasSuffix(name, "."):
		return bad("ends with .")
	}
	for _, c := range strings.Split(name, "/") {
		switch {
		case c == "":
			return bad("empty path component")
		case strings.HasPrefix(c, "."):
			return bad("component starts with .")
		case strings.HasSuffix(c, ".lock"):
			return bad("component ends with .lock")
		}
	}
	return nil
}
