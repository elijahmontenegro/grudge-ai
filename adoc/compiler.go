package adoc

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	includeRe   = regexp.MustCompile(`include::([^\[]+)\[\]`)
	attributeRe = regexp.MustCompile(`\{([a-zA-Z0-9_-]+)\}`)
	attrDefRe   = regexp.MustCompile(`^:([a-zA-Z0-9_-]+):\s*(.*)$`)
	commentRe   = regexp.MustCompile(`^//.*$`)
)

// Compile resolves include:: directives, AsciiDoc-style attribute
// substitutions, and strips // line comments in an AsciiDoc file
// read from the OS filesystem.
func Compile(entryPath string) (string, error) {
	return compileFile(entryPath, make(map[string]string), make(map[string]bool))
}

// CompileFS is the embed.FS-friendly variant of Compile. Paths inside
// the FS use forward slashes (path.Join semantics) and are interpreted
// as fsys-relative.
func CompileFS(fsys fs.FS, entryPath string) (string, error) {
	return compileFSFile(fsys, entryPath, make(map[string]string), make(map[string]bool))
}

func compileFile(p string, attrs map[string]string, visited map[string]bool) (string, error) {
	absPath, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}

	if visited[absPath] {
		return "", fmt.Errorf("circular include: %s", absPath)
	}
	visited[absPath] = true

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", p, err)
	}

	dir := filepath.Dir(absPath)
	out, err := processLines(string(data), attrs, func(includeRel string) (string, error) {
		return compileFile(filepath.Join(dir, includeRel), attrs, visited)
	})
	if err != nil {
		return "", err
	}

	delete(visited, absPath)
	return out, nil
}

func compileFSFile(fsys fs.FS, p string, attrs map[string]string, visited map[string]bool) (string, error) {
	cleanPath := path.Clean(p)

	if visited[cleanPath] {
		return "", fmt.Errorf("circular include: %s", cleanPath)
	}
	visited[cleanPath] = true

	data, err := fs.ReadFile(fsys, cleanPath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", p, err)
	}

	dir := path.Dir(cleanPath)
	out, err := processLines(string(data), attrs, func(includeRel string) (string, error) {
		return compileFSFile(fsys, path.Join(dir, includeRel), attrs, visited)
	})
	if err != nil {
		return "", err
	}

	delete(visited, cleanPath)
	return out, nil
}

// processLines walks a document body once. Recognized syntax:
//   - `// ...`     line comment (stripped from output)
//   - `:name: val` attribute definition (captured + stripped)
//   - `include::path[]` directive (resolved via resolveInclude)
//   - `{name}` references substituted from attrs.
//
// A trailing newline on the input file is treated as a line
// terminator, not as an empty trailing line — so a file containing
// "hello\n" produces "hello\n" in output, not "hello\n\n".
func processLines(data string, attrs map[string]string, resolveInclude func(string) (string, error)) (string, error) {
	var out strings.Builder
	body := strings.TrimSuffix(data, "\n")
	for _, line := range strings.Split(body, "\n") {
		if commentRe.MatchString(line) {
			continue
		}

		if m := attrDefRe.FindStringSubmatch(line); m != nil {
			attrs[m[1]] = m[2]
			continue
		}

		if m := includeRe.FindStringSubmatch(line); m != nil {
			content, err := resolveInclude(m[1])
			if err != nil {
				return "", err
			}
			out.WriteString(content)
			continue
		}

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
	return out.String(), nil
}
