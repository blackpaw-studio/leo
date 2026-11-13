package consult

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// guardConfig routes every stable role to a template. The profile deliberately
// maps the read-only roles to templates whose names say nothing about being
// read-only: the guard must decide from the role, never from the template.
func guardConfig() *config.Config {
	cfg := testConfig()
	cfg.Templates["scout"] = config.TemplateConfig{Harness: "claude", Model: "opus"}
	cfg.Templates["builder"] = config.TemplateConfig{Harness: "claude", Model: "opus"}
	cfg.Delegation = &config.DelegationConfig{
		ActiveProfile: "p",
		Profiles: map[string]config.Profile{"p": {Roles: map[string]config.RoleTarget{
			"explore":         {Template: "scout"},
			"plan":            {Template: "claude"},
			"review":          {Template: "claude"},
			"review.security": {Template: "claude"},
			"implement":       {Template: "builder"},
			"implement.hard":  {Template: "builder"},
		}}},
	}
	return cfg
}

// startRole starts a dispatch under parentID as the given role, resolving the
// role's template the way the web handler does.
func startRole(t *testing.T, d *Dispatcher, role, parentID string) (Record, error) {
	t.Helper()
	cfg := guardConfig()
	res, err := cfg.ResolveRole(role)
	if err != nil {
		t.Fatalf("ResolveRole(%s): %v", role, err)
	}
	started, err := d.Start(context.Background(), cfg, Request{
		Template: res.Template, Role: role, Prompt: "q", Kind: "dispatch",
		Cwd: t.TempDir(), ParentDispatchID: parentID,
	})
	if err != nil {
		return Record{}, err
	}
	rec, err := d.Get(started.ID)
	if err != nil {
		t.Fatalf("Get(%s): %v", started.ID, err)
	}
	return rec, nil
}

func mustStartRole(t *testing.T, d *Dispatcher, role, parentID string) Record {
	t.Helper()
	rec, err := startRole(t, d, role, parentID)
	if err != nil {
		t.Fatalf("start %s under %q: %v", role, parentID, err)
	}
	return rec
}

func TestReadOnlyParentCannotDispatchWriteCapableRoles(t *testing.T) {
	for _, parent := range []string{"plan", "review", "review.security", "explore"} {
		for _, child := range []string{"implement", "implement.hard", "plan", "review", "review.security"} {
			t.Run(parent+"->"+child, func(t *testing.T) {
				d := newNestedDispatcher(t)
				root := mustStartRole(t, d, parent, "")
				_, err := startRole(t, d, child, root.ID)
				if err == nil {
					t.Fatalf("%s dispatched %s; a read-only run must not gain write access through a child", parent, child)
				}
				var verr *ValidationError
				if !errors.As(err, &verr) {
					t.Fatalf("err = %T %v, want a ValidationError", err, err)
				}
				for _, want := range []string{parent, "explore"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("err %q should name %q", err, want)
					}
				}
			})
		}
	}
}

func TestReadOnlyParentMayDispatchExplore(t *testing.T) {
	for _, parent := range []string{"plan", "review", "review.security", "explore"} {
		t.Run(parent, func(t *testing.T) {
			d := newNestedDispatcher(t)
			root := mustStartRole(t, d, parent, "")
			child := mustStartRole(t, d, "explore", root.ID)
			if child.ParentDispatchID != root.ID {
				t.Fatalf("parent = %q, want %q", child.ParentDispatchID, root.ID)
			}
		})
	}
}

func TestReadOnlyParentCannotUseTemplateEscapeHatch(t *testing.T) {
	d := newNestedDispatcher(t)
	root := mustStartRole(t, d, "plan", "")
	cases := map[string]Request{
		"template, no role":            {Template: "builder"},
		"explore role, other template": {Template: "builder", Role: "explore"},
		"unmapped role":                {Template: "builder", Role: "janitor"},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			req.Prompt, req.Kind, req.Cwd, req.ParentDispatchID = "q", "dispatch", t.TempDir(), root.ID
			if _, err := d.Start(context.Background(), guardConfig(), req); err == nil {
				t.Fatal("a read-only parent started a template-routed dispatch")
			}
		})
	}
}

func TestReadOnlyParentConsultsOnlyReadOnlyRoleTemplates(t *testing.T) {
	d := newNestedDispatcher(t)
	root := mustStartRole(t, d, "plan", "")
	consult := func(template string) error {
		_, err := d.Start(context.Background(), guardConfig(), Request{Template: template, Prompt: "q", Kind: "consult", Cwd: t.TempDir(), ParentDispatchID: root.ID})
		return err
	}
	if err := consult("builder"); err == nil {
		t.Fatal("a read-only parent consulted the write role's template")
	}
	if err := consult("scout"); err != nil {
		t.Fatalf("consult of a read-only role's template: %v", err)
	}
}

func TestReadOnlyRestrictionFollowsAncestry(t *testing.T) {
	d := newNestedDispatcher(t)
	root := mustStartRole(t, d, "plan", "")
	// A role-less, template-routed run whose recorded parent is a read-only
	// dispatch (e.g. a record from before the guard existed) is still inside
	// the read-only tree.
	legacy := Record{ID: "d-aaaaaaaaaaaa", Kind: "dispatch", Template: "builder", ParentDispatchID: root.ID}
	d.mu.Lock()
	d.runs[legacy.ID] = &runState{record: legacy}
	d.mu.Unlock()
	if _, err := startRole(t, d, "implement", legacy.ID); err == nil {
		t.Fatal("restriction was laundered through a role-less intermediate dispatch")
	}
}

func TestImplementParentStillDispatchesWithinCaps(t *testing.T) {
	d := newNestedDispatcher(t)
	root := mustStartRole(t, d, "implement", "")
	for _, child := range []string{"implement", "implement.hard", "plan", "review", "explore"} {
		if _, err := startRole(t, d, child, root.ID); err != nil {
			t.Fatalf("implement -> %s: %v", child, err)
		}
	}
}

func TestTopLevelCallerIsUnrestricted(t *testing.T) {
	d := newNestedDispatcher(t)
	mustStartRole(t, d, "implement", "")
	mustStartRole(t, d, "plan", "")
}

func TestReadOnlySenderMayOnlyMessageReadOnlyDispatches(t *testing.T) {
	d := newNestedDispatcher(t)
	planner := mustStartRole(t, d, "plan", "")
	builder := mustStartRole(t, d, "implement", "")
	scout := mustStartRole(t, d, "explore", planner.ID)

	if err := d.CheckSendAllowed(planner.ID, builder.ID); err == nil {
		t.Fatal("a read-only dispatch was allowed to drive an implement dispatch")
	}
	if err := d.CheckSendAllowed(planner.ID, scout.ID); err != nil {
		t.Fatalf("read-only sender -> read-only target: %v", err)
	}
	if err := d.CheckSendAllowed(builder.ID, scout.ID); err != nil {
		t.Fatalf("write-capable sender: %v", err)
	}
	if err := d.CheckSendAllowed("", builder.ID); err != nil {
		t.Fatalf("top-level sender: %v", err)
	}
}

func TestRefusalsCarryTheStablePolicyCode(t *testing.T) {
	d := newNestedDispatcher(t)
	root := mustStartRole(t, d, "plan", "")
	_, err := startRole(t, d, "implement", root.ID)
	if err == nil || !strings.HasPrefix(err.Error(), PolicyRefusedCode+":") {
		t.Fatalf("err = %v, want it to start with %q so callers can tell a policy refusal from an outage", err, PolicyRefusedCode+":")
	}
	_, err = startRole(t, d, "implement", "d-000000000000")
	if err == nil || !strings.HasPrefix(err.Error(), PolicyRefusedCode+":") {
		t.Fatalf("unverifiable parent err = %v, want the policy code too", err)
	}
}

func TestUnverifiableParentIsRefusedNotTreatedAsTopLevel(t *testing.T) {
	d := newNestedDispatcher(t)
	for _, id := range []string{"d-000000000000", "../x", "a/b"} {
		if _, err := startRole(t, d, "implement", id); err == nil {
			t.Fatalf("parent %q that cannot be resolved was treated as a top-level caller", id)
		}
	}
	mustStartRole(t, d, "implement", "")
}

func TestUnverifiableSenderIsRefused(t *testing.T) {
	d := newNestedDispatcher(t)
	builder := mustStartRole(t, d, "implement", "")
	if err := d.CheckSendAllowed("d-000000000000", builder.ID); err == nil {
		t.Fatal("a sender dispatch id that cannot be resolved was allowed")
	}
}

// forget drops a run's record, as pruning does once enough newer runs end.
func forget(d *Dispatcher, id string) {
	d.mu.Lock()
	delete(d.runs, id)
	d.mu.Unlock()
}

func TestReadOnlyRestrictionSurvivesAPrunedAncestor(t *testing.T) {
	d := newNestedDispatcher(t)
	root := mustStartRole(t, d, "plan", "")
	scout := mustStartRole(t, d, "explore", root.ID)
	forget(d, root.ID)
	if _, err := startRole(t, d, "implement", scout.ID); err == nil || !strings.Contains(err.Error(), "explore") {
		t.Fatalf("implement under a read-only tree whose root was pruned: err = %v", err)
	}
	mustStartRole(t, d, "explore", scout.ID)
}

func TestWritableTreeSurvivesAPrunedAncestor(t *testing.T) {
	d := newNestedDispatcher(t)
	root := mustStartRole(t, d, "implement", "")
	child := mustStartRole(t, d, "implement", root.ID)
	forget(d, root.ID)
	mustStartRole(t, d, "implement", child.ID)
}

func TestRecordedTreeAccessFollowsTheRoleChain(t *testing.T) {
	d := newNestedDispatcher(t)
	planner := mustStartRole(t, d, "plan", "")
	scout := mustStartRole(t, d, "explore", planner.ID)
	builder := mustStartRole(t, d, "implement", "")
	for rec, want := range map[string]string{planner.ID: TreeReadOnly, scout.ID: TreeReadOnly, builder.ID: TreeWritable} {
		got, _ := d.Get(rec)
		if got.TreeAccess != want {
			t.Fatalf("%s TreeAccess = %q, want %q", rec, got.TreeAccess, want)
		}
	}
}
