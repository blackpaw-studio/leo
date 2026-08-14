package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/leotools"
	"github.com/blackpaw-studio/leo/internal/templates"
)

// msgPrefixFormat is the wire format prepended to a delivered message so the
// recipient can identify the sender. Keep in sync with any consumer that parses it.
const msgPrefixFormat = leotools.MessagePrefixFormat

// toolDef is the MCP wire shape for a tool.
type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// toolHandler runs a tool against the given args and returns a text result.
type toolHandler func(args map[string]any) (string, error)

// registry holds the tool definitions and their handlers.
type registry struct {
	defs               []toolDef
	handlers           map[string]toolHandler
	contextualHandlers map[string]func(context.Context, map[string]any) (string, error)
	// denied records tools this agent's template took away, so a call to one
	// reports why rather than claiming the tool does not exist. A model that
	// remembers a tool from an earlier session will call it even though
	// tools/list hides it.
	denied map[string]bool
	// perms is the template's permission set, consulted both when
	// registering tools and inside the handlers whose arguments it narrows.
	perms leotools.Permissions
}

// newRegistry builds the Leo tool surface bound to the given daemon client
// and process name (the "self" the slash commands operate on). Local tools
// (currently just leo_skill) are always registered. When client is nil (no
// daemon listener reachable), the daemon-backed tools are omitted entirely
// rather than registered with a handler that would nil-deref or fail on
// every call.
//
// perms is the spawning template's permission set (zero value =
// unrestricted). Denied tools are never registered at all, which removes them
// from tools/list and the handler map in one move; the Can* allowlists are
// enforced inside the handlers they narrow, before the daemon is called.
func newRegistry(client *daemonClient, processName string, perms leotools.Permissions) *registry {
	r := &registry{
		handlers:           make(map[string]toolHandler),
		contextualHandlers: make(map[string]func(context.Context, map[string]any) (string, error)),
		denied:             make(map[string]bool),
		perms:              perms,
	}

	objectSchema := func(props map[string]any, required ...string) map[string]any {
		s := map[string]any{
			"type":       "object",
			"properties": props,
		}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	emptyArgs := objectSchema(map[string]any{})

	r.add(toolDef{
		Name:        "leo_skill",
		Description: "Load Leo's operational instructions on demand. Call with no arguments to list available skills (managing scheduled tasks, reading/debugging logs, daemon control, config reference, workspace maintenance, agent management). Call with `name` set to a skill name to get that skill's full step-by-step instructions. Use this whenever you need to operate Leo.",
		InputSchema: objectSchema(map[string]any{
			"name": map[string]any{"type": "string", "description": "Skill name (with or without .md), e.g. \"managing-tasks\". Omit to list all available skills."},
		}),
	}, func(args map[string]any) (string, error) {
		return handleLeoSkill(args, perms)
	})

	if client == nil {
		return r
	}

	r.add(toolDef{
		Name:        "leo_clear",
		Description: "Clear the supervised Claude's conversation context. Sends '/clear' + Enter via tmux. NOTE: this interrupts the current turn — reply via the channel BEFORE calling this tool, never after.",
		InputSchema: emptyArgs,
	}, func(args map[string]any) (string, error) {
		if err := client.sendKeys(processName, []string{"/clear", "Enter"}); err != nil {
			return "", err
		}
		return "Cleared context for process " + processName, nil
	})

	r.add(toolDef{
		Name:        "leo_compact",
		Description: "Compact the supervised Claude's conversation context. Sends '/compact' + Enter via tmux. NOTE: this interrupts the current turn — reply via the channel BEFORE calling this tool, never after.",
		InputSchema: emptyArgs,
	}, func(args map[string]any) (string, error) {
		if err := client.sendKeys(processName, []string{"/compact", "Enter"}); err != nil {
			return "", err
		}
		return "Compacting context for process " + processName, nil
	})

	r.add(toolDef{
		Name:        "leo_interrupt",
		Description: "Interrupt the current operation in the supervised Claude (sends Escape repeatedly via tmux). Use for the /stop slash command.",
		InputSchema: emptyArgs,
	}, func(args map[string]any) (string, error) {
		if err := client.interrupt(processName); err != nil {
			return "", err
		}
		return "Interrupted process " + processName, nil
	})

	r.add(toolDef{
		Name:        "leo_list_tasks",
		Description: "List all configured Leo scheduled tasks with their schedule, enabled state, and next run time.",
		InputSchema: emptyArgs,
	}, func(args map[string]any) (string, error) {
		data, err := client.listTasks()
		if err != nil {
			return "", err
		}
		return string(data), nil
	})

	r.add(toolDef{
		Name:        "leo_run_task",
		Description: "Trigger a configured Leo task to run immediately (out of schedule).",
		InputSchema: objectSchema(map[string]any{
			"name": map[string]any{"type": "string", "description": "Task name as defined in leo.yaml."},
		}, "name"),
	}, func(args map[string]any) (string, error) {
		name, err := stringArg(args, "name")
		if err != nil {
			return "", err
		}
		if _, err := client.runTask(name); err != nil {
			return "", err
		}
		return "Started task " + name, nil
	})

	r.add(toolDef{
		Name:        "leo_toggle_task",
		Description: "Toggle a Leo task's enabled state (enabled → disabled, disabled → enabled). Persists to leo.yaml.",
		InputSchema: objectSchema(map[string]any{
			"name": map[string]any{"type": "string", "description": "Task name as defined in leo.yaml."},
		}, "name"),
	}, func(args map[string]any) (string, error) {
		name, err := stringArg(args, "name")
		if err != nil {
			return "", err
		}
		data, err := client.toggleTask(name)
		if err != nil {
			return "", err
		}
		return string(data), nil
	})

	r.add(toolDef{
		Name:        "leo_list_templates",
		Description: "List all Leo agent templates available for spawning ephemeral agents.",
		InputSchema: emptyArgs,
	}, func(args map[string]any) (string, error) {
		data, err := client.listTemplates()
		if err != nil {
			return "", err
		}
		return string(data), nil
	})

	r.add(toolDef{
		Name:        "leo_spawn_agent",
		Description: allowNote("Spawn an ephemeral Leo agent from a template, optionally against a repo. Returns the agent's name and workspace path.", "spawn these templates", perms.CanSpawn),
		InputSchema: objectSchema(map[string]any{
			"template": map[string]any{"type": "string", "description": "Template name as defined in leo.yaml templates section."},
			"repo":     map[string]any{"type": "string", "description": "Optional target repo as 'owner/repo' (cloned to a worktree) or a workspace name. Omit to run the template as-is in its own workspace; the agent is named after the template."},
			"name":     map[string]any{"type": "string", "description": "Optional explicit agent name; if omitted, generated from template+repo (or just the template name when repo is omitted)."},
		}, "template"),
	}, func(args map[string]any) (string, error) {
		template, err := stringArg(args, "template")
		if err != nil {
			return "", err
		}
		if !perms.AllowsSpawn(template) {
			return "", denialError("spawn template", template, "templates", perms.CanSpawn)
		}
		repo, _ := args["repo"].(string)
		name, _ := args["name"].(string)
		data, err := client.spawnAgent(template, repo, name)
		if err != nil {
			return "", err
		}
		return string(data), nil
	})

	r.add(toolDef{
		Name:        "leo_list_agents",
		Description: "List all running ephemeral Leo agents with their name, status, and workspace.",
		InputSchema: emptyArgs,
	}, func(args map[string]any) (string, error) {
		data, err := client.listAgents()
		if err != nil {
			return "", err
		}
		return string(data), nil
	})

	r.add(toolDef{
		Name:        "leo_stop_agent",
		Description: "Stop a running ephemeral Leo agent by name.",
		InputSchema: objectSchema(map[string]any{
			"name": map[string]any{"type": "string", "description": "Agent name (or shorthand) returned by leo_list_agents."},
		}, "name"),
	}, func(args map[string]any) (string, error) {
		name, err := stringArg(args, "name")
		if err != nil {
			return "", err
		}
		if _, err := client.stopAgent(name); err != nil {
			return "", err
		}
		return "Stopped agent " + name, nil
	})

	r.add(toolDef{
		Name:        "leo_send_message",
		Description: allowNote("Send a text message to another *running* Leo agent or process (see leo_list_agents). It arrives in the recipient's prompt as a new turn, prefixed with your name, and returns immediately without their reply. 'to' is the target's name; 'message' is the text. NOT for asking a different model a question — to consult a model by name (\"consult fable\", \"ask codex\"), use leo_consult instead; those names are templates, not agents.", "message", perms.CanMessage),
		InputSchema: objectSchema(map[string]any{
			"to":      map[string]any{"type": "string", "description": "Target agent/process name (as shown by leo_list_agents or leo status)."},
			"message": map[string]any{"type": "string", "description": "The message body to deliver."},
		}, "to", "message"),
	}, func(args map[string]any) (string, error) {
		to, err := stringArg(args, "to")
		if err != nil {
			return "", err
		}
		message, err := stringArg(args, "message")
		if err != nil {
			return "", err
		}
		if to == processName {
			return "", fmt.Errorf("cannot send a message to yourself (%q)", processName)
		}
		if !perms.AllowsMessage(to) {
			return "", denialError("message", to, "targets", perms.CanMessage)
		}
		body := fmt.Sprintf(msgPrefixFormat, processName, message)
		if err := client.sendMessage(to, processName, body); err != nil {
			return "", err
		}
		return "Sent message to " + to, nil
	})

	r.addContext(toolDef{
		Name:        "leo_consult",
		Description: allowNote(consultDescription, "consult these templates", perms.CanConsult),
		InputSchema: objectSchema(map[string]any{
			"template": map[string]any{"type": "string", "description": "Template name from leo.yaml supplying harness/model/env."},
			"prompt":   map[string]any{"type": "string", "description": "Self-contained question for the consultant."},
			"model":    map[string]any{"type": "string", "description": "Optional model override, validated against the template's harness."},
		}, "template", "prompt"),
	}, func(ctx context.Context, args map[string]any) (string, error) {
		template, err := stringArg(args, "template")
		if err != nil {
			return "", err
		}
		if !perms.AllowsConsult(template) {
			return "", denialError("consult template", template, "templates", perms.CanConsult)
		}
		prompt, err := stringArg(args, "prompt")
		if err != nil {
			return "", err
		}
		model, _ := args["model"].(string)
		data, err := client.consult(ctx, processName, template, model, prompt)
		if err != nil {
			return "", err
		}
		var result struct {
			Harness string `json:"harness"`
			Model   string `json:"model"`
			Text    string `json:"text"`
		}
		if err := json.Unmarshal(data, &result); err != nil || result.Text == "" {
			return string(data), nil
		}
		return fmt.Sprintf("[consult · %s/%s]\n%s", result.Harness, result.Model, result.Text), nil
	})

	return r
}

// consultDescription is the leo_consult tool description. Named rather than
// inlined so the permission suffix can be appended without the literal
// swallowing the call to allowNote.
const consultDescription = "Run a one-off consultant subagent for a second opinion from another model. " +
	"Use this whenever you are asked to consult, ask, check with, or get a second opinion from another model by name — \"consult fable\", \"ask codex about this\", \"what does opus think\". " +
	"Those names are templates (see leo_list_templates), not running agents, so leo_send_message is the wrong tool for them. " +
	"The template determines the harness and model; `model` optionally overrides the template's model. " +
	"The prompt must be self-contained: the consultant sees none of your conversation, only files in your workspace. " +
	"Waits for and returns the consultant's answer directly. For a council, call this concurrently with different templates and reconcile the returned answers."

// allowNote appends the allowlist to a tool description so the model sees the
// boundary up front instead of discovering it by failing a call. An empty
// allowlist places no restriction, so the description is returned untouched.
func allowNote(description, verb string, allowed []string) string {
	if len(allowed) == 0 {
		return description
	}
	return fmt.Sprintf("%s You may only %s: %s.", description, verb, strings.Join(allowed, ", "))
}

// denialError reports an argument the template's allowlist rejects, naming
// what is permitted so the model can correct itself in one turn rather than
// probing. Callers only reach this with a non-empty allowlist.
func denialError(action, value, noun string, allowed []string) error {
	return fmt.Errorf("not permitted to %s %q; allowed %s: %s", action, value, noun, strings.Join(allowed, ", "))
}

// parsePermissions decodes the LEO_PERMISSIONS payload written at spawn time.
// An empty value means the template placed no restriction. The bool reports
// success: a malformed payload must never be read as "unrestricted", since
// that would hand an agent the full surface precisely when Leo has lost track
// of what it should have.
func parsePermissions(raw string) (leotools.Permissions, bool) {
	if raw == "" {
		return leotools.Permissions{}, true
	}
	var perms leotools.Permissions
	if err := json.Unmarshal([]byte(raw), &perms); err != nil {
		return leotools.Permissions{}, false
	}
	return perms, true
}

// handleLeoSkill is the leo_skill tool handler. It is pure-local: it reads
// embedded skill templates in-process and never touches the daemon client,
// so it ignores the process-scoped args every other handler closes over.
//
// A restricted agent gets a permission notice ahead of the content. The
// skills document several capabilities through more than one route — the
// `leo` CLI and the daemon's HTTP API both spawn agents — so an agent reading
// them cold would try one, burn a turn, and be refused. Telling it up front
// is cheaper than letting it find out.
func handleLeoSkill(args map[string]any, perms leotools.Permissions) (string, error) {
	name, _ := args["name"].(string)

	body, err := func() (string, error) {
		if name == "" {
			return renderSkillCatalog()
		}
		return readNamedSkill(templates.NormalizeSkillName(name))
	}()
	if err != nil {
		return "", err
	}
	return permissionNotice(perms) + body, nil
}

// permissionNotice renders the banner prepended to skill content for a
// restricted agent, or "" when the template restricts nothing.
//
// It says the capability is *withheld* rather than that every route is
// blocked, because that is what is actually true: the leo MCP tools and the
// leo CLI enforce this, while the daemon's HTTP API and another agent's tmux
// session do not. For those, instruction is the only lever there is — so the
// notice names them explicitly instead of implying they are closed.
func permissionNotice(perms leotools.Permissions) string {
	if perms.IsZero() {
		return ""
	}

	var b strings.Builder
	b.WriteString("> **Permissions notice.** This agent's template withholds some Leo capabilities:\n>\n")
	if len(perms.DenyTools) > 0 {
		fmt.Fprintf(&b, "> - denied tools: %s\n", strings.Join(perms.DenyTools, ", "))
	}
	for _, list := range []struct {
		label   string
		entries []string
	}{
		{"may only spawn templates", perms.CanSpawn},
		{"may only message", perms.CanMessage},
		{"may only consult templates", perms.CanConsult},
	} {
		if len(list.entries) > 0 {
			fmt.Fprintf(&b, "> - %s: %s\n", list.label, strings.Join(list.entries, ", "))
		}
	}
	b.WriteString(">\n> Anything below that uses a withheld capability will be refused — through\n")
	b.WriteString("> the leo MCP tools and the `leo` CLI alike. The other routes documented\n")
	b.WriteString("> here reach the same capabilities and are withheld too: do not fall back\n")
	b.WriteString("> to the daemon's HTTP API, another agent's tmux session, or editing\n")
	b.WriteString("> leo.yaml to work around this. Ask the operator instead.\n\n")
	return b.String()
}

// renderSkillCatalog builds the "no arguments" listing of all skills.
func renderSkillCatalog() (string, error) {
	catalog, err := templates.SkillCatalog()
	if err != nil {
		return "", fmt.Errorf("loading skill catalog: %w", err)
	}

	var b strings.Builder
	b.WriteString("Available Leo skills (call leo_skill with name=<name> for full instructions):\n")
	for _, meta := range catalog {
		fmt.Fprintf(&b, "- %s — %s\n", meta.Name, meta.Summary)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// readNamedSkill returns the full content of the named skill, or an error
// listing the valid names if it doesn't exist.
func readNamedSkill(name string) (string, error) {
	for _, file := range templates.SkillFiles() {
		if templates.NormalizeSkillName(file) == name {
			return templates.ReadSkill(file)
		}
	}

	catalog, err := templates.SkillCatalog()
	if err != nil {
		return "", fmt.Errorf("unknown skill %q", name)
	}
	names := make([]string, 0, len(catalog))
	for _, meta := range catalog {
		names = append(names, meta.Name)
	}
	return "", fmt.Errorf("unknown skill %q; valid skills: %s", name, strings.Join(names, ", "))
}

func (r *registry) add(def toolDef, h toolHandler) {
	if r.skip(def.Name) {
		return
	}
	r.defs = append(r.defs, def)
	r.handlers[def.Name] = h
}

func (r *registry) addContext(def toolDef, h func(context.Context, map[string]any) (string, error)) {
	if r.skip(def.Name) {
		return
	}
	r.defs = append(r.defs, def)
	r.contextualHandlers[def.Name] = h
}

// skip reports whether name is denied, recording it so callContext can
// explain the refusal instead of reporting an unknown tool.
func (r *registry) skip(name string) bool {
	if !r.perms.DeniesTool(name) {
		return false
	}
	r.denied[name] = true
	return true
}

func (r *registry) list() []toolDef {
	return r.defs
}

func (r *registry) call(name string, raw json.RawMessage) (string, error) {
	return r.callContext(context.Background(), name, raw)
}

func (r *registry) callContext(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if r.denied[name] {
		return "", fmt.Errorf("tool %q is not permitted for this agent", name)
	}
	if h, ok := r.contextualHandlers[name]; ok {
		var args map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
		}
		return h(ctx, args)
	}
	h, ok := r.handlers[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	args := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	return h(args)
}

func stringArg(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing required argument %q", key)
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", fmt.Errorf("argument %q must be a non-empty string", key)
	}
	return s, nil
}
