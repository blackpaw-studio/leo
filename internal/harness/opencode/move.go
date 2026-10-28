package opencode

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// stageDir is where opencode session exports sit inside the move staging dir.
const stageDir = "harness/opencode"

var (
	_ harness.SessionMover = Opencode{}
	_ harness.Forker       = Opencode{}
)

// SessionRoot: opencode keeps sessions in one shared database, so nothing
// travels as files; sessions go by export/import instead.
func (Opencode) SessionRoot(map[string]string) string { return "" }

// SessionFiles: none (see SessionRoot).
func (Opencode) SessionFiles(string, string, []string) ([]string, error) { return nil, nil }

// PrepareSend exports each session to staging with `opencode export <id>`,
// run in the source workspace under the agent's env. A local copy skips it:
// the database already holds the session the fork starts from.
func (o Opencode) PrepareSend(ctx context.Context, mc harness.MoveContext, stage string) error {
	if mc.Copy && mc.Local {
		return nil
	}
	dir := filepath.Join(stage, stageDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, id := range mc.SessionIDs {
		if id == "" {
			continue
		}
		cmd := execCommand(ctx, o.Binary(), "export", id)
		cmd.Dir = mc.Workspace
		cmd.Env = withEnv(mc.Env)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("opencode export %s: %w: %s", id, err, strings.TrimSpace(stderr.String()))
		}
		if err := os.WriteFile(filepath.Join(dir, id+".json"), stdout.Bytes(), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// FinishReceive imports each staged session with `opencode import <file>`.
// It runs in the destination workspace because import rewrites a session's
// directory to its cwd; re-importing upserts. A local copy skips it (see
// PrepareSend).
func (o Opencode) FinishReceive(ctx context.Context, mc harness.MoveContext, stage string) error {
	if mc.Copy && mc.Local {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(stage, stageDir, "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, file := range files {
		cmd := execCommand(ctx, o.Binary(), "import", file)
		cmd.Dir = mc.Workspace
		cmd.Env = withEnv(mc.Env)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("opencode import %s: %w: %s", filepath.Base(file), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// withEnv is the daemon's environment with env layered on top, as a spawned
// agent sees it.
func withEnv(env map[string]string) []string {
	out := os.Environ()
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// ForkArgs resumes src as a fork: `-s <src> --fork`. opencode picks the
// fork's id itself; once it is discovered the refresher swaps in `-s <id>`.
func (Opencode) ForkArgs(args []string, src, _ string) []string {
	return append(stripForkFlag(stripSessionFlag(args)), "-s", src, "--fork")
}

func stripForkFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != "--fork" {
			out = append(out, a)
		}
	}
	return out
}

func hasForkFlag(args []string) bool {
	for _, a := range args {
		if a == "--fork" {
			return true
		}
	}
	return false
}
