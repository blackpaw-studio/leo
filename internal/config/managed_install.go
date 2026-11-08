package config

import "github.com/blackpaw-studio/leo/internal/managed"

// managedProbe reports whether the install whose state dir is stateDir has a
// valid managed marker (state/managed.json). Load consults it once per load,
// so getters stay pure. Tests replace it with SetManagedProbe.
var managedProbe = func(stateDir string) bool {
	return managed.Load(stateDir, managed.DefaultDeps()).Managed
}

// SetManagedProbe replaces the managed-install probe and returns a func
// restoring the previous one.
func SetManagedProbe(p func(stateDir string) bool) (restore func()) {
	prev := managedProbe
	managedProbe = p
	return func() { managedProbe = prev }
}
