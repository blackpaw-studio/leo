package secretenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const sentinel = "S3CR3T-SENTINEL"

// fakeRun is a Runner that records each argv and replies with fixed output.
type fakeRun struct {
	calls  [][]string
	stdout string
	stderr string
	code   int
	err    error
}

func (f *fakeRun) run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	f.calls = append(f.calls, slices.Clone(argv))
	return []byte(f.stdout), []byte(f.stderr), f.code, f.err
}

func failRun(t *testing.T) Runner {
	return func(context.Context, []string) ([]byte, []byte, int, error) {
		t.Fatal("runner must not be called")
		return nil, nil, 0, nil
	}
}

func reasonOf(t *testing.T, err error) string {
	t.Helper()
	var re *ResolutionError
	if !errors.As(err, &re) {
		t.Fatalf("error %v (%T) is not a *ResolutionError", err, err)
	}
	return re.Reason
}

func TestResolveExecArgv(t *testing.T) {
	cases := []struct {
		name   string
		goos   string
		ref    Ref
		stdout string
		argv   []string
		want   string
	}{
		{
			name: "keychain with account", goos: "darwin",
			ref:    Ref{Backend: Keychain, Service: "S", Account: "A"},
			stdout: "val\n",
			argv:   []string{"/usr/bin/security", "find-generic-password", "-s", "S", "-a", "A", "-w"},
			want:   "val",
		},
		{
			name: "keychain without account", goos: "darwin",
			ref:    Ref{Backend: Keychain, Service: "S"},
			stdout: "val\n",
			argv:   []string{"/usr/bin/security", "find-generic-password", "-s", "S", "-w"},
			want:   "val",
		},
		{
			name: "keychain strips only the newline security prints", goos: "darwin",
			ref:    Ref{Backend: Keychain, Service: "S"},
			stdout: "val\n\n",
			argv:   []string{"/usr/bin/security", "find-generic-password", "-s", "S", "-w"},
			want:   "val\n",
		},
		{
			name: "secret-service sorts attributes", goos: "linux",
			ref:    Ref{Backend: SecretService, Attributes: map[string]string{"service": "leo", "key": "A"}},
			stdout: "v",
			argv:   []string{"secret-tool", "lookup", "key", "A", "service", "leo"},
			want:   "v",
		},
		{
			name: "secret-service keeps output as is", goos: "linux",
			ref:    Ref{Backend: SecretService, Attributes: map[string]string{"k": "v"}},
			stdout: "v\n",
			argv:   []string{"secret-tool", "lookup", "k", "v"},
			want:   "v\n",
		},
		{
			name: "command passes argv verbatim", goos: "darwin",
			ref:    Ref{Backend: Command, Argv: []string{"pass", "show", "leo/d"}},
			stdout: "v\r\n",
			argv:   []string{"pass", "show", "leo/d"},
			want:   "v",
		},
		{
			name: "command trims every trailing newline", goos: "linux",
			ref:    Ref{Backend: Command, Argv: []string{"x"}},
			stdout: " v \n\n",
			argv:   []string{"x"},
			want:   " v ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRun{stdout: tc.stdout}
			got, err := System{GOOS: tc.goos, Run: f.run}.Resolve(tc.ref)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got != tc.want {
				t.Errorf("value = %q, want %q", got, tc.want)
			}
			if len(f.calls) != 1 || !slices.Equal(f.calls[0], tc.argv) {
				t.Errorf("argv = %q, want [%q]", f.calls, tc.argv)
			}
		})
	}
}

func TestResolveExecFailures(t *testing.T) {
	cases := []struct {
		name   string
		goos   string
		ref    Ref
		run    fakeRun
		reason string
	}{
		{
			name: "keychain item not found", goos: "darwin",
			ref:    Ref{Backend: Keychain, Service: "S"},
			run:    fakeRun{code: 44, stderr: "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain.\n"},
			reason: "item not found",
		},
		{
			name: "keychain other failure carries the first stderr line", goos: "darwin",
			ref:    Ref{Backend: Keychain, Service: "S"},
			run:    fakeRun{code: 51, stderr: "security: User interaction is not allowed.\nsecond line\n"},
			reason: "exited 51: security: User interaction is not allowed.",
		},
		{
			name: "keychain stderr is capped", goos: "darwin",
			ref:    Ref{Backend: Keychain, Service: "S"},
			run:    fakeRun{code: 1, stderr: strings.Repeat("x", 300)},
			reason: "exited 1: " + strings.Repeat("x", 200),
		},
		{
			name: "secret-service failure", goos: "linux",
			ref:    Ref{Backend: SecretService, Attributes: map[string]string{"k": "v"}},
			run:    fakeRun{code: 1},
			reason: "exited 1",
		},
		{
			name: "command failure gives the exit code only", goos: "linux",
			ref:    Ref{Backend: Command, Argv: []string{"x"}},
			run:    fakeRun{code: 3, stderr: "boom\n"},
			reason: "exited 3",
		},
		{
			name: "command that cannot start", goos: "linux",
			ref:    Ref{Backend: Command, Argv: []string{"x"}},
			run:    fakeRun{code: -1, err: errors.New(`exec: "x": executable file not found in $PATH`)},
			reason: `could not run: exec: "x": executable file not found in $PATH`,
		},
		{
			name: "NUL byte in output", goos: "linux",
			ref:    Ref{Backend: Command, Argv: []string{"x"}},
			run:    fakeRun{stdout: "a\x00b"},
			reason: "value contains a NUL byte",
		},
		{
			name: "output over the cap", goos: "linux",
			ref:    Ref{Backend: Command, Argv: []string{"x"}},
			run:    fakeRun{stdout: strings.Repeat("a", MaxValueSize+1)},
			reason: "value exceeds 64 KiB",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := tc.run
			_, err := System{GOOS: tc.goos, Run: run.run}.Resolve(tc.ref)
			if got := reasonOf(t, err); got != tc.reason {
				t.Errorf("reason = %q, want %q", got, tc.reason)
			}
		})
	}
}

func TestResolveErrorsNeverCarryOutput(t *testing.T) {
	refs := []struct {
		goos string
		ref  Ref
	}{
		{"darwin", Ref{Backend: Keychain, Service: "S"}},
		{"linux", Ref{Backend: SecretService, Attributes: map[string]string{"k": "v"}}},
		{"linux", Ref{Backend: Command, Argv: []string{"x"}}},
	}
	for _, r := range refs {
		f := &fakeRun{stdout: sentinel + "\n", stderr: "", code: 1}
		_, err := System{GOOS: r.goos, Run: f.run}.Resolve(r.ref)
		if err == nil {
			t.Fatalf("%s: want an error", r.ref.Backend)
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Errorf("%s: error %q carries stdout", r.ref.Backend, err)
		}
	}
	// A command's stderr can echo its input; it never reaches the error.
	f := &fakeRun{stderr: sentinel, code: 2}
	_, err := System{GOOS: "linux", Run: f.run}.Resolve(Ref{Backend: Command, Argv: []string{"x"}})
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Errorf("command error %q carries stderr", err)
	}
}

func TestResolvePlatformGating(t *testing.T) {
	cases := []struct {
		goos string
		ref  Ref
		want string
	}{
		{"linux", Ref{Backend: Keychain, Service: "S"}, "not supported on linux"},
		{"darwin", Ref{Backend: SecretService, Attributes: map[string]string{"k": "v"}}, "not supported on darwin"},
		{"darwin", Ref{Backend: SystemdCreds, Name: "n"}, "not supported on darwin"},
	}
	for _, tc := range cases {
		_, err := System{GOOS: tc.goos, Run: failRun(t)}.Resolve(tc.ref)
		if got := reasonOf(t, err); got != tc.want {
			t.Errorf("%s on %s: reason = %q, want %q", tc.ref.Backend, tc.goos, got, tc.want)
		}
	}
}

func TestResolveTimeout(t *testing.T) {
	block := func(ctx context.Context, _ []string) ([]byte, []byte, int, error) {
		<-ctx.Done()
		return []byte(sentinel), nil, -1, ctx.Err()
	}
	_, err := System{GOOS: "darwin", Run: block, Timeout: time.Millisecond}.Resolve(Ref{Backend: Keychain, Service: "S"})
	if got := reasonOf(t, err); got != "timed out after 1ms" {
		t.Errorf("reason = %q", got)
	}
}

func TestResolveInvalidRefNeverRuns(t *testing.T) {
	_, err := System{GOOS: "linux", Run: failRun(t)}.Resolve(Ref{Backend: Command})
	if got := reasonOf(t, err); !strings.HasPrefix(got, "invalid reference: ") {
		t.Errorf("reason = %q", got)
	}
}

func writeFile(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveFile(t *testing.T) {
	dir := t.TempDir()
	uid := os.Getuid()
	cases := []struct {
		name   string
		path   string
		getuid func() int
		want   string
		reason string
	}{
		{name: "0600", path: writeFile(t, dir, "a", "tok\n", 0o600), want: "tok"},
		{name: "0400 with CRLF", path: writeFile(t, dir, "b", "tok\r\n", 0o400), want: "tok"},
		{name: "0640 refused", path: writeFile(t, dir, "c", sentinel, 0o640), reason: "mode 0640 is too open (want 0600 or stricter)"},
		{name: "0700 refused", path: writeFile(t, dir, "d", sentinel, 0o700), reason: "mode 0700 is too open (want 0600 or stricter)"},
		{name: "directory refused", path: dir, reason: "not a regular file"},
		{name: "foreign owner refused", path: writeFile(t, dir, "e", sentinel, 0o600), getuid: func() int { return uid + 1 }, reason: "not owned by the current user"},
		{name: "relative path refused", path: "rel/path", reason: "path must be absolute"},
		{name: "missing file", path: filepath.Join(dir, "missing"), reason: "file not found"},
		{name: "NUL refused", path: writeFile(t, dir, "f", "a\x00b", 0o600), reason: "value contains a NUL byte"},
		{name: "over the cap", path: writeFile(t, dir, "g", strings.Repeat("a", MaxValueSize+1), 0o600), reason: "value exceeds 64 KiB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := System{GOOS: "linux", Run: failRun(t), Getuid: tc.getuid}
			got, err := s.Resolve(Ref{Backend: File, Path: tc.path})
			if tc.reason != "" {
				if r := reasonOf(t, err); r != tc.reason {
					t.Errorf("reason = %q, want %q", r, tc.reason)
				}
				if strings.Contains(err.Error(), sentinel) {
					t.Errorf("error %q carries the file contents", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got != tc.want {
				t.Errorf("value = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveSystemdCreds(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "b-token", "tok\n", 0o400)
	lookup := func(k string) (string, bool) {
		if k == "CREDENTIALS_DIRECTORY" {
			return dir, true
		}
		return "", false
	}
	s := System{GOOS: "linux", Run: failRun(t), LookupEnv: lookup}
	got, err := s.Resolve(Ref{Backend: SystemdCreds, Name: "b-token"})
	if err != nil || got != "tok" {
		t.Fatalf("Resolve = %q, %v; want tok", got, err)
	}
	if _, err := s.Resolve(Ref{Backend: SystemdCreds, Name: "absent"}); reasonOf(t, err) != "credential not found" {
		t.Errorf("missing credential: %v", err)
	}
	unset := System{GOOS: "linux", Run: failRun(t), LookupEnv: func(string) (string, bool) { return "", false }}
	_, err = unset.Resolve(Ref{Backend: SystemdCreds, Name: "b-token"})
	if got := reasonOf(t, err); got != "CREDENTIALS_DIRECTORY is not set (load the credential with LoadCredential= in the unit)" {
		t.Errorf("reason = %q", got)
	}
}

// TestDefaultRunnerRealExec runs the real exec seam: a mocked Runner has hidden
// an argv bug before.
func TestDefaultRunnerRealExec(t *testing.T) {
	s := System{GOOS: runtime.GOOS}
	got, err := s.Resolve(Ref{Backend: Command, Argv: []string{"/bin/sh", "-c", `printf "v\n"`}})
	if err != nil || got != "v" {
		t.Fatalf("Resolve = %q, %v; want v", got, err)
	}
	// The shell assembles the sentinel, so only stdout (not argv) carries it.
	_, err = s.Resolve(Ref{Backend: Command, Argv: []string{"/bin/sh", "-c", "printf 'S3CR3T-%s' SENTINEL; exit 3"}})
	if r := reasonOf(t, err); r != "exited 3" {
		t.Errorf("reason = %q, want exited 3", r)
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Errorf("error %q carries stdout", err)
	}
	_, err = s.Resolve(Ref{Backend: Command, Argv: []string{"/nonexistent/leo-secretenv-test"}})
	if r := reasonOf(t, err); !strings.HasPrefix(r, "could not run: ") {
		t.Errorf("reason = %q", r)
	}
}

func TestDefaultRunnerEmptyStdin(t *testing.T) {
	// A command reading stdin sees EOF at once instead of hanging on the
	// daemon's stdin.
	got, err := System{GOOS: runtime.GOOS}.Resolve(Ref{Backend: Command, Argv: []string{"/bin/sh", "-c", `cat; printf done`}})
	if err != nil || got != "done" {
		t.Fatalf("Resolve = %q, %v; want done", got, err)
	}
}

func TestResolutionErrorText(t *testing.T) {
	err := &ResolutionError{Ref: Ref{Backend: Keychain, Service: "S"}, Reason: "item not found"}
	if got := err.Error(); got != "secret keychain(service=S): item not found" {
		t.Errorf("Error() = %q", got)
	}
}

func TestDescribe(t *testing.T) {
	got, err := Describe{}.Resolve(Ref{Backend: Keychain, Service: "X"})
	if err != nil || got != "<secret keychain(service=X)>" {
		t.Errorf("Describe = %q, %v", got, err)
	}
}

func TestRefString(t *testing.T) {
	cases := []struct {
		ref  Ref
		want string
	}{
		{Ref{Backend: Keychain, Service: "OP_TOKEN", Account: "evan"}, "keychain(service=OP_TOKEN, account=evan)"},
		{Ref{Backend: Keychain, Service: "has space"}, `keychain(service="has space")`},
		{Ref{Backend: SecretService, Attributes: map[string]string{"service": "leo", "key": "A"}}, "secret-service(key=A, service=leo)"},
		{Ref{Backend: SystemdCreds, Name: "b-token"}, "systemd-creds(name=b-token)"},
		{Ref{Backend: File, Path: "~/.config/leo/c.token"}, "file(path=~/.config/leo/c.token)"},
		{Ref{Backend: Command, Argv: []string{"pass", "show", "leo/d"}}, "command(argv=[pass show leo/d])"},
		{Ref{Backend: Command, Argv: []string{"sh", "-c", "a b"}}, `command(argv=[sh -c "a b"])`},
		{Ref{Backend: "vault", Path: "x"}, "vault(path=x)"},
	}
	for _, tc := range cases {
		if got := tc.ref.String(); got != tc.want {
			t.Errorf("String() = %s, want %s", got, tc.want)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		ref  Ref
		want string // "" means valid
	}{
		{"keychain ok", Ref{Backend: Keychain, Service: "S", Account: "A"}, ""},
		{"keychain missing service", Ref{Backend: Keychain}, "keychain requires service"},
		{"keychain foreign key", Ref{Backend: Keychain, Service: "S", Path: "/x"}, "path is not valid for keychain"},
		{"secret-service ok", Ref{Backend: SecretService, Attributes: map[string]string{"k": "v"}}, ""},
		{"secret-service missing attributes", Ref{Backend: SecretService}, "secret-service requires attributes"},
		{"secret-service empty attribute", Ref{Backend: SecretService, Attributes: map[string]string{"k": ""}}, "attributes must have non-empty names and values"},
		{"secret-service foreign key", Ref{Backend: SecretService, Attributes: map[string]string{"k": "v"}, Service: "S"}, "service is not valid for secret-service"},
		{"systemd-creds ok", Ref{Backend: SystemdCreds, Name: "b-token"}, ""},
		{"systemd-creds missing name", Ref{Backend: SystemdCreds}, "systemd-creds requires name"},
		{"systemd-creds nested name", Ref{Backend: SystemdCreds, Name: "a/b"}, "name must be a single path element"},
		{"systemd-creds dotdot", Ref{Backend: SystemdCreds, Name: ".."}, "name must be a single path element"},
		{"file ok", Ref{Backend: File, Path: "~/x"}, ""},
		{"file missing path", Ref{Backend: File}, "file requires path"},
		{"file foreign key", Ref{Backend: File, Path: "/x", Argv: []string{"a"}}, "argv is not valid for file"},
		{"command ok", Ref{Backend: Command, Argv: []string{"pass"}}, ""},
		{"command missing argv", Ref{Backend: Command}, "command requires argv"},
		{"command empty argv[0]", Ref{Backend: Command, Argv: []string{""}}, "argv[0] must not be empty"},
		{"command foreign keys", Ref{Backend: Command, Argv: []string{"a"}, Name: "n", Account: "x"}, "account is not valid for command; name is not valid for command"},
		{"missing backend", Ref{Service: "S"}, "missing backend (secret: keychain, secret-service, systemd-creds, file or command)"},
		{"unknown backend", Ref{Backend: "vault"}, `unknown backend "vault" (secret: keychain, secret-service, systemd-creds, file or command)`},
		{"unknown keys", Ref{Backend: File, Path: "/x", Unknown: []string{"zeta", "alpha"}}, `unknown key "alpha"; unknown key "zeta"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ref.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("Validate = %v, want nil", err)
			case tc.want != "" && (err == nil || err.Error() != tc.want):
				t.Errorf("Validate = %v, want %q", err, tc.want)
			}
		})
	}
}
