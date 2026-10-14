package drain

import (
	"fmt"
	"time"
)

// CancelText tells a drained agent the restart was called off.
const CancelText = "[leo] Restart cancelled. Resume your work."

// ResumeText tells a drained agent the new daemon is up.
const ResumeText = "[leo] Daemon restarted. Resume where you left off."

const pauseTemplate = "[leo] URGENT: the leo daemon restarts in %d minutes and this session may be killed. Stop now: do not start new steps, tool calls, or dispatches. Finish or abandon the current step, save or commit work in progress, write a short note of where you are and what's next, then end your turn immediately. Anything unsaved when the restart happens may be lost. You'll get a resume message afterward."

// PauseText is the message asking a busy agent to stop before a restart.
// The timeout is stated in whole minutes, rounded up, and never below one.
func PauseText(timeout time.Duration) string {
	return fmt.Sprintf(pauseTemplate, TimeoutMinutes(timeout))
}

// TimeoutMinutes is timeout in whole minutes, rounded up, at least one.
func TimeoutMinutes(timeout time.Duration) int {
	minutes := int((timeout + time.Minute - 1) / time.Minute)
	if minutes < 1 {
		return 1
	}
	return minutes
}
