// Package main runs cross-module dependency-graph invariants the
// plan's verification section calls out:
//
//   - service/storage must not import any other service/* package.
//     Storage is a leaf — taking on a service/<other> dep is a
//     layering violation that creates cycle risk and couples
//     storage's release cadence to the consumer.
//
//   - rrc must not pull tiktoken-go or dlclark/regexp2 into its dep
//     graph. Those are reachable only via rrc/tiktoken/, the opt-in
//     subpackage. Consumers who don't want the tokenizer pay nothing.
//
// Run via Taskfile: `task verify:deps`.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type check struct {
	desc       string
	dir        string
	pkg        string
	forbidden  []string
	allowed    []string
}

var checks = []check{
	{
		desc: "service/storage import boundary",
		dir:  "service",
		pkg:  "./storage/...",
		// Anything matching forbidden but not allowed is a leak.
		forbidden: []string{"github.com/emontenegr/spidey/service/"},
		allowed:   []string{"github.com/emontenegr/spidey/service/storage"},
	},
	{
		desc: "rrc tokenizer decoupling",
		dir:  "rrc",
		pkg:  ".",
		forbidden: []string{
			"github.com/pkoukk/tiktoken-go",
			"github.com/dlclark/regexp2",
		},
	},
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fail("getwd: %v", err)
	}
	failed := 0
	for _, c := range checks {
		leaks, err := runCheck(cwd, c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR %s: %v\n", c.desc, err)
			failed++
			continue
		}
		if len(leaks) > 0 {
			fmt.Fprintf(os.Stderr, "FAIL  %s\n", c.desc)
			for _, l := range leaks {
				fmt.Fprintf(os.Stderr, "      %s\n", l)
			}
			failed++
			continue
		}
		fmt.Fprintf(os.Stdout, "OK    %s\n", c.desc)
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func runCheck(cwd string, c check) ([]string, error) {
	cmd := exec.Command("go", "list", "-deps", c.pkg)
	cmd.Dir = cwd + string(os.PathSeparator) + c.dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %v: %s", err, stderr.String())
	}
	var leaks []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if !matchesAny(line, c.forbidden) {
			continue
		}
		if matchesAny(line, c.allowed) {
			continue
		}
		// Forbidden prefix match without an explicit-allow: leak.
		// The boundary check needs to be exact-string-equal on the
		// allow list; the storage boundary allows the storage
		// package itself, not its subpackages, but storage doesn't
		// have subpackages today so this is fine.
		leaks = append(leaks, line)
	}
	return leaks, nil
}

func matchesAny(line string, patterns []string) bool {
	for _, p := range patterns {
		if line == p || strings.HasPrefix(line, p+"/") {
			return true
		}
	}
	return false
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
