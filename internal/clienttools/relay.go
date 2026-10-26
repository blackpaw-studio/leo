package clienttools

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// RelayOptions injects the relay's collaborators.
type RelayOptions struct {
	Registry RegistryOptions
	// Calls carries the call tracker's clock and id seams; its Registry and
	// Publish fields are set by NewRelay.
	Calls CallsOptions
	// Flag persists the ever-registered flag and names. Required.
	Flag *EverRegistered
	// Publish receives client_tools.* events. Nil publishes nothing.
	Publish observe.Publisher
}

// Relay is the client-tools relay: the registry of declared tools, the
// parked calls, and the persisted ever-registered flag, behind one seam for
// the HTTP handlers, the event stream and the agents' MCP servers.
type Relay struct {
	registry *Registry
	calls    *Calls
	flag     *EverRegistered
	publish  observe.Publisher

	// mu serializes registry changes with the follow-up work they trigger
	// (failing parked calls, announcing), so those see a consistent diff.
	mu sync.Mutex
}

// NewRelay builds a relay and seeds its registry with the persisted names.
func NewRelay(opts RelayOptions) *Relay {
	reg := NewRegistry(opts.Registry)
	reg.Seed(opts.Flag.Names())
	callOpts := opts.Calls
	callOpts.Registry = reg
	callOpts.Publish = opts.Publish
	return &Relay{registry: reg, calls: NewCalls(callOpts), flag: opts.Flag, publish: opts.Publish}
}

// ListData is the tool listing the HTTP API and the MCP server read.
type ListData struct {
	Revision       uint64       `json:"revision"`
	EverRegistered bool         `json:"ever_registered"`
	Tools          []ListedTool `json:"tools"`
}

// Connect admits a provider stream and returns its client id.
func (r *Relay) Connect() ClientID { return r.registry.Connect() }

// Disconnect ends a provider stream: its tools are withdrawn (unless another
// client provides them), its ownerships released, and calls parked on a
// group that lost its last provider fail with no_client.
func (r *Relay) Disconnect(id ClientID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	before := groupSet(r.registry.Snapshot())
	if _, changed := r.registry.Disconnect(id); changed {
		after := groupSet(r.registry.Snapshot())
		for g := range before {
			if !after[g] {
				r.calls.FailGroup(g)
			}
		}
		r.announceLocked()
	}
	r.calls.ReleaseClient(id)
}

// Register declares a client's tools for a group and returns the revision.
func (r *Relay) Register(id ClientID, group string, tools []Tool) (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rev, changed, err := r.registry.Register(id, group, tools)
	if err != nil {
		return rev, err
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	r.flag.Record(names)
	if changed {
		r.announceLocked()
	}
	return rev, nil
}

func (r *Relay) announceLocked() {
	if r.publish == nil {
		return
	}
	snap := r.registry.Snapshot()
	names := make([]string, 0, len(snap.Tools))
	for _, t := range snap.Tools {
		names = append(names, t.Name)
	}
	r.publish.Publish(observe.Event{Type: observe.EventClientToolsChanged, Payload: &observe.ClientToolsChangedPayload{
		Revision: snap.Revision, Tools: names,
	}})
}

func groupSet(s Snapshot) map[string]bool {
	out := map[string]bool{}
	for _, t := range s.Tools {
		out[t.Group] = true
	}
	return out
}

// ClientExists reports whether id is a connected provider stream.
func (r *Relay) ClientExists(id ClientID) bool { return r.registry.ClientExists(id) }

// List returns the registered tools, the revision and the ever-registered flag.
func (r *Relay) List() ListData {
	snap := r.registry.Snapshot()
	return ListData{Revision: snap.Revision, EverRegistered: r.flag.Registered(), Tools: snap.Tools}
}

// IsKnownName reports whether name is, or ever was, a client tool on this
// daemon — including names persisted from before a restart.
func (r *Relay) IsKnownName(name string) bool { return r.registry.EverSeen(name) }

// Call runs a client tool for an agent; see Calls.Call.
func (r *Relay) Call(ctx context.Context, agent, tool string, args json.RawMessage) (Result, error) {
	return r.calls.Call(ctx, agent, tool, args)
}

// Claim lets a provider take a parked request; see Calls.Claim.
func (r *Relay) Claim(id string, client ClientID) (Request, error) { return r.calls.Claim(id, client) }

// Settle ends a claimed request; see Calls.Settle.
func (r *Relay) Settle(id string, client ClientID, res Result) error {
	return r.calls.Settle(id, client, res)
}

// Pending lists the unsettled requests.
func (r *Relay) Pending() []PendingRequest { return r.calls.Pending() }

// CancelAgent ends a gone agent's parked calls.
func (r *Relay) CancelAgent(agent string) { r.calls.CancelAgent(agent) }

// ProviderStream adapts a Relay to the string ids the events stream uses.
type ProviderStream struct{ relay *Relay }

// Provider returns the adapter for a provider events stream.
func (r *Relay) Provider() ProviderStream { return ProviderStream{relay: r} }

// Connect admits the stream and returns its client id.
func (p ProviderStream) Connect() string { return string(p.relay.Connect()) }

// Disconnect ends the stream's client.
func (p ProviderStream) Disconnect(id string) { p.relay.Disconnect(ClientID(id)) }
