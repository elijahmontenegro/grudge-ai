// Command promptdump renders the full model-facing surface with mock
// data and prints it to stdout. The model sees TWO composed authored
// surfaces every turn:
//
//  1. The system prompt — composed from service/prompt/templates/*.adoc
//  2. The tool schemas — Name + Description per tool, composed from
//     service/agent/tools/descriptions/*.adoc
//
// By default this command prints BOTH because both are what the model
// actually sees. Use -prompt=false or -tools=false to suppress one
// surface; -mode picks plan/autonomous variants of the system prompt.
//
// Usage:
//
//	go run ./tools/promptdump                    # both surfaces (default)
//	go run ./tools/promptdump -mode plan         # both, with plan-mode prompt
//	go run ./tools/promptdump -tools=false       # just the system prompt
//	go run ./tools/promptdump -prompt=false      # just the tool descriptions
//
// This is a workflow tool — it lets you read the assembled model-facing
// surface without booting the full service. For regression-locked
// snapshots see service/prompt/template_test.go.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/elijahmontenegro/grudge/service/agent/tools"
	"github.com/elijahmontenegro/grudge/service/prompt"
)

func main() {
	mode := flag.String("mode", "normal", "agent mode: normal, plan, autonomous")
	userName := flag.String("user", "elijahmontenegro", "user name for the User section")
	sandboxed := flag.Bool("sandboxed", false, "sandbox flag")
	showPrompt := flag.Bool("prompt", true, "render the system prompt")
	showTools := flag.Bool("tools", true, "render the tool descriptions")
	flag.Parse()

	if !*showPrompt && !*showTools {
		fmt.Fprintln(os.Stderr, "both -prompt and -tools are false; nothing to render")
		os.Exit(2)
	}

	if *showPrompt {
		fmt.Println("================================================================")
		fmt.Println("SYSTEM PROMPT (composed from service/prompt/templates/*.adoc)")
		fmt.Println("================================================================")
		asm, err := prompt.NewAssembler()
		if err != nil {
			fmt.Fprintf(os.Stderr, "NewAssembler: %v\n", err)
			os.Exit(1)
		}
		data := prompt.TemplateData{
			UserName:    *userName,
			Sandboxed:   *sandboxed,
			WorkingDirs: []string{"/example/working/dir"},
			AgentsMD:    []string{"## Example project instructions\n\nMounted AGENTS.md content lands here."},
			PlanContent: "",
			PlanDir:     "/example/plan/dir",
			CurrentTime: "2026-05-29T00:00:00Z (mock)",
			Mode:        *mode,
		}
		out, err := asm.Assemble(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Assemble: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(out)
	}

	if *showTools {
		if *showPrompt {
			fmt.Println()
		}
		fmt.Println("================================================================")
		fmt.Println("TOOL SCHEMAS (Name + Description per tool — fed to the model")
		fmt.Println("via ADK FunctionDeclaration alongside the system prompt)")
		fmt.Println("================================================================")
		descs := tools.AllDescriptions()
		names := make([]string, 0, len(descs))
		for n := range descs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Println()
			fmt.Printf("--- %s ---\n", name)
			fmt.Println(descs[name])
		}
	}
}
