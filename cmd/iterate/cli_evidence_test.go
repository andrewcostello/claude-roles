package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yourorg/claude-workflow/statefile"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("ITERATE_TEST_REVIEWER_MODE"); mode != "" {
		code, err := reviewToolFixture(mode)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(90)
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func fixtureFlag(name string) string {
	value := ""
	for i := 1; i+1 < len(os.Args); i++ {
		if os.Args[i] == name {
			value = os.Args[i+1]
		}
	}
	return value
}

func reviewToolFixture(mode string) (int, error) {
	if mode == "recheck_provider" {
		fmt.Println(`{"verifications":[{"index":0,"status":"RESOLVED","evidence":"fixture"}],"new_findings":[],"summary":"offline fixture"}`)
		return 0, nil
	}
	if mode == "failed_recheck_provider" {
		return 4, nil
	}
	if mode == "no_output" {
		return 0, nil
	}
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return 0, err
	}
	export := FindingsExport{ReviewedSHA: strings.TrimSpace(string(head)), BaseRef: fixtureFlag("-base"), Risk: fixtureFlag("-risk"), Verdict: "APPROVE"}
	path := fixtureFlag("-findings-out")
	switch mode {
	case "iterate":
		export.Verdict = "ITERATE"
		export.Findings = []ExportFinding{{Severity: "HIGH", File: "feature.txt", Line: 1, Title: "fixture finding"}}
	case "reject":
		export.Verdict = "REJECT"
	case "wrong_head":
		export.ReviewedSHA = testHeadSHA
	case "dirty":
		if err := os.WriteFile("feature.txt", []byte("changed during review\n"), 0600); err != nil {
			return 0, err
		}
	case "head_move":
		if out, err := exec.Command("git", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "concurrent change").CombinedOutput(); err != nil {
			return 0, fmt.Errorf("commit: %s: %w", out, err)
		}
	case "state_change":
		if err := statefile.Update(os.Getenv("ITERATE_TEST_STATE_PATH"), nil, func(doc statefile.Document) error {
			var cls statefile.Document
			if err := doc.Decode("classification", &cls); err != nil {
				return err
			}
			if err := cls.Set("human_pr_gate", false); err != nil {
				return err
			}
			return doc.Set("classification", cls)
		}); err != nil {
			return 0, err
		}
	case "locked_state":
		if err := os.WriteFile(os.Getenv("ITERATE_TEST_STATE_PATH")+".lock", []byte("fixture lock"), 0600); err != nil {
			return 0, err
		}
	}
	var data []byte
	if path == "" {
		path = fixtureFlag("-out")
		priorData, err := os.ReadFile(fixtureFlag("-findings"))
		if err != nil {
			return 0, err
		}
		var prior FindingsExport
		if err := json.Unmarshal(priorData, &prior); err != nil {
			return 0, err
		}
		maxNew, err := strconv.Atoi(fixtureFlag("-max-new"))
		if err != nil {
			return 0, err
		}
		rr := RoundResult{Tool: "recheck", Verdict: "APPROVE", Floor: fixtureFlag("-min-severity"), HeadSHA: export.ReviewedSHA, ReviewedSHA: prior.ReviewedSHA, PriorChecked: 1, Resolved: 1, MaxNewGiven: maxNew}
		if mode == "recheck_wrong_prior" {
			rr.ReviewedSHA = testHeadSHA
		}
		if mode == "recheck_changed_input" {
			if err := os.WriteFile(fixtureFlag("-findings"), append(priorData, '\n'), 0600); err != nil {
				return 0, err
			}
		}
		data, err = json.Marshal(rr)
		if err != nil {
			return 0, err
		}
	} else {
		data, err = json.Marshal(export)
		if err != nil {
			return 0, err
		}
	}
	if mode == "empty_object" {
		data = []byte(`{}`)
	}
	if mode == "duplicate_key" {
		data = append(data[:len(data)-1], []byte(`,"verdict":"APPROVE"}`)...)
	}
	if mode == "symlink_result" {
		return 0, os.Symlink(os.Getenv("ITERATE_TEST_OLD_RESULT"), path)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return 0, err
	}
	if mode == "failed_after_output" {
		return 2, nil
	}
	return 0, nil
}

func fixtureGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func cliEvidenceFixture(t *testing.T) (string, string, *RunState) {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "init", "-b", "main")
	fixtureGit(t, repo, "config", "user.name", "Test")
	fixtureGit(t, repo, "config", "user.email", "test@example.invalid")
	fixtureGit(t, repo, "config", "core.hooksPath", "/dev/null")
	fixtureGit(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", "feature.txt")
	fixtureGit(t, repo, "commit", "-m", "base")
	base := fixtureGit(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "commit", "-am", "feature")
	head := fixtureGit(t, repo, "rev-parse", "HEAD")
	state := &RunState{
		SchemaVersion: 1, TaskKey: "X", Repo: Repo{Worktree: repo, BaseRef: "main", BaseSHA: base, HeadSHA: head},
		Classification: &Classification{Risk: "high", RecheckMinSeverity: "high", ReviewerArgs: []string{"-risk", "high", "-base", "main", "-cwd", repo}},
		Gates:          map[string]Gate{"test": {Status: "pass"}},
	}
	path := filepath.Join(dir, "run.json")
	writeJSON(t, path, state)
	return dir, path, state
}

func TestCLIReviewEvidence(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "iterate")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	reviewer, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		mode string
		want int
	}{
		{"approve", 0}, {"iterate", 1}, {"reject", 2}, {"no_output", 2},
		{"failed_after_output", 2}, {"wrong_head", 2}, {"dirty", 2}, {"head_move", 2},
		{"state_change", 2}, {"locked_state", 2}, {"empty_object", 2}, {"duplicate_key", 2},
		{"symlink_result", 2}, {"recheck", 0},
		{"recheck_wrong_prior", 2}, {"recheck_changed_input", 2},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			dir, path, state := cliEvidenceFixture(t)
			oldResult := findingsPath(dir, "X", 1)
			writeJSON(t, oldResult, FindingsExport{Verdict: "APPROVE", ReviewedSHA: state.Repo.HeadSHA, BaseRef: state.Repo.BaseSHA, Risk: "high"})
			if strings.HasPrefix(tc.mode, "recheck") {
				prior := findingsPath(dir, "X", 2)
				writeJSON(t, prior, FindingsExport{Verdict: "ITERATE", ReviewedSHA: state.Repo.BaseSHA, BaseRef: state.Repo.BaseSHA, Risk: "high", Findings: []ExportFinding{{Severity: "HIGH"}}})
				state.Rounds = []Round{
					{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 2},
					{Round: 2, Kind: "full", Verdict: "ITERATE", ReviewedSHA: state.Repo.BaseSHA, NewFindingCount: 1, FindingsPath: prior},
				}
				writeJSON(t, path, state)
			}
			cmd := exec.Command(bin, "run", "-run-state", path, "-reviewer-bin", reviewer, "-recheck-bin", reviewer)
			cmd.Env = append(os.Environ(), "ITERATE_TEST_REVIEWER_MODE="+tc.mode, "ITERATE_TEST_STATE_PATH="+path, "ITERATE_TEST_OLD_RESULT="+oldResult,
				"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
			out, err := cmd.CombinedOutput()
			if code := exitCodeOf(err); code != tc.want {
				t.Fatalf("exit %d, want %d: %s", code, tc.want, out)
			}
			if tc.want == 2 && bytes.Contains(out, []byte("Verdict: APPROVE")) {
				t.Fatalf("refused evidence printed approval: %s", out)
			}
			got, err := readRunState(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.mode == "locked_state" || tc.mode == "state_change" {
				if len(got.Rounds) != 0 || bytes.Contains(out, []byte("=== ROUND ")) {
					t.Fatalf("conflicting evidence recorded: %+v: %s", got.Rounds, out)
				}
				return
			}
			if len(got.Rounds) != len(state.Rounds)+1 {
				t.Fatalf("wrong round count: %+v", got.Rounds)
			}
			last := got.Rounds[len(got.Rounds)-1]
			if verdictExit(last.Verdict) != tc.want {
				t.Fatalf("recorded result differs from exit: %+v", last)
			}
			if tc.mode == "approve" {
				if filepath.Dir(last.FindingsPath) == dir || !strings.HasPrefix(filepath.Base(filepath.Dir(last.FindingsPath)), "iterate-attempt-") {
					t.Fatalf("not an invocation-specific output: %s", last.FindingsPath)
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, args := range [][]string{{"next"}, {"run"}, {"run", "-dry-run"}} {
					args = append(args, "-run-state", path)
					if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
						t.Fatalf("current approval refused: %s: %v", out, err)
					}
					after, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatalf("stop appended a fictitious review: %s: %v", after, err)
					}
				}
				fixtureGit(t, state.Repo.Worktree, "commit", "--allow-empty", "-m", "new revision")
				for _, args := range [][]string{{"next"}, {"run", "-dry-run"}} {
					out, err := exec.Command(bin, append(args, "-run-state", path)...).CombinedOutput()
					if exitCodeOf(err) != exitEscalate {
						t.Fatalf("reused stale approval: %s: %v", out, err)
					}
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("dry-run mutated stale state: %s: %v", after, err)
				}
			}
		})
	}
}

func TestCLIActualRecheckContract(t *testing.T) {
	binDir := t.TempDir()
	iterateBin := filepath.Join(binDir, "iterate")
	recheckBin := filepath.Join(binDir, "recheck")
	for dir, bin := range map[string]string{".": iterateBin, "../recheck": recheckBin} {
		cmd := exec.Command("go", "build", "-o", bin, ".")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %s: %v", dir, out, err)
		}
	}
	provider, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(provider, filepath.Join(binDir, "claude")); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"complete", "no_prior_findings", "failed_provider"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			dir, path, state := cliEvidenceFixture(t)
			prior := findingsPath(dir, "X", 2)
			findings := []ExportFinding{{Severity: "HIGH", File: "feature.txt", Line: 1, Title: "prior finding"}}
			if mode == "no_prior_findings" {
				findings = nil
			}
			writeJSON(t, prior, FindingsExport{Verdict: "ITERATE", ReviewedSHA: state.Repo.BaseSHA, BaseRef: state.Repo.BaseSHA, Risk: "high", Findings: findings})
			state.Rounds = []Round{
				{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 2},
				{Round: 2, Kind: "full", Verdict: "ITERATE", ReviewedSHA: state.Repo.BaseSHA, NewFindingCount: 1, FindingsPath: prior},
			}
			writeJSON(t, path, state)
			providerMode := "recheck_provider"
			want := exitApprove
			if mode != "complete" {
				want = exitEscalate
			}
			if mode == "failed_provider" {
				providerMode = "failed_recheck_provider"
			}
			cmd := exec.Command(iterateBin, "run", "-run-state", path, "-recheck-bin", recheckBin)
			cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "ITERATE_TEST_REVIEWER_MODE="+providerMode)
			out, err := cmd.CombinedOutput()
			if code := exitCodeOf(err); code != want {
				t.Fatalf("actual recheck contract: got %d, want %d: %s", code, want, out)
			}
			got, err := readRunState(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Rounds) != 3 || verdictExit(got.Rounds[2].Verdict) != want {
				t.Fatalf("wrong stored evidence: %+v", got.Rounds)
			}
		})
	}
}
