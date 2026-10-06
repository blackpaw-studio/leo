package bridge

import "testing"

const agentBeta = "leo-beta"

// A launch whose mod connected may already have been handed its opening,
// and run it, even if the stream is down at the moment of the fallback (a
// reload, a reconnect): falling back then would deliver it again. So only
// a generation whose mod never connected is forgotten.
func TestAFallbackSparesAGenerationWhoseModEverConnected(t *testing.T) {
	h := newTestHub(newFakeClock())
	never := mustOpen(t, h, agentA)
	if !h.ForgetGenUnlessEverConnected(never) {
		t.Fatal("kept a generation whose mod never connected")
	}

	dropped := mustOpen(t, h, agentBeta)
	mustConnect(t, h, agentBeta).Close()
	if h.Connected(agentBeta) {
		t.Fatal("the stream is still up")
	}
	if h.ForgetGenUnlessEverConnected(dropped) {
		t.Fatal("forgot a generation whose mod connected and dropped")
	}
}
