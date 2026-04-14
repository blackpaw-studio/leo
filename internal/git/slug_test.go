package git

import (
	"errors"
	"strings"
	"testing"
)

func TestSlugifyBranch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{"simple", "main", "main", nil},
		{"feature-slash", "feat/foo", "feat-foo", nil},
		{"mixed-case", "Feat/Foo", "feat-foo", nil},
		{"space", "feat/foo bar", "feat-foo-bar", nil},
		{"multiple-disallowed", "feat/foo@bar!baz", "feat-foo-bar-baz", nil},
		{"collapse-double-slash", "feat//foo", "feat-foo", nil},
		{"trim-edges", "-feat/foo-", "feat-foo", nil},
		{"trim-trailing-dot", "feat.", "feat", nil},
		{"preserve-internal-dot", "v1.2_rc", "v1.2_rc", nil},
		{"empty", "", "", ErrEmptySlug},
		{"only-disallowed", "///", "", ErrEmptySlug},
		{"only-dots-dashes", "-.-", "", ErrEmptySlug},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := SlugifyBranch(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v (slug %q)", tc.wantErr, err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("slug mismatch: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestBoundedSlug(t *testing.T) {
	t.Parallel()

	t.Run("under-limit-passthrough", func(t *testing.T) {
		t.Parallel()
		got := BoundedSlug("short", 40)
		if got != "short" {
			t.Fatalf("expected unchanged, got %q", got)
		}
	})

	t.Run("exact-limit-passthrough", func(t *testing.T) {
		t.Parallel()
		s := strings.Repeat("a", 40)
		got := BoundedSlug(s, 40)
		if got != s {
			t.Fatalf("expected unchanged, got %q", got)
		}
	})

	t.Run("truncated-has-correct-length", func(t *testing.T) {
		t.Parallel()
		got := BoundedSlug(strings.Repeat("a", 100), 40)
		if len(got) != 40 {
			t.Fatalf("expected length 40, got %d (%q)", len(got), got)
		}
	})

	t.Run("distinct-inputs-distinct-outputs", func(t *testing.T) {
		t.Parallel()
		a := BoundedSlug(strings.Repeat("a", 100), 40)
		b := BoundedSlug(strings.Repeat("b", 100), 40)
		if a == b {
			t.Fatalf("distinct inputs produced identical bounded slugs: %q", a)
		}
	})

	t.Run("zero-max-passthrough", func(t *testing.T) {
		t.Parallel()
		got := BoundedSlug("anything", 0)
		if got != "anything" {
			t.Fatalf("expected passthrough on zero maxLen, got %q", got)
		}
	})

	t.Run("small-max-returns-hash-prefix", func(t *testing.T) {
		t.Parallel()
		got := BoundedSlug("some-long-branch-name", 4)
		if len(got) != 4 {
			t.Fatalf("expected length 4, got %d (%q)", len(got), got)
		}
	})

	t.Run("truncation-trims-trailing-dash", func(t *testing.T) {
		t.Parallel()
		// A slug that would be cut mid-dash; trailing dash on the head
		// should be trimmed before the hash joins.
		got := BoundedSlug("aa-bb-cc-dd-ee-ff-gg-hh-ii-jj-kk-ll", 15)
		if strings.Contains(got, "--") {
			t.Fatalf("should not contain double dash: %q", got)
		}
	})
}
