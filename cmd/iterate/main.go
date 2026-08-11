// Command iterate is the review-loop controller: it decides which review tool
// runs this round, builds its argv, executes it, records the round in the run
// state, and computes the convergence verdict mechanically.
//
// It exists because iteration-protocol.md still asks a person to do arithmetic
// mid-loop:
//
//	iteration-protocol.md:131  "-max-new <prior round's new-finding count minus 1>"
//
// That is a loop invariant being maintained by a language model between two
// deterministic tools. Everything else in the protocol is equally mechanical:
// round 1-2 are full re-audits, rounds 3+ are targeted verification, a prior
// finding that is STILL_OPEN or REGRESSED escalates immediately, and a new
// finding count that is not strictly decreasing escalates. All of it is
// arithmetic over rounds[] in the run state.
//
// Two subcommands:
//
//	iterate next  — print the decision and argv for the next round; runs nothing
//	iterate run   — do the above, execute the tool, record the round, exit with
//	                the verdict
//
// Exit codes match cmd/recheck so the loop composes:
//
//	0 APPROVE   1 ITERATE   2 ESCALATE   3 INVALID_INPUT
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	schemaVersion = 1

	exitApprove  = 0
	exitIterate  = 1
	exitEscalate = 2
	exitInvalid  = 3

	// defaultCeiling is a runaway backstop, not the real stop condition. The
	// STILL_OPEN/REGRESSED and not-converging triggers fire long before it.
	defaultCeiling = 4

	// fullAuditRounds is how many rounds get a complete re-audit. Round 2's full
	// re-audit is deliberate: PR 1285's round-2 pass found a real money bug in a
	// file round 1 had reviewed and passed. From round 3 the discovery budget is
	// spent and re-reading unchanged files creates a regression spiral.
	fullAuditRounds = 2
)

// ─── run state (see config/run-state.schema.json) ────────────────────────────

type RunState struct {
	SchemaVersion    int             `json:"schema_version"`
	TaskKey          string          `json:"task_key,omitempty"`
	CreatedAt        string          `json:"created_at,omitempty"`
	UpdatedAt        string          `json:"updated_at,omitempty"`
	Repo             Repo            `json:"repo"`
	Classification   *Classification `json:"classification,omitempty"`
	Gates            map[string]Gate `json:"gates,omitempty"`
	Rounds           []Round         `json:"rounds,omitempty"`
	Round            int             `json:"round,omitempty"`
	Verdict          string          `json:"verdict,omitempty"`
	Status           string          `json:"status,omitempty"`
	EscalationReason string          `json:"escalation_reason,omitempty"`
	PR               map[string]any  `json:"pr,omitempty"`
	DeferredFindings []any           `json:"deferred_findings,omitempty"`
}

type Repo struct {
	Worktree string `json:"worktree"`
	BaseRef  string `json:"base_ref"`
	BaseSHA  string `json:"base_sha"`
	HeadSHA  string `json:"head_sha,omitempty"`
	Dirty    bool   `json:"dirty,omitempty"`
	Branch   string `json:"branch,omitempty"`
}

type Classification struct {
	Risk               string   `json:"risk"`
	Components         []string `json:"components,omitempty"`
	RecheckMinSeverity string   `json:"recheck_min_severity,omitempty"`
	ReviewerArgs       []string `json:"reviewer_args,omitempty"`
}

type Gate struct {
	Status     string `json:"status"`
	SkipReason string `json:"skip_reason,omitempty"`
}

type Round struct {
	Round                 int        `json:"round"`
	Kind                  string     `json:"kind"` // full | recheck
	ReviewedSHA           string     `json:"reviewed_sha,omitempty"`
	Status                string     `json:"status,omitempty"`
	Verdict               string     `json:"verdict"`
	FindingsPath          string     `json:"findings_path,omitempty"`
	Reviewers             []Reviewer `json:"reviewers,omitempty"`
	PriorFindingsResolved int        `json:"prior_findings_resolved,omitempty"`
	PriorStillOpen        int        `json:"prior_findings_still_open,omitempty"`
	PriorRegressed        int        `json:"prior_findings_regressed,omitempty"`
	NewFindingCount       int        `json:"new_finding_count"`
	MaxNewAllowed         int        `json:"max_new_allowed,omitempty"`
	AtOrAboveFloorCount   int        `json:"at_or_above_floor_count,omitempty"`
	CompletedAt           string     `json:"completed_at,omitempty"`
}

type Reviewer struct {
	Name    string `json:"name"`
	Score   int    `json:"score,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ─── tool payloads ───────────────────────────────────────────────────────────

// FindingsExport mirrors cmd/reviewer's -findings-out. Duplicated rather than
// shared because each cmd/ tool is its own stdlib-only module.
type FindingsExport struct {
	ReviewedSHA string          `json:"reviewed_sha"`
	BaseRef     string          `json:"base_ref"`
	Risk        string          `json:"risk"`
	Verdict     string          `json:"verdict"`
	Findings    []ExportFinding `json:"findings"`
}

type ExportFinding struct {
	Severity string `json:"severity"`
	Blocking bool   `json:"blocking"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Title    string `json:"title"`
}

// RoundResult mirrors cmd/recheck's -out.
type RoundResult struct {
	Tool         string `json:"tool"`
	Verdict      string `json:"verdict"`
	ExitCode     int    `json:"exit_code"`
	Floor        string `json:"floor"`
	ReviewedSHA  string `json:"reviewed_sha"`
	HeadSHA      string `json:"head_sha"`
	PriorChecked int    `json:"prior_checked"`
	Resolved     int    `json:"resolved"`
	StillOpen    int    `json:"still_open"`
	Regressed    int    `json:"regressed"`
	NewAtFloor   int    `json:"new_at_floor"`
	MaxNewGiven  int    `json:"max_new_given"`
	ChangedFiles int    `json:"changed_files"`
	Summary      string `json:"summary"`
}

var severityRank = map[string]int{"CRITICAL": 4, "HIGH": 3, "MEDIUM": 2, "LOW": 1}

// ─── decision ────────────────────────────────────────────────────────────────

// decision is what the controller concluded before running anything. A decision
// with Stop set means the loop is over and no tool should run.
type decision struct {
	Round     int
	Kind      string // full | recheck
	Stop      bool
	Verdict   string // set when Stop
	ExitCode  int
	Reasons   []string
	MaxNew    int // computed for recheck rounds; 0 means "any new finding escalates"
	Floor     string
	PriorPath string // prior round's findings JSON, input to recheck
}

// decide applies iteration-protocol.md's convergence rules to the rounds already
// recorded. Every branch here was previously a sentence a person had to apply
// correctly at 2am, mid-loop, after reading a review report.
func decide(state *RunState, ceiling int, findingsDir, taskKey string) decision {
	rounds := state.Rounds
	n := len(rounds)
	d := decision{Round: n + 1, Floor: floorFor(state)}

	// Escalate immediately on a finding that survived its dedicated fix round.
	// A finding that outlives a clean, focused fix attempt is a design problem;
	// more patching will not help.
	if n > 0 {
		last := rounds[n-1]
		if last.PriorStillOpen > 0 || last.PriorRegressed > 0 {
			d.Stop, d.Verdict, d.ExitCode = true, "ESCALATE", exitEscalate
			d.Reasons = append(d.Reasons, fmt.Sprintf(
				"round %d left %d finding(s) STILL_OPEN and %d REGRESSED — a finding gets one dedicated fix round",
				last.Round, last.PriorStillOpen, last.PriorRegressed))
			return d
		}
		if last.Verdict == "APPROVE" {
			d.Stop, d.Verdict, d.ExitCode = true, "APPROVE", exitApprove
			d.Reasons = append(d.Reasons, fmt.Sprintf("round %d verdict was APPROVE — loop complete", last.Round))
			return d
		}
		if last.Verdict == "REJECT" {
			d.Stop, d.Verdict, d.ExitCode = true, "ESCALATE", exitEscalate
			d.Reasons = append(d.Reasons, fmt.Sprintf("round %d verdict was REJECT", last.Round))
			return d
		}
	}

	// Convergence: the new at-or-above-floor count must strictly decrease.
	if n >= 2 {
		prev, last := rounds[n-2], rounds[n-1]
		if last.NewFindingCount >= prev.NewFindingCount && last.NewFindingCount > 0 {
			d.Stop, d.Verdict, d.ExitCode = true, "ESCALATE", exitEscalate
			d.Reasons = append(d.Reasons, fmt.Sprintf(
				"new findings not strictly decreasing (round %d: %d → round %d: %d) — the change is defect-dense and patching is not converging",
				prev.Round, prev.NewFindingCount, last.Round, last.NewFindingCount))
			return d
		}
	}

	if n >= ceiling {
		d.Stop, d.Verdict, d.ExitCode = true, "ESCALATE", exitEscalate
		d.Reasons = append(d.Reasons, fmt.Sprintf(
			"iteration ceiling %d reached with findings still open — write Status: Blocked with the per-round lineage", ceiling))
		return d
	}

	// Which tool runs this round.
	if d.Round <= fullAuditRounds {
		d.Kind = "full"
		if d.Round == 1 {
			d.Reasons = append(d.Reasons, "round 1: full discovery panel")
		} else {
			d.Reasons = append(d.Reasons, "round 2: full re-audit — deliberate second look catches what round 1 missed while attention was on the flagged items (PR 1285)")
		}
		return d
	}

	d.Kind = "recheck"
	d.PriorPath = rounds[n-1].FindingsPath
	if d.PriorPath == "" {
		d.PriorPath = findingsPath(findingsDir, taskKey, rounds[n-1].Round)
	}
	// The invariant the protocol asked a human to compute: strictly decreasing.
	d.MaxNew = rounds[n-1].NewFindingCount
	d.Reasons = append(d.Reasons,
		fmt.Sprintf("round %d: targeted verification only — discovery budget spent after round 2", d.Round),
		fmt.Sprintf("-max-new %d = round %d's new-finding count, so this round must find strictly fewer than %d (recheck ITERATEs only while new < max-new)",
			maxNewFor(d.MaxNew), rounds[n-1].Round, d.MaxNew))
	return d
}

// maxNewFor converts a prior new-finding count into recheck's -max-new.
//
// recheck ITERATEs while newAtFloor < maxNew, so passing the prior count P
// verbatim enforces "strictly fewer than last round". The protocol doc used to
// say "P minus 1", which requires newAtFloor < P-1 — a drop of two — and would
// escalate a genuinely converging 3 -> 2 round. Pass P.
//
// 0 means "any new finding escalates", which is exactly right when the prior
// round found none.
func maxNewFor(priorNew int) int {
	if priorNew <= 0 {
		return 0
	}
	return priorNew
}

func floorFor(state *RunState) string {
	if state.Classification != nil && state.Classification.RecheckMinSeverity != "" {
		return state.Classification.RecheckMinSeverity
	}
	return "high"
}

func findingsPath(dir, taskKey string, round int) string {
	key := taskKey
	if key == "" {
		key = "task"
	}
	return filepath.Join(dir, fmt.Sprintf("findings-%s-r%d.json", key, round))
}

// ─── argv construction ───────────────────────────────────────────────────────

// buildArgv assembles the tool invocation. Reviewer args come from the
// classification verbatim so -risk and -component cannot be forgotten or
// mistyped by this caller either.
func buildArgv(state *RunState, d decision, bins binaries, findingsDir, taskKey string) (string, []string) {
	if d.Kind == "full" {
		args := append([]string{}, state.Classification.ReviewerArgs...)
		args = append(args, "-findings-out", findingsPath(findingsDir, taskKey, d.Round))
		return bins.Reviewer, args
	}

	args := []string{
		"-worktree", state.Repo.Worktree,
		"-findings", d.PriorPath,
		"-risk", state.Classification.Risk,
		"-min-severity", d.Floor,
		"-max-new", fmt.Sprint(maxNewFor(d.MaxNew)),
		"-out", roundResultPath(findingsDir, taskKey, d.Round),
	}
	return bins.Recheck, args
}

func roundResultPath(dir, taskKey string, round int) string {
	key := taskKey
	if key == "" {
		key = "task"
	}
	return filepath.Join(dir, fmt.Sprintf("round-%s-r%d.json", key, round))
}

type binaries struct {
	Reviewer string
	Recheck  string
}

func defaultBinaries() binaries {
	home := os.Getenv("HOME")
	root := filepath.Join(home, "Project/claude-workflow")
	if exe, err := os.Executable(); err == nil {
		if candidate := filepath.Join(filepath.Dir(exe), "..", ".."); dirExists(candidate) {
			root = candidate
		}
	}
	return binaries{
		Reviewer: filepath.Join(root, "cmd/reviewer/main"),
		Recheck:  filepath.Join(root, "cmd/recheck/recheck"),
	}
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// ─── recording ───────────────────────────────────────────────────────────────

// recordFull builds the round entry from cmd/reviewer's findings export.
func recordFull(d decision, path string, exitCode int) (Round, error) {
	r := Round{
		Round: d.Round, Kind: "full", FindingsPath: path,
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path built by this process
	if err != nil {
		// The reviewer produced no findings file: that is an incomplete round,
		// not a clean one. Say so rather than recording a silent zero.
		r.Status = "review_unavailable"
		r.Verdict = "ESCALATE"
		return r, fmt.Errorf("reviewer wrote no findings file at %s (exit %d): %w", path, exitCode, err)
	}
	var export FindingsExport
	if err := json.Unmarshal(data, &export); err != nil {
		r.Status = "invalid_input"
		r.Verdict = "ESCALATE"
		return r, fmt.Errorf("findings file %s is not valid JSON: %w", path, err)
	}

	r.Status = "review_complete"
	r.Verdict = strings.ToUpper(export.Verdict)
	r.ReviewedSHA = export.ReviewedSHA
	r.AtOrAboveFloorCount = countAtOrAbove(export.Findings, d.Floor)
	r.NewFindingCount = r.AtOrAboveFloorCount
	return r, nil
}

// recordRecheck builds the round entry from cmd/recheck's -out payload.
func recordRecheck(d decision, path string, exitCode int) (Round, error) {
	r := Round{
		Round: d.Round, Kind: "recheck",
		MaxNewAllowed: maxNewFor(d.MaxNew),
		CompletedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path built by this process
	if err != nil {
		r.Status = "review_unavailable"
		r.Verdict = "ESCALATE"
		return r, fmt.Errorf("recheck wrote no round result at %s (exit %d): %w", path, exitCode, err)
	}
	var rr RoundResult
	if err := json.Unmarshal(data, &rr); err != nil {
		r.Status = "invalid_input"
		r.Verdict = "ESCALATE"
		return r, fmt.Errorf("round result %s is not valid JSON: %w", path, err)
	}

	r.Status = "review_complete"
	r.Verdict = rr.Verdict
	r.ReviewedSHA = rr.HeadSHA
	r.PriorFindingsResolved = rr.Resolved
	r.PriorStillOpen = rr.StillOpen
	r.PriorRegressed = rr.Regressed
	r.NewFindingCount = rr.NewAtFloor
	r.AtOrAboveFloorCount = rr.PriorChecked
	return r, nil
}

func countAtOrAbove(findings []ExportFinding, floor string) int {
	n := 0
	want := severityRank[strings.ToUpper(floor)]
	for _, f := range findings {
		if severityRank[strings.ToUpper(f.Severity)] >= want || f.Blocking {
			n++
		}
	}
	return n
}

// verdictExit maps a recorded round's verdict onto this tool's exit code.
func verdictExit(v string) int {
	switch strings.ToUpper(v) {
	case "APPROVE":
		return exitApprove
	case "ITERATE":
		return exitIterate
	default:
		return exitEscalate
	}
}

// ─── run state I/O ───────────────────────────────────────────────────────────

func readRunState(p string) (*RunState, error) {
	data, err := os.ReadFile(p) // #nosec G304 -- p is the -run-state flag
	if err != nil {
		return nil, err
	}
	var s RunState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// appendRound merges one round into the run state, preserving every field other
// nodes own.
//
// iterate's licence is SIX mutations and nothing else: it appends one element to
// rounds[], and it sets round, verdict, updated_at, status and escalation_reason.
// The previous version of this sentence named four of the six — it omitted
// updated_at and escalation_reason, both of which this function has always
// assigned — and it was wrong from the day it was written. That is the finding
// worth carrying: a hand-list drifted by a third and nobody noticed. The
// authority is preserve.go's enumeration, derived from this function's own
// assignment sites, and the six EditKinds below are what makes the licence
// executable rather than a comment.
//
// It merges into the document's own BYTES through ApplyRoundRecord rather than
// round-tripping it through this package's closed structs. The struct round trip
// that used to live here (json.Unmarshal into RunState → json.MarshalIndent)
// silently destroyed every JSON path those structs do not declare — measured at
// 60 paths, the classification collapsing from 15 keys to 4, 34 paths destroyed
// inside the 9 gate records cmd/gates had written minutes earlier, and 3 destroyed
// retroactively inside rounds[0] by an append whose only intended effect was to
// add rounds[1]. cmd/gates over the result exited 3 INVALID_INPUT: after one
// `iterate run` the pipeline could not gate at all. See preserve.go, which carries
// the contract, the measurement and the rejected alternatives.
//
// There is NO FALLBACK to the old marshal path on error. A fallback would make
// the fix vacuous by turning the failure mode into the error handler.
func appendRound(p string, r Round, verdict string, escalation string) error {
	// One read, two views: these are the bytes that get edited, and they are the
	// bytes the length below is taken from. Re-reading for the merge would put a
	// window between the two.
	raw, state, err := LoadRunStateDocument(p)
	if err != nil {
		return err
	}

	// AtIndex and RoundNumber are both computed from the length BEFORE the append,
	// which is why they differ by one, and both are taken from the document just
	// read rather than from load()'s earlier read.
	edits := []Edit{
		{Kind: EditKindAppendRound, Record: r, AtIndex: len(state.Rounds)},
		{Kind: EditKindSetRound, RoundNumber: len(state.Rounds) + 1},
		{Kind: EditKindSetVerdict, Verdict: strings.ToLower(verdict)},
		{Kind: EditKindSetUpdatedAt, UpdatedAt: time.Now().UTC().Format(time.RFC3339)},
	}
	switch strings.ToUpper(verdict) {
	case "APPROVE":
		// the driver flips this to done once the PR is raised
		edits = append(edits, Edit{Kind: EditKindSetStatus, Status: "in_progress"})
	case "ITERATE":
		edits = append(edits, Edit{Kind: EditKindSetStatus, Status: "in_progress"})
	default:
		edits = append(edits, Edit{Kind: EditKindSetStatus, Status: "escalated"})
		if escalation != "" {
			// CONDITIONAL, and the condition is the source's own guard. An edit list
			// without it does not license `escalation_reason` at all.
			edits = append(edits, Edit{Kind: EditKindSetEscalationReason, EscalationReason: escalation})
		}
	}

	data, err := ApplyRoundRecord(raw, edits)
	if err != nil {
		return err
	}
	// #nosec G306 -- the run state is a shared build artifact other nodes read.
	return os.WriteFile(p, data, 0644)
}

// ─── main ────────────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitInvalid)
	}
	switch os.Args[1] {
	case "next":
		os.Exit(cmdNext(os.Args[2:]))
	case "run":
		os.Exit(cmdRun(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(exitInvalid)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `iterate — review-loop controller

  iterate next -run-state R    decide the next round and print its argv; runs nothing
  iterate run  -run-state R    decide, execute the tool, record the round, exit with the verdict

Exit codes: 0 APPROVE, 1 ITERATE, 2 ESCALATE, 3 INVALID_INPUT
`)
}

type opts struct {
	runState    string
	findingsDir string
	ceiling     int
	reviewerBin string
	recheckBin  string
}

func bind(fs *flag.FlagSet, o *opts) {
	fs.StringVar(&o.runState, "run-state", "", "Run-state JSON from cmd/classify (required)")
	fs.StringVar(&o.findingsDir, "findings-dir", "", "Directory for findings/round JSON (default: alongside the run state)")
	fs.IntVar(&o.ceiling, "ceiling", defaultCeiling, "Iteration ceiling — a runaway backstop, not the real stop condition ($MAX_ITERATIONS)")
	fs.StringVar(&o.reviewerBin, "reviewer-bin", "", "Path to the cmd/reviewer binary")
	fs.StringVar(&o.recheckBin, "recheck-bin", "", "Path to the cmd/recheck binary")
}

func load(o *opts) (*RunState, decision, string, binaries, int) {
	var zero decision
	if o.runState == "" {
		fmt.Fprintln(os.Stderr, "=== ITERATE: INVALID_INPUT ===\n  ✗ -run-state is required")
		return nil, zero, "", binaries{}, exitInvalid
	}
	state, err := readRunState(o.runState)
	if err != nil {
		fmt.Fprintf(os.Stderr, "=== ITERATE: INVALID_INPUT ===\n  ✗ read run state: %v\n", err)
		return nil, zero, "", binaries{}, exitInvalid
	}
	if state.Classification == nil {
		fmt.Fprintln(os.Stderr, "=== ITERATE: INVALID_INPUT ===\n  ✗ run state has no classification — run cmd/classify first")
		return nil, zero, "", binaries{}, exitInvalid
	}

	dir := o.findingsDir
	if dir == "" {
		dir = filepath.Dir(o.runState)
	}
	bins := defaultBinaries()
	if o.reviewerBin != "" {
		bins.Reviewer = o.reviewerBin
	}
	if o.recheckBin != "" {
		bins.Recheck = o.recheckBin
	}

	return state, decide(state, o.ceiling, dir, state.TaskKey), dir, bins, 0
}

func cmdNext(args []string) int {
	fs := flag.NewFlagSet("next", flag.ExitOnError)
	var o opts
	bind(fs, &o)
	_ = fs.Parse(args)

	state, d, dir, bins, code := load(&o)
	if code != 0 {
		return code
	}
	printDecision(state, d)
	if d.Stop {
		return d.ExitCode
	}
	bin, argv := buildArgv(state, d, bins, dir, state.TaskKey)
	fmt.Printf("\nNext command:\n  %s %s\n", bin, strings.Join(argv, " "))
	if d.Kind == "full" {
		fmt.Println("\n(pipe the diff to it: git -C WT diff BASE...HEAD | <the above>)")
	}
	return exitIterate
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var o opts
	bind(fs, &o)
	dryRun := fs.Bool("dry-run", false, "Decide and print, but do not execute")
	_ = fs.Parse(args)

	state, d, dir, bins, code := load(&o)
	if code != 0 {
		return code
	}
	printDecision(state, d)

	if d.Stop {
		if err := appendRound(o.runState, Round{
			Round: d.Round, Kind: "controller", Verdict: d.Verdict,
			CompletedAt: time.Now().UTC().Format(time.RFC3339),
		}, d.Verdict, strings.Join(d.Reasons, "; ")); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: failed to record the stop decision: %v\n", err)
		}
		return d.ExitCode
	}

	bin, argv := buildArgv(state, d, bins, dir, state.TaskKey)
	if *dryRun {
		fmt.Printf("\nWould run:\n  %s %s\n", bin, strings.Join(argv, " "))
		return exitIterate
	}
	if _, err := os.Stat(bin); err != nil {
		fmt.Fprintf(os.Stderr, "=== ITERATE: INVALID_INPUT ===\n  ✗ %s not found — build it or pass -reviewer-bin/-recheck-bin\n", bin)
		return exitInvalid
	}

	toolExit, err := execTool(state, d, bin, argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "=== ITERATE: INVALID_INPUT ===\n  ✗ %v\n", err)
		return exitInvalid
	}
	return recordAndReport(o.runState, state, d, dir, toolExit)
}

// execTool runs the chosen review tool, streaming its output. Full rounds get
// the diff on stdin; recheck computes its own iteration diff from the worktree.
func execTool(state *RunState, d decision, bin string, argv []string) (int, error) {
	fmt.Printf("\nRunning: %s %s\n\n", bin, strings.Join(argv, " "))
	// #nosec G204 -- bin is a configured tool path and argv is built above from
	// the run state; there is no untrusted input on this path.
	cmd := exec.Command(bin, argv...)
	cmd.Dir = state.Repo.Worktree
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if d.Kind == "full" {
		diff, err := gitDiff(state)
		if err != nil {
			return 0, err
		}
		cmd.Stdin = strings.NewReader(diff)
	}
	return exitCodeOf(cmd.Run()), nil
}

// recordAndReport ingests the tool's machine-readable output, appends the round,
// and returns the verdict exit code.
func recordAndReport(runStatePath string, state *RunState, d decision, dir string, toolExit int) int {
	round, err := recordRound(d, dir, state.TaskKey, toolExit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n=== ITERATE: ROUND INCOMPLETE ===\n  ✗ %v\n", err)
	}
	if err := appendRound(runStatePath, round, round.Verdict, roundEscalation(round)); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: failed to record round %d: %v\n", round.Round, err)
	}
	printRound(round)
	return verdictExit(round.Verdict)
}

func recordRound(d decision, dir, taskKey string, toolExit int) (Round, error) {
	if d.Kind == "full" {
		return recordFull(d, findingsPath(dir, taskKey, d.Round), toolExit)
	}
	return recordRecheck(d, roundResultPath(dir, taskKey, d.Round), toolExit)
}

func roundEscalation(r Round) string {
	if r.PriorStillOpen > 0 || r.PriorRegressed > 0 {
		return fmt.Sprintf("round %d: %d STILL_OPEN, %d REGRESSED — a finding that survives its dedicated fix round is a design problem",
			r.Round, r.PriorStillOpen, r.PriorRegressed)
	}
	if r.Status == "review_unavailable" {
		return fmt.Sprintf("round %d produced no machine-readable result", r.Round)
	}
	return ""
}

func gitDiff(state *RunState) (string, error) {
	base := state.Repo.BaseSHA
	if base == "" {
		base = state.Repo.BaseRef
	}
	if base == "" {
		return "", fmt.Errorf("run state has no base to diff against")
	}
	// #nosec G204 -- base comes from cmd/classify, which resolved it via rev-parse.
	cmd := exec.Command("git", "diff", base+"...HEAD")
	cmd.Dir = state.Repo.Worktree
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff %s...HEAD in %s: %w", base, state.Repo.Worktree, err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return "", fmt.Errorf("diff against %s is empty — there is nothing to review", base)
	}
	return string(out), nil
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}

// ─── output ──────────────────────────────────────────────────────────────────

func printDecision(state *RunState, d decision) {
	fmt.Println("=== ITERATION DECISION ===")
	fmt.Printf("Task:  %s\n", emptyDash(state.TaskKey))
	fmt.Printf("Risk:  %s   Components: %s   Floor: %s\n",
		state.Classification.Risk, emptyDash(strings.Join(state.Classification.Components, ",")), d.Floor)
	fmt.Printf("Rounds so far: %d\n", len(state.Rounds))
	for _, r := range state.Rounds {
		fmt.Printf("  r%d %-10s %-9s resolved=%d still_open=%d regressed=%d new=%d\n",
			r.Round, r.Kind, r.Verdict, r.PriorFindingsResolved, r.PriorStillOpen, r.PriorRegressed, r.NewFindingCount)
	}

	if d.Stop {
		fmt.Printf("\nDecision: STOP — %s\n", d.Verdict)
	} else {
		fmt.Printf("\nDecision: round %d, %s re-review\n", d.Round, d.Kind)
	}
	for _, r := range d.Reasons {
		fmt.Printf("  · %s\n", r)
	}
}

func printRound(r Round) {
	fmt.Printf("\n=== ROUND %d RECORDED (%s) ===\n", r.Round, r.Kind)
	fmt.Printf("Verdict: %s   Status: %s\n", r.Verdict, emptyDash(r.Status))
	if r.Kind == "recheck" {
		fmt.Printf("Prior findings: %d resolved, %d still open, %d regressed\n",
			r.PriorFindingsResolved, r.PriorStillOpen, r.PriorRegressed)
		fmt.Printf("New at-or-above floor: %d (allowed < %d)\n", r.NewFindingCount, r.MaxNewAllowed)
	} else {
		fmt.Printf("Findings at-or-above floor: %d\n", r.AtOrAboveFloorCount)
	}
	if r.PriorStillOpen > 0 || r.PriorRegressed > 0 {
		fmt.Println("\nESCALATE: a finding survived its dedicated fix round. Do not spend another round on it — write Status: Blocked with the per-round lineage.")
	}
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
