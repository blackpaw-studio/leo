package agentmove

import (
	"os"
	"testing"
)

func TestLocalHostNameOverride(t *testing.T) {
	real, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(HostnameEnv, "")
	if h, err := LocalHost(); err != nil || h.Hostname != real {
		t.Fatalf("LocalHost() = %+v, %v; want hostname %q", h, err, real)
	}
	t.Setenv(HostnameEnv, "peer-b")
	if h, err := LocalHost(); err != nil || h.Hostname != "peer-b" {
		t.Fatalf("LocalHost() with %s = %+v, %v", HostnameEnv, h, err)
	}
}
