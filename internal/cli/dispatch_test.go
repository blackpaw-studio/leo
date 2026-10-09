package cli

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDispatchReleaseCommand(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	var method, path, auth string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"id":"d/x","status":"released"}}`))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	home := t.TempDir()
	state := filepath.Join(home, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "api.token"), []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "leo.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("web:\n  port: %d\ntasks: {}\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	old := cfgFile
	cfgFile = configPath
	t.Cleanup(func() { cfgFile = old })
	var out strings.Builder
	oldOut := consultStdout
	consultStdout = &out
	t.Cleanup(func() { consultStdout = oldOut })
	cmd := newDispatchReleaseCmd()
	cmd.SetArgs([]string{"d/x"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if method != "POST" || path != "/api/dispatch/d%2Fx/release" || auth != "Bearer tok" || out.String() != "released\n" {
		t.Fatalf("method=%s path=%s auth=%q out=%q", method, path, auth, out.String())
	}
}

// A supervised agent gets LEO_DISPATCH_ID and LEO_CONFIG blanked rather than
// unset (see service.sessionEnvArgs); blank must read as "not a dispatch".
func TestDispatchReportTreatsBlankIdentityAsUnset(t *testing.T) {
	var hits atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) })}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	configPath := filepath.Join(t.TempDir(), "leo.yaml")
	port := listener.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("web:\n  port: %d\ntasks: {}\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, id, config string }{
		{"blank id", "", configPath},
		{"blank config", "d-canary", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LEO_DISPATCH_ID", tc.id)
			t.Setenv("LEO_CONFIG", tc.config)
			cmd := newDispatchReportCmd()
			if err := cmd.RunE(cmd, nil); err != nil {
				t.Fatalf("report: %v", err)
			}
			if n := hits.Load(); n != 0 {
				t.Fatalf("report posted %d times with a blank identity", n)
			}
		})
	}
}

func TestDispatchRunOmittedModeLeavesDefaultToDaemonAndWaitsOnItsChoice(t *testing.T) {
	var postBody, waitQuery string
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/dispatch":
			b, _ := io.ReadAll(r.Body)
			postBody = string(b)
			_, _ = w.Write([]byte(`{"ok":true,"data":{"id":"d-x","mode":"interactive"}}`))
		case "/api/dispatch/wait":
			waitQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"ok":true,"data":[{"id":"d-x","status":"idle","outcome":"finished","text":"ok"}]}`))
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "api.token"), []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "leo.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("web:\n  port: %d\ntasks: {}\n", listener.Addr().(*net.TCPAddr).Port)), 0o600); err != nil {
		t.Fatal(err)
	}
	oldCfg, oldOut := cfgFile, consultStdout
	cfgFile, consultStdout = configPath, io.Discard
	t.Cleanup(func() { cfgFile, consultStdout = oldCfg, oldOut })
	cmd := newDispatchRunCmd()
	cmd.SetArgs([]string{"coding", "work", "--cwd", home})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(postBody, `"mode"`) {
		t.Fatalf("omitted --mode must not be sent: %s", postBody)
	}
	if !strings.Contains(waitQuery, "id=d-x%231") {
		t.Fatalf("wait query %q, want the interactive opening turn d-x#1", waitQuery)
	}
}

func TestDispatchRunSendsReleaseOnFinishOnlyWhenTheFlagIsGiven(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"omitted", nil, ""},
		{"bare flag", []string{"--release-on-finish"}, `"release_on_finish":true`},
		{"explicit opt-out", []string{"--release-on-finish=false"}, `"release_on_finish":false`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var postBody string
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/dispatch":
					b, _ := io.ReadAll(r.Body)
					postBody = string(b)
					_, _ = w.Write([]byte(`{"ok":true,"data":{"id":"d-x","mode":"interactive"}}`))
				case "/api/dispatch/wait":
					_, _ = w.Write([]byte(`{"ok":true,"data":[{"id":"d-x","status":"idle","outcome":"finished","text":"ok"}]}`))
				}
			})}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() { _ = server.Close() })
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "state", "api.token"), []byte("tok"), 0o600); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(home, "leo.yaml")
			if err := os.WriteFile(configPath, []byte(fmt.Sprintf("web:\n  port: %d\ntasks: {}\n", listener.Addr().(*net.TCPAddr).Port)), 0o600); err != nil {
				t.Fatal(err)
			}
			oldCfg, oldOut := cfgFile, consultStdout
			cfgFile, consultStdout = configPath, io.Discard
			t.Cleanup(func() { cfgFile, consultStdout = oldCfg, oldOut })
			cmd := newDispatchRunCmd()
			cmd.SetArgs(append([]string{"--role", "explore", "look", "--cwd", home}, c.args...))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if c.want == "" {
				if strings.Contains(postBody, "release_on_finish") {
					t.Fatalf("omitted flag must not be sent: %s", postBody)
				}
				return
			}
			if !strings.Contains(postBody, c.want) {
				t.Fatalf("body %s, want %s", postBody, c.want)
			}
		})
	}
}

func TestDispatchCallerFieldsAttributeChildrenToTheirParentDispatch(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want map[string]any
	}{
		"supervised agent": {map[string]string{"LEO_PROCESS_NAME": "alpha"}, map[string]any{"from": "alpha"}},
		"dispatch child":   {map[string]string{"LEO_PROCESS_NAME": "dispatch:d-abc", "LEO_DISPATCH_ID": "d-abc"}, map[string]any{"parent_dispatch_id": "d-abc"}},
		"invalid id":       {map[string]string{"LEO_PROCESS_NAME": "alpha", "LEO_DISPATCH_ID": "../x"}, map[string]any{"from": "alpha"}},
		"neither":          {map[string]string{}, nil},
	} {
		got := dispatchCallerFields(func(k string) string { return tc.env[k] })
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: fields = %v, want %v", name, got, tc.want)
		}
	}
}
