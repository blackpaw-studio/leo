package consult

import (
	"fmt"

	"github.com/blackpaw-studio/leo/internal/config"
)

// ReadOnlyChildRole is the one role a dispatch inside a read-only tree may
// start: explore is itself read-only, so the tree can never reach write
// access however deep it nests.
const ReadOnlyChildRole = "explore"

// PolicyRefusedCode starts the message of every refusal that is policy, not an
// outage: callers (the leo-bridge mod) match it to avoid treating a refusal as
// "leo_dispatch is unavailable" and falling back to native agents.
const PolicyRefusedCode = "policy_refused"

// Record.TreeAccess values.
const (
	TreeReadOnly = "read_only"
	TreeWritable = "writable"
)

// treeVerdict is what a dispatch's ancestry says about write access.
type treeVerdict struct {
	restricted bool
	role       string // the read-only role that restricts it
	via        string // the dispatch whose record says so
}

// treeOf decides whether the dispatch id sits in a read-only tree. The role
// comes from the daemon's own records, never from the caller. A run started by
// this code carries TreeAccess, so one record answers; only a record from
// before the guard is walked up its parent chain, and a gap in such a chain
// (an ancestor pruned long ago) is not held against it, since a caller cannot
// forge a legacy record. An id that names no dispatch at all is an error: the
// caller claimed a parent the daemon cannot verify, which must not read as a
// top-level caller.
func (d *Dispatcher) treeOf(id string) (treeVerdict, error) {
	seen := map[string]bool{}
	for i := range MaxDispatchDepth + 2 {
		if id == "" || seen[id] {
			return treeVerdict{}, nil
		}
		seen[id] = true
		rec, ok := d.dispatchRecord(id)
		if !ok {
			if i == 0 {
				return treeVerdict{}, invalidf("%s: parent dispatch %q cannot be verified (unknown, or not a dispatch); refusing rather than treating the caller as top-level", PolicyRefusedCode, id)
			}
			return treeVerdict{}, nil
		}
		switch rec.TreeAccess {
		case TreeReadOnly:
			return treeVerdict{restricted: true, role: rec.ReadOnlyRole, via: rec.ID}, nil
		case TreeWritable:
			return treeVerdict{}, nil
		}
		if config.IsReadOnlyRole(rec.Role) {
			return treeVerdict{restricted: true, role: rec.Role, via: rec.ID}, nil
		}
		id = rec.ParentDispatchID
	}
	return treeVerdict{}, nil
}

// dispatchRecord is the record of a dispatch (not a consult) by id.
func (d *Dispatcher) dispatchRecord(id string) (Record, bool) {
	if !ValidDispatchID(id) {
		return Record{}, false
	}
	rec, _, err := d.lookup(id)
	if err != nil || rec.Kind != "dispatch" {
		return Record{}, false
	}
	return rec, true
}

// treeAccessFor is the TreeAccess and ReadOnlyRole a new run records: read-only
// when its own role is, or when its parent's tree is.
func treeAccessFor(role string, parent treeVerdict) (access, readOnlyRole string) {
	switch {
	case config.IsReadOnlyRole(role):
		return TreeReadOnly, role
	case parent.restricted:
		return TreeReadOnly, parent.role
	}
	return TreeWritable, ""
}

// checkReadOnlyParent refuses a start under parentID that could hand a
// read-only tree write access: inside one, only a role-routed explore
// dispatch is admitted, and a consult only of a read-only role's template
// (consults run a template with no role, and the harnesses do not enforce
// their advisory preamble). Top-level starts and starts under a
// write-capable parent are unaffected.
func (d *Dispatcher) checkReadOnlyParent(cfg *config.Config, parent treeVerdict, req Request) error {
	if !parent.restricted {
		return nil
	}
	if requestKind(req) == "consult" {
		if isReadOnlyRoleTemplate(cfg, req.Template) {
			return nil
		}
		return invalidf("%s: dispatch %s runs as read-only role %q and may only consult a read-only role's template (the %q role's, for one); template %q is not one", PolicyRefusedCode, parent.via, parent.role, ReadOnlyChildRole, req.Template)
	}
	if req.Role == ReadOnlyChildRole {
		// The role must route to its own template: a caller cannot pair the
		// explore role with another template.
		if res, err := cfg.ResolveRole(ReadOnlyChildRole); err == nil && res.Template == req.Template {
			return nil
		}
	}
	return invalidf("%s: dispatch %s runs as read-only role %q and may only dispatch role %q; refusing %s", PolicyRefusedCode, parent.via, parent.role, ReadOnlyChildRole, describeRequestedWork(req))
}

// CheckSendAllowed refuses a follow-up (message or permission decision) from
// the dispatch senderID to targetID when senderID sits in a read-only tree and
// the target is not read-only: driving an existing implement dispatch is as
// good as starting one. An empty sender is a top-level caller; a nonempty one
// that names no dispatch is refused.
func (d *Dispatcher) CheckSendAllowed(senderID, targetID string) error {
	sender, err := d.treeOf(senderID)
	if err != nil || !sender.restricted {
		return err
	}
	target, _, err := d.lookup(targetID)
	if err != nil {
		return nil // an unknown target is reported by the send itself
	}
	if config.IsReadOnlyRole(target.Role) {
		return nil
	}
	return invalidf("%s: dispatch %s runs as read-only role %q and may only message read-only dispatches; %s has role %q", PolicyRefusedCode, sender.via, sender.role, target.ID, target.Role)
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
