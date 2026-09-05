package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stateWith(rounds ...Round) *RunState {
	return &RunState{
		SchemaVersion: schemaVersion,
		TaskKey:       "SMG-1",
		Repo:          Repo{Worktree: "/wt", BaseRef: "origin/main", BaseSHA: "abc1234"},
		Classification: &Classification{
			Risk:               "critical",
			Components:         []string{"wallet"},
			RecheckMinSeverity: "medium",
			ReviewerArgs:       []string{"-cwd", "/wt", "-base", "origin/main", "-risk", "critical", "-component", "wallet"},
		},
		Rounds: rounds,
	}
}

// ─── tool selection ──────────────────────────────────────────────────────────

func TestDecide_Round1IsFullPanel(t *testing.T) {
	t.Parallel()
	d := decide(stateWith(), defaultCeiling, "/tmp", "SMG-1")
	if d.Stop {
		t.Fatal("should not stop with zero rounds")
	}
	if d.Round != 1 || d.Kind != "full" {
		t.Errorf("d = round %d kind %q, want round 1 full", d.Round, d.Kind)
	}
}

// Round 2's full re-audit is deliberate — PR 1285's second look found a real
// money bug in a file round 1 had passed.
func TestDecide_Round2IsStillFull(t *testing.T) {
	t.Parallel()
	d := decide(stateWith(Round{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 3}),
		defaultCeiling, "/tmp", "SMG-1")
	if d.Round != 2 || d.Kind != "full" {
		t.Errorf("d = round %d kind %q, want round 2 full", d.Round, d.Kind)
	}
}

func TestDecide_Round3SwitchesToRecheck(t *testing.T) {
	t.Parallel()
	d := decide(stateWith(
		Round{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 5},
		Round{Round: 2, Kind: "full", Verdict: "ITERATE", NewFindingCount: 3, FindingsPath: "/tmp/f-r2.json"},
	), defaultCeiling, "/tmp", "SMG-1")

	if d.Round != 3 || d.Kind != "recheck" {
		t.Fatalf("d = round %d kind %q, want round 3 recheck", d.Round, d.Kind)
	}
	if d.PriorPath != "/tmp/f-r2.json" {
		t.Errorf("PriorPath = %q, want the round-2 findings", d.PriorPath)
	}
}

// ─── the arithmetic a human used to do ───────────────────────────────────────

func TestDecide_MaxNewComesFromPriorRound(t *testing.T) {
	t.Parallel()
	d := decide(stateWith(
		Round{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 5},
		Round{Round: 2, Kind: "full", Verdict: "ITERATE", NewFindingCount: 3, FindingsPath: "/tmp/f.json"},
	), defaultCeiling, "/tmp", "SMG-1")

	if d.MaxNew != 3 {
		t.Errorf("MaxNew = %d, want 3 (round 2's new count)", d.MaxNew)
	}
	// recheck escalates when new >= max-new, so passing the prior count enforces
	// "strictly fewer than last round".
	if got := maxNewFor(d.MaxNew); got != 3 {
		t.Errorf("maxNewFor = %d, want 3", got)
	}
}

func TestMaxNewFor_ZeroPriorMeansAnyNewEscalates(t *testing.T) {
	t.Parallel()
	if got := maxNewFor(0); got != 0 {
		t.Errorf("maxNewFor(0) = %d, want 0 — recheck reads 0 as 'any new finding escalates'", got)
	}
	if got := maxNewFor(-1); got != 0 {
		t.Errorf("maxNewFor(-1) = %d, want 0", got)
	}
}

// ─── escalation triggers ─────────────────────────────────────────────────────

func TestDecide_StillOpenEscalatesImmediately(t *testing.T) {
	t.Parallel()
	d := decide(stateWith(
		Round{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 2},
		Round{Round: 2, Kind: "full", Verdict: "ITERATE", NewFindingCount: 1},
		Round{Round: 3, Kind: "recheck", Verdict: "ESCALATE", PriorStillOpen: 1},
	), defaultCeiling, "/tmp", "SMG-1")

	if !d.Stop || d.Verdict != "ESCALATE" || d.ExitCode != exitEscalate {
		t.Fatalf("d = %+v, want an immediate ESCALATE", d)
	}
	if !strings.Contains(strings.Join(d.Reasons, " "), "one dedicated fix round") {
		t.Errorf("reasons should explain the rule: %v", d.Reasons)
	}
}

func TestDecide_RegressedEscalatesImmediately(t *testing.T) {
	t.Parallel()
	d := decide(stateWith(
		Round{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 2},
		Round{Round: 2, Kind: "full", Verdict: "ITERATE", PriorRegressed: 1},
	), defaultCeiling, "/tmp", "SMG-1")
	if !d.Stop || d.Verdict != "ESCALATE" {
		t.Errorf("d = %+v, want ESCALATE on a regression", d)
	}
}

// New findings while prior ones resolve is the panel working. It only escalates
// when the count stops shrinking.
func TestDecide_ConvergingIterationContinues(t *testing.T) {
	t.Parallel()
	d := decide(stateWith(
		Round{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 5},
		Round{Round: 2, Kind: "full", Verdict: "ITERATE", NewFindingCount: 2, FindingsPath: "/tmp/f.json"},
	), defaultCeiling, "/tmp", "SMG-1")
	if d.Stop {
		t.Errorf("converging run should continue: %+v", d)
	}
}

func TestDecide_NotConvergingEscalates(t *testing.T) {
	t.Parallel()
	d := decide(stateWith(
		Round{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 3},
		Round{Round: 2, Kind: "full", Verdict: "ITERATE", NewFindingCount: 3},
	), defaultCeiling, "/tmp", "SMG-1")

	if !d.Stop || d.Verdict != "ESCALATE" {
		t.Fatalf("d = %+v, want ESCALATE — 3 then 3 is not decreasing", d)
	}
	if !strings.Contains(strings.Join(d.Reasons, " "), "defect-dense") {
		t.Errorf("reasons = %v", d.Reasons)
	}
}

func TestDecide_ZeroNewTwiceIsNotAStall(t *testing.T) {
	t.Parallel()
	// 0 then 0 means clean rounds, not a stall — the guard only fires above zero.
	d := decide(stateWith(
		Round{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 0},
		Round{Round: 2, Kind: "full", Verdict: "ITERATE", NewFindingCount: 0, FindingsPath: "/tmp/f.json"},
	), defaultCeiling, "/tmp", "SMG-1")
	if d.Stop && d.Verdict == "ESCALATE" {
		t.Errorf("zero-new rounds should not escalate as non-converging: %+v", d)
	}
}

func TestDecide_ApproveStopsTheLoop(t *testing.T) {
	t.Parallel()
	state := stateWith(Round{Round: 1, Kind: "full", Status: "review_complete", Verdict: "APPROVE", ReviewedSHA: testHeadSHA})
	state.Repo.HeadSHA = testHeadSHA
	state.Gates = map[string]Gate{"test": {Status: "pass"}}
	d := decide(state, defaultCeiling, "/tmp", "SMG-1")
	if !d.Stop || d.Verdict != "APPROVE" || d.ExitCode != exitApprove {
		t.Errorf("d = %+v, want APPROVE stop with exit 0", d)
	}
}

func TestDecide_CeilingBlocks(t *testing.T) {
	t.Parallel()
	rounds := []Round{
		{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 4},
		{Round: 2, Kind: "full", Verdict: "ITERATE", NewFindingCount: 3},
		{Round: 3, Kind: "recheck", Verdict: "ITERATE", NewFindingCount: 2},
		{Round: 4, Kind: "recheck", Verdict: "ITERATE", NewFindingCount: 1},
	}
	d := decide(stateWith(rounds...), defaultCeiling, "/tmp", "SMG-1")
	if !d.Stop || d.Verdict != "ESCALATE" {
		t.Fatalf("d = %+v, want ceiling stop", d)
	}
	if !strings.Contains(strings.Join(d.Reasons, " "), "ceiling") {
		t.Errorf("reasons = %v", d.Reasons)
	}

	// Critical systems converging at the medium floor legitimately run longer.
	if d2 := decide(stateWith(rounds...), 8, "/tmp", "SMG-1"); d2.Stop {
		t.Errorf("raised ceiling should allow another round: %+v", d2)
	}
}

// ─── argv construction ───────────────────────────────────────────────────────

func TestBuildArgv_FullUsesClassificationArgsVerbatim(t *testing.T) {
	t.Parallel()
	state := stateWith()
	d := decide(state, defaultCeiling, "/out", "SMG-1")
	bin, argv := buildArgv(state, d, binaries{Reviewer: "/bin/reviewer"}, "/out", "SMG-1")

	if bin != "/bin/reviewer" {
		t.Errorf("bin = %q", bin)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"-risk critical", "-component wallet", "-base origin/main", "-findings-out /out/findings-SMG-1-r1.json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q missing %q", joined, want)
		}
	}
}

func TestBuildArgv_RecheckCarriesFloorAndMaxNew(t *testing.T) {
	t.Parallel()
	state := stateWith(
		Round{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 5},
		Round{Round: 2, Kind: "full", Verdict: "ITERATE", NewFindingCount: 2, FindingsPath: "/out/findings-SMG-1-r2.json"},
	)
	d := decide(state, defaultCeiling, "/out", "SMG-1")
	bin, argv := buildArgv(state, d, binaries{Recheck: "/bin/recheck"}, "/out", "SMG-1")

	if bin != "/bin/recheck" {
		t.Errorf("bin = %q", bin)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"-worktree /wt",
		"-findings /out/findings-SMG-1-r2.json",
		"-risk critical",
		"-min-severity medium", // component preset → critical-system bar
		"-max-new 2",
		"-out /out/round-SMG-1-r3.json",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q missing %q", joined, want)
		}
	}
}

func TestFloorFor_DefaultsToHigh(t *testing.T) {
	t.Parallel()
	s := &RunState{Classification: &Classification{Risk: "high"}}
	if got := floorFor(s); got != "high" {
		t.Errorf("floorFor = %q, want high", got)
	}
	s.Classification.RecheckMinSeverity = "medium"
	if got := floorFor(s); got != "medium" {
		t.Errorf("floorFor = %q, want medium", got)
	}
}

// ─── recording ───────────────────────────────────────────────────────────────

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRecordFull_CountsAtOrAboveFloor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	writeJSON(t, p, FindingsExport{
		ReviewedSHA: testHeadSHA, BaseRef: testBaseSHA, Risk: "critical", Verdict: "ITERATE",
		Findings: []ExportFinding{
			{Severity: "CRITICAL"}, {Severity: "HIGH"}, {Severity: "MEDIUM"}, {Severity: "LOW"},
		},
	})

	r, err := recordFull(decision{Round: 1, Floor: "medium"}, p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "ITERATE" || r.Kind != "full" {
		t.Errorf("round = %+v", r)
	}
	if r.AtOrAboveFloorCount != 3 {
		t.Errorf("at-or-above medium = %d, want 3", r.AtOrAboveFloorCount)
	}

	rHigh, err := recordFull(decision{Round: 1, Floor: "high"}, p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rHigh.AtOrAboveFloorCount != 2 {
		t.Errorf("at-or-above high = %d, want 2", rHigh.AtOrAboveFloorCount)
	}
}

func TestRecordFull_BlockingCountsRegardlessOfSeverity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	writeJSON(t, p, FindingsExport{ReviewedSHA: testHeadSHA, BaseRef: testBaseSHA, Risk: "high", Verdict: "ITERATE", Findings: []ExportFinding{{Severity: "LOW", Blocking: true}}})

	r, err := recordFull(decision{Round: 1, Floor: "high"}, p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.AtOrAboveFloorCount != 1 {
		t.Errorf("blocking LOW should count: %d", r.AtOrAboveFloorCount)
	}
}

// A missing tool output is an incomplete round, never a clean one.
func TestRecordFull_MissingFileIsEscalate(t *testing.T) {
	t.Parallel()
	r, err := recordFull(decision{Round: 2, Floor: "high"}, filepath.Join(t.TempDir(), "nope.json"), 1)
	if err == nil {
		t.Fatal("expected an error for a missing findings file")
	}
	if r.Verdict != "ESCALATE" || r.Status != "review_unavailable" {
		t.Errorf("round = %+v, want ESCALATE/review_unavailable", r)
	}
}

func TestRecordFull_MalformedJSONIsEscalate(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "f.json")
	if err := os.WriteFile(p, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := recordFull(decision{Round: 1, Floor: "high"}, p, 0)
	if err == nil {
		t.Fatal("expected an error")
	}
	if r.Verdict != "ESCALATE" || r.Status != "invalid_input" {
		t.Errorf("round = %+v", r)
	}
}

func TestRecordRecheck_MapsCounts(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "round.json")
	writeJSON(t, p, RoundResult{
		Tool: "recheck", Verdict: "ITERATE", ExitCode: 1, Floor: "medium", HeadSHA: testHeadSHA, ReviewedSHA: testBaseSHA,
		PriorChecked: 4, Resolved: 4, StillOpen: 0, Regressed: 0, NewAtFloor: 1, MaxNewGiven: 2,
	})

	r, err := recordRecheck(decision{Round: 3, MaxNew: 2, Floor: "medium"}, p, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "recheck" || r.Verdict != "ITERATE" {
		t.Errorf("round = %+v", r)
	}
	if r.PriorFindingsResolved != 4 || r.NewFindingCount != 1 || r.MaxNewAllowed != 2 {
		t.Errorf("counts = %+v", r)
	}
	if r.ReviewedSHA != testHeadSHA {
		t.Errorf("ReviewedSHA = %q", r.ReviewedSHA)
	}
}

func TestVerdictExit(t *testing.T) {
	t.Parallel()
	cases := map[string]int{
		"APPROVE": exitApprove, "approve": exitApprove,
		"ITERATE": exitIterate,
		"REJECT":  exitEscalate, "ESCALATE": exitEscalate, "": exitEscalate,
	}
	for v, want := range cases {
		if got := verdictExit(v); got != want {
			t.Errorf("verdictExit(%q) = %d, want %d", v, got, want)
		}
	}
}

// ─── run state merge ─────────────────────────────────────────────────────────

func TestAppendRound_PreservesOtherNodesFields(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "run.json")
	seed := `{"schema_version":1,"task_key":"SMG-1","created_at":"2026-07-01T00:00:00Z",
	  "repo":{"worktree":"/wt","base_ref":"origin/main","base_sha":"abc1234"},
	  "classification":{"risk":"critical","components":["wallet"],"recheck_min_severity":"medium"},
	  "gates":{"test:apps/w":{"status":"pass"}}}`
	if err := os.WriteFile(p, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	if err := appendRound(p, Round{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 3}, "ITERATE", ""); err != nil {
		t.Fatal(err)
	}
	if err := appendRound(p, Round{Round: 2, Kind: "full", Verdict: "APPROVE"}, "APPROVE", ""); err != nil {
		t.Fatal(err)
	}

	state, err := readRunState(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Rounds) != 2 || state.Round != 2 {
		t.Fatalf("rounds = %+v round=%d", state.Rounds, state.Round)
	}
	if state.Classification == nil || state.Classification.Risk != "critical" {
		t.Error("classification not preserved")
	}
	if state.Gates["test:apps/w"].Status != "pass" {
		t.Error("gates not preserved")
	}
	if state.CreatedAt != "2026-07-01T00:00:00Z" {
		t.Error("created_at not preserved")
	}
	if state.Verdict != "approve" {
		t.Errorf("verdict = %q", state.Verdict)
	}
}

func TestAppendRound_EscalationReasonRecorded(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "run.json")
	seed := `{"schema_version":1,"repo":{"worktree":"/wt","base_ref":"origin/main","base_sha":"a"},
	  "classification":{"risk":"high"}}`
	if err := os.WriteFile(p, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	if err := appendRound(p, Round{Round: 1, Kind: "recheck", Verdict: "ESCALATE", PriorStillOpen: 1},
		"ESCALATE", "round 3: 1 STILL_OPEN"); err != nil {
		t.Fatal(err)
	}
	state, err := readRunState(p)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "escalated" || !strings.Contains(state.EscalationReason, "STILL_OPEN") {
		t.Errorf("state = status %q reason %q", state.Status, state.EscalationReason)
	}
}

func TestRoundEscalation(t *testing.T) {
	t.Parallel()
	if got := roundEscalation(Round{Round: 3, PriorStillOpen: 2}); !strings.Contains(got, "design problem") {
		t.Errorf("escalation = %q", got)
	}
	if got := roundEscalation(Round{Round: 1, Status: "review_unavailable"}); !strings.Contains(got, "no machine-readable result") {
		t.Errorf("escalation = %q", got)
	}
	if got := roundEscalation(Round{Round: 1, Verdict: "ITERATE"}); got != "" {
		t.Errorf("clean round should have no escalation reason: %q", got)
	}
}

// ─── paths and printing ──────────────────────────────────────────────────────

func TestPaths(t *testing.T) {
	t.Parallel()
	if got := findingsPath("/out", "SMG-9", 2); got != "/out/findings-SMG-9-r2.json" {
		t.Errorf("findingsPath = %q", got)
	}
	if got := roundResultPath("/out", "", 3); got != "/out/round-task-r3.json" {
		t.Errorf("roundResultPath with no task key = %q", got)
	}
}

func TestPrintersDoNotPanic(t *testing.T) {
	t.Parallel()
	state := stateWith(Round{Round: 1, Kind: "full", Verdict: "ITERATE", NewFindingCount: 3})
	printDecision(state, decide(state, defaultCeiling, "/tmp", "SMG-1"))
	printRound(Round{Round: 3, Kind: "recheck", Verdict: "ESCALATE", PriorStillOpen: 1, MaxNewAllowed: 2})
	printRound(Round{Round: 1, Kind: "full", Verdict: "ITERATE", AtOrAboveFloorCount: 4})
}

func TestCountAtOrAbove_CaseInsensitive(t *testing.T) {
	t.Parallel()
	f := []ExportFinding{{Severity: "critical"}, {Severity: "High"}, {Severity: "low"}}
	if got := countAtOrAbove(f, "high"); got != 2 {
		t.Errorf("countAtOrAbove = %d, want 2", got)
	}
}
