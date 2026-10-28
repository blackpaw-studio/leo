package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/secretenv"
)

var writerRef = secretenv.Ref{Backend: secretenv.Keychain, Service: "OP", Account: "evan"}

func TestSaveRoundTripsSecretRefs(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "leo.yaml", `environments:
  work:
    OP_TOKEN: {secret: keychain, service: OP, account: evan}
templates:
  coder:
    env:
      OP_TOKEN: {secret: file, path: ~/.op-token}
      REGION: us
tasks:
  beat:
    schedule: "0 * * * *"
    prompt_file: beat.md
    env:
      OP_TOKEN: {secret: command, argv: [/opt/bin/op, read, op://v/i/f]}
`)
	cfg := mustLoad(t, path)
	cfg.Defaults.Model = "sonnet" // force a full rewrite
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	re := mustLoad(t, path)
	for name, pair := range map[string][2]EnvMap{
		"environments.work": {cfg.Environments["work"], re.Environments["work"]},
		"templates.coder":   {cfg.Templates["coder"].Env, re.Templates["coder"].Env},
		"tasks.beat":        {cfg.Tasks["beat"].Env, re.Tasks["beat"].Env},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s: %v after save, want %v", name, pair[1], pair[0])
		}
	}
	if r, ok := re.Templates["coder"].Env["OP_TOKEN"].Ref(); !ok || r.Path != "~/.op-token" {
		t.Errorf("file ref = %+v, %v; want the unexpanded path kept", r, ok)
	}
}

const sharedRefYAML = `# team templates
templates:
  theirs:
    model: opus # keep
    env:
      # 1Password service token
      OP_TOKEN:
        secret: keychain # login keychain
        service: OP
        account: evan
      REGION: us
`

func sharedRefFixture(t *testing.T) splitFixture {
	t.Helper()
	dir := t.TempDir()
	return splitFixture{
		dir:        dir,
		leoPath:    writeFile(t, dir, "leo.yaml", "templates_file: shared.yaml\n"),
		sharedPath: writeFile(t, dir, "shared.yaml", sharedRefYAML),
	}
}

func TestSharedTemplateEditKeepsRefBlockByteIdentical(t *testing.T) {
	f := sharedRefFixture(t)
	cfg := f.load(t)
	tmpl := cfg.Templates["theirs"]
	tmpl.Model = "sonnet"
	cfg.Templates["theirs"] = tmpl
	if leoTouched, sharedTouched := f.save(t, cfg); leoTouched || !sharedTouched {
		t.Fatalf("leoTouched=%v sharedTouched=%v, want false/true", leoTouched, sharedTouched)
	}
	want := strings.Replace(sharedRefYAML, "model: opus # keep", "model: sonnet # keep", 1)
	if got := readFile(t, f.sharedPath); got != want {
		t.Errorf("shared file:\n%s\nwant:\n%s", got, want)
	}
}

func TestSharedTemplateSplicesLiteralAndRefSwaps(t *testing.T) {
	f := sharedRefFixture(t)
	cfg := f.load(t)
	tmpl := cfg.Templates["theirs"]
	tmpl.Env = EnvMap{"OP_TOKEN": Literal("plain"), "REGION": Secret(secretenv.Ref{Backend: secretenv.File, Path: "/etc/region"})}
	cfg.Templates["theirs"] = tmpl
	f.save(t, cfg)

	got := f.load(t).Templates["theirs"].Env
	if !reflect.DeepEqual(got, tmpl.Env) {
		t.Fatalf("env = %v, want %v", got, tmpl.Env)
	}
	body := readFile(t, f.sharedPath)
	if !strings.Contains(body, "model: opus # keep") || !strings.Contains(body, "# team templates") {
		t.Errorf("splice lost untouched bytes:\n%s", body)
	}
	if strings.Contains(body, "account: evan") {
		t.Errorf("the replaced ref's fields remain:\n%s", body)
	}
}

func TestFollowerLocalRefSurvivesSave(t *testing.T) {
	lead := `environments:
  le: {LEAD_TOKEN: {secret: keychain, service: LEAD}}
`
	local := `environments:
  me:
    OP_TOKEN: {secret: keychain, service: OP, account: evan}
templates:
  mine: {model: haiku}
`
	path := followerOnDisk(t, lead, local)
	cfg := mustLoad(t, path)
	cfg.Templates["mine"] = TemplateConfig{Model: "sonnet"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	re := mustLoad(t, path)
	if r, ok := re.Environments["me"]["OP_TOKEN"].Ref(); !ok || !reflect.DeepEqual(r, writerRef) {
		t.Errorf("local ref = %+v, %v", r, ok)
	}
	if r, ok := re.Environments["le"]["LEAD_TOKEN"].Ref(); !ok || r.Service != "LEAD" || re.EntrySource(KindEnvironment, "le") != SourceLead {
		t.Errorf("lead ref = %+v, %v", r, ok)
	}
	if body := readFile(t, path); strings.Contains(body, "LEAD") {
		t.Errorf("leo.yaml gained the lead's ref:\n%s", body)
	}
}
