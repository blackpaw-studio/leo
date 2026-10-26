//go:build darwin

package consult

import (
	"context"
	"os/exec"
)

func osProcessCwds(ctx context.Context) ([]procCwd, error) {
	return runLsofCwds(ctx, exec.CommandContext)
}
