// Command promptdump renders the model-facing surface with mock data
// and prints it to stdout. The model sees TWO composed authored
// surfaces every turn:
//
//  1. The system prompt — composed from service/prompt/templates/*.adoc
//  2. The tool schemas — Name + Description per tool, composed from
//     service/agent/tools/descriptions/*.adoc
//
// By default this command prints (1). Pass -tools to also print (2),
// or -only-tools to print only the tool descriptions.
//
// Usage:
//
//	go run ./tools/promptdump                       # system prompt, normal mode
//	go run ./tools/promptdump -mode plan            # system prompt, plan mode
//	go run ./tools/promptdump -tools                # system prompt + all tool descriptions
//	go run ./tools/promptdump -only-tools           # only tool descriptions
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

	"github.com/emontenegr/spidey/service/agent/tools"
	"github.com/emontenegr/spidey/service/prompt"
)

func main() {
	mode := flag.String("mode", "normal", "agent mode: normal, plan, autonomous")
	userName := flag.String("user", "emontenegr", "user name for the User section")
	threadName := flag.String("thread", "promptdump", "thread name")
	sandboxed := flag.Bool("sandboxed", false, "sandbox flag")
	showTools := flag.Bool("tools", false, "also print all tool descriptions")
	onlyTools := flag.Bool("only-tools", false, "print only tool descriptions, skip system prompt")
	flag.Parse()

	if !*onlyTools {
		asm, err := prompt.NewAssembler()
		if err != nil {
			fmt.Fprintf(os.Stderr, "NewAssembler: %v\n", err)
			os.Exit(1)
		}

		data := prompt.TemplateData{
			UserName:    *userName,
			ThreadName:  *threadName,
			Sandboxed:   *sandboxed,
			WorkingDirs: []string{"/example/working/dir"},
			SpideyMD:    []string{"## Example project instructions\n\nMounted SPIDEY.md content lands here."},
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

	if *showTools || *onlyTools {
		if !*onlyTools {
			fmt.Println()
			fmt.Println("================================================================")
			fmt.Println("TOOL DESCRIPTIONS (second composed surface the model sees)")
			fmt.Println("================================================================")
		}
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
