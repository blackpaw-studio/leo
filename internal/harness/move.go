package harness

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// MoveContext is one side of an agent transfer as a harness adapter sees it.
type MoveContext struct {
	// Env is the agent's resolved env on this host.
	Env map[string]string
	// Workspace is the agent's workspace on this host: the source's on the
	// sending side, the destination's (a copy's new one) on the receiving
	// side.
	Workspace string
	// SessionIDs are the source session ids to carry: the current one
	// first, then any archived per-template ones.
	SessionIDs []string
	// Copy is set for `leo agent copy` (a fork); a move leaves it false.
	Copy bool
	// Local is set when source and destination are the same host.
	Local bool
}

// SessionMover is the optional Harness capability that lets an agent's
// conversation follow it to another host. A harness without it refuses
// move and copy.
type SessionMover interface {
	// SessionRoot is the root of this harness's session store for env, ""
	// when the harness keeps nothing there that travels as files.
	SessionRoot(env map[string]string) string
	// SessionFiles lists the paths under root, relative to it (a trailing
	// "/" marks a directory), that a move sends with rsync --update. Only
	// paths that exist are listed.
	SessionFiles(root, workspace string, ids []string) ([]string, error)
	// PrepareSend writes into stage whatever cannot travel as plain files
	// (an export, a truncated snapshot), on the source.
	PrepareSend(ctx context.Context, mc MoveContext, stage string) error
	// FinishReceive applies what PrepareSend staged, on the destination,
	// before the agent starts.
	FinishReceive(ctx context.Context, mc MoveContext, stage string) error
}

// TranscriptLocator is the optional SessionMover capability of finding an
// agent's conversation in its session store, so a move can refuse an agent
// with nothing to resume before it stops anything.
type TranscriptLocator interface {
	// Transcript reports whether session id's conversation exists under
	// root; searched is where it looked, for the refusal.
	Transcript(root, workspace, id string) (searched string, found bool)
}

// Forker is the optional Harness capability `leo agent copy` needs: a launch
// that resumes src as a new, independent session. newID is the pre-issued
// id of the fork for harnesses that can pin one (claude); others ignore it
// and discover the fork's id after its first turn.
type Forker interface {
	ForkArgs(args []string, src, newID string) []string
}

// SnapshotCopier is the optional SessionMover capability of copying a
// session as of its last completed turn: PrepareSend stages that snapshot
// under the source id and the fork pins a pre-issued id. Such a copy may
// run while the source is mid-turn and sends no SessionFiles.
type SnapshotCopier interface {
	SnapshotCopy() bool
}

// CopyTree copies the regular files and directories under src into dst,
// keeping modes. Existing files in dst are overwritten; symlinks are skipped.
func CopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		}
		return nil
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	data, err := os.ReadFile(src) //nolint:gosec // src is a session file under the agent's own store
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return nil
}
