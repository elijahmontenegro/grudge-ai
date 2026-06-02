package prompt

import (
	"os"
	"path/filepath"
	"strings"
)

// LoadAgentsMD reads AGENTS.md files from mounted working directories.
// Any mounted directory can contain a AGENTS.md file — all are collected.
func LoadAgentsMD(workingDirs []string) []string {
	var contents []string
	for _, dir := range workingDirs {
		path := filepath.Join(dir, "AGENTS.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content != "" {
			contents = append(contents, content)
		}
	}
	return contents
}
