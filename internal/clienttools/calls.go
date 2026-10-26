package clienttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/chat"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// Errors a Claim or Settle can return. Each maps to one HTTP status and wire
// code in the web handlers.
var (
	ErrUnknownTool    = errors.New("unknown client tool")
	ErrUnknownRequest = errors.New("unknown request")
	ErrUnknownClient  = errors.New("unknown client")
	ErrNotProvider    = errors.New("client does not provide this tool group")
	ErrAlreadyClaimed = errors.New("request already claimed")
	ErrOwnedByOther   = errors.New("request is owned by another client")
	ErrNotClaimant    = errors.New("request was claimed by another client")
	ErrAlreadySettled = errors.New("request already settled")
	ErrInvalidResult  = errors.New("invalid result")
)

// Daemon-side failure codes a call can end with, besides the app's own.
const (
	CodeNoClient  = "no_client"
	CodeTimeout   = "timeout"
	CodeCancelled = "cancelled"
)

// settledMemory is how many settled request ids are remembered, so a late or
// repeated result is told "already settled" rather than "unknown".
const settledMemory = 1024

// Request is one parked call.
type Request = observe.ClientToolRequest

// PendingRequest is an unsettled request as listed for a reconnecting client.
type PendingRequest struct {
	Request
	ClaimedBy string `json:"claimed_by"`
}

// ToolError is a failed call's error, from the app or from the daemon.
type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Result ends a call. Content is the app's raw list of MCP content blocks,
// passed through unchanged.
type Result struct {
	OK      bool            `json:"ok"`
	Content json.RawMessage `json:"content,omitempty"`
	Error   *ToolError      `json:"error,omitempty"`
}

func failure(code, message string) Result {
	return Result{Error: &ToolError{Code: code, Message: message}}
}

// CallsOptions injects the call tracker's collaborators.
type CallsOptions struct {
	Registry *Registry
	Publish  observe.Publisher
	// After returns a channel that fires after d (nil: time.After).
	After func(time.Duration) <-chan time.Time
	// Now stamps requests (nil: time.Now).
	Now func() time.Time
	// NewRequestID mints a request id (nil: "ctr_<ulid>").
	NewRequestID func() string
}

type ownerKey struct{ agent, group string }

type parked struct {
	req       Request
	seq       uint64
	claimedBy ClientID
	done      chan struct{}
	res       Result
}

// Calls parks client-tool calls until their owning app settles them. State
// is in memory only: a daemon restart drops every parked call.
type Calls struct {
	opts CallsOptions

	mu      sync.Mutex
	seq     uint64
	pending map[string]*parked
	owners  map[ownerKey]ClientID
	settled map[string]struct{}
	recent  []string // settled ids, oldest first, capped at settledMemory
}

// NewCalls returns a call tracker over opts.Registry.
func NewCalls(opts CallsOptions) *Calls {
	if opts.After == nil {
		opts.After = time.After
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewRequestID == nil {
		ids := chat.NewULIDSource(nil, nil)
		opts.NewRequestID = func() string { return "ctr_" + ids.New() }
	}
	return &Calls{
		opts:    opts,
		pending: map[string]*parked{},
		owners:  map[ownerKey]ClientID{},
		settled: map[string]struct{}{},
	}
}

// Call parks a call to a registered tool and blocks until the owning app
// settles it, it times out (the tool's timeout_s; 0 waits for ctx), ctx ends,
// or the agent goes away. The error is non-nil only for a tool this daemon
// has never seen; every other failure is a Result.
func (c *Calls) Call(ctx context.Context, agent, toolName string, args json.RawMessage) (Result, error) {
	group, t, ok := c.opts.Registry.Lookup(toolName)
	if !ok {
		if c.opts.Registry.EverSeen(toolName) {
			return noClient(), nil
		}
		return Result{}, ErrUnknownTool
	}
	if len(c.opts.Registry.Providers(group)) == 0 {
		return noClient(), nil
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}

	p := c.park(agent, group, t, args)
	c.publishRequest(p.req)

	var timer <-chan time.Time
	if t.TimeoutS > 0 {
		timer = c.opts.After(time.Duration(t.TimeoutS) * time.Second)
	}
	select {
	case <-p.done:
	case <-timer:
		c.finish(p, failure(CodeTimeout, fmt.Sprintf("the app did not answer within %d s", t.TimeoutS)), observe.CancelTimeout)
	case <-ctx.Done():
		c.finish(p, failure(CodeCancelled, "the call was cancelled"), observe.CancelCancelled)
	}
	<-p.done
	return p.res, nil
}

func noClient() Result {
	return failure(CodeNoClient, "No connected Leo app provides this tool")
}

func (c *Calls) park(agent, group string, t Tool, args json.RawMessage) *parked {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	p := &parked{
		seq:  c.seq,
		done: make(chan struct{}),
		req: Request{
			ID: c.opts.NewRequestID(), Agent: agent, Group: group, Tool: t.Name, Args: args,
			Owner: string(c.ownerLocked(agent, group)), TimeoutS: t.TimeoutS,
			CreatedAt: c.opts.Now().UTC().Truncate(time.Millisecond),
		},
	}
	c.pending[p.req.ID] = p
	return p
}

// ownerLocked is the connected client owning (agent, group), if any.
func (c *Calls) ownerLocked(agent, group string) ClientID {
	owner := c.owners[ownerKey{agent, group}]
	if owner != "" && !c.opts.Registry.ClientExists(owner) {
		return ""
	}
	return owner
}

func (c *Calls) publishRequest(req Request) {
	if c.opts.Publish != nil {
		c.opts.Publish.Publish(observe.Event{Type: observe.EventClientToolRequest, Payload: &observe.ClientToolRequestPayload{Request: req}})
	}
}

// finish settles p with res, once. A non-empty reason also announces the
// cancellation so a provider can abort. It reports whether it won.
func (c *Calls) finish(p *parked, res Result, reason string) bool {
	c.mu.Lock()
	if _, ok := c.pending[p.req.ID]; !ok {
		c.mu.Unlock()
		return false
	}
	delete(c.pending, p.req.ID)
	c.rememberLocked(p.req.ID)
	if res.OK && len(res.Content) == 0 {
		res.Content = json.RawMessage(`[]`)
	}
	p.res = res
	close(p.done)
	c.mu.Unlock()

	if reason != "" && c.opts.Publish != nil {
		c.opts.Publish.Publish(observe.Event{Type: observe.EventClientToolCancelled, Payload: &observe.ClientToolCancelledPayload{
			ID: p.req.ID, Agent: p.req.Agent, Reason: reason,
		}})
	}
	return true
}

func (c *Calls) rememberLocked(id string) {
	c.settled[id] = struct{}{}
	c.recent = append(c.recent, id)
	if len(c.recent) > settledMemory {
		delete(c.settled, c.recent[0])
		c.recent = c.recent[1:]
	}
}

// Claim lets client take a parked request. The first claimant of an agent's
// group owns it: later requests for that (agent, group) are claimable only by
// the owner. A claimant re-claiming its own request attaches to it.
func (c *Calls) Claim(id string, client ClientID) (Request, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pending[id]
	if !ok {
		return Request{}, ErrUnknownRequest
	}
	if !c.opts.Registry.ClientExists(client) {
		return Request{}, ErrUnknownClient
	}
	if !providesLocked(c.opts.Registry, p.req.Group, client) {
		return Request{}, ErrNotProvider
	}
	if p.claimedBy != "" && p.claimedBy != client {
		return Request{}, ErrAlreadyClaimed
	}
	key := ownerKey{p.req.Agent, p.req.Group}
	if owner := c.ownerLocked(key.agent, key.group); owner != "" && owner != client {
		return Request{}, ErrOwnedByOther
	}
	p.claimedBy = client
	c.owners[key] = client
	req := p.req
	req.Owner = string(client)
	return req, nil
}

func providesLocked(r *Registry, group string, client ClientID) bool {
	for _, id := range r.Providers(group) {
		if id == client {
			return true
		}
	}
	return false
}

// Settle ends a claimed request with the app's result. Only the claimant may
// settle, and only once.
func (c *Calls) Settle(id string, client ClientID, res Result) error {
	c.mu.Lock()
	p, ok := c.pending[id]
	if !ok {
		_, was := c.settled[id]
		c.mu.Unlock()
		if was {
			return ErrAlreadySettled
		}
		return ErrUnknownRequest
	}
	if p.claimedBy != client {
		c.mu.Unlock()
		return ErrNotClaimant
	}
	c.mu.Unlock()
	if err := validateResult(res); err != nil {
		return err
	}
	if !c.finish(p, res, "") {
		return ErrAlreadySettled
	}
	return nil
}

func validateResult(res Result) error {
	if res.OK {
		if len(res.Content) == 0 {
			return nil
		}
		var blocks []json.RawMessage
		if err := json.Unmarshal(res.Content, &blocks); err != nil {
			return fmt.Errorf("%w: content must be a list of content blocks", ErrInvalidResult)
		}
		return nil
	}
	if res.Error == nil || res.Error.Code == "" {
		return fmt.Errorf("%w: a failed result needs error.code", ErrInvalidResult)
	}
	return nil
}

// Pending lists the unsettled requests, oldest first, with their current
// owner and claimant.
func (c *Calls) Pending() []PendingRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	all := make([]*parked, 0, len(c.pending))
	for _, p := range c.pending {
		all = append(all, p)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].seq < all[j].seq })
	out := make([]PendingRequest, 0, len(all))
	for _, p := range all {
		req := p.req
		req.Owner = string(c.ownerLocked(req.Agent, req.Group))
		out = append(out, PendingRequest{Request: req, ClaimedBy: string(p.claimedBy)})
	}
	return out
}

// CancelAgent ends every parked call of a gone agent.
func (c *Calls) CancelAgent(agent string) {
	for _, p := range c.parkedWhere(func(p *parked) bool { return p.req.Agent == agent }) {
		c.finish(p, failure(CodeCancelled, "the agent is gone"), observe.CancelAgentGone)
	}
}

// FailGroup ends every parked call of a group that lost its last provider.
func (c *Calls) FailGroup(group string) {
	for _, p := range c.parkedWhere(func(p *parked) bool { return p.req.Group == group }) {
		c.finish(p, noClient(), observe.CancelCancelled)
	}
}

func (c *Calls) parkedWhere(match func(*parked) bool) []*parked {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*parked
	for _, p := range c.pending {
		if match(p) {
			out = append(out, p)
		}
	}
	return out
}

// ReleaseClient drops a disconnected client's ownerships and unclaims its
// in-flight requests, announcing them again so another provider can take
// over.
func (c *Calls) ReleaseClient(client ClientID) {
	c.mu.Lock()
	for key, owner := range c.owners {
		if owner == client {
			delete(c.owners, key)
		}
	}
	var again []Request
	for _, p := range c.pending {
		if p.claimedBy == client {
			p.claimedBy = ""
			req := p.req
			req.Owner = string(c.ownerLocked(req.Agent, req.Group))
			again = append(again, req)
		}
	}
	c.mu.Unlock()
	sort.Slice(again, func(i, j int) bool { return again[i].CreatedAt.Before(again[j].CreatedAt) })
	for _, req := range again {
		c.publishRequest(req)
	}
}
