package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestSplitAndTrim(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{",,", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ,, c ", []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := splitAndTrim(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitAndTrim(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// newTestConfig writes a minimal config to a tmp home and sets cfgFile so
// loadConfig/saveConfig target it.
func newTestConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Processes: map[string]config.ProcessConfig{
			"existing": {Enabled: true, Model: "sonnet"},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// loadConfig reads via FindConfig — use explicit cfgFile.
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })
	return cfg, cfgPath
}

func TestSetProcessEnabled_TogglesAndPersists(t *testing.T) {
	_, cfgPath := newTestConfig(t)

	if err := setProcessEnabled("existing", false); err != nil {
		t.Fatalf("disable: %v", err)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Processes["existing"].Enabled {
		t.Errorf("process should be disabled after setProcessEnabled(false)")
	}

	if err := setProcessEnabled("existing", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	got, _ = config.Load(cfgPath)
	if !got.Processes["existing"].Enabled {
		t.Errorf("process should be enabled after setProcessEnabled(true)")
	}
}

func TestSetProcessEnabled_MissingProcess(t *testing.T) {
	newTestConfig(t)
	err := setProcessEnabled("does-not-exist", true)
	if err == nil {
		t.Fatal("expected error for missing process")
	}
}

// Verify saveConfig writes to the resolved configPath.
func TestSaveConfig_WritesToResolvedPath(t *testing.T) {
	_, cfgPath := newTestConfig(t)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Processes["new"] = config.ProcessConfig{Enabled: true}

	if err := saveConfig(cfg); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("config file is empty after save")
	}
}
