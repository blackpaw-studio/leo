// Package clienttools is the daemon's generic client-tools relay: connected
// apps declare tools, agents see them while a providing app is connected, and
// calls are parked until the owning app posts a result. The daemon knows
// nothing about what the tools do (docs/api/client-tools.md).
package clienttools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"sync"

	"github.com/blackpaw-studio/leo/internal/chat"
)

// MaxTools is the most tools one daemon holds across every group.
const MaxTools = 64

// Register error codes (the wire `code` of POST /api/v1/client-tools).
const (
	CodeInvalidTool   = "invalid_tool"
	CodeTooManyTools  = "too_many_tools"
	CodeUnknownClient = "unknown_client"
	CodeNameConflict  = "name_conflict"
)

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ClientID identifies one provider stream ("ct_<ulid>").
type ClientID string

// Tool is one declared tool.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	// TimeoutS is the per-call deadline in seconds; 0 means none.
	TimeoutS int `json:"timeout_s"`
}

// ListedTool is a registered tool with the group that owns it.
type ListedTool struct {
	Group string `json:"group"`
	Tool
}

// Snapshot is the registry's current tools, sorted by name, and its revision.
type Snapshot struct {
	Revision uint64       `json:"revision"`
	Tools    []ListedTool `json:"tools"`
}

// RegisterError is a rejected registration. Tool names the offending tool
// for name conflicts.
type RegisterError struct {
	Code    string
	Message string
	Tool    string
}

func (e *RegisterError) Error() string { return e.Message }

func invalidTool(format string, args ...any) *RegisterError {
	return &RegisterError{Code: CodeInvalidTool, Message: fmt.Sprintf(format, args...)}
}

func conflict(name, why string) *RegisterError {
	return &RegisterError{Code: CodeNameConflict, Message: fmt.Sprintf("tool %q %s", name, why), Tool: name}
}

// RegistryOptions injects the registry's collaborators.
type RegistryOptions struct {
	// IsBuiltin reports whether a name belongs to a built-in leo tool, which
	// a client tool may not shadow. Nil means no built-ins.
	IsBuiltin func(string) bool
	// NewClientID mints a client id. Nil mints "ct_<ulid>".
	NewClientID func() ClientID
}

type group struct {
	tools     []Tool
	providers []ClientID
}

// Registry holds the declared tool groups and the clients providing them.
// Registrations live only as long as their client; see Disconnect.
type Registry struct {
	opts RegistryOptions

	mu       sync.Mutex
	clients  map[ClientID]struct{}
	groups   map[string]*group
	byName   map[string]string // tool name -> group
	seen     map[string]bool   // every tool name ever registered (or seeded)
	revision uint64
}

// NewRegistry returns an empty registry.
func NewRegistry(opts RegistryOptions) *Registry {
	if opts.NewClientID == nil {
		ids := chat.NewULIDSource(nil, nil)
		opts.NewClientID = func() ClientID { return ClientID("ct_" + ids.New()) }
	}
	return &Registry{
		opts:    opts,
		clients: map[ClientID]struct{}{},
		groups:  map[string]*group{},
		byName:  map[string]string{},
		seen:    map[string]bool{},
	}
}

// Connect admits a new client and returns its id.
func (r *Registry) Connect() ClientID {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.opts.NewClientID()
	r.clients[id] = struct{}{}
	return id
}

// Disconnect removes a client and withdraws every group it was the last
// provider of. It returns the revision and whether the tool set changed.
func (r *Registry) Disconnect(id ClientID) (revision uint64, changed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.clients[id]; !ok {
		return r.revision, false
	}
	delete(r.clients, id)
	for name, g := range r.groups {
		g.providers = without(g.providers, id)
		if len(g.providers) > 0 {
			continue
		}
		for _, t := range g.tools {
			delete(r.byName, t.Name)
		}
		delete(r.groups, name)
		changed = true
	}
	if changed {
		r.revision++
	}
	return r.revision, changed
}

// Register declares (or replaces) client id's tools for a group. A client
// re-posting its group replaces it; an identical group from another client
// adds a provider without changing the tool set. It returns the revision and
// whether the tool set changed.
func (r *Registry) Register(id ClientID, groupName string, tools []Tool) (revision uint64, changed bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.clients[id]; !ok {
		return r.revision, false, &RegisterError{Code: CodeUnknownClient, Message: "unknown client"}
	}
	if err := r.validate(groupName, tools); err != nil {
		return r.revision, false, err
	}

	g := r.groups[groupName]
	if g != nil && hasOtherProvider(g, id) {
		if name, same := sameTools(g.tools, tools); !same {
			return r.revision, false, conflict(name, "differs from the definition another client registered for this group")
		}
		g.providers = appendUnique(g.providers, id)
		return r.revision, false, nil
	}

	if total := r.toolCountExcluding(groupName) + len(tools); total > MaxTools {
		return r.revision, false, &RegisterError{Code: CodeTooManyTools, Message: fmt.Sprintf("a daemon may hold at most %d client tools", MaxTools)}
	}

	var old []Tool
	if g != nil {
		old = g.tools
		for _, t := range old {
			delete(r.byName, t.Name)
		}
	} else {
		g = &group{}
		r.groups[groupName] = g
	}
	g.tools = cloneTools(tools)
	g.providers = appendUnique(g.providers, id)
	for _, t := range tools {
		r.byName[t.Name] = groupName
		r.seen[t.Name] = true
	}
	if _, same := sameTools(old, tools); !same || len(old) == 0 {
		changed = true
		r.revision++
	}
	return r.revision, changed, nil
}

// validate checks a registration against the contract without mutating.
func (r *Registry) validate(groupName string, tools []Tool) *RegisterError {
	if !nameRE.MatchString(groupName) {
		return invalidTool("group %q: name must match %s", groupName, nameRE)
	}
	if len(tools) == 0 {
		return invalidTool("group %q declares no tools", groupName)
	}
	if len(tools) > MaxTools {
		return &RegisterError{Code: CodeTooManyTools, Message: fmt.Sprintf("a group may register at most %d tools", MaxTools)}
	}
	seen := map[string]bool{}
	for _, t := range tools {
		if !nameRE.MatchString(t.Name) {
			return invalidTool("tool %q: name must match %s", t.Name, nameRE)
		}
		if seen[t.Name] {
			return invalidTool("tool %q declared twice", t.Name)
		}
		seen[t.Name] = true
		if t.TimeoutS < 0 {
			return invalidTool("tool %q: timeout_s must not be negative", t.Name)
		}
		if err := validateSchema(t.InputSchema); err != nil {
			return invalidTool("tool %q: input_schema %v", t.Name, err)
		}
		if r.opts.IsBuiltin != nil && r.opts.IsBuiltin(t.Name) {
			return conflict(t.Name, "conflicts with an existing tool")
		}
		if owner, ok := r.byName[t.Name]; ok && owner != groupName {
			return conflict(t.Name, "conflicts with an existing tool")
		}
	}
	return nil
}

// validateSchema is a structural check only: an object whose type is
// "object", with an object `properties` and a string-array `required` when
// present. There is no JSON Schema library in the module.
func validateSchema(raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("is required")
	}
	var s struct {
		Type       any                        `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("must be a JSON Schema object: %v", err)
	}
	if s.Type != "object" {
		return fmt.Errorf(`must have "type": "object"`)
	}
	return nil
}

func (r *Registry) toolCountExcluding(groupName string) int {
	n := 0
	for name, g := range r.groups {
		if name != groupName {
			n += len(g.tools)
		}
	}
	return n
}

// Snapshot returns the registered tools, sorted by name, and the revision.
func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	tools := make([]ListedTool, 0, len(r.byName))
	for name, g := range r.groups {
		for _, t := range g.tools {
			tools = append(tools, ListedTool{Group: name, Tool: t})
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return Snapshot{Revision: r.revision, Tools: tools}
}

// Lookup returns a registered tool and the group it belongs to.
func (r *Registry) Lookup(name string) (groupName string, t Tool, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	groupName, ok = r.byName[name]
	if !ok {
		return "", Tool{}, false
	}
	for _, c := range r.groups[groupName].tools {
		if c.Name == name {
			return groupName, c, true
		}
	}
	return "", Tool{}, false
}

// EverSeen reports whether name was ever registered (or seeded), even if it
// has since been withdrawn: a call to it fails with no_client, not as unknown.
func (r *Registry) EverSeen(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[name]
}

// Seed marks names as previously seen, for the names persisted across
// daemon restarts.
func (r *Registry) Seed(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range names {
		r.seen[n] = true
	}
}

// Has reports whether name is a currently registered client tool.
func (r *Registry) Has(name string) bool {
	_, _, ok := r.Lookup(name)
	return ok
}

// Providers returns the clients currently providing a group, oldest first.
func (r *Registry) Providers(groupName string) []ClientID {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.groups[groupName]
	if g == nil {
		return nil
	}
	return append([]ClientID(nil), g.providers...)
}

// ClientExists reports whether id is a connected client.
func (r *Registry) ClientExists(id ClientID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.clients[id]
	return ok
}

// sameTools reports whether two tool sets define the same tools. When they
// differ it names the first tool that does.
func sameTools(a, b []Tool) (firstDiff string, same bool) {
	am := indexTools(a)
	bm := indexTools(b)
	names := make([]string, 0, len(am)+len(bm))
	for n := range am {
		names = append(names, n)
	}
	for n := range bm {
		if _, ok := am[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		x, xok := am[n]
		y, yok := bm[n]
		if !xok || !yok || !sameTool(x, y) {
			return n, false
		}
	}
	return "", true
}

func indexTools(ts []Tool) map[string]Tool {
	m := make(map[string]Tool, len(ts))
	for _, t := range ts {
		m[t.Name] = t
	}
	return m
}

func sameTool(a, b Tool) bool {
	if a.Name != b.Name || a.Description != b.Description || a.TimeoutS != b.TimeoutS {
		return false
	}
	var x, y any
	if json.Unmarshal(a.InputSchema, &x) != nil || json.Unmarshal(b.InputSchema, &y) != nil {
		return bytes.Equal(a.InputSchema, b.InputSchema)
	}
	return reflect.DeepEqual(x, y)
}

func cloneTools(ts []Tool) []Tool {
	out := make([]Tool, len(ts))
	for i, t := range ts {
		t.InputSchema = append(json.RawMessage(nil), t.InputSchema...)
		out[i] = t
	}
	return out
}

func hasOtherProvider(g *group, id ClientID) bool {
	for _, p := range g.providers {
		if p != id {
			return true
		}
	}
	return false
}

func appendUnique(ids []ClientID, id ClientID) []ClientID {
	for _, x := range ids {
		if x == id {
			return ids
		}
	}
	return append(ids, id)
}

func without(ids []ClientID, id ClientID) []ClientID {
	out := ids[:0:0]
	for _, x := range ids {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}
