package prompt

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
)

// Assembler composes system prompts from independent template sections.
// Each section is memoized independently.
type Assembler struct {
	templateDir string
	cache       map[string]cachedSection
	mu          sync.RWMutex
}

type cachedSection struct {
	hash   [32]byte
	output string
}

// NewAssembler creates a prompt assembler reading templates from the given directory.
func NewAssembler(templateDir string) *Assembler {
	return &Assembler{
		templateDir: templateDir,
		cache:       make(map[string]cachedSection),
	}
}

// TemplateData provides values for template rendering.
type TemplateData struct {
	UserName    string
	ThreadName  string
	Sandboxed   bool     // true when Bash + file tools run inside the Docker workspace
	WorkingDirs []string
	SpideyMD    []string // contents of SPIDEY.md files from mounted dirs
	PlanContent string   // compiled plan content (per-turn)
	PlanDir     string   // writable plan directory path
	CurrentTime string
	Mode        string // "normal", "plan", "autonomous"
}

// Assemble renders the full system prompt from system.tmpl.
// Section rendering errors are collected and returned after execution.
func (a *Assembler) Assemble(data TemplateData) (string, error) {
	entryPath := filepath.Join(a.templateDir, "system.tmpl")
	tmplContent, err := os.ReadFile(entryPath)
	if err != nil {
		return "", err
	}

	// Collect section errors during template execution.
	var sectionErrors []string
	var sectionMu sync.Mutex

	funcMap := template.FuncMap{
		"section": func(name string) string {
			result, err := a.renderSection(name, data)
			if err != nil {
				sectionMu.Lock()
				sectionErrors = append(sectionErrors, fmt.Sprintf("section %q: %v", name, err))
				sectionMu.Unlock()
				return ""
			}
			return result
		},
		"join": strings.Join,
	}

	tmpl, err := template.New("system").Funcs(funcMap).Parse(string(tmplContent))
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	if len(sectionErrors) > 0 {
		return "", fmt.Errorf("template section errors: %s", strings.Join(sectionErrors, "; "))
	}

	return buf.String(), nil
}

func (a *Assembler) renderSection(name string, data TemplateData) (string, error) {
	path := filepath.Join(a.templateDir, name+".tmpl")
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Optional section — not every mode uses every section
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	hash := sha256.Sum256(content)

	a.mu.RLock()
	if cached, ok := a.cache[name]; ok && cached.hash == hash {
		a.mu.RUnlock()
		return cached.output, nil
	}
	a.mu.RUnlock()

	tmpl, err := template.New(name).Parse(string(content))
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute %s: %w", name, err)
	}

	output := buf.String()
	a.mu.Lock()
	a.cache[name] = cachedSection{hash: hash, output: output}
	a.mu.Unlock()

	return output, nil
}
