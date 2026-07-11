package schematest

import (
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// fakeHarness is a minimal harness.Harness implementation with one enum
// field, exercising Run's happy path plus the enum-consistency checks.
type fakeHarness struct{}

func (fakeHarness) Name() string                              { return "fake" }
func (fakeHarness) Binary() string                            { return "fake" }
func (fakeHarness) Args(harness.LaunchSpec) ([]string, error) { return nil, nil }
func (fakeHarness) SessionArgs(harness.SessionState) []string { return nil }
func (fakeHarness) ValidateModel(string) error                { return nil }
func (fakeHarness) SupportsChannels() bool                    { return false }
func (fakeHarness) ParseEvents(io.Reader) (harness.Result, error) {
	return harness.Result{}, nil
}
func (fakeHarness) Env(harness.LaunchSpec) (map[string]string, error) { return nil, nil }
func (fakeHarness) SupportsKind(harness.Kind) bool                    { return true }
func (fakeHarness) Driver() harness.SessionDriver                     { return nil }

func (fakeHarness) OptionsSchema() []harness.OptionField {
	return []harness.OptionField{
		{Key: "mode", Label: "Mode", Type: harness.OptionEnum, EnumValues: []string{"a", "b"}},
	}
}

func (fakeHarness) DecodeOptions(raw map[string]any) (any, error) {
	for k, v := range raw {
		if k != "mode" {
			return nil, fmt.Errorf("unknown option %q", k)
		}
		s, ok := v.(string)
		if !ok || (s != "a" && s != "b") {
			return nil, fmt.Errorf("mode %q is not valid (use a or b)", v)
		}
	}
	return raw, nil
}

func TestRunHappyPath(t *testing.T) {
	Run(t, fakeHarness{}, []string{"mode"}, nil)
}

func TestSampleFor(t *testing.T) {
	tests := []struct {
		name    string
		field   harness.OptionField
		samples map[string]any
		want    any
	}{
		{"override wins", harness.OptionField{Key: "k", Type: harness.OptionString}, map[string]any{"k": "override"}, "override"},
		{"bool", harness.OptionField{Key: "k", Type: harness.OptionBool}, nil, true},
		{"enum", harness.OptionField{Key: "k", Type: harness.OptionEnum, EnumValues: []string{"x", "y"}}, nil, "x"},
		{"string list", harness.OptionField{Key: "k", Type: harness.OptionStringList}, nil, []any{"x"}},
		{"yaml map", harness.OptionField{Key: "k", Type: harness.OptionYAMLMap}, nil, map[string]any{}},
		{"string default", harness.OptionField{Key: "k", Type: harness.OptionString}, nil, "x"},
		{"text default", harness.OptionField{Key: "k", Type: harness.OptionText}, nil, "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sampleFor(tt.field, tt.samples)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("sampleFor() = %v, want %v", got, tt.want)
			}
		})
	}
}
