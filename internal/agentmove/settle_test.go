package agentmove

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
)

// fencedFixture is a move fixture whose agent is fenced by run-1 to laptop,
// as a source holds it after an unknown outcome.
func fencedFixture(t *testing.T) *moveFixture {
	t.Helper()
	f := newMoveFixture(t)
	if err := f.src.FenceMove("web", agentstore.MovePending{To: "laptop", Peer: "laptop.local", RunID: "run-1", Since: receiveTime}); err != nil {
		t.Fatal(err)
	}
	return f
}

// Reconcile asks the destination how far the fenced run got: delivered marks
// the agent moved, a settled failure lifts the fence, anything else keeps it.
func TestReconcileSettlesByDestinationPhase(t *testing.T) {
	cases := map[string]struct {
		status  func(Manifest) ([]byte, error)
		outcome string
		fenced  bool
		marked  bool
	}{
		"live":      {status: jsonAnswer(StatusResult{Present: true, Status: "running", Live: true}), outcome: SettleMoved, marked: true},
		"delivered": {status: jsonAnswer(StatusResult{Present: true, Status: "stopped", Phase: PhaseLive}), outcome: SettleMoved, marked: true},
		"failed":    {status: jsonAnswer(StatusResult{Present: true, MovedTo: "studio", Status: "stopped", Phase: PhaseFailed}), outcome: SettleFailed},
		"inflight":  {status: jsonAnswer(StatusResult{Present: true, Status: "stopped", Phase: PhaseStarting}), fenced: true},
		"unknown":   {status: jsonAnswer(StatusResult{}), fenced: true},
		"unreachable": {status: func(Manifest) ([]byte, error) {
			return nil, errors.New("ssh: connect to host laptop: Connection refused")
		},
			fenced: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := fencedFixture(t)
			f.ep.answer[VerbStatus] = c.status
			res, err := f.mv.Reconcile(context.Background(), "web")
			if c.outcome == "" {
				if !HasCode(err, CodeOutcomeUnknown) || !strings.Contains(fmt.Sprint(err), "leo --host laptop agent list") {
					t.Fatalf("reconcile = %+v, %v, want outcome_unknown naming the check", res, err)
				}
			} else if err != nil || res.Outcome != c.outcome || res.To != "laptop" || res.RunID != "run-1" {
				t.Fatalf("reconcile = %+v, %v, want %s", res, err, c.outcome)
			}
			if got := f.src.fence(t, "web") != nil; got != c.fenced {
				t.Fatalf("fenced = %v, want %v", got, c.fenced)
			}
			if got := f.log.has("mark web laptop laptop.local"); got != c.marked {
				t.Fatalf("marked = %v, want %v (events %v)", got, c.marked, f.log.all())
			}
			if m := f.ep.manifests[0]; m.Transfer != "run-1" || m.TargetName() != "web" {
				t.Fatalf("status asked about %q run %q, want web run-1", m.TargetName(), m.Transfer)
			}
		})
	}
}

func TestReconcileNeedsAFence(t *testing.T) {
	f := newMoveFixture(t)
	if _, err := f.mv.Reconcile(context.Background(), "web"); !HasCode(err, CodeBadRequest) || f.log.has("verb ") {
		t.Fatalf("reconcile without a fence = %v (events %v), want bad_request and no verb", err, f.log.all())
	}
}

// At daemon start every fenced agent is reconciled, and only those.
func TestReconcilePendingSettlesFencedAgents(t *testing.T) {
	f := fencedFixture(t)
	if err := agentstore.Save(f.home, agentstore.Record{Name: "api", Workspace: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	var logged []string
	f.mv.ReconcilePending(context.Background(), func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) })
	if f.src.fence(t, "web") != nil || !f.log.has("mark web laptop") {
		t.Fatalf("web not settled: events %v", f.log.all())
	}
	if len(f.ep.manifests) != 1 {
		t.Fatalf("status asked %d times, want once (only web is fenced)", len(f.ep.manifests))
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "web") || !strings.Contains(logged[0], SettleMoved) {
		t.Fatalf("log = %q", logged)
	}
}

// Abandon lifts the fence without asking the destination: the escape hatch
// for a host that is gone for good.
func TestAbandonLiftsFenceWithoutAsking(t *testing.T) {
	f := fencedFixture(t)
	res, err := f.mv.Abandon(context.Background(), "web")
	if err != nil || res.Outcome != SettleAbandoned || res.To != "laptop" || res.RunID != "run-1" {
		t.Fatalf("abandon = %+v, %v", res, err)
	}
	if f.src.fence(t, "web") != nil || f.log.has("verb ") || f.log.has("mark ") {
		t.Fatalf("fence %+v, events %v: want the fence lifted with no verb or mark", f.src.fence(t, "web"), f.log.all())
	}
	if _, err := f.mv.Abandon(context.Background(), "web"); !HasCode(err, CodeBadRequest) {
		t.Fatalf("abandon without a fence = %v, want bad_request", err)
	}
}
