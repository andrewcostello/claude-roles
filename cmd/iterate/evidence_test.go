package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourorg/claude-workflow/statefile"
)

const (
	testHeadSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testBaseSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestAppendRoundPreservesCompleteForeignEvidence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run.json")
	seed := []byte(`{"schema_version":1,"repo":{"worktree":"/wt","base_sha":"abc1234","base_ref":"main"},
	"classification":{"risk":"critical","human_pr_gate":true,"financial_paths_touched":true,"panel":{"preset":"full","seats":4}},
	"gates":{"test":{"status":"pass","command":"go test -race ./...","metrics":{"units":9007199254740993}}},
	"rounds":[{"round":1,"kind":"full","verdict":"ITERATE","opaque":{"integer":9007199254740993}}],
	"pr":{"number":9007199254740993}}`)
	if err := os.WriteFile(path, seed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := appendRound(path, Round{Round: 2, Kind: "full", Verdict: "ITERATE"}, "ITERATE", ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal(seed, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"repo", "classification", "gates", "pr"} {
		var want, got bytes.Buffer
		if err := json.Compact(&want, before[key]); err != nil {
			t.Fatal(err)
		}
		if err := json.Compact(&got, after[key]); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want.Bytes(), got.Bytes()) {
			t.Errorf("foreign %s changed: got %s, want %s", key, got.Bytes(), want.Bytes())
		}
	}
	if !bytes.Contains(after["rounds"], []byte("9007199254740993")) {
		t.Error("previous round's complete evidence was lost")
	}
}

func TestFailedReviewerCannotReuseApprovalFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "findings.json")
	writeJSON(t, path, FindingsExport{Verdict: "APPROVE", ReviewedSHA: testHeadSHA, BaseRef: testBaseSHA, Risk: "high", Findings: []ExportFinding{}})
	round, err := recordFull(decision{Round: 1, Floor: "high"}, path, 2)
	if err == nil || round.Verdict != "ESCALATE" || round.Status != "review_unavailable" {
		t.Fatalf("failed reviewer reused old approval: %+v, %v", round, err)
	}
}

func TestRecheckRejectsInconsistentResult(t *testing.T) {
	t.Parallel()
	mutations := map[string]func(*RoundResult){
		"missing head":      func(r *RoundResult) { r.HeadSHA = "" },
		"wrong head":        func(r *RoundResult) { r.HeadSHA = testBaseSHA },
		"wrong prior":       func(r *RoundResult) { r.ReviewedSHA = testHeadSHA },
		"wrong tool":        func(r *RoundResult) { r.Tool = "reviewer" },
		"wrong floor":       func(r *RoundResult) { r.Floor = "low" },
		"wrong budget":      func(r *RoundResult) { r.MaxNewGiven = 100 },
		"wrong exit":        func(r *RoundResult) { r.ExitCode = 1 },
		"unknown verdict":   func(r *RoundResult) { r.Verdict = "PASS" },
		"negative count":    func(r *RoundResult) { r.NewAtFloor = -1 },
		"unaccounted prior": func(r *RoundResult) { r.Resolved = 0 },
		"overcounted prior": func(r *RoundResult) { r.Resolved = 2 },
		"still open":        func(r *RoundResult) { r.Resolved = 0; r.StillOpen = 1 },
		"new findings":      func(r *RoundResult) { r.NewAtFloor = 1 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			rr := RoundResult{Tool: "recheck", Verdict: "APPROVE", Floor: "high", HeadSHA: testHeadSHA, ReviewedSHA: testBaseSHA, PriorChecked: 1, Resolved: 1, MaxNewGiven: 2}
			mutate(&rr)
			path := filepath.Join(t.TempDir(), "round.json")
			writeJSON(t, path, rr)
			round, err := recordRecheck(decision{Round: 3, Floor: "high", MaxNew: 2, HeadSHA: testHeadSHA, PriorSHA: testBaseSHA, PriorCount: 1}, path, 0)
			if err == nil || round.Verdict != "ESCALATE" {
				t.Fatalf("accepted inconsistent result: %+v, %v", round, err)
			}
		})
	}
}

func TestFullReviewRejectsIncompleteOrContradictoryResult(t *testing.T) {
	t.Parallel()
	mutations := map[string]func(*FindingsExport){
		"missing head":         func(r *FindingsExport) { r.ReviewedSHA = "" },
		"abbreviated head":     func(r *FindingsExport) { r.ReviewedSHA = "aaaaaaa" },
		"wrong head":           func(r *FindingsExport) { r.ReviewedSHA = testBaseSHA },
		"wrong base":           func(r *FindingsExport) { r.BaseRef = "main" },
		"missing risk":         func(r *FindingsExport) { r.Risk = "" },
		"wrong risk":           func(r *FindingsExport) { r.Risk = "low" },
		"unknown verdict":      func(r *FindingsExport) { r.Verdict = "PASS" },
		"unknown severity":     func(r *FindingsExport) { r.Findings = []ExportFinding{{Severity: "urgent"}} },
		"finding at floor":     func(r *FindingsExport) { r.Findings = []ExportFinding{{Severity: "MEDIUM"}} },
		"blocking below floor": func(r *FindingsExport) { r.Findings = []ExportFinding{{Severity: "LOW", Blocking: true}} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			export := FindingsExport{Verdict: "APPROVE", ReviewedSHA: testHeadSHA, BaseRef: testBaseSHA, Risk: "high"}
			mutate(&export)
			path := filepath.Join(t.TempDir(), "findings.json")
			writeJSON(t, path, export)
			round, err := recordFull(decision{Round: 1, Floor: "medium", HeadSHA: testHeadSHA, BaseSHA: testBaseSHA, Risk: "high"}, path, 0)
			if err == nil || round.Verdict != "ESCALATE" {
				t.Fatalf("accepted inconsistent result: %+v, %v", round, err)
			}
		})
	}
}

func TestApprovalNeedsCurrentRevisionAndSuccessfulGates(t *testing.T) {
	t.Parallel()
	mutations := map[string]func(*RunState){
		"stale SHA":            func(s *RunState) { s.Rounds[0].ReviewedSHA = testBaseSHA },
		"missing SHA":          func(s *RunState) { s.Rounds[0].ReviewedSHA = "" },
		"incomplete review":    func(s *RunState) { s.Rounds[0].Status = "review_unavailable" },
		"dirty classification": func(s *RunState) { s.Repo.Dirty = true },
		"missing gates":        func(s *RunState) { s.Gates = nil },
		"failed gate":          func(s *RunState) { s.Gates["test"] = Gate{Status: "fail"} },
		"unavailable gate":     func(s *RunState) { s.Gates["test"] = Gate{Status: "unavailable"} },
		"unwaived gate":        func(s *RunState) { s.Gates["test"] = Gate{Status: "skipped", SkipReason: "not run"} },
		"blank waiver":         func(s *RunState) { s.Gates["test"] = Gate{Status: "skipped", SkipReason: "WAIVED:  "} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s := stateWith(Round{Round: 1, Status: "review_complete", Verdict: "APPROVE", ReviewedSHA: testHeadSHA})
			s.Repo.HeadSHA = testHeadSHA
			s.Gates = map[string]Gate{"test": {Status: "pass"}}
			mutate(s)
			d := decide(s, 4, "/out", "X")
			if !d.Stop || d.ExitCode != exitEscalate || d.Verdict != "ESCALATE" {
				t.Fatalf("accepted invalid approval: %+v", d)
			}
		})
	}
}

func TestAppendRoundRejectsDuplicateRoundWithoutChangingState(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run.json")
	writeJSON(t, path, stateWith())
	r := Round{Round: 1, Kind: "full", Verdict: "ITERATE"}
	if err := appendRound(path, r, r.Verdict, ""); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendRound(path, r, r.Verdict, ""); !errors.Is(err, statefile.ErrConflict) {
		t.Fatalf("duplicate round not refused: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("refused round changed history: %s, %v", after, err)
	}
}
