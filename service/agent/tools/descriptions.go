package tools

import (
	"embed"
	"strings"
	"unicode"

	"github.com/emontenegr/grudge/service/internal/adoc"
)

// Tool descriptions are authored content the model reads inline with
// every request (alongside the system prompt). They are composed from
// .adoc fragments embedded into the binary — same composition model
// as service/prompt/templates, different consumer.
//
// Per-tool files live under descriptions/<kebab-name>.adoc. Tool names
// are PascalCase ("FileWrite"); description files are kebab-case
// ("file-write.adoc").

//go:embed descriptions/*.adoc
var descriptionsFS embed.FS

// descriptionFor reads the .adoc description for a named tool. Missing
// or malformed files are a build-time bug (embed pattern didn't pick
// up the file, or the file has bad syntax) — panic so it surfaces at
// registration time, not silently as an empty description.
func descriptionFor(name string) string {
	path := "descriptions/" + toolNameToFile(name) + ".adoc"
	out, err := adoc.CompileFS(descriptionsFS, path)
	if err != nil {
		panic("tool description for " + name + ": " + err.Error())
	}
	return strings.TrimSpace(out)
}

// toolNameToFile converts a PascalCase tool name to a kebab-case file
// stem. "FileWrite" → "file-write", "WebSearch" → "web-search",
// "AskUserQuestion" → "ask-user-question".
func toolNameToFile(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('-')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// AllToolNames is the canonical list of tools whose descriptions live
// under descriptions/. Adding a new tool means appending its name here
// and creating the matching .adoc file; the descriptions_test enforces
// the pairing.
var AllToolNames = []string{
	"Bash",
	"FileRead", "FileEdit", "FileWrite", "NotebookEdit",
	"Glob", "Grep",
	"WebSearch", "WebFetch",
	"AskUserQuestion",
	"Agent", "SendMessage",
	"Skill", "TodoWrite",
	"EnterPlanMode", "ExitPlanMode",
	"TaskCreate", "TaskGet", "TaskUpdate", "TaskList", "TaskStop", "TaskOutput",
}

// AllDescriptions returns name→description for every tool in
// AllToolNames. Useful for dumping the full model-facing tool surface
// alongside the system prompt (the two composed halves of the model's
// input).
func AllDescriptions() map[string]string {
	out := make(map[string]string, len(AllToolNames))
	for _, name := range AllToolNames {
		out[name] = descriptionFor(name)
	}
	return out
}
