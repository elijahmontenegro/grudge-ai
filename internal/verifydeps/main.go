// Package main runs the repo's dependency-graph invariants — the
// boundary contract as executable checks rather than comments:
//
//   - service/storage must not import any other service/* package.
//     Storage is a leaf.
//
//   - rrc must not pull tiktoken-go or dlclark/regexp2 into its dep
//     graph (opt-in via rrc/tiktoken only), and must never import
//     core — the library promise in rrc/scorer.go.
//
//   - core must not import rrc or service — adapters are below the
//     engine and the application.
//
//   - sandbox and adoc are standalone: no grudge imports at all.
//
//   - adkbridge must not import service/storage — its corpus access
//     is the consumer-defined CorpusStore interface.
//
// Module boundaries make several of these impossible to violate
// without also editing a go.mod; the checks keep the contract
// executable regardless. Must be invoked from the repo root (the
// go.work makes cross-module `go list` resolve). Run via Taskfile:
// `task verify:deps`.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type check struct {
	desc      string
	pkg       string
	forbidden []string
	allowed   []string
}

var checks = []check{
	{
		desc: "service/storage import boundary",
		pkg:  "./service/storage/...",
		// Anything matching forbidden but not allowed is a leak.
		forbidden: []string{"github.com/elijahmontenegro/grudge/service/"},
		allowed:   []string{"github.com/elijahmontenegro/grudge/service/storage"},
	},
	{
		desc: "rrc tokenizer decoupling",
		pkg:  "github.com/elijahmontenegro/grudge/rrc",
		forbidden: []string{
			"github.com/pkoukk/tiktoken-go",
			"github.com/dlclark/regexp2",
		},
	},
	{
		desc:      "rrc never imports core (library promise)",
		pkg:       "github.com/elijahmontenegro/grudge/rrc/...",
		forbidden: []string{"github.com/elijahmontenegro/grudge/core"},
	},
	{
		desc:      "core never imports rrc or service",
		pkg:       "github.com/elijahmontenegro/grudge/core/...",
		forbidden: []string{"github.com/elijahmontenegro/grudge/rrc", "github.com/elijahmontenegro/grudge/service"},
	},
	{
		desc:      "sandbox is standalone (no grudge imports)",
		pkg:       "github.com/elijahmontenegro/grudge/sandbox/...",
		forbidden: []string{"github.com/elijahmontenegro/grudge/"},
		allowed:   []string{"github.com/elijahmontenegro/grudge/sandbox"},
	},
	{
		desc:      "adoc is standalone (no grudge imports)",
		pkg:       "github.com/elijahmontenegro/grudge/adoc/...",
		forbidden: []string{"github.com/elijahmontenegro/grudge/"},
		allowed:   []string{"github.com/elijahmontenegro/grudge/adoc"},
	},
	{
		desc:      "adkbridge corpus access stays interface-shaped (no storage import)",
		pkg:       "./adkbridge/...",
		forbidden: []string{"github.com/elijahmontenegro/grudge/service/storage"},
	},
}

func main() {
	failed := 0
	for _, c := range checks {
		leaks, err := runCheck(c)
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

func runCheck(c check) ([]string, error) {
	cmd := exec.Command("go", "list", "-deps", c.pkg)
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
