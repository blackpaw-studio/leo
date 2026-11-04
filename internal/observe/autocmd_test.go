package observe

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testToken = "tok-alfred"

type autoHarness struct {
	*progressHarness
	auto   *AutoCommands
	agents map[string]string // token → agent
}

func newAutoHarness(t *testing.T) *autoHarness {
	t.Helper()
	h := &autoHarness{progressHarness: newProgressHarness(t), agents: map[string]string{testToken: "alfred"}}
	h.auto = NewAutoCommands(h.clock, h.store, func(token string) (string, bool) {
		name, ok := h.agents[token]
		return name, ok
	})
	return h
}

func (h *autoHarness) send(token string, p map[string]any) {
	h.t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		h.t.Fatal(err)
	}
	h.auto.Hook(token, raw)
}

func (h *autoHarness) hook(p map[string]any) { h.t.Helper(); h.send(testToken, p) }

// autoTasks are agent's auto tasks, oldest first.
func (h *autoHarness) autoTasks(agent string) []AgentTask {
	var out []AgentTask
	for _, t := range h.store.Tasks(agent) {
		if t.Source == SourceAuto {
			out = append(out, t)
		}
	}
	return out
}

func (h *autoHarness) only(agent string) AgentTask {
	h.t.Helper()
	tasks := h.autoTasks(agent)
	if len(tasks) != 1 {
		h.t.Fatalf("want exactly one auto task, got %+v", tasks)
	}
	return tasks[0]
}

func pre(id, desc, cmd string, extra ...map[string]any) map[string]any {
	in := map[string]any{"command": cmd}
	if desc != "" {
		in["description"] = desc
	}
	p := map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_use_id": id, "tool_input": in}
	return merge(p, extra)
}

func post(id string, extra ...map[string]any) map[string]any {
	p := map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_use_id": id,
		"tool_input":    map[string]any{"command": "x"},
		"tool_response": map[string]any{"stdout": "", "interrupted": false},
	}
	return merge(p, extra)
}

func postFail(id string, extra ...map[string]any) map[string]any {
	p := map[string]any{
		"hook_event_name": "PostToolUseFailure", "tool_name": "Bash", "tool_use_id": id,
		"tool_input": map[string]any{"command": "x"}, "error": "Exit code 1", "is_interrupt": false,
	}
	return merge(p, extra)
}

func bgPre(id, desc, cmd string) map[string]any {
	return pre(id, desc, cmd, map[string]any{"tool_input": map[string]any{"command": cmd, "description": desc, "run_in_background": true}})
}

func bgPost(id, taskID string) map[string]any {
	return post(id, map[string]any{"tool_input": map[string]any{}, "tool_response": map[string]any{"stdout": "", "backgroundTaskId": taskID}})
}

func stop(tasks ...map[string]any) map[string]any {
	list := []any{}
	for _, t := range tasks {
		list = append(list, t)
	}
	return map[string]any{"hook_event_name": "Stop", "background_tasks": list}
}

func shellEntry(id string) map[string]any {
	return map[string]any{"id": id, "type": "shell", "status": "running"}
}

func notification(taskID, status string) map[string]any {
	return map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": fmt.Sprintf(
		"<task-notification>\n<task-id>%s</task-id>\n<status>%s</status>\n<summary>x</summary>\n</task-notification>", taskID, status)}
}

func userPrompt(text string) map[string]any {
	return map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": text}
}

func merge(base map[string]any, extra []map[string]any) map[string]any {
	for _, e := range extra {
		for k, v := range e {
			base[k] = v
		}
	}
	return base
}

func TestAutoCmdCommandThatEndsBeforeTheThresholdPublishesNothing(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(pre("toolu_A", "Build", "make"))

	h.clock.Advance(AutoCommandDelay - time.Second)
	h.hook(post("toolu_A"))
	h.clock.Advance(time.Minute)

	if len(h.autoTasks("alfred")) != 0 || len(h.pub.events) != 0 {
		t.Fatalf("short command flickered: %+v, %d events", h.autoTasks("alfred"), len(h.pub.events))
	}
}

func TestAutoCmdLongCommandIsPublishedAtTheThresholdWithItsRealStart(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(pre("toolu_A", "Build the Mac app", "xcodebuild"))

	h.clock.Advance(AutoCommandDelay)

	got := h.only("alfred")
	if got.Title != "Build the Mac app" || got.State != TaskRunning || got.Mode != TaskIndeterminate {
		t.Fatalf("published task wrong: %+v", got)
	}
	if !got.StartedAt.Equal(progressT0) {
		t.Fatalf("started_at = %v, want the Pre time %v", got.StartedAt, progressT0)
	}
	h.clock.Advance(5 * time.Second)
	h.hook(post("toolu_A"))
	if ended := h.only("alfred"); ended.State != TaskSucceeded {
		t.Fatalf("PostToolUse should succeed the task: %+v", ended)
	}
	h.clock.Advance(AutoExpireAfter)
	if len(h.autoTasks("alfred")) != 0 {
		t.Fatal("ended task should clear itself")
	}
}

func TestAutoCmdPairsPreAndPostByToolUseID(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(pre("toolu_A", "First", "a"))
	h.hook(pre("toolu_B", "Second", "b"))
	h.clock.Advance(AutoCommandDelay)

	h.hook(post("toolu_B"))

	byTitle := map[string]TaskState{}
	for _, task := range h.autoTasks("alfred") {
		byTitle[task.Title] = task.State
	}
	if byTitle["First"] != TaskRunning || byTitle["Second"] != TaskSucceeded {
		t.Fatalf("pairing wrong: %v", byTitle)
	}
}

func TestAutoCmdFailureFailsThenClearsAtSixtySeconds(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(pre("toolu_A", "Run tests", "go test"))
	h.clock.Advance(AutoCommandDelay)

	h.hook(postFail("toolu_A"))

	if got := h.only("alfred"); got.State != TaskFailed {
		t.Fatalf("want failed, got %+v", got)
	}
	h.clock.Advance(AutoExpireAfter - time.Second)
	if len(h.autoTasks("alfred")) != 1 {
		t.Fatal("failed task should show for 60 s")
	}
	h.clock.Advance(time.Second)
	if len(h.autoTasks("alfred")) != 0 {
		t.Fatal("failed auto task must not persist")
	}
}

func TestAutoCmdInterruptsAreCancelled(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(pre("toolu_A", "A", "a"))
	h.hook(pre("toolu_B", "B", "b"))
	h.clock.Advance(AutoCommandDelay)

	h.hook(postFail("toolu_A", map[string]any{"is_interrupt": true}))
	h.hook(post("toolu_B", map[string]any{"tool_response": map[string]any{"interrupted": true}}))

	for _, task := range h.autoTasks("alfred") {
		if task.State != TaskCancelled {
			t.Fatalf("%s: want cancelled, got %s", task.Title, task.State)
		}
	}
}

func TestAutoCmdPostBeforeAsyncPreLeavesNoGhost(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(post("toolu_A"))

	h.hook(pre("toolu_A", "Late", "a"))
	h.clock.Advance(time.Hour)

	if len(h.autoTasks("alfred")) != 0 {
		t.Fatalf("late Pre armed a ghost: %+v", h.autoTasks("alfred"))
	}
}

func TestAutoCmdTombstonesExpire(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(post("toolu_A"))
	h.clock.Advance(AutoTombstoneTTL + time.Second)

	h.hook(pre("toolu_A", "Reused", "a"))
	h.clock.Advance(AutoCommandDelay)

	if len(h.autoTasks("alfred")) != 1 {
		t.Fatal("an expired tombstone must not block a new call")
	}
}

func TestAutoCmdBackgroundShellRunsUntilItsNotification(t *testing.T) {
	for status, want := range map[string]TaskState{"completed": TaskSucceeded, "failed": TaskFailed, "killed": TaskCancelled, "weird": TaskSucceeded} {
		t.Run(status, func(t *testing.T) {
			h := newAutoHarness(t)
			h.hook(bgPre("toolu_A", "Bg build", "make"))
			h.hook(bgPost("toolu_A", "bg1"))
			h.clock.Advance(AutoCommandDelay)
			h.hook(stop(shellEntry("bg1")))
			h.clock.Advance(time.Minute)
			if got := h.only("alfred"); got.State != TaskRunning {
				t.Fatalf("a background shell outlives its PostToolUse: %+v", got)
			}

			h.hook(notification("bg1", status))

			if got := h.only("alfred"); got.State != want {
				t.Fatalf("status %q: got %s, want %s", status, got.State, want)
			}
		})
	}
}

func TestAutoCmdTaskStopCancelsABackgroundShell(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(bgPre("toolu_A", "Bg", "make"))
	h.hook(bgPost("toolu_A", "bg1"))
	h.clock.Advance(AutoCommandDelay)

	h.hook(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "TaskStop", "tool_use_id": "toolu_S",
		"tool_input": map[string]any{"task_id": "bg1"}})

	if got := h.only("alfred"); got.State != TaskCancelled {
		t.Fatalf("got %+v", got)
	}
}

func TestAutoCmdStopOmissionSucceedsABackgroundShell(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(bgPre("toolu_A", "Bg", "make"))
	h.hook(bgPost("toolu_A", "bg1"))
	h.clock.Advance(AutoCommandDelay)

	h.hook(stop())

	if got := h.only("alfred"); got.State != TaskSucceeded {
		t.Fatalf("got %+v", got)
	}
}

func TestAutoCmdBackgroundShellWithoutPreIsArmedAtPost(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(bgPost("toolu_A", "bg1"))

	h.clock.Advance(AutoCommandDelay - time.Second)
	if len(h.autoTasks("alfred")) != 0 {
		t.Fatal("published before 20 s")
	}
	h.clock.Advance(time.Second)

	got := h.only("alfred")
	if got.State != TaskRunning || got.Title != "Shell command" {
		t.Fatalf("got %+v", got)
	}
}

func TestAutoCmdStopCancelsMainLoopForegroundStraysOnly(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(pre("toolu_FG", "Stray", "a"))
	h.hook(pre("toolu_SUB", "Subagent call", "b", map[string]any{"agent_id": "ag1"}))
	h.hook(bgPre("toolu_BG", "Bg", "c"))
	h.hook(bgPost("toolu_BG", "bg1"))
	h.clock.Advance(AutoCommandDelay)

	h.hook(stop(shellEntry("bg1"), map[string]any{"id": "ag1", "type": "subagent", "status": "running"}))

	states := map[string]TaskState{}
	for _, task := range h.autoTasks("alfred") {
		states[task.Title] = task.State
	}
	want := map[string]TaskState{"Stray": TaskCancelled, "Subagent call": TaskRunning, "Bg": TaskRunning}
	if fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", states, want)
	}
}

func TestAutoCmdSubagentStopCancelsItsOwnCalls(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(pre("toolu_S1", "Sub one", "a", map[string]any{"agent_id": "ag1"}))
	h.hook(pre("toolu_S2", "Sub two", "b", map[string]any{"agent_id": "ag2"}))
	h.clock.Advance(AutoCommandDelay)

	h.hook(map[string]any{"hook_event_name": "SubagentStop", "agent_id": "ag1"})

	states := map[string]TaskState{}
	for _, task := range h.autoTasks("alfred") {
		states[task.Title] = task.State
	}
	if states["Sub one"] != TaskCancelled || states["Sub two"] != TaskRunning {
		t.Fatalf("got %v", states)
	}
}

func TestAutoCmdSessionEndCancelsEverything(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(pre("toolu_FG", "Fg", "a"))
	h.hook(bgPre("toolu_BG", "Bg", "c"))
	h.hook(bgPost("toolu_BG", "bg1"))
	h.clock.Advance(AutoCommandDelay)

	h.hook(map[string]any{"hook_event_name": "SessionEnd"})

	for _, task := range h.autoTasks("alfred") {
		if task.State != TaskCancelled {
			t.Fatalf("%s: got %s", task.Title, task.State)
		}
	}
}

func TestAutoCmdRealPromptCancelsAnInterruptedForegroundCall(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(pre("toolu_FG", "Interrupted", "a"))
	h.hook(pre("toolu_SUB", "Sub", "b", map[string]any{"agent_id": "ag1"}))
	h.hook(bgPre("toolu_BG", "Bg", "c"))
	h.hook(bgPost("toolu_BG", "bg1"))
	h.clock.Advance(AutoCommandDelay)

	h.hook(notification("zzz", "completed")) // a harness injection is not a new turn
	if h.only3("Interrupted") != TaskRunning {
		t.Fatal("a task-notification must not cancel foreground calls")
	}
	h.hook(userPrompt("ok, next"))

	if h.only3("Interrupted") != TaskCancelled || h.only3("Sub") != TaskRunning || h.only3("Bg") != TaskRunning {
		t.Fatalf("got %+v", h.autoTasks("alfred"))
	}
	h.hook(post("toolu_FG")) // a late Post for the cancelled call changes nothing
	if h.only3("Interrupted") != TaskCancelled {
		t.Fatal("late Post must be ignored")
	}
}

func (h *autoHarness) only3(title string) TaskState {
	for _, task := range h.autoTasks("alfred") {
		if task.Title == title {
			return task.State
		}
	}
	h.t.Fatalf("no auto task %q in %+v", title, h.autoTasks("alfred"))
	return ""
}

func TestAutoCmdSuppressRemovesPublishedAndArmedButNotDispatchTasks(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(pre("toolu_PUB", "Published", "a"))
	h.clock.Advance(AutoCommandDelay)
	h.hook(pre("toolu_ARM", "Armed", "b"))
	h.progressHarness.auto("alfred", AutoTask{ID: "auto.dispatch.d1", Title: "impl · implement"})

	h.auto.Suppress("alfred")
	h.clock.Advance(time.Minute)

	tasks := h.autoTasks("alfred")
	if len(tasks) != 1 || tasks[0].ID != "auto.dispatch.d1" {
		t.Fatalf("got %+v", tasks)
	}
	h.hook(post("toolu_PUB"))
	h.hook(post("toolu_ARM"))
	if got := h.autoTasks("alfred"); len(got) != 1 {
		t.Fatalf("suppressed commands must stay silent when they end: %+v", got)
	}
}

func TestAutoCmdSuppressOnlyAffectsThatAgent(t *testing.T) {
	h := newAutoHarness(t)
	h.agents["tok-bob"] = "bob"
	h.send("tok-bob", pre("toolu_B", "Bob build", "a"))
	h.hook(pre("toolu_A", "Alfred build", "a"))
	h.clock.Advance(AutoCommandDelay)

	h.auto.Suppress("alfred")

	if len(h.autoTasks("alfred")) != 0 || len(h.autoTasks("bob")) != 1 {
		t.Fatalf("alfred %+v bob %+v", h.autoTasks("alfred"), h.autoTasks("bob"))
	}
}

func TestAutoCmdUnknownTokenIsIgnoredAndStoppedLaunchesDropOut(t *testing.T) {
	h := newAutoHarness(t)
	h.send("nobody", pre("toolu_A", "Ghost", "a"))
	h.hook(pre("toolu_B", "Real", "a"))
	delete(h.agents, testToken) // the agent stopped before the timer fired

	h.clock.Advance(AutoCommandDelay)

	if len(h.autoTasks("alfred")) != 0 || len(h.store.AllTasks()) != 0 {
		t.Fatalf("got %+v", h.store.AllTasks())
	}
}

func TestAutoCmdFollowsARenameBetweenArmAndPublish(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(pre("toolu_A", "Build", "a"))
	h.agents[testToken] = "alfred2"

	h.clock.Advance(AutoCommandDelay)

	if len(h.autoTasks("alfred")) != 0 || len(h.autoTasks("alfred2")) != 1 {
		t.Fatalf("alfred %+v alfred2 %+v", h.autoTasks("alfred"), h.autoTasks("alfred2"))
	}
}

func TestAutoCmdRunningCapSkipsTheEntryAndItsEnd(t *testing.T) {
	h := newAutoHarness(t)
	for i := range MaxTasksPerClass + 1 {
		h.hook(pre(fmt.Sprintf("toolu_%d", i), fmt.Sprintf("Cmd %d", i), "a"))
	}

	h.clock.Advance(AutoCommandDelay)

	if got := len(h.autoTasks("alfred")); got != MaxTasksPerClass {
		t.Fatalf("published %d, want %d", got, MaxTasksPerClass)
	}
	h.hook(post(fmt.Sprintf("toolu_%d", MaxTasksPerClass))) // the skipped one ends: no error, no task
	if got := len(h.autoTasks("alfred")); got != MaxTasksPerClass {
		t.Fatalf("after the skipped end: %d", got)
	}
}

func TestAutoCmdCodexShellToolsAreTrackedAndOthersAreNot(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "shell", "tool_use_id": "c1",
		"tool_input": map[string]any{"command": []any{"bash", "-lc", "make"}}})
	h.hook(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Read", "tool_use_id": "c2",
		"tool_input": map[string]any{"file_path": "/etc/passwd"}})
	h.clock.Advance(AutoCommandDelay)

	got := h.only("alfred")
	if got.Title != "bash" {
		t.Fatalf("title = %q", got.Title)
	}
	h.hook(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "shell", "tool_use_id": "c1"})
	if h.only("alfred").State != TaskSucceeded {
		t.Fatal("codex Post should succeed the task")
	}
}

// The real Claude Code 2.1.318 payloads, as captured, drive the same flows.
func TestAutoCmdReplaysCapturedPayloads(t *testing.T) {
	load := func(name string) map[string]any {
		raw, err := os.ReadFile(filepath.Join("..", "web", "testdata", "hooks", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("foreground", func(t *testing.T) {
		h := newAutoHarness(t)
		h.hook(load("pre-bash-fg"))
		h.clock.Advance(AutoCommandDelay)
		if got := h.only("alfred"); got.Title != "Wait a bit" {
			t.Fatalf("title = %q", got.Title)
		}
		h.hook(load("post-bash-fg"))
		if h.only("alfred").State != TaskSucceeded {
			t.Fatal("not succeeded")
		}
	})
	t.Run("failure", func(t *testing.T) {
		h := newAutoHarness(t)
		h.hook(load("pre-bash-nodesc"))
		h.clock.Advance(AutoCommandDelay)
		if got := h.only("alfred"); got.Title != "Run false to check exit status" {
			t.Fatalf("title = %q", got.Title)
		}
		h.hook(load("postfailure-bash"))
		if h.only("alfred").State != TaskFailed {
			t.Fatal("not failed")
		}
	})
	t.Run("background shell", func(t *testing.T) {
		h := newAutoHarness(t)
		h.hook(load("pre-bash-bg"))
		h.hook(load("post-bash-bg"))
		h.clock.Advance(AutoCommandDelay)
		h.hook(load("stop-bg-running"))
		if h.only("alfred").State != TaskRunning {
			t.Fatal("background shell should still run")
		}
		h.hook(load("prompt-task-notification-completed"))
		if h.only("alfred").State != TaskSucceeded {
			t.Fatal("notification should end it")
		}
	})
	t.Run("interrupted call", func(t *testing.T) {
		h := newAutoHarness(t)
		h.hook(load("pre-bash-fg"))
		h.clock.Advance(AutoCommandDelay)
		h.hook(load("prompt-user"))
		if h.only("alfred").State != TaskCancelled {
			t.Fatal("a real prompt should cancel the interrupted call")
		}
	})
	t.Run("subagent calls survive the main Stop", func(t *testing.T) {
		h := newAutoHarness(t)
		h.hook(load("pre-bash-subagent"))
		h.clock.Advance(AutoCommandDelay)
		h.hook(load("stop-subagent-running"))
		if h.only("alfred").State != TaskRunning {
			t.Fatal("main Stop must leave a subagent's call alone")
		}
		h.hook(load("subagentstop"))
		if h.only("alfred").State != TaskCancelled {
			t.Fatal("SubagentStop should cancel it")
		}
	})
}

func TestAutoCmdDropAgentCancelsPendingCommandsOfThatAgentOnly(t *testing.T) {
	h := newAutoHarness(t)
	h.agents["tok-bob"] = "bob"
	h.hook(pre("toolu_A", "Alfred", "a"))
	h.send("tok-bob", pre("toolu_B", "Bob", "b"))

	h.auto.DropAgent("alfred")
	h.clock.Advance(AutoCommandDelay)

	if len(h.autoTasks("alfred")) != 0 || len(h.autoTasks("bob")) != 1 {
		t.Fatalf("alfred %+v bob %+v", h.autoTasks("alfred"), h.autoTasks("bob"))
	}
}

func TestAutoCmdMainStopCancelsABackgroundCallWhoseShellNeverStarted(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(bgPre("toolu_BG", "Lost post", "make")) // its PostToolUse was lost, so no task id is known
	h.clock.Advance(AutoCommandDelay)

	h.hook(stop())

	if got := h.only("alfred"); got.State != TaskCancelled {
		t.Fatalf("got %+v", got)
	}
}

func TestAutoCmdMainStopCancelsCallsOfSubagentsThatAreGone(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(pre("toolu_GONE", "Gone", "a", map[string]any{"agent_id": "ag-gone"}))
	h.hook(pre("toolu_LIVE", "Live", "b", map[string]any{"agent_id": "ag-live"}))
	h.clock.Advance(AutoCommandDelay)

	h.hook(stop(map[string]any{"id": "ag-live", "type": "subagent", "status": "running"}))

	states := map[string]TaskState{}
	for _, task := range h.autoTasks("alfred") {
		states[task.Title] = task.State
	}
	if states["Gone"] != TaskCancelled || states["Live"] != TaskRunning {
		t.Fatalf("got %v", states)
	}
}

func TestAutoCmdStopWithoutATaskListLeavesSubagentCallsAlone(t *testing.T) {
	h := newAutoHarness(t)
	h.hook(pre("toolu_S", "Sub", "a", map[string]any{"agent_id": "ag1"}))
	h.clock.Advance(AutoCommandDelay)

	h.hook(map[string]any{"hook_event_name": "Stop"})

	if h.only("alfred").State != TaskRunning {
		t.Fatal("a Stop that lists nothing says nothing about subagents")
	}
}

func TestAutoCmdLogsOnceWhenTheOpenCallLimitIsHit(t *testing.T) {
	h := newAutoHarness(t)
	var lines []string
	h.auto.logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	for i := range maxOpenCalls + 5 {
		h.hook(pre(fmt.Sprintf("toolu_%d", i), "c", "a"))
	}
	if len(lines) != 1 {
		t.Fatalf("logged %d times: %v", len(lines), lines)
	}
	h.clock.Advance(autoLogInterval)
	h.hook(pre("toolu_again", "c", "a"))
	if len(lines) != 2 {
		t.Fatalf("the log should repeat after its interval: %v", lines)
	}
}

func TestAutoCmdLogsWhenTheBackgroundShellLimitIsHit(t *testing.T) {
	h := newAutoHarness(t)
	var lines []string
	h.auto.logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	for i := range 70 {
		h.hook(bgPost(fmt.Sprintf("toolu_%d", i), fmt.Sprintf("bg%d", i)))
	}
	if len(lines) != 1 {
		t.Fatalf("logged %d times: %v", len(lines), lines)
	}
}
