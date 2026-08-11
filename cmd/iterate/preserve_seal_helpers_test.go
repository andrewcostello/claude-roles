package main

// Shared machinery for the unit G2 seals — run-state preservation in cmd/iterate.
//
// THESE FILES ARE SEALS, NOT AN IMPLEMENTATION. LoadRunStateDocument,
// ApplyRoundRecord and VerifyPreservation in preserve.go return
// errNotImplemented, and appendRound is not wired to any of them, so every row
// in preserve_seal_test.go is RED on purpose. Red is the correct state until the
// body author implements to preserve.go's doc comments. A green row in this
// suite is either (a) a fact about code that already exists at baseline,
// recorded as a CONTROL inside a red row so a future edit is noticed, or (b) a
// mistake — investigate the fixture before relaxing anything.
//
// The rules these seals hold themselves to are G1's, plus two G2 additions.
// Each is a failure this pipeline has already shipped:
//
//   - Every row is judged alongside a CONTROL in the same call. The standard
//     control is g2LegacyProjection — today's appendRound, reproduced in-process
//     from THIS PACKAGE'S OWN STRUCTS — which must show the loss the row forbids.
//     If a control stops firing, the fixture has stopped being able to exhibit
//     the defect and the row has become vacuous.
//
//   - Every row states whether it measures SOURCE or the COMMITTED ARTIFACT
//     cmd/iterate/iterate, and why. Exactly two rows touch the artifact and both
//     say so at the top.
//
//   - Every row a do-nothing body could satisfy carries PROOF OF EXECUTION. The
//     central hazard is unchanged from G1 and sharper here: ApplyRoundRecord
//     returning `original` unchanged preserves every path perfectly AND records
//     no round. g2AssertEditsLanded is the answer and every fidelity row calls it.
//
//   - NEW IN G2. Every row that builds or execs first calls
//     g2FingerprintTrackedBinaries. preserve.go finding (7) records that
//     `go build ./...` inside cmd/iterate OVERWRITES the tracked
//     cmd/iterate/iterate, that this is warned about nowhere, and that the
//     scaffold hit it and was saved by a compile error rather than by a
//     safeguard. A seal suite that silently rebuilt the artifact two of its rows
//     measure would be the frozen-artifact hazard with the evidence destroyed.
//     All THREE tracked binaries are fingerprinted, not just this package's:
//     ../classify/classify is the pinned differential baseline and ../gates/gates
//     was rebuilt by adjudicate(G1), and both are exec'd from here.
//
//   - NEW IN G2. The acceptance measure honours the CONTAINER/SUBTREE split.
//     G1's g1DivergencesOutside applied a deliberately weaker plain-prefix rule
//     so its fidelity rows would not depend on the deletion question. That
//     choice does not transfer: a plain prefix over `rounds` forgives every
//     divergence beneath it, and the three measured losses inside rounds[0] are
//     exactly what sits beneath it. g2DivergencesOutside therefore goes through
//     LicensedPaths and Licence.Allows — the scaffold's own ruling (3), which it
//     implemented rather than stubbed for this reason.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ─── the seal's own guard rails ──────────────────────────────────────────────

// g2Red turns a scaffold panic into an ordinary test failure, so one row's panic
// does not take the binary down and hide the shape of the work from the body
// author. It is not a tolerance: once the body lands nothing panics and this
// defer is inert.
func g2Red(t *testing.T) {
	t.Helper()
	if r := recover(); r != nil {
		t.Errorf("SEAL RED — panicked: %v", r)
	}
}

// The three tracked artifacts these seals touch. All three are committed
// binaries and none of them may be written by this suite.
//
//	trackedIterateBinary   THIS package's artifact. skills/iteration-protocol.md
//	                       :130 execs it by absolute path, so it — not the source
//	                       tree — is what production runs. `go build ./...` in
//	                       this directory overwrites it (preserve.go finding (7)).
//	pinnedClassifyBinary   the real v1 producer and the pinned differential
//	                       baseline. A FIXTURE, never a build artifact.
//	trackedGatesBinary     the G1-rebuilt artifact. It is the tool that runs
//	                       immediately BEFORE iterate in the pipeline, so it is
//	                       both the writer of the gate records G2 must preserve
//	                       and the oracle for whether the pipeline can still gate.
const (
	trackedIterateBinary = "./iterate"
	pinnedClassifyBinary = "../classify/classify"
	pinnedClassifyConfig = "../classify/testdata/example-monorepo.json"
	trackedGatesBinary   = "../gates/gates"
	trackedGatesConfig   = "../gates/testdata/example-gates.json"
)

// g2FingerprintTrackedBinaries hashes all three committed binaries and registers
// a cleanup that re-checks them. Any row that builds or execs calls it first.
//
// It is g1FingerprintTrackedBinary widened, and the widening is the point:
// preserve.go finding (7) names cmd/iterate/iterate as a THIRD artifact the unit
// brief did not warn about, one keystroke away from any author in this
// directory. The scaffold hit it and got away with it because an unused import
// failed the build before Go wrote the file — luck, not a safeguard.
func g2FingerprintTrackedBinaries(t *testing.T) map[string]string {
	t.Helper()
	paths := []string{trackedIterateBinary, pinnedClassifyBinary, trackedGatesBinary}
	sum := func(p string) string {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("tracked binary %s is missing: %v — it is committed to this repo and these seals measure it; a suite that skipped here would go green on exactly the checkout where it mattered", p, err)
		}
		h := sha256.Sum256(data)
		return hex.EncodeToString(h[:])
	}
	before := map[string]string{}
	for _, p := range paths {
		before[p] = sum(p)
	}
	t.Cleanup(func() {
		for _, p := range paths {
			if after := sum(p); after != before[p] {
				t.Errorf("SEAL BUG — this row rebuilt or overwrote the tracked binary %s (%s -> %s). The seals must never write one; build to a scratch path with `go build -o`. preserve.go finding (7) is the warning that did not exist before this unit.",
					p, before[p][:12], after[:12])
			}
		}
	})
	return before
}

// g2Abs resolves one of the tracked-binary constants to an absolute path.
//
// It is required, not tidiness. exec.Cmd resolves a relative Path against Dir,
// and every row that runs cmd/gates must set Dir to the fixture worktree so
// gates can discover its modules. "../gates/gates" resolved against a t.TempDir
// is "no such file or directory", and a row that fataled there would look like a
// missing artifact rather than a seal bug.
func g2Abs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("cannot resolve %s: %v", p, err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("tracked binary %s is missing: %v — it is committed to this repo and these seals measure it; a suite that skipped here would go green on exactly the checkout where it mattered", abs, err)
	}
	return abs
}

// ─── the base fixture: a run-state the real producer wrote ───────────────────

// g2WalletDiff is the one-file diff cmd/classify's own seal fixture set uses for
// its critical money-path case. Reproduced rather than imported because each
// cmd/ tool is its own Go module.
func g2WalletDiff() string {
	const f = "apps/finance-domain/wallet/service/debit.go"
	return "diff --git a/" + f + " b/" + f + "\n--- a/" + f + "\n+++ b/" + f + "\n@@ -1 +1 @@\n-old\n+new\n"
}

// g2Worktree builds the minimal tree the pipeline needs: a Go module owning the
// changed file the classification names, with one package whose coverage is
// below the 95% financial floor. Without a module, cmd/gates exits 3
// INVALID_INPUT before writing anything, and without a coverage violation the
// sharpest measured instance of the foreign-record loss — a gate that says it
// failed and no longer says why — has nothing to lose.
func g2Worktree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mod := filepath.Join(root, "apps", "finance-domain", "wallet")
	if err := os.MkdirAll(filepath.Join(mod, "service"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(mod, "go.mod"), "module example.com/wallet\n\ngo 1.21\n")
	write(filepath.Join(mod, "service", "debit.go"), `package service

func Debit(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	if n == 7 {
		return 7
	}
	if n == 8 {
		return 8
	}
	if n == 9 {
		return 9
	}
	return n
}
`)
	write(filepath.Join(mod, "service", "debit_test.go"), `package service

import "testing"

func TestDebit(t *testing.T) {
	if Debit(5) != 5 {
		t.Fatal("no")
	}
}
`)
	return root
}

// g2ProducedRunState runs the PINNED cmd/classify binary and returns the
// run-state it wrote, verbatim.
//
// PRODUCIBILITY. This is the strongest form of "production can produce this
// input" available: the input is not described, reconstructed or hand-written,
// it is produced, by the frozen v1 producer, in this call. Every classification
// key these seals defend is in the document because classify put it there.
//
// It FAILS rather than skips when the producer is absent: a differential that
// quietly skips when it cannot find its baseline goes green on exactly the
// machine where it mattered least to run.
func g2ProducedRunState(t *testing.T, worktree string) []byte {
	t.Helper()
	if _, err := os.Stat(pinnedClassifyBinary); err != nil {
		t.Fatalf("pinned v1 producer %s is missing: %v — these seals generate their fixture with it rather than hand-writing one", pinnedClassifyBinary, err)
	}
	dir := t.TempDir()
	diffPath := filepath.Join(dir, "wallet.diff")
	if err := os.WriteFile(diffPath, []byte(g2WalletDiff()), 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "run-state.json")
	cmd := exec.Command(pinnedClassifyBinary,
		"-no-git", "-config", pinnedClassifyConfig, "-task", "SMG-9001", "-out", outPath, diffPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("pinned classify failed: %v\nstderr: %s", err, stderr.String())
	}
	doc, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("pinned classify wrote no run-state: %v", err)
	}

	// repo.worktree must point at a tree the gates binary can discover modules
	// in, and base_sha must satisfy the schema's 7..40 hex pattern. -no-git
	// leaves both unusable for an end-to-end run.
	doc = g2SetTopLevel(t, doc, "repo", string(g2SetTopLevel(t, g2Member(t, doc, "repo"),
		"worktree", g2Quote(worktree), "base_sha", `"abc1234"`)))
	return doc
}

// ─── the probes ──────────────────────────────────────────────────────────────
//
// PRODUCIBILITY, group by group, because these are not alike and three of them
// carry a recorded DISPUTE.
//
//	gates (9 records)          PRODUCED BY A REAL TOOL, TODAY. g2GatesBlock is
//	                           the tracked cmd/gates binary's own output over
//	                           this exact fixture, measured (see the constant).
//	                           This is the producible twin for the whole
//	                           "paths destroyed inside a surviving container"
//	                           property, and it is why the rounds[0] probes below
//	                           are not the only leg of that property.
//	deferred_findings[0].line  SCHEMA-LEGAL AND DRIVER-OWNED. The schema types
//	pr.lines_changed           both as `integer` with NO maximum, and names a
//	                           driver as the owner of `pr` and
//	                           `deferred_findings`. Both are DECLARED fields on
//	                           this package's RunState (:73, :74) whose
//	                           declarations bottom out in `any`, so the value is
//	                           corrupted while the path survives. Measured:
//	                           9007199254740993 -> 9007199254740992, both of them.
//	                           The value is above 2^53 because that is the
//	                           smallest magnitude at which the corruption is
//	                           visible, not because a driver writes that number.
//	repo.dirty: false          NOT PRODUCED BY ANY WRITER TODAY. Seeded, and it
//	                           carries G1's DISPUTE verbatim: config/run-state
//	                           .schema.json names cmd/classify as the only writer
//	                           of `repo`, and classify declares Dirty with
//	                           `omitempty` too (classify/main.go:121), so nothing
//	                           in this repo emits repo.dirty:false. Preserving it
//	                           is still correct, costs nothing under passthrough,
//	                           and a unit whose subject is preservation must not
//	                           be the unit that argues itself out of preserving a
//	                           key. It is the ZERO-VALUE leg and it is never the
//	                           only leg of its row: the two number corruptions
//	                           above are fully producible and carry the same
//	                           conclusion — declaring a field does not save it.
//	rounds[0].prior_findings_still_open: 0
//	                           the same erasure, inside an UNLICENSED array
//	                           element. Round.PriorStillOpen is declared with
//	                           `omitempty` (main.go:107) and 0 is what
//	                           cmd/recheck reports for a clean round, so the value
//	                           is the ordinary one; that iterate's own `omitempty`
//	                           means iterate never persists it is part of why
//	                           preservation must not run through the struct.
//	contract_version           FORWARD-COMPATIBILITY PROBES, by definition not
//	classification.zzz_future_key  emitted by any writer in this repo: the
//	rounds[0].zzz_round_future     property is that a key THIS BUILD DOES NOT
//	rounds[0].evidence_id          KNOW survives, and a key a current writer
//	rounds[0].reviewers[0].zzz_reviewer_future   emits cannot test it.
//
// DISPUTE, recorded and not resolved here. config/run-state.schema.json sets
// `additionalProperties: false` at the top level and on `classification`,
// `rounds.items` and `rounds.items.reviewers.items`, so all five
// forward-compatibility probes are ILLEGAL under schema v1 as written. Three
// things, and then why they are sealed anyway:
//
//	(a) That is what a forward-compatibility probe IS. The writer that emits
//	    contract_version is schema v2, and a preservation contract that only
//	    preserved what v1 declares would be preserve-by-declaration with the
//	    declaration moved into a JSON file.
//	(b) The schema is ALREADY not a description of what these tools write.
//	    preserve.go finding (10): both cmd/gates and cmd/iterate write gate keys
//	    of the form "<gate>:<module-rel>" while the schema constrains `gates`
//	    with propertyNames.enum to bare names. Every record in g2GatesBlock is
//	    schema-illegal and was written by the tracked binary.
//	(c) G1 sealed contract_version and classification.zzz_future_key on the same
//	    reasoning and the ruling stands. G2 extends it INTO an array element,
//	    which is new, and preserve.go ruling (3b) answers the objection
//	    ("iterate wrote every element of rounds[] itself") explicitly: the
//	    retroactive loss bites the moment any other writer annotates a round.
//
// What is NOT disputed is that the property has a fully producible twin in the
// same document: 34 paths destroyed inside the 9 surviving gate records, every
// one of them written by a tool that ran minutes earlier. No row leans on a
// probe alone.

// g2GatesBlock is a `gates` member in the EXACT shape the tracked cmd/gates
// binary writes it, measured by running ../gates/gates over this fixture's
// worktree with ../gates/testdata/example-gates.json.
//
// Nine records. Their field sets are not uniform and the variation is load
// bearing, so it is reproduced rather than smoothed:
//
//	build/complexity/gosec/lint/staticcheck/test   status command ran_at
//	                                              duration_ms output_path
//	mutation                                      the above plus exit_code
//	coverage                                      status ran_at metrics{...}
//	semgrep                                       status ran_at skip_reason
//
// cmd/iterate's Gate declares TWO fields (preserve.go :93-96) — status and
// skip_reason — against cmd/gates' eight. So `semgrep` loses one path and every
// other record loses four to five, for 34 in total. The coverage record is the
// sharpest: status:"fail" is declared and survives, and the metrics that say WHY
// it failed do not.
//
// A row that runs the real binary (TestSeal_G2_EndToEnd_...) cross-checks this
// constant's field sets against what gates actually writes, so the constant
// cannot drift into a shape gates no longer produces while every row stays green.
const g2GatesBlock = `{
    "build:apps/finance-domain/wallet": {
      "status": "pass",
      "command": "go build ./...",
      "ran_at": "2026-08-11T08:17:07Z",
      "duration_ms": 10,
      "output_path": "/tmp/gate-output/build-apps_finance-domain_wallet.log"
    },
    "test:apps/finance-domain/wallet": {
      "status": "pass",
      "command": "go test -race -cover -coverprofile=/tmp/gate-output/test.coverprofile ./...",
      "ran_at": "2026-08-11T08:17:07Z",
      "duration_ms": 1200,
      "output_path": "/tmp/gate-output/test-apps_finance-domain_wallet.log"
    },
    "coverage:apps/finance-domain/wallet": {
      "status": "fail",
      "ran_at": "2026-08-11T08:17:08Z",
      "metrics": {
        "packages_evaluated": 1,
        "violations": ["apps/finance-domain/wallet/service at 54.5% < floor 95%"],
        "worst_coverage_pct": 54.5
      }
    },
    "lint:apps/finance-domain/wallet": {
      "status": "pass",
      "command": "golangci-lint run",
      "ran_at": "2026-08-11T08:17:08Z",
      "duration_ms": 690,
      "output_path": "/tmp/gate-output/lint-apps_finance-domain_wallet.log"
    },
    "complexity:apps/finance-domain/wallet": {
      "status": "pass",
      "command": "go-complexity-lint ./...",
      "ran_at": "2026-08-11T08:17:09Z",
      "duration_ms": 11,
      "output_path": "/tmp/gate-output/complexity-apps_finance-domain_wallet.log"
    },
    "gosec:apps/finance-domain/wallet": {
      "status": "pass",
      "command": "gosec ./...",
      "ran_at": "2026-08-11T08:17:09Z",
      "duration_ms": 48,
      "output_path": "/tmp/gate-output/gosec-apps_finance-domain_wallet.log"
    },
    "staticcheck:apps/finance-domain/wallet": {
      "status": "pass",
      "command": "staticcheck ./...",
      "ran_at": "2026-08-11T08:17:09Z",
      "duration_ms": 89,
      "output_path": "/tmp/gate-output/staticcheck-apps_finance-domain_wallet.log"
    },
    "mutation:apps/finance-domain/wallet": {
      "status": "fail",
      "command": "gremlins unleash --integration --threshold-efficacy 80 --tags=integration --diff abc1234 -o /tmp/gate-output/mutation.results.json .",
      "ran_at": "2026-08-11T08:17:09Z",
      "duration_ms": 3,
      "exit_code": 1,
      "output_path": "/tmp/gate-output/mutation-apps_finance-domain_wallet.log"
    },
    "semgrep": {
      "status": "fail",
      "ran_at": "2026-08-11T08:17:09Z",
      "skip_reason": "required gate could not run: required file tools/semgrep/rules.yml not found in the worktree — that file IS the gate (waive explicitly with -waive semgrep=<reason>)"
    }
  }`

// g2RoundZero is the rounds[0] element every fixture carries: a real round in
// the shape cmd/iterate itself records (recordFull's output, main.go:343-368),
// annotated by a foreign writer.
//
// The annotation is the probe; the rest is producible. verdict "ITERATE" and
// new_finding_count 3 are chosen so decide() falls through to the ceiling arm at
// -ceiling 0, which is the branch that reaches appendRound — measured.
const g2RoundZero = `{
      "round": 1,
      "kind": "full",
      "reviewed_sha": "abc1234",
      "status": "review_complete",
      "verdict": "ITERATE",
      "findings_path": "/tmp/findings-SMG-9001-r1.json",
      "new_finding_count": 3,
      "at_or_above_floor_count": 3,
      "prior_findings_still_open": 0,
      "reviewers": [
        {"name": "security", "score": 7, "verdict": "iterate", "zzz_reviewer_future": "a reviewer field no build in this repo declares"}
      ],
      "completed_at": "2026-08-11T07:00:00Z",
      "evidence_id": 9007199254740993,
      "zzz_round_future": "a round field no build in this repo declares"
    }`

// g2SeedProbes assembles the full fixture: the produced classification, the
// measured gates block, and the probes.
//
// It edits through json.RawMessage rather than through any typed struct, so the
// number literals arrive in the fixture exactly as written. A fixture that lost
// 9007199254740993 on its way IN could never seal its loss on the way out — and
// it verifies that before returning.
func g2SeedProbes(t *testing.T, doc []byte) []byte {
	t.Helper()
	doc = g2SetTopLevel(t, doc,
		"gates", g2GatesBlock,
		"rounds", "["+g2RoundZero+"]",
		"contract_version", `2`,
		"pr", `{"raised": false, "lines_changed": 9007199254740993}`,
		"deferred_findings", `[{"severity":"medium","summary":"unbounded debit path","file":"apps/finance-domain/wallet/service/debit.go","line":9007199254740993,"found_in_round":1}]`,
	)
	doc = g2SetTopLevel(t, doc, "classification", string(g2SetTopLevel(t, g2Member(t, doc, "classification"),
		"zzz_future_key", `"a key no build in this repo declares"`)))
	doc = g2SetTopLevel(t, doc, "repo", string(g2SetTopLevel(t, g2Member(t, doc, "repo"), "dirty", `false`)))

	if _, err := Diverge(doc, doc); err != nil {
		t.Fatalf("seeded fixture does not parse: %v", err)
	}
	leaves := g2Flatten(t, doc)
	for _, p := range []string{"deferred_findings[0].line", "pr.lines_changed", "rounds[0].evidence_id"} {
		if got := leaves[p]; got != "9007199254740993" {
			t.Fatalf("the seeding lost the number literal before any row could seal it: %s = %s", p, got)
		}
	}
	g2AssertFixtureIsRich(t, doc)
	return doc
}

// g2AssertFixtureIsRich fails unless the fixture carries the keys every later
// row is about. It is the anti-collapse check: a collapsed input is one of the
// seven measured vacuity shapes, and it is the shape that would let every
// fidelity row here go green while preserving nothing.
func g2AssertFixtureIsRich(t *testing.T, doc []byte) {
	t.Helper()
	leaves := g2Flatten(t, doc)
	want := map[string]string{
		// the classification — what cmd/classify wrote and iterate destroys
		"classification.recheck_min_severity":      `"medium"`,
		"classification.changed_files[0].risk":     `"critical"`,
		"classification.changed_files[0].rules[0]": `"wallet-service"`,
		"classification.panel.seats":               `5`,
		// the foreign records — what cmd/gates wrote minutes earlier
		"gates.coverage:apps/finance-domain/wallet.status": `"fail"`,
		// g2Quote, not a hand-written literal: the violation text contains '<'
		// and encoding/json re-encodes it as the six-byte < escape.
		// preserve.go names this as live in G2's inputs rather than
		// hypothetical, and it is — the hand-written form fails here.
		"gates.coverage:apps/finance-domain/wallet.metrics.violations[0]": g2Quote("apps/finance-domain/wallet/service at 54.5% < floor 95%"),
		"gates.semgrep.status":                           `"fail"`,
		"gates.build:apps/finance-domain/wallet.command": `"go build ./..."`,
		// the array element an append must not touch
		"rounds[0].zzz_round_future":                 `"a round field no build in this repo declares"`,
		"rounds[0].reviewers[0].zzz_reviewer_future": `"a reviewer field no build in this repo declares"`,
		"rounds[0].evidence_id":                      `9007199254740993`,
		"rounds[0].round":                            `1`,
		// declaration does not save values — corrupted, and erased
		"repo.dirty":                          `false`,
		"rounds[0].prior_findings_still_open": `0`,
		"deferred_findings[0].line":           `9007199254740993`,
		"pr.lines_changed":                    `9007199254740993`,
	}
	for path, lit := range want {
		got, ok := leaves[path]
		if !ok {
			t.Fatalf("fixture is collapsed: it does not carry %s, so no row here can exhibit the defect it is about", path)
		}
		if got != lit {
			t.Fatalf("fixture drifted: %s = %s, want %s — the seals were written against the measured output of the pinned producer and the tracked gates binary", path, got, lit)
		}
	}
	if n := len(g2TopLevelKeys(t, g2Member(t, doc, "classification"))); n < 15 {
		t.Fatalf("fixture is collapsed: classification has %d top-level keys, want the producer's 15 — the defect is a 15→4 loss and a thin fixture cannot show it", n)
	}
	if n := len(g2TopLevelKeys(t, g2Member(t, doc, "gates"))); n != 9 {
		t.Fatalf("fixture is collapsed: the gates block has %d records, want the 9 the tracked binary wrote — 34 of the 60 destroyed paths are inside them", n)
	}
}

// ─── the canonical edit list ─────────────────────────────────────────────────

// g2UpdatedAt is the timestamp the edits set. Fixed, so a row's expectations do
// not depend on the clock.
const g2UpdatedAt = "2026-08-11T12:00:00Z"

// g2EscalationReason is the reason cmdRun's stop branch produces at -ceiling 0,
// measured verbatim from the tracked binary's own output.
const g2EscalationReason = "iteration ceiling 0 reached with findings still open — write Status: Blocked with the per-round lineage"

// g2AppendedRound is the Round cmdRun's stop branch builds (main.go:621-624 at
// 704b65b; it was :584-587 before the fix moved main.go down by 61 lines).
// Producible: it is the record the measurement's `iterate run -ceiling 0`
// actually wrote.
func g2AppendedRound() Round {
	return Round{Round: 2, Kind: "controller", Verdict: "ESCALATE", CompletedAt: "2026-08-11T12:00:00Z"}
}

// g2Edits is the licensed edit list for the run the fixture sets up: one append
// at index 1, and the escalating arm of appendRound's switch.
//
// ALL SIX KINDS, including the conditional one. The escalating arm is chosen
// deliberately over APPROVE/ITERATE because it is the only arm that emits
// EditKindSetEscalationReason, and a canonical list that never carried the
// conditional edit would leave a sixth of the licence unexercised by every
// fidelity row. It matches the measurement exactly: `iterate run -ceiling 0`
// over this fixture wrote status "escalated", verdict "escalate" and this
// escalation_reason.
func g2Edits() []Edit {
	return []Edit{
		{Kind: EditKindAppendRound, Record: g2AppendedRound(), AtIndex: 1},
		{Kind: EditKindSetRound, RoundNumber: 2},
		{Kind: EditKindSetVerdict, Verdict: "escalate"},
		{Kind: EditKindSetUpdatedAt, UpdatedAt: g2UpdatedAt},
		{Kind: EditKindSetStatus, Status: "escalated"},
		{Kind: EditKindSetEscalationReason, EscalationReason: g2EscalationReason},
	}
}

// ─── the measure ─────────────────────────────────────────────────────────────

// g2DivergencesOutside is the seal's own acceptance measure: every path at which
// produced differs from original and which the edit list's Licence does not
// allow.
//
// It is built from Diverge, LicensedPaths and Licence.Allows — all three
// implemented in the scaffold, all three this contract's own — and NOT from
// VerifyPreservation, which is a stub. That separation is deliberate: the
// fidelity rows must be red because ApplyRoundRecord does not preserve, not
// because the checker they use is unimplemented. A row that measured a stub with
// a stub would be red for no reason at all.
//
// IT IS THE FULL CONTRACT MEASURE, not G1's weakened one. See the header: a
// plain prefix over `rounds` forgives exactly the losses G2 exists to stop.
func g2DivergencesOutside(t *testing.T, original, produced []byte, edits []Edit) []Divergence {
	t.Helper()
	lic, err := LicensedPaths(edits)
	if err != nil {
		t.Fatalf("LicensedPaths: %v", err)
	}
	ds, err := Diverge(original, produced)
	if err != nil {
		t.Fatalf("Diverge: %v", err)
	}
	var out []Divergence
	for _, d := range ds {
		if !lic.Allows(d.At) {
			out = append(out, d)
		}
	}
	return out
}

// g2Report renders a divergence list for a failure message, capped so a 60-row
// loss does not bury the sentence that explains it.
func g2Report(ds []Divergence) string {
	const capN = 14
	var b strings.Builder
	for i, d := range ds {
		if i == capN {
			fmt.Fprintf(&b, "\n  ... and %d more", len(ds)-capN)
			break
		}
		b.WriteString("\n  ")
		b.WriteString(d.String())
	}
	return b.String()
}

// ─── the control: today's appendRound, in process ────────────────────────────

// g2LegacyProjection reproduces what appendRound did BEFORE the fix —
// json.Unmarshal into this package's RunState, apply the six assignments,
// json.MarshalIndent back (7028605's main.go:441-467; appendRound is
// main.go:465-504 today and no longer takes this path) — and is the standard
// control for every fidelity row.
//
// IT MEASURES SOURCE, not the committed binary: it runs THIS package's structs
// in this process. That matters for its longevity. After the body lands,
// appendRound will no longer take this path, but the structs will still be
// closed — preserve.go's tripwires forbid widening them — so this control keeps
// showing the loss and keeps proving the fixture can exhibit it. A control that
// exec'd the tracked binary would go quiet the moment the binary was rebuilt,
// which is the frozen-artifact hazard pointed the other way.
//
// It applies the EDIT LIST rather than taking a verdict string, so the control
// and the row are provably about the same mutation.
func g2LegacyProjection(t *testing.T, original []byte, edits []Edit) []byte {
	t.Helper()
	var s RunState
	if err := json.Unmarshal(original, &s); err != nil {
		t.Fatalf("legacy control: the fixture is not a run-state today's iterate can read: %v", err)
	}
	for _, e := range edits {
		switch e.Kind {
		case EditKindAppendRound:
			s.Rounds = append(s.Rounds, e.Record)
		case EditKindSetRound:
			s.Round = e.RoundNumber
		case EditKindSetVerdict:
			s.Verdict = e.Verdict
		case EditKindSetUpdatedAt:
			s.UpdatedAt = e.UpdatedAt
		case EditKindSetStatus:
			s.Status = e.Status
		case EditKindSetEscalationReason:
			s.EscalationReason = e.EscalationReason
		default:
			t.Fatalf("legacy control cannot apply edit kind %s", e.Kind)
		}
	}
	data, err := json.MarshalIndent(&s, "", "  ")
	if err != nil {
		t.Fatalf("legacy control: marshal: %v", err)
	}
	return append(data, '\n')
}

// ─── proof of execution ──────────────────────────────────────────────────────

// g2AssertEditsLanded fails unless every licensed edit is actually present in
// the produced document.
//
// This is the answer to the central vacuity in this unit: a body that returns
// `original` unchanged preserves every path perfectly and satisfies every
// fidelity assertion in the suite while recording no round at all. Preservation
// and application have to be judged together or neither is judged.
//
// THE APPEND LEG CHECKS THE ARRAY LENGTH, not only the new element's contents.
// A body that wrote rounds[AtIndex] by REPLACING an existing element would
// satisfy a contents-only check; the length is what distinguishes an append from
// an overwrite.
//
// THE VERDICT LEG IS THE ASYMMETRIC ONE. An empty Verdict payload does not write
// an empty string, it DELETES the member — RunState.Verdict is
// `json:"verdict,omitempty"` (main.go:70) and that deletion is what iterate does
// today (preserve.go finding (8)). So an empty payload is proved by ABSENCE.
func g2AssertEditsLanded(t *testing.T, produced []byte, edits []Edit) {
	t.Helper()
	leaves := g2Flatten(t, produced)
	for _, e := range edits {
		switch e.Kind {
		case EditKindAppendRound:
			base := (JSONPath{{Key: "rounds"}, {Index: e.AtIndex, IsIndex: true}}).String()
			if got, ok := leaves[base+".round"]; !ok || got != fmt.Sprint(e.Record.Round) {
				t.Errorf("PROOF OF EXECUTION FAILED: %s.round = %s (present=%v), want %d — the round was never appended, so every preservation assertion in this row is about a document nothing happened to",
					base, got, ok, e.Record.Round)
			}
			if got := leaves[base+".kind"]; got != g2Quote(e.Record.Kind) {
				t.Errorf("PROOF OF EXECUTION FAILED: %s.kind = %s, want %s", base, got, g2Quote(e.Record.Kind))
			}
			if got := leaves[base+".verdict"]; got != g2Quote(e.Record.Verdict) {
				t.Errorf("PROOF OF EXECUTION FAILED: %s.verdict = %s, want %s — the round that decides every later escalation was recorded without its verdict", base, got, g2Quote(e.Record.Verdict))
			}
			if n := g2ArrayLen(t, produced, "rounds"); n != e.AtIndex+1 {
				t.Errorf("PROOF OF EXECUTION FAILED: rounds has %d element(s), want %d. An append that leaves the length alone has REPLACED an element, and a contents-only check cannot tell the two apart.", n, e.AtIndex+1)
			}
		case EditKindSetRound:
			if got := leaves["round"]; got != fmt.Sprint(e.RoundNumber) {
				t.Errorf("PROOF OF EXECUTION FAILED: round = %s, want %d", got, e.RoundNumber)
			}
		case EditKindSetVerdict:
			if e.Verdict == "" {
				if got, ok := leaves["verdict"]; ok {
					t.Errorf("PROOF OF EXECUTION FAILED: verdict = %s, want the member to be ABSENT. An empty verdict payload deletes the member under `omitempty` (main.go:70) — that is what iterate does today and preserve.go finding (8) records it rather than fixing it.", got)
				}
				continue
			}
			if got := leaves["verdict"]; got != g2Quote(e.Verdict) {
				t.Errorf("PROOF OF EXECUTION FAILED: verdict = %s, want %s", got, g2Quote(e.Verdict))
			}
		case EditKindSetUpdatedAt:
			if got := leaves["updated_at"]; got != g2Quote(e.UpdatedAt) {
				t.Errorf("PROOF OF EXECUTION FAILED: updated_at = %s, want %s", got, g2Quote(e.UpdatedAt))
			}
		case EditKindSetStatus:
			if got := leaves["status"]; got != g2Quote(e.Status) {
				t.Errorf("PROOF OF EXECUTION FAILED: status = %s, want %s", got, g2Quote(e.Status))
			}
		case EditKindSetEscalationReason:
			if got := leaves["escalation_reason"]; got != g2Quote(e.EscalationReason) {
				t.Errorf("PROOF OF EXECUTION FAILED: escalation_reason = %s, want %s", got, g2Quote(e.EscalationReason))
			}
		default:
			t.Fatalf("seal bug: unhandled edit kind %s", e.Kind)
		}
	}
}

// ─── small JSON tools ────────────────────────────────────────────────────────
//
// All of these go through json.RawMessage. None decodes a value into `any`,
// because that is the very corruption these seals measure.

// g2Flatten is Diverge's own walk, exposed as path -> value literal.
func g2Flatten(t *testing.T, doc []byte) map[string]string {
	t.Helper()
	leaves, err := flattenDocument(doc)
	if err != nil {
		t.Fatalf("flatten: %v\n%s", err, doc)
	}
	out := make(map[string]string, len(leaves))
	for k, l := range leaves {
		out[k] = l.literal
	}
	return out
}

// g2Member returns one top-level member's raw bytes.
func g2Member(t *testing.T, doc []byte, key string) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatalf("not a JSON object: %v", err)
	}
	v, ok := m[key]
	if !ok {
		t.Fatalf("document has no %q member", key)
	}
	return v
}

// g2SetTopLevel sets top-level members from raw JSON literals and re-emits the
// object. Untouched members keep their original bytes, so nothing the caller did
// not name can be corrupted by the edit.
func g2SetTopLevel(t *testing.T, doc []byte, kv ...string) []byte {
	t.Helper()
	if len(kv)%2 != 0 {
		t.Fatalf("seal bug: g2SetTopLevel wants key/value pairs, got %d arguments", len(kv))
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatalf("not a JSON object: %v", err)
	}
	for i := 0; i < len(kv); i += 2 {
		if !json.Valid([]byte(kv[i+1])) {
			t.Fatalf("seal bug: value for %q is not valid JSON: %s", kv[i], kv[i+1])
		}
		m[kv[i]] = json.RawMessage(kv[i+1])
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("re-emit: %v", err)
	}
	return append(out, '\n')
}

// g2SetRounds replaces the `rounds` member with the given raw element literals.
// Used by the malformation rows to hand-build a produced document that exhibits
// a shift, a prepend, a double append or a truncation.
func g2SetRounds(t *testing.T, doc []byte, elements ...string) []byte {
	t.Helper()
	return g2SetTopLevel(t, doc, "rounds", "["+strings.Join(elements, ",")+"]")
}

// g2ArrayLen returns the length of a top-level array member.
func g2ArrayLen(t *testing.T, doc []byte, key string) int {
	t.Helper()
	var els []json.RawMessage
	if err := json.Unmarshal(g2Member(t, doc, key), &els); err != nil {
		t.Fatalf("%s is not an array: %v", key, err)
	}
	return len(els)
}

// g2RoundAt returns one element of `rounds` as raw bytes.
func g2RoundAt(t *testing.T, doc []byte, i int) []byte {
	t.Helper()
	var els []json.RawMessage
	if err := json.Unmarshal(g2Member(t, doc, "rounds"), &els); err != nil {
		t.Fatalf("rounds is not an array: %v", err)
	}
	if i >= len(els) {
		t.Fatalf("rounds has %d element(s); no index %d", len(els), i)
	}
	return els[i]
}

// g2TopLevelKeys returns a JSON object's top-level keys, sorted.
func g2TopLevelKeys(t *testing.T, doc []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatalf("not a JSON object: %v", err)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// g2Quote renders a Go string as the JSON literal Diverge would report for it,
// including encoding/json's HTML escaping — so a seal's expectation and a
// Divergence's Before/After are written in the same alphabet. preserve.go names
// this as live rather than hypothetical: the coverage violation text contains
// '<' and is reported with the six-byte escape.
func g2Quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `"` + s + `"`
	}
	return string(b)
}

// g2WriteTemp writes a document to a fresh temp file and returns the path.
func g2WriteTemp(t *testing.T, name string, doc []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
