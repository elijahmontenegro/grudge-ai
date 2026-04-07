package prompt

import (
	"os"
	"path/filepath"
	"strings"
)

// LoadSpideyMD reads SPIDEY.md files from mounted working directories.
// Any mounted directory can contain a SPIDEY.md file — all are collected.
func LoadSpideyMD(workingDirs []string) []string {
	var contents []string
	for _, dir := range workingDirs {
		path := filepath.Join(dir, "SPIDEY.md")
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
