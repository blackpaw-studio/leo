package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A bad appearance rule is a `leo validate` warning: loadConfig (behind
// validate, `leo service start` and `leo run`) still succeeds, and a
// warning-only result exits 0.
func TestValidateWarnsOnBadAppearanceRule(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "leo.yaml")
	yaml := "appearance_rules:\n  - when: {field: name, op: is, value: x}\n    color: magenta\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() = %v, want a bad appearance rule not to block it", err)
	}
	findings := configWarningFindings(cfg)
	var got *Finding
	for i := range findings {
		if findings[i].Check == "appearance" {
			got = &findings[i]
		}
	}
	if got == nil || got.Severity != SeverityWarn || !strings.HasPrefix(got.Message, `appearance_rules[0]: color "magenta"`) {
		t.Fatalf("findings = %+v, want an appearance warning for rule 0", findings)
	}
	var emitErr error
	captureStdoutForConfigTests(t, func() { emitErr = emitValidateText(findings, cfg) })
	if emitErr != nil {
		t.Fatalf("leo validate with only the appearance warning = %v, want exit 0", emitErr)
	}
}
