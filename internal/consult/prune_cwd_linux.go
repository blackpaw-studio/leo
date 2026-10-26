//go:build linux

package consult

import "context"

func osProcessCwds(context.Context) ([]procCwd, error) { return readProcCwds("/proc") }
