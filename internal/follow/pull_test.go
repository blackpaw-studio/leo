package follow

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

type call struct {
	name string
	args []string
}

type reply struct {
	stdout, stderr string
	err            error
}

// fakeRunner records every call and answers from a queue.
type fakeRunner struct {
	calls   []call
	replies []reply
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string) ([]byte, []byte, error) {
	f.calls = append(f.calls, call{name, append([]string(nil), args...)})
	if len(f.replies) == 0 {
		return nil, nil, errors.New("unexpected call")
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return []byte(r.stdout), []byte(r.stderr), r.err
}

type exitErr int

func (e exitErr) Error() string { return "exit status" }
func (e exitErr) ExitCode() int { return int(e) }

const followerYAML = `client:
  default_host: other
  hosts:
    other: {ssh: evan@10.0.2.11}
    lead:
      ssh: evan@10.0.2.10
      ssh_args: ["-p", "2222"]
      leo_path: $HOME/leo dev/bin/leo
follow:
  host: lead
`

type fixture struct {
	path, state string
	now         time.Time
	runner      *fakeRunner
	puller      *Puller
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Cleanup(config.SetFollowProbes(config.FollowProbes{
		LookPath: func(n string) (string, error) { return "/bin/" + n, nil },
		Stat:     func(string) (fs.FileInfo, error) { return nil, nil },
	}))
	home := t.TempDir()
	path := filepath.Join(home, "leo.yaml")
	if err := os.WriteFile(path, []byte(followerYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fixture{path: path, state: filepath.Join(home, "state"), now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), runner: &fakeRunner{}}
	f.puller = &Puller{
		LoadConfig: func() (*config.Config, error) { return config.Load(path) },
		Runner:     f.runner,
		Now:        func() time.Time { return f.now },
	}
	return f
}

func exportJSON(t *testing.T, templates string, revision string) string {
	t.Helper()
	return `{"schema":1,"kind":"leo.follow.export","leo_version":"v0.64.0","home_id":"0011","revision":"` + revision + `","templates":` + templates + `}`
}

func (f *fixture) cacheBytes(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.state, "follow-cache.json"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPullArgv(t *testing.T) {
	f := newFixture(t)
	f.runner.replies = []reply{{stdout: exportJSON(t, `{"a":{}}`, "r1")}}
	if _, err := f.puller.Pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctl := filepath.Join(f.state, "remotes", "lead.ctl")
	want := []call{{"ssh", []string{
		"-o", "BatchMode=yes",
		"evan@10.0.2.10", "-p", "2222",
		"-o", "ControlMaster=auto", "-o", "ControlPath=" + ctl,
		"-o", "ConnectTimeout=10",
		`"$HOME"/'leo dev/bin/leo'`, "config", "export", "--follow",
	}}}
	if !reflect.DeepEqual(f.runner.calls, want) {
		t.Fatalf("calls:\n got %q\nwant %q", f.runner.calls, want)
	}
}

func TestPullWritesCacheAndStatus(t *testing.T) {
	f := newFixture(t)
	f.runner.replies = []reply{{stdout: exportJSON(t, `{"a":{"model":"opus"}}`, "r1")}}
	out, err := f.puller.Pull(context.Background())
	if err != nil || !out.Changed {
		t.Fatalf("Pull = %+v, %v", out, err)
	}
	fc, err := config.ReadFollowCache(f.state)
	if err != nil || fc == nil || fc.Host != "lead" || fc.SSH != "evan@10.0.2.10" || !fc.PulledAt.Equal(f.now) || fc.LeadRevision != "r1" || fc.LeadVersion != "v0.64.0" || fc.LeadHomeID != "0011" {
		t.Fatalf("cache = %+v, %v", fc, err)
	}
	st, _ := config.ReadFollowStatus(f.state)
	if !st.LastAttempt.Equal(f.now) || st.LastError != "" {
		t.Fatalf("status = %+v", st)
	}
	cfg, err := config.Load(f.path)
	if err != nil || cfg.TemplateSource("a") != config.SourceLead {
		t.Fatalf("follower does not see the pulled template: %v", err)
	}
}

func TestPullUnchangedExport(t *testing.T) {
	f := newFixture(t)
	body := exportJSON(t, `{"a":{}}`, "r1")
	f.runner.replies = []reply{{stdout: body}, {stdout: body}}
	if _, err := f.puller.Pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	out, err := f.puller.Pull(context.Background())
	if err != nil || out.Changed {
		t.Fatalf("second Pull = %+v, %v; want unchanged", out, err)
	}
	if fc, _ := config.ReadFollowCache(f.state); !fc.PulledAt.Equal(f.now) {
		t.Fatalf("pulled_at = %v, want the successful pull's time %v", fc.PulledAt, f.now)
	}
}

func TestPullErrorsKeepTheCache(t *testing.T) {
	cases := []struct {
		name    string
		replies []reply
		ctx     func() (context.Context, context.CancelFunc)
		code    config.FollowErrorCode
		detail  string
		calls   int
	}{
		{name: "ssh failure", replies: []reply{{stderr: "ssh: connect to host 10.0.2.10 port 2222: Connection refused", err: exitErr(255)}}, code: config.FollowUnreachable, detail: "Connection refused", calls: 1},
		{name: "timeout", replies: []reply{{err: context.DeadlineExceeded}}, code: config.FollowUnreachable, detail: "timed out", calls: 1},
		{name: "old lead", replies: []reply{{stderr: `Error: unknown command "export" for "leo config"`, err: exitErr(1)}, {stdout: "leo v0.1.0\n"}}, code: config.FollowLeadTooOld, detail: "v0.1.0", calls: 2},
		{name: "older lead", replies: []reply{{stderr: "Error: unknown flag: --follow", err: exitErr(1)}, {stdout: "leo v0.60.0\n"}}, code: config.FollowLeadTooOld, detail: "v0.60.0", calls: 2},
		{name: "lead follows", replies: []reply{{stderr: "Error: lead_is_follower: this host follows \"x\"", err: exitErr(1)}}, code: config.FollowLeadIsFollower, calls: 1},
		{name: "lead invalid", replies: []reply{{stderr: "Error: lead_config_invalid: config validation failed", err: exitErr(1)}}, code: config.FollowLeadConfigInvalid, calls: 1},
		{name: "too large", replies: []reply{{err: errExportTooLarge}}, code: config.FollowMalformed, detail: "exceeds", calls: 1},
		{name: "malformed", replies: []reply{{stdout: "{nope"}}, code: config.FollowMalformed, calls: 1},
		{name: "newer schema", replies: []reply{{stdout: `{"schema":9,"kind":"leo.follow.export"}`}}, code: config.FollowFollowerTooOld, detail: "schema 9", calls: 1},
		{name: "self", replies: []reply{{stdout: "SELF"}}, code: config.FollowSelfFollow, calls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.runner.replies = []reply{{stdout: exportJSON(t, `{"a":{}}`, "r1")}}
			if _, err := f.puller.Pull(context.Background()); err != nil {
				t.Fatal(err)
			}
			before := f.cacheBytes(t)
			f.runner.calls = nil
			replies := tc.replies
			if replies[0].stdout == "SELF" {
				id, err := config.HomeID(f.state)
				if err != nil {
					t.Fatal(err)
				}
				replies = []reply{{stdout: strings.Replace(exportJSON(t, `{}`, "r2"), `"0011"`, `"`+id+`"`, 1)}}
			}
			f.runner.replies = replies
			f.now = f.now.Add(time.Minute)
			_, err := f.puller.Pull(context.Background())
			var fe *config.FollowError
			if !errors.As(err, &fe) || fe.Code != tc.code || !strings.Contains(fe.Detail, tc.detail) {
				t.Fatalf("Pull = %v, want %s containing %q", err, tc.code, tc.detail)
			}
			if len(f.runner.calls) != tc.calls {
				t.Fatalf("calls = %q, want %d", f.runner.calls, tc.calls)
			}
			if tc.code == config.FollowLeadTooOld && f.runner.calls[1].args[len(f.runner.calls[1].args)-1] != "version" {
				t.Fatalf("second call = %q, want a version probe", f.runner.calls[1])
			}
			if after := f.cacheBytes(t); after != before {
				t.Fatal("a failed pull changed the cache")
			}
			st, _ := config.ReadFollowStatus(f.state)
			if st.LastErrorCode != tc.code || !st.LastAttempt.Equal(f.now) {
				t.Fatalf("status = %+v", st)
			}
		})
	}
}

func TestPullNotFollowing(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.path, []byte("templates: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.puller.Pull(context.Background()); !errors.Is(err, ErrNotFollowing) {
		t.Fatalf("err = %v, want ErrNotFollowing", err)
	}
	if len(f.runner.calls) != 0 {
		t.Fatal("pulled without follow")
	}
}

func TestExecRunnerCapsStdout(t *testing.T) {
	stdout, _, err := ExecRunner{}.Run(context.Background(), "head", []string{"-c", strconv.Itoa(maxExportBytes + 1), "/dev/zero"})
	if err == nil || len(stdout) > maxExportBytes {
		t.Fatalf("len(stdout) = %d, err = %v; want the cap enforced", len(stdout), err)
	}
}
