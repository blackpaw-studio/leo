package consult

import "github.com/blackpaw-studio/leo/internal/config"

// Sources of a dispatch's inherited environment layer (Record.EnvironmentsSource).
const (
	// EnvironmentsOverride: the dispatch named its own list.
	EnvironmentsOverride = "override"
	// EnvironmentsInherited: the parent dispatch's or calling agent's list.
	EnvironmentsInherited = "inherited"
	// EnvironmentsTemplate: nothing inherited; the template's list applies.
	EnvironmentsTemplate = "template"
)

// dispatchEnvironments picks a dispatch's inherited environment layer: an
// explicit override first, else the parent dispatch's effective list, else
// the calling agent's. An override is validated as caller input; inherited
// names are checked when the environment is resolved.
func dispatchEnvironments(cfg *config.Config, req Request, parent *Record) ([]string, string, error) {
	switch {
	case req.Environments != nil:
		if err := cfg.ValidateEnvironmentNames(req.Environments); err != nil {
			return nil, "", invalidf("environments: %v", err)
		}
		return append([]string{}, req.Environments...), EnvironmentsOverride, nil
	case parent != nil && len(parent.Environments) > 0:
		return append([]string{}, parent.Environments...), EnvironmentsInherited, nil
	case parent == nil && len(req.CallerEnvironments) > 0:
		return append([]string{}, req.CallerEnvironments...), EnvironmentsInherited, nil
	default:
		return nil, EnvironmentsTemplate, nil
	}
}

// parentRecord returns the record of the dispatch named by id, if it is one.
func (d *Dispatcher) parentRecord(id string) *Record {
	if !ValidDispatchID(id) {
		return nil
	}
	rec, _, err := d.lookup(id)
	if err != nil || rec.Kind != "dispatch" {
		return nil
	}
	return &rec
}
