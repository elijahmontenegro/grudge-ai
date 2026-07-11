package prompt

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/elijahmontenegro/grudge/adoc"
)

// Templates are baked into the binary. The composition root
// `system.adoc` pulls in independent behavioral fragments via
// `include::` directives.

//go:embed templates/*
var templatesFS embed.FS

// Assembler renders the system prompt for each request.
//
// Composition (pass 1: adoc include resolution) runs once at
// NewAssembler. The resulting text is parsed as text/template and
// stored. Per-request Assemble runs pass 2: text/template execution
// against the request-specific TemplateData.
type Assembler struct {
	tmpl *template.Template
}

// TemplateData provides values for template rendering. Deliberately
// absent: the thread name. It is a UI label derived by truncating the
// first message — a lossy echo of corpus content, and injecting it into
// the model's context manufactured false facts twice (a title cut
// mid-claim, "…the archive passphrase is...", read as a truncated
// environment assignment, seeded a self-sustaining refusal chain). The
// prompt boundary obeys the same law as the graph: derived projections
// of the record are not observations. The model's knowledge of the
// conversation is the conversation — window and retrieval carry the
// content losslessly. Environment carries operational facts about the
// WORLD only (sandbox, working dirs, time).
type TemplateData struct {
	UserName    string
	Sandboxed   bool // true when Bash + file tools run inside the Docker workspace
	WorkingDirs []string
	AgentsMD    []string // contents of AGENTS.md files from mounted dirs
	PlanContent string   // compiled plan content (per-turn)
	PlanDir     string   // writable plan directory path
	CurrentTime string
	Mode        string // "normal", "plan", "autonomous"
}

// NewAssembler composes the embedded fragments into a single
// text/template, parsed once at construction. A composition or
// parse failure is a build-time bug (malformed fragment, missing
// include) and is surfaced immediately as an error.
func NewAssembler() (*Assembler, error) {
	composed, err := adoc.CompileFS(templatesFS, "templates/system.adoc")
	if err != nil {
		return nil, fmt.Errorf("compose system prompt: %w", err)
	}
	tmpl, err := template.New("system").Funcs(template.FuncMap{
		"join": strings.Join,
	}).Parse(composed)
	if err != nil {
		return nil, fmt.Errorf("parse composed prompt: %w", err)
	}
	return &Assembler{tmpl: tmpl}, nil
}

// Assemble renders the system prompt against the per-request data.
func (a *Assembler) Assemble(data TemplateData) (string, error) {
	var buf bytes.Buffer
	if err := a.tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
