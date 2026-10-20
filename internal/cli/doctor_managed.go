package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/managed"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// doctorLine is one printed line: Level picks the colour ("ok", "info",
// "warn"), Text is the line.
type doctorLine struct{ Level, Text string }

// managedDoctorDeps are the probes the managed section runs.
type managedDoctorDeps struct {
	jobStatus    func(label string) (string, error)
	readlink     func(path string) (string, error)
	tmuxVersion  func(bin string) (string, error)
	resolve      func(tmux.ResolveOptions) tmux.Resolution
	cliJobLoaded func(label string) bool
	// cliLabel is the CLI install's own launchd label for this home.
	cliLabel string
}

func defaultManagedDoctorDeps(cfg *config.Config) managedDoctorDeps {
	return managedDoctorDeps{
		jobStatus: daemonStatusLabelFn,
		readlink:  os.Readlink,
		tmuxVersion: func(bin string) (string, error) {
			out, err := exec.Command(bin, "-V").Output()
			return strings.TrimSpace(string(out)), err
		},
		resolve:      tmuxResolveFn,
		cliJobLoaded: labelLoadedFn,
		cliLabel:     daemonLabelFn(cfg.HomePath),
	}
}

// managedDoctorReport describes the managed-install state: the marker, the
// manager's launchd job, the two paths it pins, the tmux binary choice, and
// the CLI job that would race the managed daemon for daemon.lock.
func managedDoctorReport(st managed.State, d managedDoctorDeps) []doctorLine {
	if !st.Managed {
		text := "Managed:        no"
		level := "info"
		if st.Warning != "" {
			text += " (" + st.Warning + ")"
			level = "warn"
		}
		return []doctorLine{{level, text}}
	}
	out := []doctorLine{{"ok", fmt.Sprintf("Managed:        yes (%s, launchd job %s)", st.Manager, st.LaunchdLabel)}}

	status, err := d.jobStatus(st.LaunchdLabel)
	jobLevel := "ok"
	if err != nil || !strings.HasPrefix(status, "running") {
		jobLevel = "warn"
	}
	out = append(out, doctorLine{jobLevel, "  Job:          " + status})

	selfText := st.SelfPath
	if target, err := d.readlink(st.SelfPath); err != nil {
		selfText += " (not a symlink)"
	} else {
		selfText += " -> " + target
	}
	out = append(out, doctorLine{"info", "  self_path:    " + selfText})

	out = append(out, tmuxPathLine(st, d))

	res := d.resolve(tmux.ResolveOptions{ManagedPath: st.TmuxPath})
	out = append(out, doctorLine{"info", fmt.Sprintf("  tmux client:  %s; new servers start with %s", res.Client, res.Start)})
	if res.Pending != "" {
		out = append(out, doctorLine{"warn", "  " + res.Pending})
	}
	if res.Err != nil {
		out = append(out, doctorLine{"warn", "  tmux error:   " + res.Err.Error()})
	}

	if d.cliJobLoaded(d.cliLabel) {
		out = append(out, doctorLine{"warn", fmt.Sprintf("  CLI job:      %s is still loaded; its daemon loses the daemon.lock race and respawns every 10s (launchctl bootout it)", d.cliLabel)})
	}
	return out
}

// tmuxPathLine checks that the pinned tmux reports the version its directory
// name claims, since that name is what the version guard keys on.
func tmuxPathLine(st managed.State, d managedDoctorDeps) doctorLine {
	dirVersion := filepath.Base(filepath.Dir(st.TmuxPath))
	reported, err := d.tmuxVersion(st.TmuxPath)
	switch {
	case err != nil:
		return doctorLine{"warn", fmt.Sprintf("  tmux_path:    %s does not run (%v)", st.TmuxPath, err)}
	case strings.TrimPrefix(reported, "tmux ") != dirVersion:
		return doctorLine{"warn", fmt.Sprintf("  tmux_path:    %s reports %s but sits in directory %s", st.TmuxPath, reported, dirVersion)}
	}
	return doctorLine{"info", fmt.Sprintf("  tmux_path:    %s (%s)", st.TmuxPath, reported)}
}

// reportManaged prints the managed section of doctor.
func reportManaged(cfg *config.Config) {
	for _, l := range managedDoctorReport(managedState(cfg), defaultManagedDoctorDeps(cfg)) {
		switch l.Level {
		case "ok":
			success.Println(l.Text)
		case "warn":
			warn.Println(l.Text)
		default:
			info.Println(l.Text)
		}
	}
}
