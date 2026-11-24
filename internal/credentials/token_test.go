package credentials

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"
)

// Fake tokens are obviously fake and unique so leak checks can search for them.
const (
	fakeClaudeToken = "sk-ant-oat-FAKE-keychain-token"
	fakeFileToken   = "sk-ant-oat-FAKE-file-token"
	fakeCodexToken  = "codex-FAKE-access-token"
)

func claudeBlob(token string) []byte {
	return []byte(`{"claudeAiOauth":{"accessToken":"` + token + `","expiresAt":1}}`)
}

type call struct {
	name string
	args []string
}

type fakeRunner struct {
	out   []byte
	code  int
	err   error
	calls []call
	// deadlineSet records whether the context carried a deadline.
	deadlineSet bool
}

func (f *fakeRunner) run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	f.calls = append(f.calls, call{name, args})
	_, f.deadlineSet = ctx.Deadline()
	return f.out, f.code, f.err
}

func fileReader(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if c, ok := files[path]; ok {
			return []byte(c), nil
		}
		return nil, fs.ErrNotExist
	}
}

func newSource(r *fakeRunner, files map[string]string) TokenSource {
	return TokenSource{Run: r.run, ReadFile: fileReader(files), User: "tester", Home: testHome, UseKeychain: true, Timeout: 5 * time.Second}
}

func TestServiceNameHashesStandardizedPath(t *testing.T) {
	if got := ServiceName("/Users/evan/.claude-blackpaw-leo", "/Users/evan"); got != "Claude Code-credentials-4d74f2ae" {
		t.Errorf("ServiceName() = %q", got)
	}
	if got := ServiceName("/Users/evan/.claude-blackpaw-leo/", "/Users/evan"); got != "Claude Code-credentials-4d74f2ae" {
		t.Errorf("trailing slash changed the service: %q", got)
	}
	if got := ServiceName("/Users/evan/.claude", "/Users/evan"); got != "Claude Code-credentials" {
		t.Errorf("default dir service = %q, want no suffix", got)
	}
}

func TestClaudeTokenReadFromKeychainViaAbsoluteSecurityCLI(t *testing.T) {
	r := &fakeRunner{out: claudeBlob(fakeClaudeToken) /* with trailing newline like security prints */}
	r.out = append(r.out, '\n')
	cred, err := newSource(r, nil).Resolve(context.Background(), KindClaudeCode, "/Users/tester/.claude-x")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if cred.Token != fakeClaudeToken {
		t.Errorf("token = %q", cred.Token)
	}
	if len(r.calls) != 1 || r.calls[0].name != "/usr/bin/security" {
		t.Fatalf("calls = %+v, want one absolute /usr/bin/security call", r.calls)
	}
	args := strings.Join(r.calls[0].args, " ")
	if !strings.HasPrefix(args, "find-generic-password -a tester -s Claude Code-credentials-") || !strings.HasSuffix(args, " -w") {
		t.Errorf("args = %q", args)
	}
	if !r.deadlineSet {
		t.Error("security call had no timeout")
	}
}

func TestClaudeFallsBackToFileOnlyWhenKeychainItemMissing(t *testing.T) {
	r := &fakeRunner{code: 44}
	files := map[string]string{"/Users/tester/.claude-x/.credentials.json": string(claudeBlob(fakeFileToken))}
	cred, err := newSource(r, files).Resolve(context.Background(), KindClaudeCode, "/Users/tester/.claude-x")
	if err != nil || cred.Token != fakeFileToken {
		t.Fatalf("Resolve() = %+v, %v; want the file token", cred, err)
	}
}

func TestClaudeMissingItemAndMissingFileIsInvalid(t *testing.T) {
	r := &fakeRunner{code: 44}
	_, err := newSource(r, nil).Resolve(context.Background(), KindClaudeCode, "/Users/tester/.claude-x")
	if err == nil || err.Reason != ReasonInvalid {
		t.Fatalf("err = %v, want %s", err, ReasonInvalid)
	}
}

func TestClaudeKeychainFailureNeedsAuthorizationAndSkipsStaleFile(t *testing.T) {
	files := map[string]string{"/Users/tester/.claude-x/.credentials.json": string(claudeBlob(fakeFileToken))}
	for name, r := range map[string]*fakeRunner{
		"exit 36 (locked)": {code: 36},
		"exit 51":          {code: 51},
		"timeout":          {err: context.DeadlineExceeded},
		"start failure":    {err: errors.New("fork/exec: nope")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newSource(r, files).Resolve(context.Background(), KindClaudeCode, "/Users/tester/.claude-x")
			if err == nil || err.Reason != ReasonNeedsAuthorization {
				t.Fatalf("err = %v, want %s (a stale file must not mask the live token)", err, ReasonNeedsAuthorization)
			}
		})
	}
}

func TestClaudeWithoutKeychainReadsFileOnly(t *testing.T) {
	r := &fakeRunner{}
	src := newSource(r, map[string]string{"/Users/tester/.claude/.credentials.json": string(claudeBlob(fakeFileToken))})
	src.UseKeychain = false
	cred, err := src.Resolve(context.Background(), KindClaudeCode, "/Users/tester/.claude")
	if err != nil || cred.Token != fakeFileToken {
		t.Fatalf("Resolve() = %+v, %v", cred, err)
	}
	if len(r.calls) != 0 {
		t.Errorf("security ran with UseKeychain=false: %+v", r.calls)
	}
}

func TestCodexReadsAuthJSON(t *testing.T) {
	files := map[string]string{"/Users/tester/.codex/auth.json": `{"tokens":{"access_token":"` + fakeCodexToken + `","account_id":"acct-1"}}`}
	cred, err := newSource(&fakeRunner{}, files).Resolve(context.Background(), KindCodex, "/Users/tester/.codex")
	if err != nil || cred.Token != fakeCodexToken || cred.AccountID != "acct-1" {
		t.Fatalf("Resolve() = %+v, %v", cred, err)
	}
}

func TestMalformedAndUnsafeTokensAreInvalidWithoutEchoingThem(t *testing.T) {
	cases := map[string]string{
		"bad json":      `{not json`,
		"no token":      `{"tokens":{}}`,
		"non-ascii":     `{"tokens":{"access_token":"tøken-SECRET"}}`,
		"has newline":   `{"tokens":{"access_token":"abc\ndef-SECRET"}}`,
		"has space":     `{"tokens":{"access_token":"abc def-SECRET"}}`,
		"bad accountid": `{"tokens":{"access_token":"ok","account_id":"a\r\nX-Evil: 1"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{"/Users/tester/.codex/auth.json": body}
			_, err := newSource(&fakeRunner{}, files).Resolve(context.Background(), KindCodex, "/Users/tester/.codex")
			if err == nil || err.Reason != ReasonInvalid {
				t.Fatalf("err = %v, want %s", err, ReasonInvalid)
			}
			if strings.Contains(err.Message, "SECRET") {
				t.Errorf("error message echoes token material: %q", err.Message)
			}
		})
	}
}
