package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Skill struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Path          string            `json:"path"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	AllowedTools  []string          `json:"allowed_tools,omitempty"`
}

type Memory struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func LoadSkills(sources ...string) ([]Skill, error) {
	byName := map[string]Skill{}
	for _, source := range sources {
		entries, err := os.ReadDir(source)
		if err != nil {
			return nil, fmt.Errorf("agent: load skills from %s: %w", source, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(source, entry.Name(), "SKILL.md")
			skill, err := ParseSkillFile(path)
			if err != nil {
				continue
			}
			byName[skill.Name] = skill
		}
	}
	out := make([]Skill, 0, len(byName))
	for _, skill := range byName {
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func ParseSkillFile(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	content := string(data)
	if !strings.HasPrefix(content, "---\n") {
		return Skill{}, fmt.Errorf("agent: %s has no frontmatter", path)
	}
	rest := strings.TrimPrefix(content, "---\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return Skill{}, fmt.Errorf("agent: %s has unterminated frontmatter", path)
	}
	fields := parseFrontmatter(rest[:end])
	skill := Skill{
		Name:          fields["name"],
		Description:   fields["description"],
		Path:          filepath.ToSlash(path),
		License:       fields["license"],
		Compatibility: fields["compatibility"],
		AllowedTools:  strings.Fields(strings.ReplaceAll(fields["allowed-tools"], ",", " ")),
	}
	if skill.Name == "" || skill.Description == "" {
		return Skill{}, fmt.Errorf("agent: %s missing name or description", path)
	}
	return skill, nil
}

func LoadMemory(paths ...string) ([]Memory, error) {
	out := make([]Memory, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("agent: load memory %s: %w", path, err)
		}
		out = append(out, Memory{Path: filepath.ToSlash(path), Content: string(data)})
	}
	return out, nil
}

func ComposeSystemPrompt(userPrompt string, skills []Skill, memory []Memory) string {
	parts := make([]string, 0, 3)
	if strings.TrimSpace(userPrompt) != "" {
		parts = append(parts, strings.TrimSpace(userPrompt))
	}
	if len(skills) > 0 {
		parts = append(parts, formatSkills(skills))
	}
	if len(memory) > 0 {
		parts = append(parts, formatMemory(memory))
	}
	return strings.Join(parts, "\n\n")
}

func formatSkills(skills []Skill) string {
	var b strings.Builder
	b.WriteString("## Skills\n")
	b.WriteString("You have access to reusable skills. Use their descriptions to decide when a skill applies. Read the referenced SKILL.md file when you need the full workflow.\n\n")
	for _, skill := range skills {
		fmt.Fprintf(&b, "- %s: %s (path: %s", skill.Name, skill.Description, skill.Path)
		annotations := skillAnnotations(skill)
		if annotations != "" {
			fmt.Fprintf(&b, "; %s", annotations)
		}
		b.WriteString(")\n")
	}
	return strings.TrimSpace(b.String())
}

func formatMemory(memory []Memory) string {
	var b strings.Builder
	b.WriteString("## Memory\n")
	for _, item := range memory {
		fmt.Fprintf(&b, "%s\n%s\n\n", item.Path, strings.TrimSpace(item.Content))
	}
	return strings.TrimSpace(b.String())
}

func skillAnnotations(skill Skill) string {
	var parts []string
	if skill.License != "" {
		parts = append(parts, "license: "+skill.License)
	}
	if skill.Compatibility != "" {
		parts = append(parts, "compatibility: "+skill.Compatibility)
	}
	if len(skill.AllowedTools) > 0 {
		parts = append(parts, "allowed-tools: "+strings.Join(skill.AllowedTools, ", "))
	}
	return strings.Join(parts, "; ")
}

func parseFrontmatter(raw string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return fields
}
