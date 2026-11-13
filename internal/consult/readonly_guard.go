package consult

import (
	"fmt"

	"github.com/blackpaw-studio/leo/internal/config"
)

// ReadOnlyChildRole is the one role a dispatch inside a read-only tree may
// start: explore is itself read-only, so the tree can never reach write
// access however deep it nests.
const ReadOnlyChildRole = "explore"

// readOnlyAncestor returns the nearest dispatch at or above id whose role is
// one of the stable read-only roles (config.IsReadOnlyRole). The walk covers
// the whole recorded ancestry, not just the immediate parent, so a role-less
// or template-routed intermediate run cannot launder the restriction. The role
// is read from the daemon's own record of that dispatch, never from anything
// the child supplied.
func (d *Dispatcher) readOnlyAncestor(id string) (Record, bool) {
	seen := map[string]bool{}
	for range MaxDispatchDepth + 2 {
		if id == "" || seen[id] || !ValidDispatchID(id) {
			return Record{}, false
		}
		seen[id] = true
		rec, _, err := d.lookup(id)
		if err != nil || rec.Kind != "dispatch" {
			return Record{}, false
		}
		if config.IsReadOnlyRole(rec.Role) {
			return rec, true
		}
		id = rec.ParentDispatchID
	}
	return Record{}, false
}

// checkReadOnlyParent refuses a start under parentID that could hand a
// read-only tree write access: inside one, only a role-routed explore
// dispatch is admitted, and a consult only of a read-only role's template
// (consults run a template with no role, and the harnesses do not enforce
// their advisory preamble). Top-level starts and starts under a
// write-capable parent are unaffected.
func (d *Dispatcher) checkReadOnlyParent(cfg *config.Config, parentID string, req Request) error {
	parent, restricted := d.readOnlyAncestor(parentID)
	if !restricted {
		return nil
	}
	if requestKind(req) == "consult" {
		if isReadOnlyRoleTemplate(cfg, req.Template) {
			return nil
		}
		return invalidf("dispatch %s runs as read-only role %q and may only consult a read-only role's template (the %q role's, for one); template %q is not one", parent.ID, parent.Role, ReadOnlyChildRole, req.Template)
	}
	if req.Role == ReadOnlyChildRole {
		// The role must route to its own template: a caller cannot pair the
		// explore role with another template.
		if res, err := cfg.ResolveRole(ReadOnlyChildRole); err == nil && res.Template == req.Template {
			return nil
		}
	}
	return invalidf("dispatch %s runs as read-only role %q and may only dispatch role %q; refusing %s", parent.ID, parent.Role, ReadOnlyChildRole, describeRequestedWork(req))
}

// CheckSendAllowed refuses a follow-up (message or permission decision) from
// the dispatch senderID to targetID when senderID sits in a read-only tree and
// the target is not read-only: driving an existing implement dispatch is as
// good as starting one. An empty or unknown sender is a top-level caller.
func (d *Dispatcher) CheckSendAllowed(senderID, targetID string) error {
	sender, restricted := d.readOnlyAncestor(senderID)
	if !restricted {
		return nil
	}
	target, _, err := d.lookup(targetID)
	if err != nil {
		return nil // an unknown target is reported by the send itself
	}
	if config.IsReadOnlyRole(target.Role) {
		return nil
	}
	return invalidf("dispatch %s runs as read-only role %q and may only message read-only dispatches; %s has role %q", sender.ID, sender.Role, target.ID, target.Role)
}

// isReadOnlyRoleTemplate reports whether template is what some read-only role
// resolves to under the active delegation profile.
func isReadOnlyRoleTemplate(cfg *config.Config, template string) bool {
	if cfg == nil || cfg.Delegation == nil {
		return false
	}
	for role := range cfg.Delegation.Profiles[cfg.Delegation.ActiveProfile].Roles {
		if !config.IsReadOnlyRole(role) {
			continue
		}
		if res, err := cfg.ResolveRole(role); err == nil && res.Template == template {
			return true
		}
	}
	return false
}

func describeRequestedWork(req Request) string {
	if req.Role != "" {
		return fmt.Sprintf("role %q on template %q", req.Role, req.Template)
	}
	return fmt.Sprintf("a template dispatch (%q)", req.Template)
}
