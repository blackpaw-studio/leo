package mcp

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

func callSkill(t *testing.T, perms leotools.Permissions, args map[string]any) string {
	t.Helper()
	reg := newRegistry(newDaemonClient("0", ""), "test-process", perms)
	out, err := callTool(reg, leotools.SkillTool, args)
	if err != nil {
		t.Fatalf("leo_skill: %v", err)
	}
	return out
}

// An unrestricted agent must see byte-identical skill content — this feature
// adds nothing to the common case.
func TestSkillUnrestrictedIsUnchanged(t *testing.T) {
	plain := callSkill(t, leotools.Permissions{}, map[string]any{"name": "agent-management"})
	if strings.Contains(plain, "Permissions notice") {
		t.Errorf("unrestricted agents must not get a notice:\n%s", plain[:200])
	}

	catalog := callSkill(t, leotools.Permissions{}, nil)
	if strings.Contains(catalog, "Permissions notice") {
		t.Errorf("unrestricted catalog must not get a notice:\n%s", catalog)
	}
}

// The skill documents the CLI *and* the HTTP API for spawning and stopping.
// A restricted agent that reads it will otherwise try one of them, burn a
// turn, and get refused — so the notice has to lead the content.
func TestSkillNoticeLeadsRestrictedContent(t *testing.T) {
	perms := leotools.Permissions{DenyTools: []string{"leo_spawn_agent", "leo_stop_agent"}}
	out := callSkill(t, perms, map[string]any{"name": "agent-management"})

	if !strings.HasPrefix(out, ">") {
		t.Errorf("notice must lead the skill body, got:\n%s", out[:200])
	}
	for _, want := range []string{"leo_spawn_agent", "leo_stop_agent"} {
		if !strings.Contains(out, want) {
			t.Errorf("notice should name the denied tool %q", want)
		}
	}
	// The skill body itself must still be there — this informs, it doesn't
	// mangle the docs into incoherence.
	if !strings.Contains(out, "# Agent Management") {
		t.Error("skill content must survive the notice")
	}
}

// The CLI and HTTP API are separate routes to the same capability. The CLI is
// gated; the HTTP API is not. The notice must not claim otherwise — it tells
// the agent the capability is withheld, which is true of every route.
func TestSkillNoticeCoversUngatedRoutes(t *testing.T) {
	perms := leotools.Permissions{DenyTools: []string{"leo_spawn_agent"}}
	out := callSkill(t, perms, map[string]any{"name": "agent-management"})

	for _, want := range []string{"CLI", "HTTP"} {
		if !strings.Contains(out, want) {
			t.Errorf("notice should address the %s route: %s", want, out[:400])
		}
	}
}

// Allowlists narrow an argument rather than removing a tool, and the CLI
// enforces can_spawn too, so they belong in the notice as well.
func TestSkillNoticeReportsAllowlists(t *testing.T) {
	perms := leotools.Permissions{
		CanSpawn:   []string{"codex"},
		CanMessage: []string{"rocket", "scout-*"},
		CanConsult: []string{"fable"},
	}
	out := callSkill(t, perms, map[string]any{"name": "agent-management"})

	for _, want := range []string{"codex", "rocket", "scout-*", "fable"} {
		if !strings.Contains(out, want) {
			t.Errorf("notice should report allowlist entry %q:\n%s", want, out[:500])
		}
	}
}

// The catalog is what an agent reads first, so it carries the notice too.
func TestSkillCatalogCarriesNotice(t *testing.T) {
	perms := leotools.Permissions{DenyTools: []string{"leo_spawn_agent"}}
	out := callSkill(t, perms, nil)

	if !strings.Contains(out, "leo_spawn_agent") {
		t.Errorf("catalog should carry the notice:\n%s", out)
	}
	if !strings.Contains(out, "Available Leo skills") {
		t.Error("catalog listing must survive the notice")
	}
}

// An unknown skill name still errors; the notice must not swallow that.
func TestSkillUnknownNameStillErrors(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "test-process",
		leotools.Permissions{DenyTools: []string{"leo_spawn_agent"}})
	if _, err := callTool(reg, leotools.SkillTool, map[string]any{"name": "nope"}); err == nil {
		t.Fatal("expected an error for an unknown skill")
	}
}
