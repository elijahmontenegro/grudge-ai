package skills

import (
	"os"
	"path/filepath"
	"strings"
)

// Skill represents a loaded skill definition.
type Skill struct {
	Name         string
	Description  string
	WhenToUse    string
	AllowedTools []string
	Arguments    []Argument
	Content      string // markdown body after frontmatter
	Path         string // source file path
}

// Argument is a skill parameter.
type Argument struct {
	Name        string
	Type        string // "string", "boolean", "number"
	Required    bool
	Description string
}

// LoadAll discovers and loads skills from user and project directories.
// Priority: user ($XDG_DATA_HOME/grudge/skills/) then project (.grudge/skills/).
func LoadAll(userSkillsDir string, projectDirs []string) []Skill {
	var skills []Skill

	// User skills
	skills = append(skills, loadFromDir(userSkillsDir)...)

	// Project skills from mounted directories
	for _, dir := range projectDirs {
		projectSkillsDir := filepath.Join(dir, ".grudge", "skills")
		skills = append(skills, loadFromDir(projectSkillsDir)...)
	}

	return skills
}

func loadFromDir(dir string) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var skills []Skill
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		skill, err := parseSkillFile(path)
		if err != nil {
			continue
		}
		skills = append(skills, skill)
	}
	return skills
}

func parseSkillFile(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	content := string(data)
	skill := Skill{Path: path}

	// Split frontmatter and content
	if strings.HasPrefix(content, "---\n") {
		end := strings.Index(content[4:], "\n---")
		if end >= 0 {
			frontmatter := content[4 : 4+end]
			skill.Content = strings.TrimSpace(content[4+end+4:])
			parseFrontmatter(&skill, frontmatter)
		}
	}

	if skill.Name == "" {
		skill.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	return skill, nil
}

func parseFrontmatter(s *Skill, fm string) {
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if key, val, ok := strings.Cut(line, ":"); ok {
			key = strings.TrimSpace(key)
			val = strings.TrimSpace(val)
			switch key {
			case "name":
				s.Name = val
			case "description":
				s.Description = val
			case "when_to_use":
				s.WhenToUse = val
			}
		}
	}
}
