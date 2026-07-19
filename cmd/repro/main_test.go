package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyRuns(t *testing.T) {
	cases := []struct {
		passes []bool
		want   string
	}{
		{[]bool{false, false, false}, "RED"},
		{[]bool{true, true, true}, "GREEN"},
		{[]bool{true, false, true}, "FLAKY"},
		{[]bool{false}, "RED"},
		{nil, "FLAKY"},
	}
	for _, c := range cases {
		if got := classifyRuns(c.passes); got != c.want {
			t.Errorf("classifyRuns(%v) = %s, want %s", c.passes, got, c.want)
		}
	}
}

func TestExtractStepsFromTSDocstring(t *testing.T) {
	spec := `/**
 * FSG-9999 / SMG-9999 — funds absent from station INITIAL frame.
 *
 * Steps:
 * 1. Create a session and join two players with known wallet balances.
 * 2. Open StreamSessionState with no resume cursor (forces INITIAL snapshot).
 * 3. Assert every roster player's funds field is populated in the frame.
 *
 * Status: fail-on-main, sealing SMG-9999.
 */
import { test } from '@playwright/test';
`
	path := filepath.Join(t.TempDir(), "spec.spec.ts")
	os.WriteFile(path, []byte(spec), 0644)

	steps, err := extractSteps(path)
	if err != nil {
		t.Fatalf("extractSteps: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("steps = %v, want 3 entries", steps)
	}
	if !strings.Contains(steps[1], "no resume cursor") {
		t.Errorf("step 2 = %q, want the INITIAL-snapshot step", steps[1])
	}
}

func TestExtractStepsStopsAtBlankAndMissing(t *testing.T) {
	spec := `// Repro for SMG-1.
// Steps:
// 1. Do the thing.
// 2. Assert the thing.
//
// Status: forward-looking guard.
`
	path := filepath.Join(t.TempDir(), "spec.spec.ts")
	os.WriteFile(path, []byte(spec), 0644)
	steps, _ := extractSteps(path)
	if len(steps) != 2 {
		t.Fatalf("steps = %v, want exactly 2 (capture must stop at blank comment line)", steps)
	}

	noSteps := filepath.Join(t.TempDir(), "bare.spec.ts")
	os.WriteFile(noSteps, []byte("// no steps here\n"), 0644)
	if steps, _ := extractSteps(noSteps); len(steps) != 0 {
		t.Errorf("expected no steps, got %v", steps)
	}
}

func TestParseUIResources(t *testing.T) {
	data := []byte(`{"items":[
		{"metadata":{"name":"bay-session"},"status":{"updateStatus":"ok","runtimeStatus":"ok"}},
		{"metadata":{"name":"smg"},"status":{"updateStatus":"in_progress","runtimeStatus":"ok"}},
		{"metadata":{"name":"nats"},"status":{"updateStatus":"ok","runtimeStatus":"error"}},
		{"metadata":{"name":"(Tiltfile)"},"status":{"updateStatus":"ok","runtimeStatus":"not_applicable"}}
	]}`)
	notOK, err := parseUIResources(data)
	if err != nil {
		t.Fatalf("parseUIResources: %v", err)
	}
	if len(notOK) != 2 {
		t.Fatalf("notOK = %v, want smg (building) and nats (error)", notOK)
	}
	joined := strings.Join(notOK, " ")
	if !strings.Contains(joined, "smg") || !strings.Contains(joined, "nats") {
		t.Errorf("notOK = %v, want smg and nats flagged", notOK)
	}
}

func TestResolveSuiteAndRelPaths(t *testing.T) {
	cfg := runConfig{worktree: "/wt", spec: "tests/e2e/playwright/tests/cross-app/foo.spec.ts"}
	if err := resolveSuite(&cfg, "", ""); err != nil {
		t.Fatalf("resolveSuite: %v", err)
	}
	if cfg.suite != "playwright" || cfg.dir != "/wt/tests/e2e/playwright" {
		t.Errorf("suite/dir = %s/%s", cfg.suite, cfg.dir)
	}
	if cfg.argv[3] != "tests/cross-app/foo.spec.ts" {
		t.Errorf("playwright rel path = %q", cfg.argv[3])
	}

	cfg = runConfig{worktree: "/wt", spec: "features/fullswing_login_no_qr.feature"}
	if err := resolveSuite(&cfg, "", ""); err != nil {
		t.Fatalf("resolveSuite karate: %v", err)
	}
	if cfg.suite != "karate" || cfg.argv[1] != "run-tests.sh" {
		t.Errorf("karate argv = %v", cfg.argv)
	}

	cfg = runConfig{worktree: "/wt", spec: "some/pkg_test.go"}
	if err := resolveSuite(&cfg, "", ""); err == nil {
		t.Error("unknown suite must require -cmd")
	}

	cfg = runConfig{worktree: "/wt"}
	if err := resolveSuite(&cfg, "go test -run TestX ./pkg/...", ""); err != nil || cfg.suite != "custom" || cfg.dir != "/wt" {
		t.Errorf("custom suite = %+v err=%v", cfg, err)
	}
}

func TestJiraCommentCarriesStepsAndSHAs(t *testing.T) {
	cfg := runConfig{spec: "tests/cross-app/foo.spec.ts", jiraKey: "SMG-9999"}
	pre := preflightResult{
		Tilt:        &stackProcess{RepoRoot: "/checkout", SHA: "aaaabbbbccccdddd"},
		ResourcesOK: true,
		Clients: map[string]*stackProcess{
			"burrito-golf": {RepoRoot: "/checkout", SHA: "aaaabbbbccccdddd"},
		},
	}
	steps := []string{"Join two players.", "Open INITIAL snapshot.", "Assert funds present."}
	comment := buildJiraComment(cfg, "RED", []bool{false, false, false}, "/wt", "eeeeffff00001111", false, pre, "PASS", steps, "expect(funds).toBeDefined()\n1 failed")

	for _, want := range []string{"aaaabbbbcccc", "eeeeffff0000", "Open INITIAL snapshot.", "please confirm these match the reported bug", "regression seal", "attached to this ticket"} {
		if !strings.Contains(comment, want) {
			t.Errorf("comment missing %q", want)
		}
	}

	// A comment with no steps must carry the do-not-trust warning.
	empty := buildJiraComment(cfg, "RED", []bool{false}, "/wt", "e1", false, pre, "PASS", nil, "out")
	if !strings.Contains(empty, "do not trust this run") {
		t.Error("no-steps comment must warn the reader")
	}
}
