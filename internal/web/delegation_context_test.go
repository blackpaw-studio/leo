package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

var (
	delegationOn  = bridge.DelegationState{Enabled: true, Section: "SECTION v1"}
	delegationV2  = bridge.DelegationState{Enabled: true, Section: "SECTION v2"}
	delegationOff = bridge.DelegationState{}
	contextNow    = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
)

func toldOf(d bridge.DelegationState) *delegationTold {
	t := toldFor(d, contextNow)
	return &t
}

func TestDelegationContextFor(t *testing.T) {
	for _, tc := range []struct {
		name, event, source string
		current             bridge.DelegationState
		told                *delegationTold
		wantText            string // "" = nothing; otherwise a substring
		wantTold            bridge.DelegationState
	}{
		{"startup on", "SessionStart", "startup", delegationOn, nil, "SECTION v1", delegationOn},
		{"resume on, told the same", "SessionStart", "resume", delegationOn, toldOf(delegationOn), "SECTION v1", delegationOn},
		{"clear on", "SessionStart", "clear", delegationOn, nil, "SECTION v1", delegationOn},
		{"compact on", "SessionStart", "compact", delegationOn, toldOf(delegationOn), "SECTION v1", delegationOn},
		{"fork on", "SessionStart", "fork", delegationOn, nil, "SECTION v1", delegationOn},
		{"startup off says nothing", "SessionStart", "startup", delegationOff, nil, "", delegationOff},
		{"resume off after on says so", "SessionStart", "resume", delegationOff, toldOf(delegationOn), "delegation is now OFF", delegationOff},
		{"resume off after off says nothing", "SessionStart", "resume", delegationOff, toldOf(delegationOff), "", delegationOff},
		{"prompt with the same policy says nothing", "UserPromptSubmit", "", delegationOn, toldOf(delegationOn), "", delegationOn},
		{"prompt after a change replaces the earlier context", "UserPromptSubmit", "", delegationV2, toldOf(delegationOn), "replaces the leo-delegation context given earlier in this session.\n\nSECTION v2", delegationV2},
		{"prompt after turning off", "UserPromptSubmit", "", delegationOff, toldOf(delegationOn), "Ignore the leo-delegation context given earlier in this session", delegationOff},
		{"prompt after turning on", "UserPromptSubmit", "", delegationOn, toldOf(delegationOff), "SECTION v1", delegationOn},
		{"prompt never told, on", "UserPromptSubmit", "", delegationOn, nil, "SECTION v1", delegationOn},
		{"prompt never told, off", "UserPromptSubmit", "", delegationOff, nil, "", delegationOff},
		{"an enabled policy with no section is off", "UserPromptSubmit", "", bridge.DelegationState{Enabled: true}, toldOf(delegationOn), "delegation is now OFF", delegationOff},
		{"another event says nothing", "Stop", "", delegationOn, nil, "", delegationOn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, next := contextFor(tc.event, tc.source, tc.current, tc.told, contextNow)

			if tc.wantText == "" && text != "" || !strings.Contains(text, tc.wantText) {
				t.Fatalf("text = %q, want it to contain %q", text, tc.wantText)
			}
			if strings.Contains(text, "system prompt") {
				t.Fatalf("text = %q, want no reference to the system prompt: the section arrives as hook context", text)
			}
			if tc.event == "Stop" {
				return
			}
			if !sameToldDelegation(next, tc.wantTold) {
				t.Fatalf("next told = %+v, want %+v", next, toldFor(tc.wantTold, contextNow))
			}
		})
	}
}

func TestDelegationContextRepeatPromptSaysNothing(t *testing.T) {
	text, told := contextFor("SessionStart", "startup", delegationOn, nil, contextNow)
	if text == "" {
		t.Fatal("startup said nothing")
	}

	for i := 0; i < 3; i++ {
		var next delegationTold
		text, next = contextFor("UserPromptSubmit", "", delegationOn, &told, contextNow)
		told = next
		if text != "" {
			t.Fatalf("prompt %d said %q, want nothing", i, text)
		}
	}
}

func TestToldStoreRoundTripsAndPrunes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "delegation-told.json")
	now := contextNow
	store := newToldStore(path, func() time.Time { return now })

	if err := store.Put("leo-a", "sid-1", toldFor(delegationOn, now)); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("leo-a", "sid-2", toldFor(delegationOff, now)); err != nil {
		t.Fatal(err)
	}

	reloaded := newToldStore(path, func() time.Time { return now })
	got, ok := reloaded.Get("leo-a", "sid-1")
	if !ok || !sameToldDelegation(got, delegationOn) {
		t.Fatalf("reloaded sid-1 = %+v, %v; want the on policy", got, ok)
	}
	if _, ok := reloaded.Get("leo-a", "sid-3"); ok {
		t.Fatal("an unknown session was told")
	}
	if _, ok := reloaded.Get("leo-b", "sid-1"); ok {
		t.Fatal("another agent's session was told")
	}

	now = now.Add(toldMaxAge + time.Hour)
	if err := reloaded.Put("leo-b", "sid-9", toldFor(delegationOn, now)); err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Get("leo-a", "sid-1"); ok {
		t.Fatal("an entry past toldMaxAge survived a write")
	}
	if _, ok := reloaded.Get("leo-b", "sid-9"); !ok {
		t.Fatal("the fresh entry was pruned")
	}
}

func TestToldStoreSurvivesACorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "told.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newToldStore(path, func() time.Time { return contextNow })

	if _, ok := store.Get("leo-a", "s"); ok {
		t.Fatal("a corrupt file told something")
	}
	if err := store.Put("leo-a", "s", toldFor(delegationOn, contextNow)); err != nil {
		t.Fatalf("Put over a corrupt file: %v", err)
	}
}
