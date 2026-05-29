// Command promptdump renders the system prompt with mock TemplateData
// values and prints it to stdout. Mock data covers every TemplateData
// field so dynamics (mode conditionals, range loops, optional sections)
// hydrate. The output is exactly what the model would see for a thread
// in the chosen mode.
//
// Usage:
//
//	go run ./tools/promptdump                 # normal mode
//	go run ./tools/promptdump -mode plan      # plan mode
//	go run ./tools/promptdump -mode autonomous
//
// This is a workflow tool — it lets you read the assembled prompt
// without booting the full service. For regression-locked output
// (bytes-identical across changes) see service/prompt/template_test.go
// which writes goldens to service/prompt/testdata/.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/emontenegr/spidey/service/prompt"
)

func main() {
	mode := flag.String("mode", "normal", "agent mode: normal, plan, autonomous")
	userName := flag.String("user", "emontenegr", "user name for the User section")
	threadName := flag.String("thread", "promptdump", "thread name")
	sandboxed := flag.Bool("sandboxed", false, "sandbox flag")
	flag.Parse()

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
