package bridgemod

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

const (
	// MinClaudeVersion is the first Claude Code release with the mods API
	// the bridge relies on. Older claudes launch the legacy way.
	MinClaudeVersion = "2.1.287"
	// TestedClaudeVersion is the newest release the mod was verified on;
	// `leo doctor` warns past it, since the mods API may change.
	TestedClaudeVersion = "2.1.289"

	// EnvBin names the variable carrying the absolute leo path the mod runs.
	EnvBin = "LEO_BRIDGE_BIN"
	// EnvAgent names the variable carrying the key the mod connects under.
	EnvAgent = "LEO_BRIDGE_AGENT"
	// EnvHome names the variable carrying the leo home of the daemon that
	// launched this claude; the mod's link dials that daemon's socket
	// rather than whatever LEO_HOME or the default home would resolve.
	EnvHome = "LEO_BRIDGE_HOME"
	// EnvLaunch names the variable carrying an id unique to the launch. The
	// mod's hot reloads keep it, a new process gets another: what an earlier
	// load handed the engine is still queued only while it matches.
	EnvLaunch = "LEO_BRIDGE_LAUNCH"

	// StaleLaunchExitCode is how `leo bridge` exits when the daemon refuses
	// its launch for good (409: a successor holds the key, or nobody adopts
	// the session): the mod stops reconnecting until it reloads. Mirrored by
	// STALE_LAUNCH_EXIT_CODE in leo-bridge/hooks/protocol.js.
	StaleLaunchExitCode = 3

	// RejectedReportExitCode is how `leo bridge report` exits when the
	// daemon refuses the report itself for good (malformed, or an event it
	// does not know): the mod drops it rather than retrying. Mirrored by
	// REJECTED_REPORT_EXIT_CODE in leo-bridge/hooks/protocol.js.
	RejectedReportExitCode = 4

	// PluginDirFlag loads a plugin directory for one claude session.
	PluginDirFlag = "--plugin-dir"

	// DefaultConnectTimeout is how long a bridged launch that carries an
	// opening prompt waits for its mod to connect before relaunching the
	// legacy way.
	DefaultConnectTimeout = 20 * time.Second

	// probeTimeout bounds one `claude --version`.
	probeTimeout = 10 * time.Second
	// probeRetryAfter is how long a failed probe is trusted before the next
	// launch probes again.
	probeRetryAfter = 5 * time.Minute
)

var (
	minVersion    = mustParseVersion(MinClaudeVersion)
	testedVersion = mustParseVersion(TestedClaudeVersion)
)

// Version is a Claude Code release number.
type Version struct{ Major, Minor, Patch int }

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Less reports whether v is an earlier release than o.
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// SupportsMods reports whether v has the mods API the bridge needs.
func SupportsMods(v Version) bool { return !v.Less(minVersion) }

// NewerThanTested reports whether v is past the release the mod was
// verified on.
func NewerThanTested(v Version) bool { return testedVersion.Less(v) }

// ParseVersion reads the release number from `claude --version` output such
// as "2.1.289 (Claude Code)". A pre-release suffix ("2.2.0-beta.1") is
// ignored.
func ParseVersion(s string) (Version, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return Version{}, errors.New("bridgemod: empty claude version")
	}
	core, _, _ := strings.Cut(fields[0], "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("bridgemod: unrecognized claude version %q", fields[0])
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || p[0] == '+' || p[0] == '-' {
			return Version{}, fmt.Errorf("bridgemod: unrecognized claude version %q", fields[0])
		}
		nums[i] = n
	}
	return Version{nums[0], nums[1], nums[2]}, nil
}

func mustParseVersion(s string) Version {
	v, err := ParseVersion(s)
	if err != nil {
		panic(err)
	}
	return v
}

// VersionProbe returns what `<claudePath> --version` prints.
type VersionProbe func(ctx context.Context, claudePath string) (string, error)

// ProbeClaudeVersion runs `<claudePath> --version`.
func ProbeClaudeVersion(ctx context.Context, claudePath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudePath, "--version") // #nosec G204 -- the configured claude binary
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("bridgemod: %s --version: %w", claudePath, err)
	}
	return string(out), nil
}

// EnvKeys are the variables a bridged launch sets for the mod. leo owns
// them: every other launch blanks them, so nothing inherited can point a mod
// anywhere.
var EnvKeys = []string{EnvBin, EnvAgent, EnvHome, EnvLaunch}

// LauncherOptions configures a Launcher.
type LauncherOptions struct {
	// StateDir is leo's state directory; the mod is materialized under it.
	StateDir string
	// LeoVersion names the materialized mod directory (see Materialize).
	LeoVersion string
	// LeoBin is the absolute leo executable the mod runs (LEO_BRIDGE_BIN).
	LeoBin string
	// LeoHome is the launching daemon's leo home (LEO_BRIDGE_HOME).
	LeoHome string
	// Probe reads a claude's version; ProbeClaudeVersion when nil.
	Probe VersionProbe
	// Log receives one line per fallback decision; os.Stderr when nil.
	Log io.Writer
	// Now is the clock for probe retries; time.Now when nil.
	Now func() time.Time
	// Processes lists running command lines for PruneMods; ListProcesses
	// when nil.
	Processes ProcessLister
}

// Launcher decides whether a claude launch loads the leo-bridge mod and, if
// so, how: the --plugin-dir to pass and the environment the mod reads. A nil
// Launcher always answers "launch the legacy way".
type Launcher struct {
	opts LauncherOptions

	mu       sync.Mutex
	versions map[string]probeResult // by resolved claude binary
	probing  map[string]*probeCall  // probes in flight, by resolved binary
	logged   map[string]bool        // fallback reasons already logged

	pruned sync.Once // earlier versions' mods, once per launcher
}

type probeResult struct {
	version Version
	err     error
	at      time.Time
}

// probeCall is one probe in flight; res is set before done is closed.
type probeCall struct {
	done chan struct{}
	res  probeResult
}

// NewLauncher builds a Launcher.
func NewLauncher(opts LauncherOptions) *Launcher {
	if opts.Probe == nil {
		opts.Probe = ProbeClaudeVersion
	}
	if opts.Log == nil {
		opts.Log = os.Stderr
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Processes == nil {
		opts.Processes = ListProcesses
	}
	return &Launcher{
		opts:     opts,
		versions: map[string]probeResult{},
		probing:  map[string]*probeCall{},
		logged:   map[string]bool{},
	}
}

// ClaudeVersion returns the release of the claude at claudePath, probing it
// once per resolved binary. Claude updates itself by repointing a symlink at
// a new versioned file, so keying by the resolved path notices an update
// without probing on every launch. A failed probe is retried after
// probeRetryAfter. Callers asking at once share one probe, which runs to
// the end whether or not they wait for it: ctx only bounds this caller's
// wait.
func (l *Launcher) ClaudeVersion(ctx context.Context, claudePath string) (Version, error) {
	if l == nil {
		return Version{}, errors.New("bridgemod: no launcher")
	}
	call, cached, ok := l.cachedOrProbe(resolveBinary(claudePath), claudePath)
	if ok {
		return cached.version, cached.err
	}
	select {
	case <-call.done:
		return call.res.version, call.res.err
	case <-ctx.Done():
		return Version{}, ctx.Err()
	}
}

// cachedOrProbe returns key's cached result while it is trusted (ok), else
// the probe of key in flight, starting one if none is.
func (l *Launcher) cachedOrProbe(key, claudePath string) (*probeCall, probeResult, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cached, ok := l.versions[key]; ok && (cached.err == nil || l.opts.Now().Sub(cached.at) < probeRetryAfter) {
		return nil, cached, true
	}
	if call, ok := l.probing[key]; ok {
		return call, probeResult{}, false
	}
	call := &probeCall{done: make(chan struct{})}
	l.probing[key] = call
	go l.runProbe(key, claudePath, call)
	return call, probeResult{}, false
}

// runProbe probes claudePath for every caller waiting on call. It is
// detached from any one caller's ctx, so a canceled launch neither fails the
// probe for the others nor leaves a failure cached; Probe bounds itself.
func (l *Launcher) runProbe(key, claudePath string, call *probeCall) {
	res := probeResult{at: l.opts.Now()}
	raw, err := l.opts.Probe(context.Background(), claudePath)
	if err == nil {
		res.version, err = ParseVersion(raw)
	}
	res.err = err
	l.mu.Lock()
	call.res = l.recordLocked(key, res)
	delete(l.probing, key)
	l.mu.Unlock()
	close(call.done)
}

func (l *Launcher) record(key string, res probeResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recordLocked(key, res)
}

// recordLocked caches res for key and returns what key now holds: a
// failure never replaces a success, since that binary is known to answer.
func (l *Launcher) recordLocked(key string, res probeResult) probeResult {
	if prev, ok := l.versions[key]; ok && prev.err == nil && res.err != nil {
		return prev
	}
	l.versions[key] = res
	return res
}

// Capable reports whether the claude at claudePath can load the mod.
func (l *Launcher) Capable(ctx context.Context, claudePath string) bool {
	if l == nil {
		return false
	}
	v, err := l.ClaudeVersion(ctx, claudePath)
	if err != nil {
		l.logOnce("probe:"+claudePath, "bridge: cannot read the claude version (%v); launching without the leo bridge", err)
		return false
	}
	if !SupportsMods(v) {
		l.logOnce("old:"+v.String(), "bridge: claude %s predates the mods API (%s); launching without the leo bridge", v, MinClaudeVersion)
		return false
	}
	return true
}

// Plan is how one claude launch loads the bridge.
type Plan struct {
	// Key is the bridge key the mod connects under (LEO_BRIDGE_AGENT).
	Key string
	// Launch is the token naming this launch's claude process
	// (LEO_BRIDGE_LAUNCH), fresh per Plan: the hub generation opened for
	// the launch accepts only its mod (see bridge.Hub.Open).
	Launch string
	// PluginDir is the materialized mod passed with --plugin-dir.
	PluginDir string
	// Env holds the launch's EnvKeys: LEO_BRIDGE_BIN, LEO_BRIDGE_AGENT,
	// LEO_BRIDGE_HOME and LEO_BRIDGE_LAUNCH (fresh per Plan).
	Env map[string]string
}

// Args returns args with --plugin-dir <PluginDir> in front. Launch-only:
// it never belongs in args persisted for a later relaunch.
func (p Plan) Args(args []string) []string {
	out := make([]string, 0, len(args)+2)
	out = append(out, PluginDirFlag, p.PluginDir)
	return append(out, args...)
}

// Plan prepares a bridged launch of the claude at claudePath under key. ok
// is false (and the caller launches the legacy way) when the claude is too
// old or unreadable, the key is unusable, or the mod cannot be written.
func (l *Launcher) Plan(ctx context.Context, claudePath, key string) (Plan, bool) {
	if l == nil || !config.ValidName(key) || !l.Capable(ctx, claudePath) {
		return Plan{}, false
	}
	dir, err := Materialize(l.opts.StateDir, l.opts.LeoVersion)
	if err != nil {
		l.logOnce("materialize", "bridge: writing the leo-bridge mod failed (%v); launching without the leo bridge", err)
		return Plan{}, false
	}
	l.pruned.Do(func() { l.pruneMods(context.WithoutCancel(ctx), dir) })
	launch := rand.Text()
	return Plan{
		Key:       key,
		Launch:    launch,
		PluginDir: dir,
		Env: map[string]string{
			EnvBin:    l.opts.LeoBin,
			EnvAgent:  key,
			EnvHome:   l.opts.LeoHome,
			EnvLaunch: launch,
		},
	}, true
}

// pruneMods removes earlier versions' mods (see PruneMods), logging what it
// removed or why it could not; it never affects the launch.
func (l *Launcher) pruneMods(ctx context.Context, current string) {
	removed, err := PruneMods(ctx, l.opts.StateDir, current, l.opts.Processes)
	for _, dir := range removed {
		fmt.Fprintf(l.opts.Log, "bridge: removed the unused leo-bridge mod %s\n", dir)
	}
	if err != nil {
		fmt.Fprintf(l.opts.Log, "bridge: pruning old leo-bridge mods: %v\n", err)
	}
}

func (l *Launcher) logOnce(reason, format string, args ...any) {
	l.mu.Lock()
	seen := l.logged[reason]
	l.logged[reason] = true
	l.mu.Unlock()
	if !seen {
		fmt.Fprintf(l.opts.Log, format+"\n", args...)
	}
}

// resolveBinary follows symlinks to the file that actually runs, falling
// back to the path itself when it cannot be resolved. A bare command name
// ("claude", as dispatches launch it) is looked up in PATH first, as exec
// would run it.
func resolveBinary(path string) string {
	target := path
	if !strings.ContainsRune(path, filepath.Separator) {
		if found, err := exec.LookPath(path); err == nil {
			target = found
		}
	}
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		return resolved
	}
	return target
}
