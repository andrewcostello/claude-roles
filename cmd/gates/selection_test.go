package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSelectionForPlan(t *testing.T) {
	t.Parallel()
	plans := []plan{{Gate: "build"}, {Gate: "test"}, {Gate: "build"}}
	for _, raw := range []string{"typo", "build,typo", "build,", ",", " "} {
		if _, err := selectionForPlan(raw, plans); err == nil {
			t.Errorf("accepted invalid selection %q", raw)
		}
	}
	if _, err := selectionForPlan("", nil); err == nil {
		t.Fatal("accepted an empty plan")
	}
	if got, err := selectionForPlan("", plans); err != nil || got != nil {
		t.Fatalf("default selection = %v, %v", got, err)
	}
	for mask := 1; mask < 4; mask++ {
		want := map[string]bool{}
		var names []string
		for bit, name := range []string{"build", "test"} {
			if mask&(1<<bit) != 0 {
				want[name] = true
				names = append(names, " "+name+" ", name)
			}
		}
		got, err := selectionForPlan(strings.Join(names, ","), plans)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("mask %d: got %v, %v; want %v", mask, got, err, want)
		}
	}
}

func TestOnlySelectionCLI(t *testing.T) {
	t.Parallel()
	binary := filepath.Join(t.TempDir(), "gates")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	cases := []struct {
		name      string
		only      string
		waiver    string
		dryRun    bool
		noPlan    bool
		wantCode  int
		wantFinal string
	}{
		{name: "unknown", only: "typo", wantCode: exitInvalid, wantFinal: "GATES: INVALID_INPUT"},
		{name: "mixed unknown", only: "build,typo", wantCode: exitInvalid, wantFinal: "GATES: INVALID_INPUT"},
		{name: "empty token", only: "build,", wantCode: exitInvalid, wantFinal: "GATES: INVALID_INPUT"},
		{name: "only delimiters", only: ",,", wantCode: exitInvalid, wantFinal: "GATES: INVALID_INPUT"},
		{name: "only whitespace", only: " ", wantCode: exitInvalid, wantFinal: "GATES: INVALID_INPUT"},
		{name: "not applicable", only: "security", wantCode: exitInvalid, wantFinal: "GATES: INVALID_INPUT"},
		{name: "partial", only: "build", wantCode: exitFail, wantFinal: "GATES: FAIL"},
		{name: "full", wantFinal: "GATES: PASS"},
		{name: "explicit full", only: "build,test", wantFinal: "GATES: PASS"},
		{name: "duplicate and whitespace", only: " build ,test,build", wantFinal: "GATES: PASS"},
		{name: "explicit waiver", only: "build", waiver: "test=isolated diagnostic approved", wantFinal: "GATES: PASS"},
		{name: "invalid dry run", only: "typo", dryRun: true, wantCode: exitInvalid, wantFinal: "GATES: INVALID_INPUT"},
		{name: "valid dry run", only: "build", dryRun: true, wantFinal: "END GATE PLAN"},
		{name: "empty plan", noPlan: true, wantCode: exitInvalid, wantFinal: "GATES: INVALID_INPUT"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			write := func(name string, data []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			write("go.mod", []byte("module fixture\n"))
			state, err := json.Marshal(RunState{
				SchemaVersion: 1, Repo: Repo{Worktree: root},
				Classification: &Classification{Risk: "low", ChangedFiles: []FileClass{{Path: "main.go"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			write("run.json", state)
			trigger := "always"
			if tc.noPlan {
				trigger = "high_or_critical"
			}
			config := `{"schema_version":1,"module_marker":"go.mod","gates":{
			  "build":{"command":"echo build","trigger":"` + trigger + `","scope":"module"},
			  "test":{"command":"echo test","trigger":"` + trigger + `","scope":"module"},
			  "security":{"command":"false","trigger":"high_or_critical","scope":"module"}}}`
			write("gates.json", []byte(config))
			args := []string{"-run-state", filepath.Join(root, "run.json"), "-config", filepath.Join(root, "gates.json")}
			if tc.only != "" {
				args = append(args, "-only", tc.only)
			}
			if tc.waiver != "" {
				args = append(args, "-waive", tc.waiver)
			}
			if tc.dryRun {
				args = append(args, "-dry-run")
			}
			cmd := exec.Command(binary, args...)
			output, err := cmd.CombinedOutput()
			if cmd.ProcessState == nil {
				t.Fatalf("launch: %v", err)
			}
			if got := cmd.ProcessState.ExitCode(); got != tc.wantCode {
				t.Fatalf("exit = %d, want %d\n%s", got, tc.wantCode, output)
			}
			if !strings.Contains(string(output), tc.wantFinal) {
				t.Fatalf("missing %q\n%s", tc.wantFinal, output)
			}
			if tc.wantCode != 0 || tc.dryRun {
				if strings.Contains(string(output), "GATES: PASS") {
					t.Fatalf("non-acceptance reported PASS\n%s", output)
				}
			}
			if tc.wantCode == exitInvalid || tc.dryRun {
				if _, err := os.Stat(filepath.Join(root, "gate-output")); !os.IsNotExist(err) {
					t.Fatalf("non-execution created gate output: %v", err)
				}
				got, err := os.ReadFile(filepath.Join(root, "run.json"))
				if err != nil || string(got) != string(state) {
					t.Fatalf("non-execution changed run state: %v", err)
				}
			}
		})
	}
}
