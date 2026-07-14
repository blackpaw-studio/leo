package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/templates"
)

// msgPrefixFormat is the wire format prepended to a delivered message so the
// recipient can identify the sender. Keep in sync with any consumer that parses it.
const msgPrefixFormat = "[message from %s] %s"

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
	defs     []toolDef
	handlers map[string]toolHandler
}

// newRegistry builds the Leo tool surface bound to the given daemon client
// and process name (the "self" the slash commands operate on). Local tools
// (currently just leo_skill) are always registered. When client is nil (no
// daemon listener reachable), the daemon-backed tools are omitted entirely
// rather than registered with a handler that would nil-deref or fail on
// every call.
func newRegistry(client *daemonClient, processName string) *registry {
	r := &registry{handlers: make(map[string]toolHandler)}

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
	}, handleLeoSkill)

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
		Description: "Spawn an ephemeral Leo agent from a template, optionally against a repo. Returns the agent's name and workspace path.",
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
		Description: "Send a text message to another Leo agent or process. It arrives in the recipient's Claude prompt as a new turn, prefixed with your name. Use leo_list_agents to discover running agents. 'to' is the target's name; 'message' is the text.",
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
		body := fmt.Sprintf(msgPrefixFormat, processName, message)
		if err := client.sendMessage(to, body); err != nil {
			return "", err
		}
		return "Sent message to " + to, nil
	})

	return r
}

// handleLeoSkill is the leo_skill tool handler. It is pure-local: it reads
// embedded skill templates in-process and never touches the daemon client,
// so it ignores the process-scoped args every other handler closes over.
func handleLeoSkill(args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return renderSkillCatalog()
	}
	return readNamedSkill(templates.NormalizeSkillName(name))
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
	r.defs = append(r.defs, def)
	r.handlers[def.Name] = h
}

func (r *registry) list() []toolDef {
	return r.defs
}

func (r *registry) call(name string, raw json.RawMessage) (string, error) {
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
