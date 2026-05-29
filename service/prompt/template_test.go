package prompt

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGoldens = flag.Bool("update", false, "rewrite golden files with current output")

// fixture returns a TemplateData with every field populated to a
// known value, varying only Mode. This is the deterministic input
// for snapshot comparison: bytes-identical extractions of fragments
// from system.adoc must produce the same output for the same fixture.
func fixture(mode string) TemplateData {
	return TemplateData{
		UserName:    "emontenegr",
		ThreadName:  "test",
		Sandboxed:   false,
		WorkingDirs: []string{"/test"},
		SpideyMD:    []string{"## test"},
		PlanContent: "",
		PlanDir:     "/plans/test",
		CurrentTime: "2026-05-29T00:00:00Z",
		Mode:        mode,
	}
}

func TestAssembleSnapshot(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		golden string
	}{
		{"normal", "normal", "system_normal.golden"},
		{"plan", "plan", "system_plan.golden"},
		{"autonomous", "autonomous", "system_autonomous.golden"},
	}

	asm, err := NewAssembler()
	if err != nil {
		t.Fatalf("NewAssembler: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := asm.Assemble(fixture(tc.mode))
			if err != nil {
				t.Fatalf("Assemble(%s): %v", tc.mode, err)
			}

			goldenPath := filepath.Join("testdata", tc.golden)

			if *updateGoldens {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatalf("mkdir testdata: %v", err)
				}
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if string(want) != got {
				t.Errorf("assembled prompt differs from %s\n\n--- want ---\n%s\n--- got ---\n%s",
					goldenPath, string(want), got)
			}
		})
	}
}
