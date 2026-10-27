package setup

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// A setup re-run on a follower keeps the follow block and the lead layer,
// and writes no lead entry into leo.yaml.
func TestBuildConfigKeepsFollowLayer(t *testing.T) {
	t.Cleanup(config.SetFollowProbes(config.FollowProbes{
		LookPath: func(n string) (string, error) { return "/bin/" + n, nil },
		Stat:     func(string) (fs.FileInfo, error) { return nil, nil },
	}))
	home := t.TempDir()
	export := `{"schema":1,"kind":"leo.follow.export","revision":"r","templates":{"lt":{"model":"opus"}},"environments":{"le":{"A":"b"}},` +
		`"delegation":{"active_profile":"day","profiles":{"day":{"roles":{"implement":"lt"}}}},"defaults":{"max_turns":9}}`
	exp, err := config.ParseFollowExport([]byte(export))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.WriteFollowCache(filepath.Join(home, "state"), config.FollowCache{Host: "lead", Digest: exp.Digest(), Export: []byte(export)}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "leo.yaml")
	if err := os.WriteFile(path, []byte("client:\n  hosts:\n    lead: {ssh: evan@10.0.2.10}\nfollow:\n  host: lead\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	existing, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(path, buildConfig(filepath.Join(home, "workspace"), existing)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	text, _ := os.ReadFile(path)
	if strings.Contains(string(text), "lt:") || strings.Contains(string(text), "le:") || !strings.Contains(string(text), "follow:") {
		t.Fatalf("leo.yaml after setup:\n%s", text)
	}
	re, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if re.TemplateSource("lt") != config.SourceLead || re.Delegation == nil || re.Delegation.ActiveProfile != "day" || re.Defaults.MaxTurns != 9 {
		t.Fatalf("layer after setup: lt=%q delegation=%+v max_turns=%d", re.TemplateSource("lt"), re.Delegation, re.Defaults.MaxTurns)
	}
}
