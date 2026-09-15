//go:build e2e

package e2e

import (
	"net/http"
	"testing"

	"github.com/blackpaw-studio/leo/internal/consult"
)

// The output route must be a safe snapshot during an active fake-harness turn
// and still expose the same run stream once the turn completes.
func TestDispatchOutputSnapshotWhileRunningAndAfterCompletion(t *testing.T) {
	s := newInteractiveE2EWithDelay(t, 1000)
	started := s.dispatch(t, "output snapshot")
	var live consult.Output
	s.request(t, http.MethodGet, "/api/dispatch/"+started.ID+"/output?tail=60", nil, &live)
	if live.ID != started.ID {
		t.Fatalf("live output id = %+v", live)
	}
	_ = s.wait(t, started.ID+"#1")
	var done consult.Output
	s.request(t, http.MethodGet, "/api/dispatch/"+started.ID+"/output?tail=60", nil, &done)
	if done.ID != started.ID {
		t.Fatalf("completed output id = %+v", done)
	}
}
