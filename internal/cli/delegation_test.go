package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func delegationCLIConfig() *config.Config {
	return &config.Config{Web: config.WebConfig{Enabled: true}, Templates: map[string]config.TemplateConfig{"one": {}, "two": {}}, Defaults: config.DefaultsConfig{}, Delegation: &config.DelegationConfig{Roles: map[string]config.RoleSpec{"implement": {UseFor: "write code"}}, ActiveProfile: "a", Profiles: map[string]config.Profile{"a": {Roles: map[string]config.RoleTarget{"implement": {Template: "one"}}}, "b": {Roles: map[string]config.RoleTarget{"implement": {Template: "two"}}}}}}
}

func delegationCommand(t *testing.T, args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()
	cmd := newDelegationCmd()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	return out, errOut, cmd.Execute()
}

func TestDelegationUseRoundTripAndUnknownPreservesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leo.yaml")
	if err := config.Save(path, delegationCLIConfig()); err != nil {
		t.Fatal(err)
	}
	old := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = old })
	_, _, err := delegationCommand(t, "use", "b")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil || loaded.Delegation.ActiveProfile != "b" {
		t.Fatalf("cfg=%+v err=%v", loaded.Delegation, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = delegationCommand(t, "use", "missing")
	if err == nil {
		t.Fatal("expected unknown profile")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("file changed: %v", err)
	}
}

func TestDelegationRenderResolveAndAbsentConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leo.yaml")
	if err := config.Save(path, delegationCLIConfig()); err != nil {
		t.Fatal(err)
	}
	old := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = old })
	out, _, err := delegationCommand(t, "render")
	if err != nil || out.String() != config.RenderDelegationInstructions(delegationCLIConfig()) {
		t.Fatalf("render=%q err=%v", out, err)
	}
	out, _, err = delegationCommand(t, "resolve", "implement")
	if err != nil || !strings.Contains(out.String(), "implement\tone") {
		t.Fatalf("resolve=%q err=%v", out, err)
	}
	if _, _, err := delegationCommand(t, "resolve", "missing"); err == nil {
		t.Fatal("expected unmapped role")
	}
	if err := config.Save(path, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := delegationCommand(t, "list"); err == nil || !strings.Contains(err.Error(), "delegation not configured") {
		t.Fatalf("err=%v", err)
	}
}
