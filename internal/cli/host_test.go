package cli

import (
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func findHostSub(t *testing.T, name string) {
	t.Helper()
	c, _, err := newHostCmd().Find([]string{name})
	if err != nil || c.Name() != name {
		t.Fatal(name, err)
	}
}
func TestHostListViaDaemon(t *testing.T)       { findHostSub(t, "list") }
func TestHostConnectViaDaemon(t *testing.T)    { findHostSub(t, "connect") }
func TestHostDisconnectViaDaemon(t *testing.T) { findHostSub(t, "disconnect") }
func TestAgentAttachRemoteUsesHubControlPath(t *testing.T) {
	r := config.HostResolution{Name: "x", ControlPath: "/c", Host: config.HostConfig{SSH: "u@x", SSHArgs: []string{"-p", "2"}}}
	got := append([]string{"-t"}, buildSSHArgs(r)...)
	want := []string{"-t", "u@x", "-p", "2", "-o", "ControlMaster=auto", "-o", "ControlPath=/c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}
