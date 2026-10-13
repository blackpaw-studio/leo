package chat

import (
	"context"
	"fmt"
	"strings"
)

// maxFailureSummaryRunes caps how much of a failed run's error a card quotes.
const maxFailureSummaryRunes = 500

// TaskFailureRequest describes a oneshot task run that ended in failure.
type TaskFailureRequest struct {
	Task     string
	RunID    string
	Attempts int
	Error    string
	LogPath  string
}

// TaskFailure posts a failure card into the task owner's thread, through the
// same path as a task's own posts (owner resolution, missing-owner note,
// kind=task, push).
func (s *Service) TaskFailure(ctx context.Context, req TaskFailureRequest) (Message, error) {
	switch {
	case req.Task == "":
		return Message{}, fmt.Errorf("%w: task is required", ErrInvalid)
	case req.RunID == "":
		return Message{}, fmt.Errorf("%w: run_id is required", ErrInvalid)
	case req.Attempts < 1:
		return Message{}, fmt.Errorf("%w: attempts must be at least 1", ErrInvalid)
	}
	return s.Post(ctx, PostRequest{
		Caller: taskCallerPrefix + req.Task,
		Task:   req.Task,
		RunID:  req.RunID,
		Text:   failureCardText(req),
	})
}

func failureCardText(req TaskFailureRequest) string {
	summary := req.Error
	if r := []rune(summary); len(r) > maxFailureSummaryRunes {
		summary = string(r[:maxFailureSummaryRunes])
	}
	fence := codeFence(summary)
	text := fmt.Sprintf("**Task failed: %s**\nRun `%s` · %d attempt(s)\n%s\n%s\n%s", req.Task, req.RunID, req.Attempts, fence, summary, fence)
	if req.LogPath != "" {
		text += fmt.Sprintf("\nLog: `%s`", req.LogPath)
	}
	return text
}

// codeFence returns a backtick fence longer than any backtick run in body,
// so the body cannot close its own code block.
func codeFence(body string) string {
	longest, run := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}
