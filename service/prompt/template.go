package prompt

import (
	"bytes"
	"crypto/sha256"
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
	UserName      string
	ThreadName    string
	WorkingDirs   []string
	SpideyMD      []string // contents of SPIDEY.md files from mounted dirs
	PlanContent   string   // compiled plan content (per-turn)
	CurrentTime   string
	Mode          string // "normal", "plan", "autonomous"
}

// Assemble renders the full system prompt from system.tmpl.
func (a *Assembler) Assemble(data TemplateData) (string, error) {
	entryPath := filepath.Join(a.templateDir, "system.tmpl")
	tmplContent, err := os.ReadFile(entryPath)
	if err != nil {
		return "", err
	}

	funcMap := template.FuncMap{
		"section": func(name string) string {
			return a.renderSection(name, data)
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
	return buf.String(), nil
}

func (a *Assembler) renderSection(name string, data TemplateData) string {
	path := filepath.Join(a.templateDir, name+".tmpl")
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	hash := sha256.Sum256(content)

	a.mu.RLock()
	if cached, ok := a.cache[name]; ok && cached.hash == hash {
		a.mu.RUnlock()
		return cached.output
	}
	a.mu.RUnlock()

	tmpl, err := template.New(name).Parse(string(content))
	if err != nil {
		return ""
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return ""
	}

	output := buf.String()
	a.mu.Lock()
	a.cache[name] = cachedSection{hash: hash, output: output}
	a.mu.Unlock()

	return output
}
