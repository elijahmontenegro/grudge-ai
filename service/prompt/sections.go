package prompt

import (
	"os"
	"path/filepath"
	"strings"
)

// LoadGrudgeMD reads GRUDGE.md files from mounted working directories.
// Any mounted directory can contain a GRUDGE.md file — all are collected.
func LoadGrudgeMD(workingDirs []string) []string {
	var contents []string
	for _, dir := range workingDirs {
		path := filepath.Join(dir, "GRUDGE.md")
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
