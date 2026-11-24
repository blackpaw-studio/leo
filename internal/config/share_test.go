package config

import (
	"strings"
	"testing"
)

func shareErrors(cfg *Config) string {
	err := cfg.Validate()
	if err == nil {
		return ""
	}
	var out []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if strings.Contains(line, "share") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func TestShareReadableByValidation(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		wantErr string // "" means no share error
	}{
		{name: "each prefix", entries: []string{"agent:leoterm", "template:marketing", "task:weekly-digest"}},
		{name: "unprefixed", entries: []string{"marketing"}, wantErr: "readable_by[0]"},
		{name: "unknown prefix", entries: []string{"agent:a", "user:evan"}, wantErr: "readable_by[1]"},
		{name: "wildcard star", entries: []string{"template:*"}, wantErr: "wildcard"},
		{name: "wildcard glob", entries: []string{"agent:leo-?"}, wantErr: "wildcard"},
		{name: "wildcard class", entries: []string{"task:[ab]"}, wantErr: "wildcard"},
		{name: "empty name", entries: []string{"agent:"}, wantErr: "readable_by[0]"},
		{name: "remote agent", entries: []string{"agent:leo@other"}, wantErr: "remote"},
		{name: "duplicate", entries: []string{"agent:a", "agent:a"}, wantErr: "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{HomePath: t.TempDir(), Templates: map[string]TemplateConfig{
				"t": {Share: ShareConfig{ReadableBy: tt.entries}},
			}}
			got := shareErrors(cfg)
			if tt.wantErr == "" {
				if got != "" {
					t.Fatalf("unexpected share errors: %s", got)
				}
				return
			}
			if !strings.Contains(got, "templates.t.share.readable_by") || !strings.Contains(got, tt.wantErr) {
				t.Fatalf("share errors = %q, want one naming templates.t.share.readable_by and %q", got, tt.wantErr)
			}
		})
	}
}

func TestParseGrant(t *testing.T) {
	kind, name, err := ParseGrant("template:marketing")
	if err != nil || kind != GrantTemplate || name != "marketing" {
		t.Fatalf("ParseGrant = %q %q %v", kind, name, err)
	}
	if _, _, err := ParseGrant("marketing"); err == nil {
		t.Fatal("ParseGrant accepted an unprefixed entry")
	}
}

func TestTemplatesFileRoundTripsShare(t *testing.T) {
	const shared = "templates:\n  t:\n    model: opus\n    share:\n      # who may read\n      readable_by:\n        - template:marketing\n        - agent:leoterm\n"
	dir := t.TempDir()
	leoPath := writeFile(t, dir, "leo.yaml", "templates_file: shared.yaml\n")
	writeFile(t, dir, "shared.yaml", shared)
	cfg, err := Load(leoPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Templates["t"].Share.ReadableBy; len(got) != 2 || got[0] != "template:marketing" || got[1] != "agent:leoterm" {
		t.Fatalf("shared template share = %+v", got)
	}
	out := editSharedAndRead(t, shared, setModel("t", "sonnet"))
	// An edit elsewhere in the entry keeps the share block and its comment.
	if !strings.Contains(out, "model: sonnet\n    share:\n      # who may read\n      readable_by:\n        - template:marketing\n        - agent:leoterm\n") {
		t.Fatalf("share block not kept:\n%s", out)
	}
}

func TestSharedTemplateWithoutShareWritesNoShareKey(t *testing.T) {
	const shared = "templates:\n  t:\n    model: opus\n"
	out := editSharedAndRead(t, shared, setModel("t", "sonnet"))
	if strings.Contains(out, "share") {
		t.Fatalf("zero Share wrote a share key:\n%s", out)
	}
}
