package templates

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed *.md skills/*.md
var fs embed.FS

type AgentData struct {
	Workspace string
}

type UserProfileData struct {
	UserName    string
	Role        string
	About       string
	Preferences string
	Timezone    string
}

// RenderHeartbeat returns the heartbeat template content.
func RenderHeartbeat() (string, error) {
	content, err := fs.ReadFile("heartbeat.md")
	if err != nil {
		return "", fmt.Errorf("reading heartbeat template: %w", err)
	}
	return string(content), nil
}

// RenderUserProfile renders the user profile template.
func RenderUserProfile(data UserProfileData) (string, error) {
	return renderTemplate("user-profile.md", data)
}

// RenderClaudeWorkspace renders the CLAUDE.md template for the agent workspace.
func RenderClaudeWorkspace(data AgentData) (string, error) {
	return renderTemplate("claude-workspace.md", data)
}

// SkillFiles returns the list of skill file names to deploy.
func SkillFiles() []string {
	return []string{
		"managing-tasks.md",
		"debugging-logs.md",
		"daemon-management.md",
		"config-reference.md",
		"workspace-maintenance.md",
	}
}

// ReadSkill returns the raw content of a skill file.
func ReadSkill(name string) (string, error) {
	content, err := fs.ReadFile("skills/" + name)
	if err != nil {
		return "", fmt.Errorf("reading skill %s: %w", name, err)
	}
	return string(content), nil
}

func renderTemplate(filename string, data any) (string, error) {
	content, err := fs.ReadFile(filename)
	if err != nil {
		return "", fmt.Errorf("reading template %s: %w", filename, err)
	}

	tmpl, err := template.New(filename).Parse(string(content))
	if err != nil {
		return "", fmt.Errorf("parsing template %s: %w", filename, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing template %s: %w", filename, err)
	}

	return buf.String(), nil
}
