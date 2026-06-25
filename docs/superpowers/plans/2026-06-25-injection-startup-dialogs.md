# Robust Injection Past Claude Startup Dialogs — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make tmux message injection survive claude's startup/announcement dialogs — by (1) hardening the readiness classifier so a menu/confirm dialog is never read as a ready input box, and (2) generalizing the supervise-loop prompt handler into a generic dialog dismisser that declines unknown dialogs with Esc.

**Architecture:** Two cooperating changes. `classifyInput` (tmux) returns `inputUnknown` for a numbered-menu glyph line or a confirm/cancel-chrome pane, so the injector keeps waiting instead of mis-pasting into a dialog. `dismissStartupDialog` (service, called every 5s in `waitForSessionEnd`) clears the dialog: Enter for the known "Resume from summary", nothing for consequential (trust/permission/delete/overwrite) dialogs, Esc for any other blocking dialog. The decision is a pure function (`startupDialogKey`) for testability.

**Tech Stack:** Go, tmux (`tmux.Args`/`tmux.PaneTarget`), regexp. Tests: standard `go test -race`, table-driven.

**Spec:** `docs/superpowers/specs/2026-06-25-injection-startup-dialogs-design.md`

**Conventions:**
- Single test: `go test -race -run TestName ./internal/<pkg>/`
- Before done: `make test && make lint` (CI also runs golangci-lint with **gofmt** — run `gofmt -l internal/` and fix any listed files before committing).
- Commit per task (conventional commits).

---

## File map

| File | Change |
|---|---|
| `internal/tmux/inject.go` | `classifyInput`: menu-option / dialog-chrome → `inputUnknown`; add `regexp` import + patterns/helpers |
| `internal/tmux/inject_test.go` | classifyInput table tests; injectPrompt waits-through-menu test |
| `internal/service/process.go` | `autoResumePrompt` → `dismissStartupDialog` + pure `startupDialogKey` + helpers; update call site |
| `internal/service/dialog_test.go` *(new)* | `startupDialogKey` table tests |

---

## Task 1: Harden `classifyInput` against menus and dialog chrome

**Files:**
- Modify: `internal/tmux/inject.go` (imports; `classifyInput` ~165-178)
- Test: `internal/tmux/inject_test.go`

- [ ] **Step 1: Write failing tests**

Add to `internal/tmux/inject_test.go`:

```go
func TestClassifyInputDistinguishesMenusFromInputBox(t *testing.T) {
	cases := []struct {
		name string
		pane string
		want inputState
	}{
		{"empty input box", paneWithInput(""), inputEmpty},
		{"probe char in box", paneWithInput("."), inputHasContent},
		{"real typed prompt", paneWithInput("hello world"), inputHasContent},
		{
			"numbered menu option after glyph",
			"  Try the new fullscreen renderer?\n  ❯ 1. Yes, try it\n    2. Not now\n",
			inputUnknown,
		},
		{
			"confirm/cancel dialog chrome",
			"  Some dialog\n  ❯ Proceed\n  Enter to confirm · Esc to cancel\n",
			inputUnknown,
		},
		{
			"paren-style numbered option",
			"  ❯ 1) Option A\n    2) Option B\n",
			inputUnknown,
		},
		{"no glyph at all", "just some output\nno prompt here\n", inputUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyInput(c.pane); got != c.want {
				t.Fatalf("classifyInput = %v, want %v", got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run TestClassifyInputDistinguishesMenusFromInputBox ./internal/tmux/`
Expected: FAIL — the numbered-menu and chrome cases currently return `inputHasContent`/`inputEmpty`, not `inputUnknown`.

- [ ] **Step 3: Add the `regexp` import**

In `internal/tmux/inject.go`, add `"regexp"` to the import block (it currently imports `context`, `fmt`, `os/exec`, `strings`, `time`):

```go
import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)
```

- [ ] **Step 4: Add patterns + rewrite `classifyInput`**

Replace the existing `classifyInput` function with:

```go
// menuOptionPattern matches a numbered selection-menu option like "1. Yes" or
// "2) No" — the shape of claude's interactive dialog options. Such a line is not
// a content-bearing input box even though it follows the prompt glyph.
var menuOptionPattern = regexp.MustCompile(`^\d+[.)]\s`)

// hasDialogChrome reports whether a captured pane shows an interactive dialog's
// confirm/cancel footer rather than a plain input box.
func hasDialogChrome(pane string) bool {
	return strings.Contains(pane, "Enter to confirm") && strings.Contains(pane, "Esc to cancel")
}

// classifyInput inspects a captured pane for claude's input line (the last line
// beginning with the prompt glyph) and reports whether it carries text. A
// selection menu or a confirm/cancel dialog is reported as inputUnknown — the
// glyph there is a menu selector, not a ready input box, so callers keep waiting
// instead of pasting into the dialog.
func classifyInput(pane string) inputState {
	if hasDialogChrome(pane) {
		return inputUnknown
	}
	lines := strings.Split(pane, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimLeft(lines[i], " \t")
		if !strings.HasPrefix(line, claudePromptGlyph) {
			continue
		}
		content := strings.TrimSpace(line[len(claudePromptGlyph):])
		if content == "" {
			return inputEmpty
		}
		if menuOptionPattern.MatchString(content) {
			return inputUnknown
		}
		return inputHasContent
	}
	return inputUnknown
}
```

- [ ] **Step 5: Run — verify PASS (and no regressions in existing inject tests)**

Run: `go test -race -run 'TestClassifyInput|TestInjectPrompt|TestInputHasContent' ./internal/tmux/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -l internal/tmux/ # expect no output
git add internal/tmux/inject.go internal/tmux/inject_test.go
git commit -m "fix: classifyInput treats menus + confirm dialogs as not-a-ready-input"
```

---

## Task 2: Generic startup-dialog dismisser

**Files:**
- Modify: `internal/service/process.go` (`autoResumePrompt` ~793-805 → `dismissStartupDialog`; call site line ~789)
- Test: `internal/service/dialog_test.go` (new)

- [ ] **Step 1: Write failing tests** (new file `internal/service/dialog_test.go`)

```go
package service

import "testing"

func TestStartupDialogKey(t *testing.T) {
	cases := []struct {
		name string
		pane string
		want string
	}{
		{
			"resume-from-summary accepted with Enter",
			"  Resume from summary?\n  Press Enter to confirm\n",
			"Enter",
		},
		{
			"fullscreen renderer announcement declined with Escape",
			"  Try the new fullscreen renderer?\n  ❯ 1. Yes, try it\n    2. Not now\n  Enter to confirm · Esc to cancel\n",
			"Escape",
		},
		{
			"numbered menu without chrome still declined",
			"  Pick a theme\n  ❯ 1. Dark\n    2. Light\n",
			"Escape",
		},
		{
			"trust prompt left for a human",
			"  Do you trust the files in this folder?\n  ❯ 1. Yes, proceed\n    2. No, exit\n  Enter to confirm · Esc to cancel\n",
			"",
		},
		{
			"permission dialog left alone",
			"  Grant permission to run this tool?\n  ❯ 1. Allow\n  Esc to cancel\n",
			"",
		},
		{"plain empty prompt does nothing", "──────\n❯ \n──────\n", ""},
		{"ordinary output does nothing", "doing some work...\nstill working\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := startupDialogKey(c.pane); got != c.want {
				t.Fatalf("startupDialogKey = %q, want %q", got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run — verify FAIL**

Run: `go test -race -run TestStartupDialogKey ./internal/service/`
Expected: FAIL — `startupDialogKey` undefined.

- [ ] **Step 3: Replace `autoResumePrompt` with the decision function + dismisser**

In `internal/service/process.go`, replace the whole `autoResumePrompt` function (and its doc comment) with:

```go
// dialogDenyPattern marks dialogs that make a consequential decision — never
// auto-answered, always left for a human. Word-boundaried, case-insensitive.
var dialogDenyPattern = regexp.MustCompile(`(?i)\b(trust|permission|delete|overwrite)\b`)

// menuLinePattern matches a numbered selection-menu option anywhere in the pane,
// optionally preceded by the selection glyph (e.g. "❯ 1. Yes" or "  2) No").
var menuLinePattern = regexp.MustCompile(`(?m)^\s*❯?\s*\d+[.)]\s`)

// startupDialogKey decides how to clear a blocking claude startup/announcement
// dialog visible in pane. It returns the tmux key to send ("Enter" or "Escape"),
// or "" to leave the pane untouched. Pure (no I/O) so it is unit-tested directly.
//
// Order matters:
//  1. "Resume from summary" is a known prompt we ACCEPT (Enter).
//  2. A dialog mentioning a consequential decision (trust/permission/delete/
//     overwrite) is left for a human — never auto-answered.
//  3. Any other blocking dialog (a numbered-option menu, or a confirm/cancel
//     footer) is an announcement/opt-in we DECLINE with Escape so the agent's
//     behavior stays stable.
func startupDialogKey(pane string) string {
	if strings.Contains(pane, "Resume from summary") && strings.Contains(pane, "Enter to confirm") {
		return "Enter"
	}
	if dialogDenyPattern.MatchString(pane) {
		return ""
	}
	if strings.Contains(pane, "Esc to cancel") || menuLinePattern.MatchString(pane) {
		return "Escape"
	}
	return ""
}

// dismissStartupDialog captures the session's recent pane and clears a blocking
// claude startup/announcement dialog so message injection isn't stuck behind it.
// See startupDialogKey for the policy. Best-effort: capture/send failures are
// ignored and retried on the next poll.
func dismissStartupDialog(tmuxPath, sessionName, processName string) {
	out, err := exec.Command(tmuxPath, tmux.Args("capture-pane", "-t", tmux.PaneTarget(sessionName), "-p", "-S", "-10")...).Output()
	if err != nil {
		return
	}
	key := startupDialogKey(string(out))
	if key == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "[%s] dismissing startup dialog with %s\n", processName, key)
	exec.Command(tmuxPath, tmux.Args("send-keys", "-t", tmux.PaneTarget(sessionName), key)...).Run() //nolint:errcheck
}
```

(`regexp`, `strings`, `fmt`, `os`, `os/exec`, and `tmux` are all already imported in `process.go`.)

- [ ] **Step 4: Update the call site**

In `internal/service/process.go`, in `waitForSessionEnd`, change the line (~789):

```go
		autoResumePrompt(tmuxPath, sessionName, id.Name())
```
to:
```go
		dismissStartupDialog(tmuxPath, sessionName, id.Name())
```

- [ ] **Step 5: Run — verify PASS + build**

Run: `go build ./... && go test -race -run TestStartupDialogKey ./internal/service/`
Expected: builds; PASS. (If any other test referenced `autoResumePrompt`, update it — a repo-wide `grep -rn autoResumePrompt internal/` should return nothing after this task.)

- [ ] **Step 6: Commit**

```bash
gofmt -l internal/service/ # expect no output
git add internal/service/process.go internal/service/dialog_test.go
git commit -m "feat: generic startup-dialog dismisser (Esc declines, denylist guards trust/permission)"
```

---

## Task 3: Prove `InjectPrompt` waits through a menu then delivers

**Files:**
- Test: `internal/tmux/inject_test.go`

This is the end-to-end guard: when a menu is shown (the dismisser hasn't cleared it yet), the injector must NOT paste; once the real input box appears (dismisser cleared it), it pastes exactly once.

- [ ] **Step 1: Write the test**

Add to `internal/tmux/inject_test.go`:

```go
// TestInjectPromptWaitsThroughMenu proves the injector does not paste while a
// startup-dialog menu is showing (classifyInput reports it as not-ready), and
// delivers exactly once after the dialog clears to a real input box — the
// scenario behind the dropped auto-wake message.
func TestInjectPromptWaitsThroughMenu(t *testing.T) {
	var got [][]string
	captureCalls := 0
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		if len(args) >= 3 && args[2] == "capture-pane" {
			captureCalls++
			// First two captures show a blocking menu; then the real input box.
			if captureCalls < 3 {
				return exec.Command("printf", "%s", "  Try the new fullscreen renderer?\n  ❯ 1. Yes, try it\n    2. Not now\n  Enter to confirm · Esc to cancel\n")
			}
			return exec.Command("printf", "%s", paneWithInput(inputProbe))
		}
		return exec.Command("true")
	}

	if err := injectPrompt(context.Background(), "tmux", "leo-session-foo", "body", 10, time.Millisecond); err != nil {
		t.Fatalf("injectPrompt: %v", err)
	}

	// Body pasted exactly once, and only after the menu cleared.
	if n := countSub(got, "paste-buffer"); n != 1 {
		t.Fatalf("body must be pasted exactly once, got %d paste-buffer calls: %#v", n, got)
	}
	// The paste must come AFTER at least the third capture (menu gone).
	if captureCalls < 3 {
		t.Fatalf("expected to probe through the menu (>=3 captures), got %d", captureCalls)
	}
	last := got[len(got)-1]
	if last[3] != "send-keys" || last[len(last)-1] != "Enter" {
		t.Fatalf("last call must be submit Enter, got %#v", last)
	}
}
```

- [ ] **Step 2: Run — verify PASS**

Run: `go test -race -run TestInjectPromptWaitsThroughMenu ./internal/tmux/`
Expected: PASS (Task 1's classifier change makes the menu captures classify as `inputUnknown`, so the probe keeps going until the real box appears). If it FAILS by pasting during the menu, Task 1 is incomplete.

- [ ] **Step 3: Commit**

```bash
git add internal/tmux/inject_test.go
git commit -m "test: InjectPrompt waits through a startup menu before delivering"
```

---

## Final verification

- [ ] **Full suite + lint + gofmt**

Run:
```bash
go test -race ./...
make lint
gofmt -l internal/    # must print nothing
```
Expected: all PASS / clean. (gofmt is enforced by CI's golangci-lint — a formatting miss fails CI even when `make lint` passes locally.)

- [ ] **Manual smoke (optional, needs a daemon + a claude build that shows a dialog)**
  Reuse the isolated-test-daemon technique (memory `reference_isolated_leo_test_daemon`): suspend an agent, trigger a state where claude resumes into an announcement dialog, send a message via `POST /web/process/<name>/message`, and confirm the dialog is Esc-dismissed within ~5s and the message lands in the transcript.

---

## Spec coverage check

- Classifier hardening (menu pattern + confirm/cancel chrome → `inputUnknown`) → Task 1.
- Generic dismisser in the 5s poll loop; Resume→Enter kept; denylist trust/permission/delete/overwrite; else Esc → Task 2.
- Pure `startupDialogKey` decision for testability → Task 2.
- Cooperation (classifier waits, dismisser clears, then deliver) → Task 3 (injector side) + Task 2 (dismisser side).
- All best-effort error handling; budget/fall-open net retained → unchanged in `injectPrompt`; dismisser ignores errors.
