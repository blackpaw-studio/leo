package harness

import (
	"io"
	"reflect"
	"testing"
)

type fakeHarness struct{ name string }

func (f fakeHarness) Name() string                              { return f.name }
func (f fakeHarness) Binary() string                            { return f.name }
func (f fakeHarness) Args(LaunchSpec) ([]string, error)         { return nil, nil }
func (f fakeHarness) SessionArgs(SessionState) []string         { return nil }
func (f fakeHarness) ValidateModel(string) error                { return nil }
func (f fakeHarness) DecodeOptions(map[string]any) (any, error) { return struct{}{}, nil }
func (f fakeHarness) OptionsSchema() []OptionField              { return nil }
func (f fakeHarness) SupportsChannels() bool                    { return false }
func (f fakeHarness) ParseEvents(io.Reader) (Result, error)     { return Result{}, nil }
func (f fakeHarness) Env(LaunchSpec) (map[string]string, error) { return nil, nil }
func (f fakeHarness) SupportsKind(Kind) bool                    { return true }
func (f fakeHarness) Driver() SessionDriver                     { return nil }

func TestRegistryGetAndNames(t *testing.T) {
	reset := snapshotRegistry(t)
	defer reset()

	Register(fakeHarness{name: "zeta"})
	Register(fakeHarness{name: "alpha"})

	tests := []struct {
		name    string
		get     string
		wantErr bool
		want    string
	}{
		{"known name", "alpha", false, "alpha"},
		{"unknown name", "missing", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := Get(tt.get)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Get(%q) err = %v, wantErr %v", tt.get, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if h.Name() != tt.want {
				t.Fatalf("Get(%q).Name() = %q, want %q", tt.get, h.Name(), tt.want)
			}
		})
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
	stringTests := []struct {
		name              string
		primary, fallback string
		want              string
	}{
		{"non-empty primary", "a", "b", "a"},
		{"empty primary", "", "b", "b"},
	}
	for _, tt := range stringTests {
		t.Run("FallbackString/"+tt.name, func(t *testing.T) {
			if got := FallbackString(tt.primary, tt.fallback); got != tt.want {
				t.Fatalf("FallbackString(%q,%q) = %q, want %q", tt.primary, tt.fallback, got, tt.want)
			}
		})
	}

	sliceTests := []struct {
		name              string
		primary, fallback []string
		want              []string
	}{
		{"non-empty primary", []string{"x"}, []string{"y"}, []string{"x"}},
		{"empty primary", nil, []string{"y"}, []string{"y"}},
	}
	for _, tt := range sliceTests {
		t.Run("FallbackSlice/"+tt.name, func(t *testing.T) {
			if got := FallbackSlice(tt.primary, tt.fallback); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("FallbackSlice(%v,%v) = %v, want %v", tt.primary, tt.fallback, got, tt.want)
			}
		})
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
