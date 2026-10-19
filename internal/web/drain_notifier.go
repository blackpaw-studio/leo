package web

import (
	"context"
	"errors"

	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/drain"
)

// DrainNotifier lets the daemon's drain message agents through the same
// core as every other agent message (see chatDeliverer for the precedent).
func (s *Server) DrainNotifier() drain.Notifier { return drainNotifier{s} }

type drainNotifier struct{ s *Server }

// Message delivers text to name, a bridged delivery going under id.
func (n drainNotifier) Message(ctx context.Context, name, text, id string) (drain.Delivery, error) {
	out := n.s.messageAgentAs(ctx, name, "", text, id)
	if out.err != "" {
		return drain.Delivery{}, errors.New(out.err)
	}
	return drain.Delivery{Transport: out.transport, Queued: out.bridgeQueued}, nil
}

// Holds reports whether name's durable outbox still holds command id.
func (n drainNotifier) Holds(name, id string) (bool, error) {
	return n.s.bridgeRouter.Holds(name, id)
}

// RunningDispatches lists the running interactive dispatches for the
// daemon's drain, which shows them beside the busy agents but never
// pauses them: the restarted daemon adopts them.
func (s *Server) RunningDispatches() []drain.Probe {
	if s.consults == nil {
		return nil
	}
	return dispatchProbes(s.consults.RunningInteractive())
}

// dispatchProbes names each running dispatch by its name, or its id when
// it has none.
func dispatchProbes(records []consult.Record) []drain.Probe {
	probes := make([]drain.Probe, 0, len(records))
	for _, rec := range records {
		name := rec.Name
		if name == "" {
			name = rec.ID
		}
		probes = append(probes, drain.Probe{Name: name, Exists: true, Busy: true, Kind: drain.KindDispatch})
	}
	return probes
}
