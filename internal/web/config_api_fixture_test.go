package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// configAPIFixtureYAML is the leo.yaml the config API tests run against: every
// resource kind populated, a delegation block, and values for the cascade.
const configAPIFixtureYAML = `defaults:
  model: sonnet
  max_turns: 10
  environments:
    - base
web:
  enabled: true
  port: 8370
client:
  default_host: remote
  hosts:
    remote:
      ssh: me@box
environments:
  base:
    A: "1"
templates:
  coder:
    model: opus
    workspace: /tmp/coder
    harness_options:
      permission_mode: plan
    permissions:
      can_spawn:
        - reviewer
  reviewer:
    model: haiku
tasks:
  heartbeat:
    schedule: "0 * * * *"
    prompt_file: heartbeat.md
    enabled: true
  nightly:
    schedule: "0 3 * * *"
    prompt_file: prompts/nightly.md
delegation:
  active_profile: fast
  roles:
    coder:
      use_for: writing code
  profiles:
    fast:
      roles:
        coder:
          template: reviewer
    deep:
      roles:
        coder:
          template: coder
`

// newConfigService writes the fixture into a temp home and returns a service
// over it plus the config path. reload, when non-nil, stands in for the daemon.
func newConfigService(t *testing.T, reload ConfigReloader) (*ConfigService, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(path, []byte(configAPIFixtureYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o750); err != nil {
		t.Fatal(err)
	}
	svc := NewConfigService(ConfigServiceOptions{ConfigPath: path, Reloader: reload})
	return svc, path
}

func revisionOnDisk(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return config.RevisionOf(data)
}
