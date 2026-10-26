//go:build !darwin && !linux

package consult

import (
	"context"
	"errors"
)

// Without a known way to list process working directories, prune cannot rule
// writers out and keeps deferred worktrees.
func osProcessCwds(context.Context) ([]procCwd, error) {
	return nil, errors.New("process working directories are not readable on this OS")
}
