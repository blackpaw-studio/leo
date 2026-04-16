package mcp

import (
	"encoding/json"
	"fmt"
)

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

// newRegistry builds the full Leo tool surface bound to the given daemon
// client and process name (the "self" the slash commands operate on).
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
		Description: "Spawn an ephemeral Leo agent from a template against a repo. Returns the agent's name and workspace path.",
		InputSchema: objectSchema(map[string]any{
			"template": map[string]any{"type": "string", "description": "Template name as defined in leo.yaml templates section."},
			"repo":     map[string]any{"type": "string", "description": "Target repo as 'owner/repo' (cloned to a worktree) or a workspace name."},
			"name":     map[string]any{"type": "string", "description": "Optional explicit agent name; if omitted, generated from template+repo."},
		}, "template", "repo"),
	}, func(args map[string]any) (string, error) {
		template, err := stringArg(args, "template")
		if err != nil {
			return "", err
		}
		repo, err := stringArg(args, "repo")
		if err != nil {
			return "", err
		}
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

	return r
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
