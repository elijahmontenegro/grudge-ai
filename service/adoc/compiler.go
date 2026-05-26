package adoc

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	includeRe   = regexp.MustCompile(`include::([^\[]+)\[\]`)
	attributeRe = regexp.MustCompile(`\{([a-zA-Z0-9_-]+)\}`)
	attrDefRe   = regexp.MustCompile(`^:([a-zA-Z0-9_-]+):\s*(.*)$`)
)

// Compile resolves include:: directives and attribute substitutions in an
// AsciiDoc file. This is infrastructure for plan artifact composition — not
// a full asciidoctor implementation.
func Compile(entryPath string) (string, error) {
	return compileFile(entryPath, make(map[string]string), make(map[string]bool))
}

func compileFile(path string, attrs map[string]string, visited map[string]bool) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	if visited[absPath] {
		return "", fmt.Errorf("circular include: %s", absPath)
	}
	visited[absPath] = true

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}

	dir := filepath.Dir(absPath)
	var out strings.Builder

	for _, line := range strings.Split(string(data), "\n") {
		// Extract attribute definitions
		if m := attrDefRe.FindStringSubmatch(line); m != nil {
			attrs[m[1]] = m[2]
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}

		// Resolve include:: directives
		if m := includeRe.FindStringSubmatch(line); m != nil {
			includePath := filepath.Join(dir, m[1])
			content, err := compileFile(includePath, attrs, visited)
			if err != nil {
				return "", err
			}
			out.WriteString(content)
			continue
		}

		// Substitute attributes
		resolved := attributeRe.ReplaceAllStringFunc(line, func(match string) string {
			name := match[1 : len(match)-1]
			if val, ok := attrs[name]; ok {
				return val
			}
			return match
		})
		out.WriteString(resolved)
		out.WriteByte('\n')
	}

	delete(visited, absPath)
	return out.String(), nil
}
