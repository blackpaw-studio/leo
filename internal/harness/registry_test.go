package harness

import (
	"reflect"
	"testing"
)

type fakeHarness struct{ name string }

func (f fakeHarness) Name() string                      { return f.name }
func (f fakeHarness) Binary() string                    { return f.name }
func (f fakeHarness) Args(LaunchSpec) ([]string, error) { return nil, nil }
func (f fakeHarness) SessionArgs(SessionState) []string { return nil }

func TestRegistryGetAndNames(t *testing.T) {
	reset := snapshotRegistry(t)
	defer reset()

	Register(fakeHarness{name: "zeta"})
	Register(fakeHarness{name: "alpha"})

	h, err := Get("alpha")
	if err != nil {
		t.Fatalf("Get(alpha): %v", err)
	}
	if h.Name() != "alpha" {
		t.Fatalf("Get(alpha).Name() = %q", h.Name())
	}

	if _, err := Get("missing"); err == nil {
		t.Fatal("Get(missing): expected error, got nil")
	}

	// Names() is sorted regardless of registration order.
	got := Names()
	want := []string{"alpha", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	reset := snapshotRegistry(t)
	defer reset()

	Register(fakeHarness{name: "dup"})
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Register did not panic")
		}
	}()
	Register(fakeHarness{name: "dup"})
}

func TestFallbackHelpers(t *testing.T) {
	if got := FallbackString("a", "b"); got != "a" {
		t.Fatalf("FallbackString(a,b) = %q", got)
	}
	if got := FallbackString("", "b"); got != "b" {
		t.Fatalf("FallbackString('',b) = %q", got)
	}
	if got := FallbackSlice([]string{"x"}, []string{"y"}); !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("FallbackSlice non-empty primary = %v", got)
	}
	if got := FallbackSlice(nil, []string{"y"}); !reflect.DeepEqual(got, []string{"y"}) {
		t.Fatalf("FallbackSlice empty primary = %v", got)
	}
}

// snapshotRegistry empties the package registry for a test and returns a
// restore func. Registration happens in adapter init()s in real binaries;
// tests need a clean slate.
func snapshotRegistry(t *testing.T) func() {
	t.Helper()
	saved := registry
	registry = map[string]Harness{}
	return func() { registry = saved }
}
