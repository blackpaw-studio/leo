package config

import "fmt"

// ResolveTaskTarget returns the agent name a persistent task delivers its
// prompts to, plus the effective TemplateConfig that agent is (or would be)
// spawned from. When the task names a `template:`, the target is that
// template's agent (agentName = task.Template, implicit = false). Otherwise
// the target is implicit: an agent named after the task itself, backed by a
// TemplateConfig synthesized from the task's own fields (implicit = true).
// Returns an error for unknown tasks, non-persistent tasks, or a `template:`
// referencing a template that does not exist.
func (c *Config) ResolveTaskTarget(taskName string) (string, TemplateConfig, bool, error) {
	task, ok := c.Tasks[taskName]
	if !ok {
		return "", TemplateConfig{}, false, fmt.Errorf("task %q not found", taskName)
	}
	if task.Runtime != "persistent" {
		return "", TemplateConfig{}, false, fmt.Errorf("task %q is not runtime: persistent", taskName)
	}

	if task.Template == "" {
		return taskName, TemplateConfig{
			Workspace:      task.Workspace,
			Model:          task.Model,
			Channels:       task.Channels,
			DevChannels:    task.DevChannels,
			Harness:        task.Harness,
			HarnessOptions: task.HarnessOptions,
			Env:            task.Env,
		}, true, nil
	}

	tmpl, ok := c.Templates[task.Template]
	if !ok {
		return "", TemplateConfig{}, false, fmt.Errorf("task %q references templates.%s which is not defined", taskName, task.Template)
	}
	return task.Template, tmpl, false, nil
}

// channelSubset reports whether every element of want appears in have.
// Returns (missing, false) when an element of want is absent from have.
// Returns ("", true) when want is empty or fully covered.
func channelSubset(want, have []string) (string, bool) {
	set := make(map[string]struct{}, len(have))
	for _, c := range have {
		set[c] = struct{}{}
	}
	for _, c := range want {
		if _, ok := set[c]; !ok {
			return c, false
		}
	}
	return "", true
}
