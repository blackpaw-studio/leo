package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/session"
)

// sessionTmuxTarget resolves a persistent task's logical session name to the
// concrete tmux session it is supervised under. Topology A/B sessions are
// hosted as "leo-session-<name>". This is the mapping the daemon's prompt
// injector must target — the bare logical name is only the router's FIFO
// key.
func sessionTmuxTarget(cfg *config.Config, taskName string) (string, error) {
	name, _, _, err := cfg.ResolveSession(taskName)
	if err != nil {
		return "", err
	}
	return service.SessionTmuxName(name), nil
}

// persistentImpl is a seam for tests to override the runPersistent dispatch.
var persistentImpl = runPersistent

// wrapPromptForPersistent prepends the leo invocation marker and (when
// channels are non-empty) appends the delivery footer. The marker lets the
// Claude-side Stop hook correlate its session_id with the daemon's pending
// invocation, and the footer tells the agent which plugin to deliver via.
func wrapPromptForPersistent(invID, body string, channels []string) string {
	marker := fmt.Sprintf("<!-- leo:invocation=%s -->\n", invID)
	if len(channels) == 0 {
		return marker + body
	}
	return marker + body + "\n\n---\nWhen finished, deliver your final reply to the user via these channel plugin(s): " +
		strings.Join(channels, ", ") + ".\n"
}

// promptForPersistent renders the prompt to enqueue for a persistent task,
// branching on the task's harness. claude keeps wrapPromptForPersistent's
// marker+footer wrap byte-identical (completion arrives later via the
// Stop-hook Report path, which needs the marker to correlate). Non-claude
// harnesses enqueue the bare assembled prompt: their driver's Inject runs
// the turn to completion synchronously and returns a *harness.Result
// directly to the router, so there is no async callback to correlate and no
// marker to strip from the model's context. Channels are never non-empty
// here for a non-claude task — SupportsChannels()==false is enforced at
// config validation — so there is no delivery footer to omit either.
func promptForPersistent(cfg *config.Config, task config.TaskConfig, invID, body string) string {
	if cfg.TaskHarness(task) == "claude" {
		return wrapPromptForPersistent(invID, body, task.Channels)
	}
	return body
}

// runPersistent dispatches a task through the daemon's session router rather
// than spawning a fresh claude process. It enqueues the prompt, long-polls
// for completion, persists the session id on success, and records history.
func runPersistent(cfg *config.Config, taskName string) error {
	task, err := resolveTask(cfg, taskName)
	if err != nil {
		return err
	}
	sessName, _, _, err := cfg.ResolveSession(taskName)
	if err != nil {
		return fmt.Errorf("resolving session for task %q: %w", taskName, err)
	}
	tmuxTarget, err := sessionTmuxTarget(cfg, taskName)
	if err != nil {
		return fmt.Errorf("resolving tmux session for task %q: %w", taskName, err)
	}
	body, err := assemblePrompt(cfg, task)
	if err != nil {
		return fmt.Errorf("assembling prompt: %w", err)
	}
	invID := newInvocationID16()
	wrapped := promptForPersistent(cfg, task, invID, body)

	timeout := cfg.TaskTimeout(task)
	// Allow a small grace window on top of the per-task timeout so the
	// daemon's own deadline fires (and reports a clean "timeout" result)
	// before our long-poll context cancels and we lose the result entirely.
	ctx, cancel := context.WithTimeout(context.Background(), timeout+30*time.Second)
	defer cancel()

	enq, err := daemon.EnqueueTask(ctx, cfg.HomePath, daemon.EnqueueRequest{
		InvocationID: invID,
		Session:      sessName,
		TmuxSession:  tmuxTarget,
		Task:         taskName,
		Prompt:       wrapped,
		Channels:     task.Channels,
		QueueMax:     task.QueueMax,
		Timeout:      timeout,
	})
	if err != nil {
		handlePersistentFailure(cfg, taskName, fmt.Sprintf("enqueue: %v", err))
		return fmt.Errorf("enqueue: %w", err)
	}
	if !enq.Accepted {
		handlePersistentFailure(cfg, taskName, "rejected: "+enq.Reason)
		return fmt.Errorf("enqueue rejected: %s", enq.Reason)
	}

	aw, err := daemon.AwaitTask(ctx, cfg.HomePath, enq.InvocationID)
	if err != nil {
		handlePersistentFailure(cfg, taskName, fmt.Sprintf("await: %v", err))
		return fmt.Errorf("await: %w", err)
	}
	if !aw.OK {
		handlePersistentFailure(cfg, taskName, "task: "+aw.Err)
		return fmt.Errorf("task failed: %s", aw.Err)
	}

	if aw.SessionID != "" {
		store := session.NewStore(cfg.HomePath)
		if err := store.Set("session:"+sessName, aw.SessionID); err != nil {
			// Non-fatal: the task succeeded; we just couldn't persist
			// the session id for next-run resume.
			fmt.Printf("warning: failed to persist session id: %v\n", err)
		}
	}
	hist := history.NewStore(cfg.HomePath)
	if err := hist.Record(taskName, 0, history.ReasonSuccess, ""); err != nil {
		fmt.Printf("warning: failed to record history: %v\n", err)
	}
	return nil
}

// enqueueFollowUp is a seam for tests. In production it fires a follow-up
// failure-notice prompt back into the same session (no claude -p).
var enqueueFollowUp = func(ctx context.Context, homePath string, req daemon.EnqueueRequest) {
	_, _ = daemon.EnqueueTask(ctx, homePath, req)
}

// handlePersistentFailure records a failed history entry and, when the task
// has notify_on_fail set with non-empty channels, enqueues a brief follow-up
// failure notice into the same persistent session.
func handlePersistentFailure(cfg *config.Config, taskName, reason string) {
	recordPersistentFailure(cfg, taskName, reason)

	task, ok := cfg.Tasks[taskName]
	if !ok {
		return
	}
	if !task.NotifyOnFail || len(task.Channels) == 0 {
		return
	}
	sessName, _, _, err := cfg.ResolveSession(taskName)
	if err != nil {
		return
	}
	tmuxTarget, err := sessionTmuxTarget(cfg, taskName)
	if err != nil {
		return
	}
	body := fmt.Sprintf(
		"The previous task %q failed: %s. Send a brief failure notice to the user via channels: %s.",
		taskName, reason, strings.Join(task.Channels, ", "),
	)
	invID := newInvocationID16()
	wrapped := wrapPromptForPersistent(invID, body, task.Channels)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	enqueueFollowUp(ctx, cfg.HomePath, daemon.EnqueueRequest{
		InvocationID: invID,
		Session:      sessName,
		TmuxSession:  tmuxTarget,
		Task:         taskName + ":notify",
		Prompt:       wrapped,
		Channels:     task.Channels,
		QueueMax:     0, // default = 5; failure notice should not be rejected
		Timeout:      60 * time.Second,
	})
	// Fire-and-forget: we do NOT await the result. The pump processes the
	// notice once the original task's slot clears (Report or timeout); if
	// the queue is at QueueMax the notice is silently dropped. The `leo run`
	// subprocess exits without blocking on the notice either way.
}

// recordPersistentFailure writes a failed entry to the history store. Errors
// from the store are swallowed (with a warning) because the task itself has
// already failed and we don't want to mask that error.
func recordPersistentFailure(cfg *config.Config, taskName, reason string) {
	hist := history.NewStore(cfg.HomePath)
	if err := hist.Record(taskName, 1, reason, ""); err != nil {
		fmt.Printf("warning: failed to record history: %v\n", err)
	}
}

// newInvocationID16 returns a random 16-byte hex-encoded id (32 chars).
// Matches the daemon's internal newInvocationID() shape so markers and
// router ids are interchangeable.
func newInvocationID16() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
