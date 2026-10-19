package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// adoptLaunchOption is the pane option a bridged dispatch's launch token is
// kept in, so a restarted daemon can re-open the dispatch's bridge key for
// the claude still running there (see AdoptBridge). The token is already in
// the pane's environment; the option only lets the daemon read it back.
const adoptLaunchOption = "@leo_bridge_launch"

// AdoptProbe is what a restarted daemon learns of a dispatch's pane.
type AdoptProbe struct {
	Presence PanePresence
	// Owned: the pane runs the dispatch's own launch command.
	Owned bool
	// Launch is the bridge launch token the pane was tagged with; "" for a
	// launch without the bridge, or one from before tagging.
	Launch string
}

// tagLaunch records a bridged launch's token on its pane. A failure only
// costs adoption the bridge: an untagged pane is adopted hook-driven.
func (r *TmuxInteractiveRuntime) tagLaunch(ctx context.Context, id, pane, launch string) {
	if err := r.run(ctx, "set-option", "-p", "-t", pane, adoptLaunchOption, launch); err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: tagging pane %s with its bridge launch: %v\n", id, pane, err)
	}
}

// ProbeAdoption reads pane in one tmux call: whether it is there and alive,
// whether it runs dispatch id, and its launch tag. A pane tmux cannot find
// is PaneAbsent with no error; any other failure leaves the answer unknown.
func (r *TmuxInteractiveRuntime) ProbeAdoption(pane, id string) (AdoptProbe, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = interactiveCommandTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	format := "#{pane_id}\t#{pane_dead}\t#{" + adoptLaunchOption + "}\t#{pane_start_command}"
	out, err := r.command(ctx, "list-panes", "-t", pane, "-F", format).CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && strings.Contains(strings.ToLower(string(out)), "can't find pane") {
			return AdoptProbe{Presence: PaneAbsent}, nil
		}
		return AdoptProbe{}, fmt.Errorf("probe tmux pane %q: %w: %s", pane, err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) < 4 || fields[0] != pane {
			continue
		}
		probe := AdoptProbe{Presence: PanePresentAlive, Owned: startCommandHasDispatchID(fields[3], id), Launch: fields[2]}
		if fields[1] == "1" {
			probe.Presence = PanePresentDead
		}
		return probe, nil
	}
	return AdoptProbe{Presence: PaneAbsent}, nil
}

// AdoptPane registers a pane the runtime did not launch itself (a restarted
// daemon's surviving dispatch) as Launch would have: its harness's composer
// classifier and its placement.
func (r *TmuxInteractiveRuntime) AdoptPane(pane, harnessName, placement string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.classifiers == nil {
		r.classifiers = make(map[string]tmux.ComposerClassifier)
	}
	if harnessName == "codex" {
		r.classifiers[pane] = tmux.CodexComposerClassifier
	} else {
		r.classifiers[pane] = tmux.ClaudeComposerClassifier
	}
	if r.placements == nil {
		r.placements = make(map[string]string)
	}
	r.placements[pane] = placement
}

// AdoptBridge re-opens dispatch id's bridge key for launch, the token its
// surviving claude was started with, so that claude's mod can reconnect and
// report again. Its mod said hello to the previous daemon, so its reports,
// not the shell hooks', keep driving the dispatch: the ones it retried
// across the restart may land before its reconnect's hello does.
func (r *TmuxInteractiveRuntime) AdoptBridge(id, pane, caller, launch string) error {
	hub := r.bridgeWiring().Hub
	if hub == nil {
		return errors.New("the leo bridge is not enabled")
	}
	key := DispatchBridgeKey(id)
	target, err := hub.Open(key, launch)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bridged == nil {
		r.bridged = map[string]*bridgedDispatch{}
	}
	r.bridged[id] = &bridgedDispatch{id: id, key: key, launch: launch, caller: caller, target: target, pane: pane, ownsReports: true}
	return nil
}
