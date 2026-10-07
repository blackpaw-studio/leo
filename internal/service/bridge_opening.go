package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/session"
)

// An opening that rides argv counts as delivered to its conversation once
// the conversation's claude transcript shows it prompted: the ground truth,
// which neither a claude stuck at a dialog (never submitted) nor one that
// ran it and died at once (submitted) can fool. The transcript is checked
// every openingTranscriptPoll while the launch runs and once more when it
// ends. Vars so tests can point and pace them.
var (
	openingTranscriptPath = session.JSONLPath
	openingTranscriptPoll = 2 * time.Second
)

// conversationArg returns the conversation args select: --session-id's
// value (a fresh conversation under a known id) or --resume's. "" when
// neither is set: claude then starts a conversation nobody knows the id of
// yet.
func conversationArg(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--session-id", "--resume":
			return args[i+1]
		}
	}
	return ""
}

// queuedOpening is an opening a bridged launch queued and its mod has not
// acked: the launch token it went to and its command id.
type queuedOpening struct{ launch, id string }

// openingDelivery tracks one supervise loop's claude opening brief: which
// conversations have it. A conversation has it once the bridge mod acked it
// there, or a legacy launch carried it in on argv (or by paste) and held up.
// A launch gets the opening unless the conversation it names has it, so
// every fresh conversation gets it, and a resumed one gets it only if it
// never did. The agent record keeps the latest conversation to get it, so
// a later loop (a restore after a daemon restart) knows too, and the
// opening a bridged launch queued, so a restarted daemon adopting that
// launch's session can queue it again under the same id.
type openingDelivery struct {
	briefPath string
	// text is the brief, read once; "" when it is empty or unreadable,
	// and then it only ever rides argv.
	text string
	// record writes the agent's delivery state; nil without an agent
	// record (a process, not an agent).
	record openingRecord
	// queued is what the record said a bridged launch queued, as the loop
	// started: the opening an adopted session may still need.
	queued queuedOpening
	// transcriptPath and transcriptPoll are openingTranscriptPath and
	// openingTranscriptPoll as the loop started.
	transcriptPath func(workDir, conversation string) (string, error)
	transcriptPoll time.Duration

	mu   sync.Mutex
	have map[string]bool // opening ids of conversations that have it
}

// openingRecord is where an agent's opening delivery state persists.
type openingRecord interface {
	delivered(id, launch string)
	queued(launch, id string)
}

// agentOpeningRecord persists to the agentstore record in home of the
// agent id names when it writes: a live rename moves the record, and the
// launch's later writes follow it.
type agentOpeningRecord struct {
	home string
	id   *procIdentity
}

func (r agentOpeningRecord) delivered(id, launch string) {
	name := r.id.Name()
	if err := agentstore.SetOpeningAcked(r.home, name, id, launch); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] warning: recording the opening prompt's delivery: %v\n", name, err)
	}
}

func (r agentOpeningRecord) queued(launch, id string) {
	name := r.id.Name()
	if err := agentstore.SetOpeningQueued(r.home, name, launch, id); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] warning: recording the queued opening prompt (an adopted session could not get it again): %v\n", name, err)
	}
}

// newOpeningDelivery sets up spec's opening from what the agent's record
// says was delivered and queued.
func newOpeningDelivery(homePath string, spec ProcessSpec, id *procIdentity) *openingDelivery {
	o := &openingDelivery{
		briefPath:      spec.OpeningBriefPath,
		transcriptPath: openingTranscriptPath,
		transcriptPoll: openingTranscriptPoll,
		have:           map[string]bool{},
	}
	if spec.OpeningBriefPath == "" {
		return o
	}
	if text, err := os.ReadFile(spec.OpeningBriefPath); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] warning: reading the opening brief: %v; it rides argv\n", id.Name(), err)
	} else {
		o.text = string(text)
	}
	if spec.Kind != harness.KindAgent {
		return o
	}
	o.record = agentOpeningRecord{home: homePath, id: id}
	if recs, err := agentstore.Load(agentstore.FilePath(homePath)); err == nil {
		rec := recs[spec.Name]
		if rec.OpeningAckedID != "" {
			o.have[rec.OpeningAckedID] = true
		}
		o.queued = queuedOpening{launch: rec.OpeningQueuedLaunch, id: rec.OpeningQueuedID}
	}
	return o
}

// has reports whether conversation has the opening already. One nobody
// knows the id of is fresh and never has.
func (o *openingDelivery) has(conversation string) bool {
	if conversation == "" || o.text == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.have[bridge.OpeningID(conversation, o.text)]
}

// bridgeable reports whether a bridged launch into conversation has the
// opening to deliver: there is one, it is readable, and the conversation
// does not have it.
func (o *openingDelivery) bridgeable(conversation string) bool {
	return o.text != "" && !o.has(conversation)
}

// delivered records that conversation got the opening, on behalf of launch
// ("" for a legacy launch). Nothing is recorded for a conversation nobody
// knows the id of.
func (o *openingDelivery) delivered(conversation, launch string) {
	if conversation == "" || o.text == "" {
		return
	}
	id := bridge.OpeningID(conversation, o.text)
	o.mu.Lock()
	o.have[id] = true
	o.mu.Unlock()
	if o.record != nil {
		o.record.delivered(id, launch)
	}
}

// settle takes bl's opening ticket once it has settled: an ack records the
// conversation the opening landed in (the session the mod's hello named,
// else the one the launch's args did) as having it.
func (o *openingDelivery) settle(bl bridgeLaunch) {
	if !isAcked(bl.ticket) {
		return
	}
	landed := bl.ticket.AckedIn()
	if landed == "" {
		landed = bl.conversation
	}
	if bl.conversation != "" && bl.conversation != landed {
		o.mu.Lock()
		o.have[bridge.OpeningID(bl.conversation, o.text)] = true
		o.mu.Unlock()
	}
	o.delivered(landed, bl.plan.Launch)
}

// isAcked reports whether t has settled with the mod's ok.
func isAcked(t *bridge.Ticket) bool {
	if t == nil {
		return false
	}
	select {
	case <-t.Done():
		return t.Err() == nil
	default:
		return false
	}
}

// watchTranscript records conversation as having the opening once its
// transcript, in workDir's claude project, shows the opening prompted: a
// legacy launch carried it on argv, or an adopted legacy session did. It
// checks while ctx (the launch) runs; the returned settle, called once the
// launch is over, waits for that and checks one last time, so a claude
// that ran the opening and died at once has it recorded before the next
// launch is planned. Nothing is watched for a conversation nobody knows
// the id of, or one that has the opening already.
func (o *openingDelivery) watchTranscript(ctx context.Context, workDir, conversation string) (settle func()) {
	if conversation == "" || o.text == "" || o.has(conversation) {
		return func() {}
	}
	path, err := o.transcriptPath(workDir, conversation)
	if err != nil {
		return func() {}
	}
	watch := claudeharness.NewPromptWatch(path, o.text)
	seen := func() bool {
		ok, err := watch.Seen()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: checking %s for the opening prompt: %v\n", path, err)
		}
		if ok {
			o.delivered(conversation, "")
		}
		return ok
	}
	done := make(chan struct{})
	var found bool
	go func() {
		defer close(done)
		for !seen() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(o.transcriptPoll):
			}
		}
		found = true
	}()
	return func() {
		<-done
		if !found {
			seen()
		}
	}
}

// queueOpening queues bl's opening on the bridge ahead of the launch, so
// nothing sent to the agent can overtake it, and returns bl carrying it.
// It goes under an id scoped by the launch's conversation, or by the launch
// itself when its conversation is fresh and unnamed, and is recorded as
// queued for the launch before the session starts. ok is false when the hub
// refused it; bl's generation is then forgotten and the caller treats the
// launch as legacy.
func (s *Supervisor) queueOpening(id *procIdentity, bl bridgeLaunch, opening *openingDelivery) (bridgeLaunch, bool) {
	scope := bl.conversation
	if scope == "" {
		scope = bl.plan.Launch
	}
	cmd := bridge.Opening(scope, opening.text)
	bl, ok := s.enqueueOpening(id, bl, cmd, true)
	if ok && opening.record != nil {
		opening.record.queued(bl.plan.Launch, cmd.ID)
	}
	return bl, ok
}

// requeueAdoptedOpening queues again, under the id it went out under, the
// opening the adopted session's launch queued and its mod never acked
// (the daemon that launched it died first). If the mod did run it, its
// dedup turns the repeat into a re-ack. Anything another launch queued is
// not this session's: it had none queued, or its opening rode argv.
func (s *Supervisor) requeueAdoptedOpening(id *procIdentity, bl bridgeLaunch, opening *openingDelivery) bridgeLaunch {
	q := opening.queued
	if q.id == "" || q.launch != bl.plan.Launch || opening.text == "" {
		return bl
	}
	cmd := bridge.Deliver(opening.text, true)
	cmd.ID = q.id
	bl, _ = s.enqueueOpening(id, bl, cmd, false)
	return bl
}

// enqueueOpening queues cmd, bl's opening, on bl's generation: as a gate
// for a launch, which a refusal abandons, so nothing behind the opening can
// have run there (see bridge.Hub.EnqueueGate); plainly for an adopted
// session, which is never abandoned. Either way it takes no slot under the
// agent's cap, so a full outbox is carried over whole behind it.
func (s *Supervisor) enqueueOpening(id *procIdentity, bl bridgeLaunch, cmd bridge.Command, gate bool) (bridgeLaunch, bool) {
	w := s.bridgeWiring()
	enqueue := w.hub.EnqueueOpening
	if gate {
		enqueue = w.hub.EnqueueGate
	}
	ticket, err := enqueue(bl.target, cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] queueing the opening prompt on the bridge: %v; launching without the bridge\n", id.Name(), err)
		w.hub.ForgetGen(bl.target)
		s.unbindAttentionLaunch(bl.plan.Key, bl.plan.Launch)
		id.setLegacy()
		return bridgeLaunch{}, false
	}
	bl.opening, bl.ticket = cmd.Text, ticket
	return bl, true
}

// trackOpeningAck waits for the mod's verdict on bl's opening. An ack marks
// it delivered. A refusal means it never ran: a launch is abandoned for a
// legacy relaunch that carries it on argv; an adopted session is live and
// is kept as it is. ctx ends with the launch.
func (s *Supervisor) trackOpeningAck(ctx context.Context, id *procIdentity, bl bridgeLaunch, opening *openingDelivery, tmuxPath string, fellBack *atomic.Bool) {
	err := bl.ticket.Wait(ctx)
	if ctx.Err() != nil {
		return // the launch is over; its end settles the ticket
	}
	switch {
	case err == nil:
		opening.settle(bl)
	case errors.Is(err, bridge.ErrRejected) && bl.adopted:
		fmt.Fprintf(os.Stderr, "[%s] the leo bridge refused the opening prompt (%v); the adopted session keeps running without it\n", id.Name(), err)
	case errors.Is(err, bridge.ErrRejected):
		fmt.Fprintf(os.Stderr, "[%s] the leo bridge refused the opening prompt (%v); relaunching without it\n", id.Name(), err)
		s.fallBackFromBridge(id, bl.target, tmuxPath, fellBack, true)
	}
}
