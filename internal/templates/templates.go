package templates

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed *.md skills/*.md
var fs embed.FS

type UserProfileData struct {
	UserName    string
	Role        string
	About       string
	Preferences string
	Timezone    string
}

// RenderUserProfile renders the user profile template.
func RenderUserProfile(data UserProfileData) (string, error) {
	return renderTemplate("user-profile.md", data)
}

// SkillFiles returns the list of skill file names to deploy.
func SkillFiles() []string {
	return []string{
		"managing-tasks.md",
		"debugging-logs.md",
		"daemon-management.md",
		"config-reference.md",
		"workspace-maintenance.md",
		"agent-management.md",
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

// SkillMeta is the catalog-level description of a skill: enough to let a
// model decide whether to load the full content via ReadSkill.
type SkillMeta struct {
	// Name is the skill's filename stem (e.g. "managing-tasks"), with no
	// ".md" extension.
	Name string
	// Title is the text of the skill file's leading "# " heading.
	Title string
	// Summary is the first non-empty, non-heading paragraph, collapsed to
	// a single line.
	Summary string
}

// SkillCatalog returns metadata for every embedded skill, parsed from each
// file's leading heading and first descriptive paragraph.
func SkillCatalog() ([]SkillMeta, error) {
	files := SkillFiles()
	catalog := make([]SkillMeta, 0, len(files))

	for _, file := range files {
		content, err := ReadSkill(file)
		if err != nil {
			return nil, fmt.Errorf("building skill catalog: %w", err)
		}

		title, summary := parseSkillMeta(content)
		catalog = append(catalog, SkillMeta{
			Name:    strings.TrimSuffix(file, ".md"),
			Title:   title,
			Summary: summary,
		})
	}

	return catalog, nil
}

// parseSkillMeta extracts a skill's title (the first "# " heading) and
// summary (the first non-empty, non-heading paragraph, joined onto one
// line) from its markdown content.
func parseSkillMeta(content string) (title, summary string) {
	lines := strings.Split(content, "\n")

	var summaryLines []string
	inSummary := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if title == "" && strings.HasPrefix(trimmed, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			continue
		}

		if title == "" {
			continue
		}

		if trimmed == "" {
			if inSummary {
				break
			}
			continue
		}

		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		inSummary = true
		summaryLines = append(summaryLines, trimmed)
	}

	summary = strings.TrimSpace(strings.Join(summaryLines, " "))
	return title, summary
}

// NormalizeSkillName strips an optional ".md" suffix so callers can pass
// either "managing-tasks" or "managing-tasks.md".
func NormalizeSkillName(name string) string {
	return strings.TrimSuffix(name, ".md")
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
