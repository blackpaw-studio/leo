package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/drain"
)

// serveSocket serves h on workDir's daemon socket.
func serveSocket(t *testing.T, h http.Handler) string {
	t.Helper()
	workDir := tmpWorkDir(t)
	ln, err := net.Listen("unix", SockPath(workDir))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: time.Second}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { srv.Close() })
	return workDir
}

// A daemon from before graceful restart has no /drain routes: its mux
// answers 404 (or 405) in plain text.
func TestDrainClientOldDaemonIsUnsupported(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		workDir := serveSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "404 page not found", code)
		}))
		ctx := context.Background()
		if _, err := DrainBusy(ctx, workDir); !errors.Is(err, ErrDrainUnsupported) {
			t.Errorf("%d: DrainBusy err = %v, want ErrDrainUnsupported", code, err)
		}
		if _, err := DrainBegin(ctx, workDir, time.Minute); !errors.Is(err, ErrDrainUnsupported) {
			t.Errorf("%d: DrainBegin err = %v, want ErrDrainUnsupported", code, err)
		}
	}
}

func TestDrainClientRoundTrip(t *testing.T) {
	s := drainServer(t, drain.Probe{Name: "a", Exists: true, Busy: true, Transport: "bridge"})
	workDir := serveSocket(t, s.Handler())
	ctx := context.Background()

	busy, err := DrainBusy(ctx, workDir)
	if err != nil || len(busy) != 1 || busy[0].Name != "a" {
		t.Fatalf("DrainBusy = %+v, %v", busy, err)
	}
	st, err := DrainBegin(ctx, workDir, 90*time.Second)
	if err != nil || st.Phase != drain.PhaseDraining || st.TimeoutMinutes != 2 {
		t.Fatalf("DrainBegin = %+v, %v", st, err)
	}
	if _, err := DrainBegin(ctx, workDir, time.Minute); !errors.Is(err, drain.ErrInProgress) {
		t.Fatalf("second DrainBegin err = %v, want drain.ErrInProgress", err)
	}
	if st, err := DrainStatus(ctx, workDir); err != nil || st.ID == "" {
		t.Fatalf("DrainStatus = %+v, %v", st, err)
	}
	if err := DrainCommit(ctx, workDir, false); !errors.Is(err, drain.ErrNotDrained) {
		t.Fatalf("DrainCommit(false) err = %v, want drain.ErrNotDrained", err)
	}
	if err := DrainCommit(ctx, workDir, true); err != nil {
		t.Fatalf("DrainCommit(true): %v", err)
	}
	if err := DrainCancel(ctx, workDir); err != nil {
		t.Fatalf("DrainCancel: %v", err)
	}
	if err := DrainCancel(ctx, workDir); !errors.Is(err, drain.ErrNoDrain) {
		t.Fatalf("second DrainCancel err = %v, want drain.ErrNoDrain", err)
	}
}
