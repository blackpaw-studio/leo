package observe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/bgshell"
)

// Automatic command tasks: a long shell command an agent runs shows up as an
// auto task without the agent reporting it. The tracker reads the PreToolUse,
// PostToolUse(Failure), Stop, SubagentStop, UserPromptSubmit and SessionEnd
// hooks the daemon already receives for a supervised agent. A command is
// published only once it has run AutoCommandDelay, so short ones never flicker.

const (
	// AutoCommandDelay is how long a command must run before it is published.
	AutoCommandDelay = 20 * time.Second
	// AutoTombstoneTTL is how long an ended tool_use_id is remembered, so a
	// late (async) PreToolUse cannot arm a ghost task for a call that is over.
	AutoTombstoneTTL = 2 * time.Minute
	// autoCmdIDPrefix prefixes command task ids; the rest is a hash of the
	// tool_use_id (which is mixed-case and fails the task id alphabet).
	autoCmdIDPrefix = AutoIDPrefix + "cmd."
	autoCmdIDHexLen = 12
	// maxOpenCalls and maxTombstones bound one launch's tracker state, which
	// grows from model-driven tool calls.
	maxOpenCalls  = 64
	maxTombstones = 64
)

// autoCommandTools are the tool names whose calls are commands. Claude Code
// names its shell tool Bash. The rest are Codex's possible names for its shell
// tool; they are UNVERIFIED (Codex was not captured, see the commit notes), so
// Codex commands are matched by this list alone and only ever succeed or get
// cancelled (Codex has no PostToolUseFailure and no exit field is known).
var autoCommandTools = []string{"Bash", "shell", "local_shell", "exec_command", "container.exec"}

var notificationStatus = regexp.MustCompile(`<status>\s*([A-Za-z_-]+)\s*</status>`)

// AutoCommands tracks command calls per supervised launch (keyed by its
// attention token) and publishes them as auto tasks. All methods are nil-safe.
type AutoCommands struct {
	mu       sync.Mutex
	clock    FeedClock
	store    *ProgressStore
	agentFor func(token string) (agent string, ok bool)
	launches map[string]*cmdLaunch
}

// cmdLaunch is one launch's open calls and background shells.
type cmdLaunch struct {
	calls map[string]*cmdCall // by tool_use_id
	bg    []bgshell.Task
	tombs []tombstone
}

type tombstone struct {
	useID string
	at    time.Time
}

// cmdCall is one command call, from its PreToolUse (or the PostToolUse that
// started a background shell) to its end.
type cmdCall struct {
	useID      string
	agentID    string // set for a call made inside a subagent
	title      string
	startedAt  time.Time
	background bool
	bgID       string // the background shell's task id, once known
	published  bool   // an auto task exists for it
	skipped    bool   // never published (cap) or suppressed: its end is ignored
	stopTimer  func() bool
	gen        uint64
}

func (c *cmdCall) key() string { return autoCmdKey(c.useID) }

// autoCmdKey is the task id for a tool_use_id.
func autoCmdKey(useID string) string {
	sum := sha256.Sum256([]byte(useID))
	return autoCmdIDPrefix + hex.EncodeToString(sum[:])[:autoCmdIDHexLen]
}

// NewAutoCommands creates a tracker publishing into store. agentFor resolves a
// launch's token to its current agent name at publish time (renames follow; a
// stopped launch resolves to nothing and its calls are dropped).
func NewAutoCommands(clock FeedClock, store *ProgressStore, agentFor func(token string) (string, bool)) *AutoCommands {
	if clock == nil {
		clock = realFeedClock{}
	}
	return &AutoCommands{clock: clock, store: store, agentFor: agentFor, launches: map[string]*cmdLaunch{}}
}

// hookEvent is the part of a hook payload the tracker reads.
type hookEvent struct {
	name        string // normalized hook_event_name
	tool        string
	useID       string
	agentID     string
	description string
	command     string
	background  bool // tool_input.run_in_background
	interrupted bool // tool_response.interrupted
	isInterrupt bool // PostToolUseFailure.is_interrupt
	prompt      string
	raw         map[string]any
}

func parseHookEvent(payload []byte) (hookEvent, bool) {
	var p map[string]any
	if json.Unmarshal(payload, &p) != nil {
		return hookEvent{}, false
	}
	input, _ := p["tool_input"].(map[string]any)
	resp, _ := p["tool_response"].(map[string]any)
	e := hookEvent{
		name:        strings.ToLower(strings.ReplaceAll(bgshell.Str(p, "hook_event_name"), "_", "")),
		tool:        bgshell.Str(p, "tool_name"),
		useID:       bgshell.Str(p, "tool_use_id"),
		agentID:     bgshell.Str(p, "agent_id"),
		description: bgshell.Str(input, "description"),
		command:     commandText(input["command"]),
		prompt:      bgshell.Str(p, "prompt"),
		raw:         p,
	}
	e.background, _ = input["run_in_background"].(bool)
	e.interrupted, _ = resp["interrupted"].(bool)
	e.isInterrupt, _ = p["is_interrupt"].(bool)
	return e, e.name != ""
}

// commandText is a tool_input.command: a string, or an argv array.
func commandText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		parts := make([]string, 0, len(c))
		for _, w := range c {
			if s, ok := w.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

func (e hookEvent) isCommandTool() bool { return slices.Contains(autoCommandTools, e.tool) }

// Hook applies one hook payload of the launch holding token.
func (a *AutoCommands) Hook(token string, payload []byte) {
	if a == nil {
		return
	}
	e, ok := parseHookEvent(payload)
	if !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, known := a.agentFor(token); !known {
		a.dropLocked(token)
		return
	}
	l := a.launches[token]
	if l == nil {
		l = &cmdLaunch{calls: map[string]*cmdCall{}}
		a.launches[token] = l
	}
	switch e.name {
	case "pretooluse":
		a.preLocked(token, l, e)
	case "posttooluse", "posttoolusefailure":
		a.postLocked(token, l, e)
	case "stop":
		a.stopLocked(token, l, e)
	case "subagentstop":
		a.cancelWhereLocked(token, l, func(c *cmdCall) bool { return e.agentID != "" && c.agentID == e.agentID })
	case "userpromptsubmit":
		a.promptLocked(token, l, e)
	case "sessionend":
		a.cancelWhereLocked(token, l, func(*cmdCall) bool { return true })
		l.bg = nil
	}
}

func (a *AutoCommands) preLocked(token string, l *cmdLaunch, e hookEvent) {
	if !e.isCommandTool() || e.useID == "" || l.calls[e.useID] != nil || a.tombstonedLocked(l, e.useID) {
		return
	}
	a.armLocked(token, l, &cmdCall{
		useID: e.useID, agentID: e.agentID, background: e.background,
		title: autoCmdTitle(e.description, e.command),
	})
}

func (a *AutoCommands) postLocked(token string, l *cmdLaunch, e hookEvent) {
	// Background-shell bookkeeping runs for every tool event: a TaskStop call
	// ends a shell, and a Bash call that started one adds it.
	bg, decisions := bgshell.TrackToolEvent(l.bg, e.name, e.raw, a.clock.Now())
	l.bg = bg
	a.endDecisionsLocked(token, l, decisions, e)
	if !e.isCommandTool() || e.useID == "" {
		return
	}
	resp, _ := e.raw["tool_response"].(map[string]any)
	if bgID := bgshell.Str(resp, "backgroundTaskId"); bgID != "" {
		c := l.calls[e.useID]
		if c == nil {
			if a.tombstonedLocked(l, e.useID) {
				return
			}
			c = &cmdCall{useID: e.useID, agentID: e.agentID, title: autoCmdTitle(e.description, e.command)}
			a.armLocked(token, l, c)
		}
		c.background, c.bgID = true, bgID
		return
	}
	c := l.calls[e.useID]
	if c == nil {
		a.tombstoneLocked(l, e.useID)
		return
	}
	switch {
	case e.name == "posttoolusefailure" && e.isInterrupt, e.name == "posttooluse" && e.interrupted:
		a.endLocked(token, l, c, TaskCancelled)
	case e.name == "posttoolusefailure":
		a.endLocked(token, l, c, TaskFailed)
	default:
		a.endLocked(token, l, c, TaskSucceeded)
	}
}

func (a *AutoCommands) stopLocked(token string, l *cmdLaunch, e hookEvent) {
	bg, decisions := bgshell.TrackStop(l.bg, e.raw)
	l.bg = bg
	a.endDecisionsLocked(token, l, decisions, e)
	a.cancelWhereLocked(token, l, isMainLoopForeground)
}

func (a *AutoCommands) promptLocked(token string, l *cmdLaunch, e hookEvent) {
	if bgshell.IsHarnessInjection(e.prompt) {
		bg, decisions := bgshell.TrackPrompt(l.bg, e.prompt)
		l.bg = bg
		a.endDecisionsLocked(token, l, decisions, e)
		return
	}
	// A turn began, so a main-loop foreground call still open was interrupted:
	// Claude fires no Post, PostFailure or Stop for it.
	a.cancelWhereLocked(token, l, isMainLoopForeground)
}

func isMainLoopForeground(c *cmdCall) bool { return c.agentID == "" && !c.background }

// endDecisionsLocked ends the calls of the background shells the tracker
// pruned, by what pruned them.
func (a *AutoCommands) endDecisionsLocked(token string, l *cmdLaunch, decisions []bgshell.Decision, e hookEvent) {
	for _, d := range decisions {
		if d.Decision != bgshell.DecisionPruned {
			continue
		}
		var state TaskState
		switch d.Reason {
		case bgshell.ReasonStopOK, bgshell.ReasonStopFailedGone:
			state = TaskCancelled
		case bgshell.ReasonTaskNotification:
			state = notificationState(e.prompt)
		default: // omitted from a Stop's list: gone, outcome unknown
			state = TaskSucceeded
		}
		for _, c := range l.calls {
			if c.bgID == d.TaskID {
				a.endLocked(token, l, c, state)
			}
		}
	}
}

// notificationState maps a task-notification's <status> to an end state; an
// unknown status counts as success.
func notificationState(prompt string) TaskState {
	m := notificationStatus.FindStringSubmatch(prompt)
	if len(m) < 2 {
		return TaskSucceeded
	}
	switch strings.ToLower(m[1]) {
	case "failed":
		return TaskFailed
	case "killed":
		return TaskCancelled
	}
	return TaskSucceeded
}

// cancelWhereLocked cancels every open call match accepts.
func (a *AutoCommands) cancelWhereLocked(token string, l *cmdLaunch, match func(*cmdCall) bool) {
	for _, c := range slices.Collect(mapValues(l.calls)) {
		if match(c) {
			a.endLocked(token, l, c, TaskCancelled)
		}
	}
}

func mapValues(m map[string]*cmdCall) func(func(*cmdCall) bool) {
	return func(yield func(*cmdCall) bool) {
		for _, c := range m {
			if !yield(c) {
				return
			}
		}
	}
}

// armLocked starts c's publish timer and records it.
func (a *AutoCommands) armLocked(token string, l *cmdLaunch, c *cmdCall) {
	if len(l.calls) >= maxOpenCalls {
		return
	}
	c.startedAt = a.clock.Now()
	l.calls[c.useID] = c
	gen := c.gen
	c.stopTimer = a.clock.AfterFunc(AutoCommandDelay, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if c.gen == gen && l.calls[c.useID] == c {
			a.publishLocked(token, c)
		}
	})
}

// publishLocked makes c's auto task visible (it has run AutoCommandDelay).
func (a *AutoCommands) publishLocked(token string, c *cmdCall) {
	c.stopTimer = nil
	if c.skipped {
		return
	}
	agent, ok := a.agentFor(token)
	if !ok {
		return
	}
	_, err := a.store.UpsertAuto(agent, AutoTask{ID: c.key(), Title: c.title, State: TaskRunning, StartedAt: c.startedAt})
	if err != nil {
		c.skipped = true // over the cap (or unavailable): its end is ignored too
		return
	}
	c.published = true
}

// endLocked ends c: an unpublished call vanishes silently, a published one
// moves to state. Either way the id is remembered so a late event for it is
// ignored.
func (a *AutoCommands) endLocked(token string, l *cmdLaunch, c *cmdCall, state TaskState) {
	c.gen++
	if c.stopTimer != nil {
		c.stopTimer()
		c.stopTimer = nil
	}
	delete(l.calls, c.useID)
	a.tombstoneLocked(l, c.useID)
	if !c.published || c.skipped {
		return
	}
	if agent, ok := a.agentFor(token); ok {
		_, _ = a.store.UpsertAuto(agent, AutoTask{ID: c.key(), State: state})
	}
}

func (a *AutoCommands) tombstoneLocked(l *cmdLaunch, useID string) {
	if useID == "" {
		return
	}
	l.tombs = append(l.pruneTombsLocked(a.clock.Now()), tombstone{useID: useID, at: a.clock.Now()})
	if over := len(l.tombs) - maxTombstones; over > 0 {
		l.tombs = l.tombs[over:]
	}
}

func (a *AutoCommands) tombstonedLocked(l *cmdLaunch, useID string) bool {
	l.tombs = l.pruneTombsLocked(a.clock.Now())
	return slices.ContainsFunc(l.tombs, func(t tombstone) bool { return t.useID == useID })
}

// pruneTombsLocked drops tombstones older than AutoTombstoneTTL.
func (l *cmdLaunch) pruneTombsLocked(now time.Time) []tombstone {
	return slices.DeleteFunc(l.tombs, func(t tombstone) bool { return now.Sub(t.at) >= AutoTombstoneTTL })
}

// Suppress removes every running auto command task of agent, published or
// still armed, and silences those calls until they end: the agent reported its
// own task for the same work. Dispatch tasks are never touched.
func (a *AutoCommands) Suppress(agent string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for token, l := range a.launches {
		if name, ok := a.agentFor(token); !ok || name != agent {
			continue
		}
		for _, c := range l.calls {
			c.skipped = true
			if c.stopTimer != nil {
				c.stopTimer()
				c.stopTimer = nil
			}
		}
	}
	a.store.RemoveAutoRunning(agent, autoCmdIDPrefix)
}

// Drop forgets a launch (its agent stopped), cancelling its pending timers.
func (a *AutoCommands) Drop(token string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropLocked(token)
}

func (a *AutoCommands) dropLocked(token string) {
	l := a.launches[token]
	if l == nil {
		return
	}
	for _, c := range l.calls {
		c.gen++
		if c.stopTimer != nil {
			c.stopTimer()
		}
	}
	delete(a.launches, token)
}
