package cli

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
