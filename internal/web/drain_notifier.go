package web

import (
	"context"
	"errors"

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
