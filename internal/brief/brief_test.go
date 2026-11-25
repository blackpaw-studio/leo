package brief

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC)

func TestNormalizeStoresATrimmedBriefStampedWithNow(t *testing.T) {
	got, err := Normalize(Input{
		Goal:      "  Ship the brief  ",
		Status:    "writing tests",
		Decisions: []string{"atomic writes"},
		Open:      []string{"codex probe"},
	}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if got.Goal != "Ship the brief" || got.Status != "writing tests" {
		t.Fatalf("got %+v", got)
	}
	if !got.UpdatedAt.Equal(fixedNow) {
		t.Fatalf("updated_at = %v", got.UpdatedAt)
	}
}

func TestNormalizeEmptyInputClears(t *testing.T) {
	for _, in := range []Input{{}, {Goal: "  ", Decisions: []string{"", "  "}}} {
		got, err := Normalize(in, fixedNow)
		if err != nil {
			t.Fatal(err)
		}
		if !got.IsZero() {
			t.Fatalf("want a cleared brief, got %+v", got)
		}
	}
}

func TestNormalizeRejectsOverLengthAndTooMany(t *testing.T) {
	long := strings.Repeat("x", MaxLineRunes+1)
	cases := map[string]Input{
		"goal":               {Goal: long},
		"status":             {Status: long},
		"decisions":          {Decisions: []string{long}},
		"open":               {Open: []string{long}},
		"too many decisions": {Decisions: []string{"1", "2", "3", "4", "5", "6"}},
		"too many open":      {Open: []string{"1", "2", "3", "4", "5", "6"}},
	}
	for name, in := range cases {
		if _, err := Normalize(in, fixedNow); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestNormalizeCountsRunesNotBytes(t *testing.T) {
	line := strings.Repeat("é", MaxLineRunes)
	if _, err := Normalize(Input{Goal: line}, fixedNow); err != nil {
		t.Fatalf("140 runes must fit: %v", err)
	}
}

func TestNormalizeStripsControlCharsAndFlattensNewlines(t *testing.T) {
	got, err := Normalize(Input{Goal: "a\nb\r\nc\td\x00e\x1b[31mf‮g"}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if got.Goal != "a b c de[31mfg" {
		t.Fatalf("goal = %q", got.Goal)
	}
}

func TestNormalizeDropsEmptyListItemsBeforeCountingThem(t *testing.T) {
	got, err := Normalize(Input{Decisions: []string{"a", "", "b", "  ", "c", "d", "e"}}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Decisions) != 5 {
		t.Fatalf("decisions = %v", got.Decisions)
	}
}

func TestClampedTruncatesAnOversizedBrief(t *testing.T) {
	b := Brief{Goal: strings.Repeat("é", 200), Open: []string{"1", "2", "3", "4", "5", "6", "7"}}
	c := b.Clamped()
	if n := len([]rune(c.Goal)); n != MaxLineRunes {
		t.Fatalf("goal runes = %d", n)
	}
	if len(c.Open) != MaxItems {
		t.Fatalf("open = %v", c.Open)
	}
	if len(b.Open) != 7 {
		t.Fatal("Clamped must not mutate its receiver")
	}
}

func TestTrackerNudgesEveryTenTurnsAndResetsOnWrite(t *testing.T) {
	tr := NewTracker()
	turn := func() { tr.TurnCompleted("a") }
	for i := 1; i <= 9; i++ {
		turn()
		if tr.Nudge("a") {
			t.Fatalf("nudged after %d turns", i)
		}
	}
	turn()
	if !tr.Nudge("a") {
		t.Fatal("want a nudge at turn 10")
	}
	for i := 11; i <= 19; i++ {
		turn()
		if tr.Nudge("a") {
			t.Fatalf("nudged again at turn %d", i)
		}
	}
	turn()
	if !tr.Nudge("a") {
		t.Fatal("want a nudge at turn 20")
	}
	for i := 0; i < 9; i++ {
		turn()
	}
	tr.Wrote("a")
	turn()
	if tr.Nudge("a") {
		t.Fatal("a write must restart the count")
	}
}

func TestTrackerNudgeWithoutNewTurnsIsStillOncePerWindow(t *testing.T) {
	tr := NewTracker()
	for i := 0; i < 10; i++ {
		tr.TurnCompleted("a")
	}
	if !tr.Nudge("a") || tr.Nudge("a") {
		t.Fatal("want exactly one nudge for ten turns")
	}
}

func TestTrackerKeepsAgentsApart(t *testing.T) {
	tr := NewTracker()
	for i := 0; i < 10; i++ {
		tr.TurnCompleted("a")
	}
	if tr.Nudge("b") {
		t.Fatal("b has had no turns")
	}
}
