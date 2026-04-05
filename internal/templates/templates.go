package templates

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed *.md
var fs embed.FS

type AgentData struct {
	Name      string
	UserName  string
	Workspace string
}

type UserProfileData struct {
	UserName    string
	Role        string
	About       string
	Preferences string
	Timezone    string
}

// AgentTemplates returns the available agent template names.
func AgentTemplates() []string {
	return []string{"chief-of-staff", "dev-assistant", "skeleton"}
}

// RenderAgent renders an agent template with the given data.
func RenderAgent(templateName string, data AgentData) (string, error) {
	filename := fmt.Sprintf("agent-%s.md", templateName)
	return renderTemplate(filename, data)
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
