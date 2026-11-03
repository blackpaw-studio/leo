package observe

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestProgressValidateID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		ok   bool
	}{
		{"simple", "build", true},
		{"allowed punctuation", "a.b_c-1", true},
		{"max length", strings.Repeat("a", 64), true},
		{"empty", "", false},
		{"too long", strings.Repeat("a", 65), false},
		{"uppercase", "Build", false},
		{"space", "my build", false},
		{"slash", "a/b", false},
		{"unicode", "büild", false},
		{"newline", "a\nb", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := TaskUpdate{ID: tc.id}.Normalize()
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrTaskInvalid) {
				t.Fatalf("want ErrTaskInvalid, got %v", err)
			}
		})
	}
}

func TestProgressValidateStrings(t *testing.T) {
	t.Run("trims and strips control and format characters", func(t *testing.T) {
		got, err := TaskUpdate{
			ID:     "a",
			Title:  ptr("  Xcode\x1b[31m build\n\t\u202e "),
			Status: ptr(" comp\u200biling\x00 "),
		}.Normalize()
		if err != nil {
			t.Fatal(err)
		}
		if *got.Title != "Xcode[31m build" {
			t.Fatalf("title = %q", *got.Title)
		}
		if *got.Status != "compiling" {
			t.Fatalf("status = %q", *got.Status)
		}
	})
	t.Run("title at limit is accepted, counted in runes", func(t *testing.T) {
		if _, err := (TaskUpdate{ID: "a", Title: ptr(strings.Repeat("é", 80))}).Normalize(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("title over limit is rejected", func(t *testing.T) {
		_, err := TaskUpdate{ID: "a", Title: ptr(strings.Repeat("a", 81))}.Normalize()
		if !errors.Is(err, ErrTaskInvalid) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("title empty after cleaning is rejected", func(t *testing.T) {
		_, err := TaskUpdate{ID: "a", Title: ptr(" \n\t ")}.Normalize()
		if !errors.Is(err, ErrTaskInvalid) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("status over limit is rejected", func(t *testing.T) {
		_, err := TaskUpdate{ID: "a", Status: ptr(strings.Repeat("a", 121))}.Normalize()
		if !errors.Is(err, ErrTaskInvalid) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty status is allowed and clears", func(t *testing.T) {
		got, err := TaskUpdate{ID: "a", Status: ptr("  ")}.Normalize()
		if err != nil || *got.Status != "" {
			t.Fatalf("got %v, %v", got.Status, err)
		}
	})
}

func TestProgressValidatePercent(t *testing.T) {
	t.Run("clamps into 0-100", func(t *testing.T) {
		for in, want := range map[float64]float64{-5: 0, 0: 0, 62.5: 62.5, 100: 100, 250: 100} {
			got, err := TaskUpdate{ID: "a", Percent: ptr(in)}.Normalize()
			if err != nil || *got.Percent != want {
				t.Fatalf("percent %v -> %v, %v; want %v", in, got.Percent, err, want)
			}
		}
	})
	t.Run("NaN and Inf are rejected", func(t *testing.T) {
		for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
			_, err := TaskUpdate{ID: "a", Percent: ptr(v)}.Normalize()
			if !errors.Is(err, ErrTaskInvalid) {
				t.Fatalf("percent %v: got %v", v, err)
			}
		}
	})
	t.Run("percent with indeterminate true is rejected", func(t *testing.T) {
		_, err := TaskUpdate{ID: "a", Percent: ptr(10.0), Indeterminate: ptr(true)}.Normalize()
		if !errors.Is(err, ErrTaskInvalid) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("percent with indeterminate false is fine", func(t *testing.T) {
		if _, err := (TaskUpdate{ID: "a", Percent: ptr(10.0), Indeterminate: ptr(false)}).Normalize(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestProgressValidateStateAndETA(t *testing.T) {
	for _, s := range []TaskState{"", TaskRunning, TaskSucceeded, TaskFailed, TaskCancelled} {
		if _, err := (TaskUpdate{ID: "a", State: s}).Normalize(); err != nil {
			t.Fatalf("state %q: %v", s, err)
		}
	}
	if _, err := (TaskUpdate{ID: "a", State: "paused"}).Normalize(); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("unknown state: got %v", err)
	}
	for _, v := range []float64{-1, math.NaN(), math.Inf(1), MaxTaskETA.Seconds() + 1} {
		if _, err := (TaskUpdate{ID: "a", ETASeconds: ptr(v)}).Normalize(); !errors.Is(err, ErrTaskInvalid) {
			t.Fatalf("eta %v: got %v", v, err)
		}
	}
	if _, err := (TaskUpdate{ID: "a", ETASeconds: ptr(90.0)}).Normalize(); err != nil {
		t.Fatal(err)
	}
}

func TestProgressValidateDoesNotMutateInput(t *testing.T) {
	title := "  hi  "
	in := TaskUpdate{ID: "a", Title: &title}
	if _, err := in.Normalize(); err != nil {
		t.Fatal(err)
	}
	if title != "  hi  " {
		t.Fatalf("input mutated: %q", title)
	}
}
