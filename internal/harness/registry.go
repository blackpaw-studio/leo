package harness

import (
	"fmt"
	"sort"
)

var registry = map[string]Harness{}

// Register adds an adapter under its Name. Adapter packages call this from
// init(). Duplicate registration is a programmer error, so it panics.
func Register(h Harness) {
	if _, dup := registry[h.Name()]; dup {
		panic(fmt.Sprintf("harness: duplicate registration for %q", h.Name()))
	}
	registry[h.Name()] = h
}

// Get returns the adapter registered under name.
func Get(name string) (Harness, error) {
	h, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown harness %q (registered: %v)", name, Names())
	}
	return h, nil
}

// Names returns the registered harness names, sorted.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
