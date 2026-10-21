package cli

import "github.com/blackpaw-studio/leo/internal/tmux"

// tmuxResolveFn is the seam over tmux.Resolve.
var tmuxResolveFn = tmux.Resolve

// resolveTmux is the tmux binary the CLI runs: under a managed install the
// binary matching the running server's version, otherwise tmux.Locate. When
// the config cannot be loaded the CLI cannot tell whether it is managed, so it
// falls back to the plain lookup. A pending upgrade is deliberately not
// reported here: the daemon logs it and doctor shows it.
func resolveTmux() (string, error) {
	var opts tmux.ResolveOptions
	if cfg, err := loadConfig(); err == nil {
		opts.ManagedPath = managedState(cfg).TmuxPath
	}
	res := tmuxResolveFn(opts)
	if res.Client == "" {
		return "", res.Err
	}
	return res.Client, nil
}
