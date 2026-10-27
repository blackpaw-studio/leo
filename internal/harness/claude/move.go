package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/session"
)

// stageDir is where a copy's claude snapshot sits inside the move staging dir.
const stageDir = "harness/claude"

var (
	_ harness.SessionMover   = Claude{}
	_ harness.Forker         = Claude{}
	_ harness.SnapshotCopier = Claude{}
)

// SnapshotCopy: a copy takes the transcript up to its last completed turn
// (PrepareSend) and the fork pins its new id with --session-id.
func (Claude) SnapshotCopy() bool { return true }

// SessionRoot is the claude config dir env resolves to.
func (Claude) SessionRoot(env map[string]string) string {
	dir, err := session.ConfigDir(env)
	if err != nil {
		return ""
	}
	return dir
}

// SessionFiles is the workspace's whole project dir (every transcript, its
// sidecar dirs and memory/) plus file-history/<id>/ for each id. Session env,
// todos and shell snapshots are per-launch state and stay behind.
func (Claude) SessionFiles(root, workspace string, ids []string) ([]string, error) {
	candidates := []string{filepath.Join("projects", session.ProjectSlug(workspace))}
	for _, id := range ids {
		if id != "" {
			candidates = append(candidates, filepath.Join("file-history", id))
		}
	}
	var out []string
	for _, rel := range candidates {
		info, err := os.Stat(filepath.Join(root, rel))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			out = append(out, rel+"/")
		}
	}
	return out, nil
}

// PrepareSend stages a copy's snapshot: the current transcript cut after its
// last completed turn, its sidecar dir, and project memory. A move stages
// nothing; its files travel as SessionFiles.
func (c Claude) PrepareSend(_ context.Context, mc harness.MoveContext, stage string) error {
	if !mc.Copy {
		return nil
	}
	if len(mc.SessionIDs) == 0 || mc.SessionIDs[0] == "" {
		return errors.New("claude: no session to copy")
	}
	sid := mc.SessionIDs[0]
	projDir := filepath.Join(c.SessionRoot(mc.Env), "projects", session.ProjectSlug(mc.Workspace))
	data, err := os.ReadFile(filepath.Join(projDir, sid+".jsonl")) //nolint:gosec // the agent's own transcript
	if err != nil {
		return fmt.Errorf("claude: reading transcript: %w", err)
	}
	snapshot, ok := TruncateAtLastEndTurn(data)
	if !ok {
		return errors.New("claude: the conversation has no completed turn to copy yet")
	}
	out := filepath.Join(stage, stageDir)
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, sid+".jsonl"), snapshot, 0o600); err != nil {
		return err
	}
	for _, sub := range []string{sid, "memory"} {
		if err := copyTreeIfPresent(filepath.Join(projDir, sub), filepath.Join(out, sub)); err != nil {
			return fmt.Errorf("claude: staging %s: %w", sub, err)
		}
	}
	return nil
}

// FinishReceive places a copy's snapshot under the new workspace's project
// dir. Nothing there is overwritten: memory/ is seeded only when absent.
func (c Claude) FinishReceive(_ context.Context, mc harness.MoveContext, stage string) error {
	if !mc.Copy || len(mc.SessionIDs) == 0 {
		return nil
	}
	sid := mc.SessionIDs[0]
	in := filepath.Join(stage, stageDir)
	projDir := filepath.Join(c.SessionRoot(mc.Env), "projects", session.ProjectSlug(mc.Workspace))
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		return err
	}
	transcript := filepath.Join(projDir, sid+".jsonl")
	if _, err := os.Stat(transcript); errors.Is(err, fs.ErrNotExist) {
		data, err := os.ReadFile(filepath.Join(in, sid+".jsonl")) //nolint:gosec // staged by this move
		if err != nil {
			return fmt.Errorf("claude: staged transcript: %w", err)
		}
		if err := os.WriteFile(transcript, data, 0o600); err != nil {
			return err
		}
	}
	for _, sub := range []string{sid, "memory"} {
		if _, err := os.Stat(filepath.Join(projDir, sub)); err == nil {
			continue
		}
		if err := copyTreeIfPresent(filepath.Join(in, sub), filepath.Join(projDir, sub)); err != nil {
			return fmt.Errorf("claude: placing %s: %w", sub, err)
		}
	}
	return nil
}

func copyTreeIfPresent(src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return harness.CopyTree(src, dst)
}

// ForkArgs resumes src as a new session pinned to newID: claude requires
// --fork-session alongside --session-id when resuming.
func (Claude) ForkArgs(args []string, src, newID string) []string {
	out := StripSessionArgs(args)
	return append(out, "--resume", src, "--fork-session", "--session-id", newID)
}

// StripSessionArgs returns args without any --session-id/--resume pair or
// --fork-session flag.
func StripSessionArgs(args []string) []string {
	out := make([]string, 0, len(args)+5)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session-id", "--resume":
			i++ // drop the value too
		case "--fork-session":
		default:
			out = append(out, args[i])
		}
	}
	return out
}

// TruncateAtLastEndTurn cuts a transcript after its last assistant line
// whose stop_reason is end_turn, the end of the last completed turn. ok is
// false when there is none.
func TruncateAtLastEndTurn(data []byte) (out []byte, ok bool) {
	end := -1
	offset := 0
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		offset += len(line)
		if isEndTurn(line) {
			end = offset
		}
	}
	if end < 0 {
		return nil, false
	}
	return data[:end], true
}

func isEndTurn(line []byte) bool {
	if !bytes.Contains(line, []byte(`"end_turn"`)) {
		return false
	}
	var entry struct {
		Type    string `json:"type"`
		Message struct {
			StopReason string `json:"stop_reason"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return false
	}
	return entry.Type == "assistant" && entry.Message.StopReason == "end_turn"
}
