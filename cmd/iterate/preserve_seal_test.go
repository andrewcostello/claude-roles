package main

// Unit G2 seals — cmd/iterate must stop destroying the classification, the gate
// records, and the rounds it did not write.
//
// Sixteen rows. All sixteen are RED at the scaffold commit, and the failure list
// is the body author's checklist. preserve_seal_helpers_test.go carries the
// rules these rows hold themselves to; read its header first.
//
// WHAT EACH ROW MEASURES — source, or the committed artifact cmd/iterate/iterate.
// The distinction matters because cmd/iterate/iterate is TRACKED, and a row that
// execs it measures a frozen artifact that cannot see a source fix.
//
//	rows 1-13   SOURCE, in process. They call this package's own functions, or
//	            its own source text. Row 13 is appendRound itself — the seam.
//	row 14      SOURCE, out of process: `go build -o <scratch>` and run THAT,
//	            with the TRACKED cmd/gates binary as the oracle. Building to a
//	            scratch path is not hygiene here, it is the whole difference
//	            between measuring the fix and overwriting the artifact — see
//	            preserve.go finding (7).
//	row 15      THE COMMITTED ARTIFACT, deliberately, with its trigger stated.
//	row 16      SOURCE, as text: main.go parsed with go/parser.
//
// THE MEASURED DEFECT these rows are written against, re-measured in this
// worktree rather than inherited from the scaffold's note. Pinned cmd/classify
// produced the base document; the TRACKED cmd/gates ran a full pass over it;
// probes were seeded through json.RawMessage; the TRACKED cmd/iterate ran
// `run -ceiling 0`. Compared per JSON path:
//
//	REMOVED 60   ADDED 8   CHANGED 3
//
//	34 of the 60 are destroyed INSIDE the 9 surviving gate records — one more
//	than the scaffold's 33, because this fixture's mutation gate also carries an
//	exit_code. 3 are destroyed retroactively inside rounds[0] by an append whose
//	only intent was rounds[1]. 12 are classification keys. 2 of the 3 CHANGED are
//	number corruptions in DECLARED fields.
//
//	cmd/gates over the result: exit 3 INVALID_INPUT, "no Go modules own any
//	changed file — nothing this tool can gate". The pipeline cannot gate at all.
//
// Two bounds, confirmed rather than assumed: `iterate next` leaves the file
// BYTE-IDENTICAL, and the collapse is a one-shot fixed point (a second run
// removes 0 more).

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ─── 1. round-trip fidelity ──────────────────────────────────────────────────

// TestSeal_G2_ApplyRoundRecord_PreservesEveryPathOutsideTheLicensedEdits is the
// core row. Most of what follows is a named consequence of it.
//
// MEASURES SOURCE, in process.
//
// INPUT: a run-state written by the pinned cmd/classify binary in this call,
// carrying the tracked cmd/gates binary's measured output and the probes
// documented on g2SeedProbes.
//
// CONTROL, judged in the same call: g2LegacyProjection — today's appendRound
// reproduced from this package's own structs — must show a loss of 50+ paths
// over the identical input and edit list. If the control ever comes out clean,
// the fixture has stopped being able to exhibit the defect and this row has
// become vacuous; investigate the fixture, do not relax the assertion.
//
// PROOF OF EXECUTION: g2AssertEditsLanded, which for the append checks the array
// LENGTH and not only the new element's contents.
func TestSeal_G2_ApplyRoundRecord_PreservesEveryPathOutsideTheLicensedEdits(t *testing.T) {
	defer g2Red(t)

	original := g2SeedProbes(t, g2ProducedRunState(t, g2Worktree(t)))
	edits := g2Edits()

	// CONTROL FIRST: prove the fixture can exhibit the defect before asking
	// anything of the body.
	legacy := g2DivergencesOutside(t, original, g2LegacyProjection(t, original, edits), edits)
	if len(legacy) < 50 {
		t.Fatalf("CONTROL FAILED: today's round-trip destroys only %d paths outside the licensed edits, and the measured defect is 60+. Either the fixture collapsed or the structs were widened — this row cannot certify anything until the control fires.%s",
			len(legacy), g2Report(legacy))
	}

	produced, err := ApplyRoundRecord(original, edits)
	if err != nil {
		t.Fatalf("ApplyRoundRecord: %v", err)
	}
	if len(produced) == 0 {
		t.Fatal("ApplyRoundRecord returned no bytes and no error — a successful merge that produced nothing would erase the run-state at the call site")
	}

	if v := g2DivergencesOutside(t, original, produced, edits); len(v) > 0 {
		t.Errorf("SEAL RED — %d JSON path(s) outside the licensed edits diverged. FidelityPathwise means every path present in the original and not covered by a licensed edit carries an IDENTICAL VALUE LITERAL in the produced document:%s",
			len(v), g2Report(v))
	}

	g2AssertEditsLanded(t, produced, edits)

	// iterate must still be able to read what it wrote: it runs once per round,
	// and a preserved document its own reader rejects has traded one failure for
	// a worse one.
	if _, err := readRunState(g2WriteTemp(t, "produced.json", produced)); err != nil {
		t.Errorf("SEAL RED — iterate cannot re-read its own output: %v", err)
	}
}

// ─── 2. unknown keys ─────────────────────────────────────────────────────────

// TestSeal_G2_ApplyRoundRecord_PreservesUnknownKeysTopLevelAndNested seals the
// forward-compatibility property the v2 sidecar exists to work around
// (classify/contract.go:467-473), whose second half G2 falsifies.
//
// MEASURES SOURCE, in process.
//
// FOUR LEGS, and the last two are what make the row producible rather than
// hypothetical. The DISPUTE about the first two — schema v1 sets
// additionalProperties:false, so no v1 document may legally carry them — is
// recorded in full on g2SeedProbes and is not relitigated here.
//
//	contract_version                     a key a NEWER writer emits
//	classification.zzz_future_key        the same, nested
//	classification.changed_files[0].risk a key NO STRUCT in cmd/iterate declares,
//	                                     that the pinned classify writes on every
//	                                     critical run, and that iterate destroys
//	                                     today
//	gates.build:….command                a key written by the tool that ran
//	                                     immediately before iterate, minutes ago
//
// The unknown-key property is not a future concern in this package. Production
// is already losing keys to it, on every run, from two different writers.
func TestSeal_G2_ApplyRoundRecord_PreservesUnknownKeysTopLevelAndNested(t *testing.T) {
	defer g2Red(t)

	original := g2SeedProbes(t, g2ProducedRunState(t, g2Worktree(t)))
	edits := g2Edits()

	unknown := map[string]string{
		"contract_version":                               "2",
		"classification.zzz_future_key":                  g2Quote("a key no build in this repo declares"),
		"classification.changed_files[0].risk":           g2Quote("critical"),
		"gates.build:apps/finance-domain/wallet.command": g2Quote("go build ./..."),
	}

	before := g2Flatten(t, original)
	for path, lit := range unknown {
		if got, ok := before[path]; !ok || got != lit {
			t.Fatalf("fixture does not carry %s = %s (got %s, present=%v); the row cannot seal a key its input does not have", path, lit, got, ok)
		}
	}

	// CONTROL — every one of them must be destroyed by today's projection.
	legacy := g2Flatten(t, g2LegacyProjection(t, original, edits))
	for path := range unknown {
		if _, ok := legacy[path]; ok {
			t.Fatalf("CONTROL FAILED: today's round-trip PRESERVES %s. This row cannot distinguish a fix from the status quo — check whether a struct was widened, which preserve.go's tripwires forbid.", path)
		}
	}

	produced, err := ApplyRoundRecord(original, edits)
	if err != nil {
		t.Fatalf("ApplyRoundRecord: %v", err)
	}
	after := g2Flatten(t, produced)
	for path, lit := range unknown {
		got, ok := after[path]
		if !ok {
			t.Errorf("SEAL RED — %s was DROPPED. Preserve-by-passthrough must carry a key it cannot name: nothing in the merge may decode the subtree that holds it.", path)
			continue
		}
		if got != lit {
			t.Errorf("SEAL RED — %s = %s, want %s. Present-with-a-different-value is not preserved; FidelityKeySet would accept this and is explicitly NOT normative.", path, got, lit)
		}
	}

	g2AssertEditsLanded(t, produced, edits)
}

// ─── 3. declaring a field does not save its value ────────────────────────────

// TestSeal_G2_ApplyRoundRecord_PreservesNumberLiteralsInDeclaredFields is the
// row that kills the declaration design a second time, on ground G1 never had to
// defend.
//
// MEASURES SOURCE, in process.
//
// G1's argument against widening the structs was that a struct cannot carry a
// key it does not declare. This row is the sharper one: DECLARING THE FIELD DOES
// NOT SAVE THE VALUE. RunState.PR is map[string]any (:73) and
// RunState.DeferredFindings is []any (:74), so both members survive as PATHS and
// every number inside them is routed through float64 and comes back changed.
// Measured twice in this package's own output:
//
//	deferred_findings[0].line  9007199254740993 -> 9007199254740992
//	pr.lines_changed           9007199254740993 -> 9007199254740992
//
// Both are schema-declared integers with no maximum, in members the schema
// assigns to a driver. A fix by declaration would have to declare not just every
// key but every key's full TYPE, all the way down, for two members whose
// contents this package has no business knowing.
//
// It is also the row that makes FidelityPathwise's "identical value LITERAL"
// normative rather than decorative: a decoded-value comparison calls this
// corruption preserved, because both literals are the same float64.
//
// SECOND LEG, same mechanism pointed at absence: repo.dirty:false and
// rounds[0].prior_findings_still_open:0 are DECLARED fields of this package's
// own structs, and `omitempty` erases both. repo.dirty carries G1's recorded
// DISPUTE (no writer emits it today) and is therefore never this row's only
// evidence — the two number corruptions above are fully producible and carry
// the same conclusion on their own.
//
// CONTROL, same call: the legacy projection must show the corruption and the
// erasure, and a decoded-value comparison over the same pair must call the
// corruption CLEAN — the row proves the necessity of its own measure.
func TestSeal_G2_ApplyRoundRecord_PreservesNumberLiteralsInDeclaredFields(t *testing.T) {
	defer g2Red(t)

	original := g2SeedProbes(t, g2ProducedRunState(t, g2Worktree(t)))
	edits := g2Edits()

	const big = "9007199254740993"
	const rounded = "9007199254740992"

	legacy := g2Flatten(t, g2LegacyProjection(t, original, edits))

	// CONTROL A — the corruption really happens, in a path that SURVIVES.
	for _, p := range []string{"deferred_findings[0].line", "pr.lines_changed"} {
		got, ok := legacy[p]
		if !ok {
			t.Fatalf("CONTROL FAILED: %s is absent from today's projection, so this row would be about a removal and not a corruption — the point is that the PATH survives and the VALUE does not", p)
		}
		if got != rounded {
			t.Fatalf("CONTROL FAILED: %s = %s after today's round-trip, want the measured %s. Without the corruption this row cannot distinguish a value-preserving fix from a path-preserving one.", p, got, rounded)
		}
	}

	// CONTROL B — a key-set reading calls that document clean. This is why
	// FidelityKeySet is named and rejected rather than left unmentioned.
	origLeaves := g2Flatten(t, original)
	if _, ok := origLeaves["deferred_findings[0].line"]; !ok {
		t.Fatal("CONTROL FAILED: the fixture has no deferred_findings[0].line")
	}
	if _, ok := legacy["deferred_findings[0].line"]; !ok {
		t.Fatal("CONTROL FAILED: key-set equality would already have caught this, and the row's argument is that it would not")
	}

	// CONTROL C — the zero-value erasure of DECLARED fields.
	zeroValued := map[string]string{
		"repo.dirty":                          "false",
		"rounds[0].prior_findings_still_open": "0",
	}
	for p, lit := range zeroValued {
		if got, ok := origLeaves[p]; !ok || got != lit {
			t.Fatalf("fixture does not carry %s = %s (got %s); the omitempty leg has nothing to seal", p, lit, got)
		}
		if _, ok := legacy[p]; ok {
			t.Fatalf("CONTROL FAILED: today's projection preserves %s, so `omitempty` is no longer erasing it and this leg certifies nothing", p)
		}
	}

	produced, err := ApplyRoundRecord(original, edits)
	if err != nil {
		t.Fatalf("ApplyRoundRecord: %v", err)
	}
	after := g2Flatten(t, produced)

	for _, p := range []string{"deferred_findings[0].line", "pr.lines_changed"} {
		if got := after[p]; got != big {
			t.Errorf("SEAL RED — %s = %s, want the source literal %s. The member is DECLARED and its declaration bottoms out in `any`, so a merge that decodes it corrupts it. Nothing in ApplyRoundRecord may decode `pr` or `deferred_findings` at all.", p, got, big)
		}
	}
	for p, lit := range zeroValued {
		if got, ok := after[p]; !ok || got != lit {
			t.Errorf("SEAL RED — %s = %s (present=%v), want %s. `omitempty` erases a declared zero value, which is the second reason preserve-by-declaration does not actually preserve.", p, got, ok, lit)
		}
	}

	g2AssertEditsLanded(t, produced, edits)
}

// ─── 4. the foreign records ──────────────────────────────────────────────────

// TestSeal_G2_ApplyRoundRecord_PreservesTheGateRecordsItDidNotWrite is the row
// for the loss with the worst blast radius, and it is deliberately NOT a second
// property — see preserve.go ruling (2). The passthrough that saves
// changed_files saves gates.*.command by the same act of not decoding, with no
// extra line of code. What differs is who is hurt and who could ever notice.
//
// MEASURES SOURCE, in process.
//
// THREE THINGS THIS ROW SHOWS THAT ROW 1's COUNT DOES NOT:
//
//	(a) THE RECORDS SURVIVE BY NAME. All nine gate keys are still there after
//	    today's projection. A reading that checks which gates ran reports
//	    success on a document that has lost 34 paths inside them. The row asserts
//	    that survival as a CONTROL, so the argument is executable rather than
//	    prose.
//	(b) IT DESTROYS EVIDENCE, NOT CONFIGURATION. command, ran_at, duration_ms,
//	    output_path, exit_code and metrics are the audit trail of whether a gate
//	    ACTUALLY RAN. After `iterate run`, {"status":"pass"} is indistinguishable
//	    from a hand-typed pass — the "we did not check but it is fine" status
//	    cmd/gates' own config says it exists to forbid.
//	(c) THE SHARPEST INSTANCE, measured: the coverage gate's
//	    metrics.violations[0] — "apps/finance-domain/wallet/service at 54.5% <
//	    floor 95%" — is destroyed while status:"fail" survives. The document says
//	    a gate failed and no longer says why.
//
// AND THE FACT THAT MAKES IT WORSE THAN ANYTHING IN G1: no test in either module
// can see this. cmd/iterate's suite round-trips cmd/iterate's structs and is
// green. cmd/gates' suite measures documents cmd/gates wrote. The loss exists
// only in the composition, the two packages are separate Go modules with no
// shared test, and it has been green on both sides the whole time. The last
// CONTROL in this row is that fact, made executable: the struct-shaped reading
// through THIS package's own RunState reports every gate present and correct on
// the very document the row rejects.
func TestSeal_G2_ApplyRoundRecord_PreservesTheGateRecordsItDidNotWrite(t *testing.T) {
	defer g2Red(t)

	original := g2SeedProbes(t, g2ProducedRunState(t, g2Worktree(t)))
	edits := g2Edits()

	// Every path inside `gates` the fixture carries, and what the two-field Gate
	// keeps: status and skip_reason. Everything else goes.
	const cov = "gates.coverage:apps/finance-domain/wallet"
	evidence := map[string]string{
		"gates.build:apps/finance-domain/wallet.command":      g2Quote("go build ./..."),
		"gates.build:apps/finance-domain/wallet.ran_at":       g2Quote("2026-08-11T08:17:07Z"),
		"gates.build:apps/finance-domain/wallet.duration_ms":  "10",
		"gates.mutation:apps/finance-domain/wallet.exit_code": "1",
		"gates.test:apps/finance-domain/wallet.output_path":   g2Quote("/tmp/gate-output/test-apps_finance-domain_wallet.log"),
		cov + ".metrics.violations[0]":                        g2Quote("apps/finance-domain/wallet/service at 54.5% < floor 95%"),
		cov + ".metrics.worst_coverage_pct":                   "54.5",
		cov + ".metrics.packages_evaluated":                   "1",
		"gates.semgrep.ran_at":                                g2Quote("2026-08-11T08:17:09Z"),
	}
	survives := map[string]string{
		cov + ".status":        g2Quote("fail"),
		"gates.semgrep.status": g2Quote("fail"),
		"gates.build:apps/finance-domain/wallet.status": g2Quote("pass"),
	}

	before := g2Flatten(t, original)
	for p, lit := range evidence {
		if got := before[p]; got != lit {
			t.Fatalf("fixture does not carry %s = %s (got %s); g2GatesBlock has drifted from what the tracked cmd/gates binary writes", p, lit, got)
		}
	}

	legacyDoc := g2LegacyProjection(t, original, edits)
	legacy := g2Flatten(t, legacyDoc)

	// CONTROL (a) — the nine records survive BY NAME.
	if n := len(g2TopLevelKeys(t, g2Member(t, legacyDoc, "gates"))); n != 9 {
		t.Fatalf("CONTROL FAILED: today's projection left %d gate records, want all 9. The row's argument is that the records survive while their contents do not, and it needs them to survive.", n)
	}
	// CONTROL (b) — the evidence is gone.
	for p := range evidence {
		if _, ok := legacy[p]; ok {
			t.Fatalf("CONTROL FAILED: today's projection preserves %s. cmd/iterate's Gate declares two fields against cmd/gates' eight — if that is no longer true, a struct was widened and preserve.go's tripwires forbid it.", p)
		}
	}
	// CONTROL (c) — and the status that no longer says why survives beside it.
	for p, lit := range survives {
		if got := legacy[p]; got != lit {
			t.Fatalf("CONTROL FAILED: %s = %s after today's projection, want %s. The whole point is a FAILING gate whose reason was destroyed; without the surviving status this is an ordinary deletion.", p, got, lit)
		}
	}
	// CONTROL (d) — and no test in this module can see any of it. This is the
	// struct-shaped reading, and it is one of the seven measured vacuity shapes:
	// a test that reads through the same struct shape that caused the loss.
	if s, err := readRunState(g2WriteTemp(t, "legacy.json", legacyDoc)); err != nil {
		t.Fatalf("CONTROL FAILED: the destroyed document is not readable at all: %v", err)
	} else if len(s.Gates) != 9 || s.Gates[strings.TrimPrefix(cov, "gates.")].Status != "fail" {
		t.Fatalf("CONTROL FAILED: the struct-shaped reading no longer reports 9 gates with coverage failing. This row's argument is that such a reading passes while the document is being destroyed, and it needs that reading to pass.")
	}

	produced, err := ApplyRoundRecord(original, edits)
	if err != nil {
		t.Fatalf("ApplyRoundRecord: %v", err)
	}
	after := g2Flatten(t, produced)
	for p, lit := range evidence {
		got, ok := after[p]
		if !ok {
			t.Errorf("SEAL RED — %s was DESTROYED. It is the audit trail of whether that gate actually ran; without it a recorded pass is indistinguishable from a hand-typed one.", p)
			continue
		}
		if got != lit {
			t.Errorf("SEAL RED — %s = %s, want %s", p, got, lit)
		}
	}
	if _, ok := after[cov+".metrics.violations[0]"]; !ok {
		t.Errorf("SEAL RED — the coverage gate still says status:%s and no longer says why. That is the exact status cmd/gates' config comment calls the thing it exists to prevent: one that means \"we did not check but it is fine\".", after[cov+".status"])
	}

	g2AssertEditsLanded(t, produced, edits)
}

// ─── 5. the licence is per tool; the property is per document ────────────────

// TestSeal_G2_Licence_GatesIsLicensedForGatesAndForbiddenForIterate seals
// preserve.go ruling (2)(c): `gates` is a LICENSED path for cmd/gates and a
// FORBIDDEN one for cmd/iterate. Same path, same document, opposite verdicts.
//
// MEASURES SOURCE, in process — and, for the control, the SOURCE TEXT of a
// different module.
//
// WHY IT DESERVES A ROW. It is the proof that a shared "run-state ownership"
// table would be wrong. There is no fact about `gates` that settles whether a
// write to it is licensed; the answer is a fact about the WRITER, derived from
// that writer's own source. If this were one table somebody had to keep true,
// the day it drifted every tool would silently inherit the wrong licence.
//
// CONTROL, same call, and it reaches across the module boundary the way
// preserve.go's SHARED FIDELITY REGION note recommends: ../gates/preserve.go is
// read from disk and must still license `gates` unconditionally. The two
// packages cannot import each other — they are separate Go modules — but they
// are in one checkout, and cmd/gates' own seals already read across this
// boundary. If cmd/gates ever stops licensing `gates`, this control fires and
// the asymmetry this row asserts has become something else.
func TestSeal_G2_Licence_GatesIsLicensedForGatesAndForbiddenForIterate(t *testing.T) {
	defer g2Red(t)

	// CONTROL — the other side of the asymmetry, read from the other module's
	// source. AllowedPrefixes seeds every non-empty edit list with `gates`.
	gatesSrc, err := os.ReadFile("../gates/preserve.go")
	if err != nil {
		t.Fatalf("cannot read ../gates/preserve.go: %v — this row's control is the OTHER side of the asymmetry and cannot be asserted without it", err)
	}
	if !strings.Contains(string(gatesSrc), `out := []JSONPath{{{Key: "gates"}}}`) {
		t.Fatalf("CONTROL FAILED: cmd/gates' AllowedPrefixes no longer seeds the licence with `gates`. The asymmetry this row seals is between two live licences; if the other side changed, re-derive it before touching anything here.")
	}
	if !strings.Contains(string(gatesSrc), "EditKindSetGateResult") {
		t.Fatal("CONTROL FAILED: cmd/gates no longer declares EditKindSetGateResult, so it no longer has an edit kind that writes `gates`")
	}

	original := g2SeedProbes(t, g2ProducedRunState(t, g2Worktree(t)))
	edits := g2Edits()

	// THE STRUCTURAL HALF — iterate's own licence, over its own full edit list,
	// allows nothing at or beneath `gates`. LicensedPaths and Allows are
	// implemented in the scaffold, so this half is a fact about the contract as
	// it stands and it is recorded here so a later widening is noticed.
	lic, err := LicensedPaths(edits)
	if err != nil {
		t.Fatalf("LicensedPaths: %v", err)
	}
	forbidden := []JSONPath{
		{{Key: "gates"}},
		{{Key: "gates"}, {Key: "semgrep"}},
		{{Key: "gates"}, {Key: "semgrep"}, {Key: "status"}},
		{{Key: "gates"}, {Key: "coverage:apps/finance-domain/wallet"}, {Key: "metrics"}, {Key: "violations"}, {Index: 0, IsIndex: true}},
	}
	for _, p := range forbidden {
		if lic.Allows(p) {
			t.Errorf("SEAL RED — cmd/iterate's licence allows %s. Nothing in cmd/iterate writes a gate result: the enumeration at preserve.go's `what cmd/iterate is licensed to change` is six edits derived from eight assignment sites and none of them touches `gates`.", p)
		}
	}
	// …and it does allow the one array position the append claims, so the row is
	// not passing because the licence is empty.
	if !lic.Allows(JSONPath{{Key: "rounds"}, {Index: 1, IsIndex: true}, {Key: "verdict"}}) {
		t.Fatal("CONTROL FAILED: the licence allows nothing at rounds[1] either, so the assertions above would hold for a licence that permits nothing at all")
	}

	// THE BEHAVIOURAL HALF — a produced document in which a gate field was
	// rewritten must be reported as a violation. This is what makes the row red
	// today: VerifyPreservation is a stub.
	tampered := g2SetTopLevel(t, original, "gates", strings.Replace(g2GatesBlock,
		`"status": "fail",
      "ran_at": "2026-08-11T08:17:09Z",
      "skip_reason"`,
		`"status": "pass",
      "ran_at": "2026-08-11T08:17:09Z",
      "skip_reason"`, 1))
	if g2Flatten(t, tampered)["gates.semgrep.status"] != g2Quote("pass") {
		t.Fatal("seal bug: the tampered fixture did not actually change gates.semgrep.status; the leg below would assert nothing")
	}

	violations, err := VerifyPreservation(original, tampered, edits, FidelityPathwise)
	if err != nil {
		t.Errorf("SEAL RED — VerifyPreservation could not perform the check: %v", err)
	}
	found := false
	for _, v := range violations {
		if v.At.String() == "gates.semgrep.status" {
			found = true
		}
	}
	if !found {
		t.Errorf("SEAL RED — VerifyPreservation did not report gates.semgrep.status as a violation (%d violation(s) reported). A gate flipped from fail to pass by the tool that runs AFTER the gates is the most consequential single edit this document can carry, and cmd/iterate has no licence for it.%s",
			len(violations), g2Report(violations))
	}
}

// ─── 6. rounds[0..N-1] are not licensed ──────────────────────────────────────

// TestSeal_G2_ApplyRoundRecord_DoesNotTouchTheRoundsItDidNotAppend seals
// preserve.go ruling (3b), the substance of G2 and the thing that does not
// transfer from G1.
//
// MEASURES SOURCE, in process.
//
// MEASURED: `state.Rounds = append(...)` followed by MarshalIndent re-marshals
// EVERY element through the Round struct, so an append whose only intended
// effect was to add rounds[1] destroyed three paths inside rounds[0] —
// zzz_round_future, reviewers[0].zzz_reviewer_future, and evidence_id, an
// integer above 2^53 destroyed rather than corrupted because Round does not
// declare it.
//
// THE PROBES ARE FORWARD-COMPATIBILITY PROBES and the DISPUTE about their
// schema-legality is recorded on g2SeedProbes. They are not this property's only
// evidence: row 4's 34 paths destroyed inside 9 surviving gate records carry the
// identical property — a container that survives while its contents do not — and
// every one of those was written by a real tool minutes earlier. The objection
// preserve.go answers ("iterate wrote every element of rounds[] itself, so there
// is nothing in there it can lose") is true of the rounds iterate wrote and is
// not the property; the loss bites the moment any other writer annotates a round.
//
// SECOND LEG: the byte-level obligation. rounds[0] must come back as the BYTES
// it arrived as, because "nothing decodes rounds[0]" is the mechanism, not a
// consequence. A body that decoded and re-encoded it could still pass a pathwise
// check while having thrown away the one guarantee that makes the property
// robust to the next unknown thing.
//
// CONTROLS, same call: the legacy projection destroys all three, AND the licence
// itself refuses them — Allows(rounds[0].…) is false while Allows(rounds[1].…)
// is true, which is the container-versus-subtree distinction that makes a
// subtree licence over `rounds` the wrong answer.
func TestSeal_G2_ApplyRoundRecord_DoesNotTouchTheRoundsItDidNotAppend(t *testing.T) {
	defer g2Red(t)

	original := g2SeedProbes(t, g2ProducedRunState(t, g2Worktree(t)))
	edits := g2Edits()

	retroactive := map[string]string{
		"rounds[0].zzz_round_future":                 g2Quote("a round field no build in this repo declares"),
		"rounds[0].reviewers[0].zzz_reviewer_future": g2Quote("a reviewer field no build in this repo declares"),
		"rounds[0].evidence_id":                      "9007199254740993",
	}

	// CONTROL A — the licence refuses rounds[0] and permits rounds[1]. A subtree
	// licence over `rounds` would forgive exactly the three losses below.
	lic, err := LicensedPaths(edits)
	if err != nil {
		t.Fatalf("LicensedPaths: %v", err)
	}
	for p := range retroactive {
		var seg JSONPath
		if p == "rounds[0].reviewers[0].zzz_reviewer_future" {
			seg = JSONPath{{Key: "rounds"}, {Index: 0, IsIndex: true}, {Key: "reviewers"}, {Index: 0, IsIndex: true}, {Key: "zzz_reviewer_future"}}
		} else {
			seg = JSONPath{{Key: "rounds"}, {Index: 0, IsIndex: true}, {Key: strings.TrimPrefix(p, "rounds[0].")}}
		}
		if lic.Allows(seg) {
			t.Fatalf("CONTROL FAILED: the licence allows %s. An append licenses exactly ONE new index; if `rounds` has become a subtree licence, the three measured retroactive losses are forgiven and this row cannot fire.", p)
		}
	}

	// CONTROL B — today's append really does destroy them.
	legacy := g2Flatten(t, g2LegacyProjection(t, original, edits))
	for p := range retroactive {
		if _, ok := legacy[p]; ok {
			t.Fatalf("CONTROL FAILED: today's append preserves %s, so the retroactive loss is not being exhibited", p)
		}
	}
	// …while rounds[0] survives as an element, which is what makes it retroactive
	// rather than a deletion.
	if got := legacy["rounds[0].round"]; got != "1" {
		t.Fatalf("CONTROL FAILED: rounds[0] did not survive today's append at all (rounds[0].round = %s); the row is about paths destroyed INSIDE a surviving element", got)
	}

	produced, err := ApplyRoundRecord(original, edits)
	if err != nil {
		t.Fatalf("ApplyRoundRecord: %v", err)
	}
	after := g2Flatten(t, produced)
	for p, lit := range retroactive {
		got, ok := after[p]
		if !ok {
			t.Errorf("SEAL RED — %s was destroyed RETROACTIVELY by an append whose only intended effect was to add rounds[1]. rounds[0..AtIndex-1] are not licensed: not to renumber, not to backfill, not to re-encode.", p)
			continue
		}
		if got != lit {
			t.Errorf("SEAL RED — %s = %s, want %s", p, got, lit)
		}
	}

	// SECOND LEG — byte identity of the untouched element. This is not the
	// withdrawn "byte-identity is free" claim: it is a claim about ONE subtree
	// that the contract says nothing may decode, and it is checkable exactly
	// because nothing may decode it.
	if g2ArrayLen(t, produced, "rounds") == 2 {
		if before, after := string(g2RoundAt(t, original, 0)), string(g2RoundAt(t, produced, 0)); before != after {
			t.Errorf("SEAL RED — rounds[0] did not come back as the bytes it arrived as. ApplyRoundRecord must decode `rounds` exactly one level, to []json.RawMessage, and re-emit each existing element verbatim; a body that re-encodes an element it did not change has thrown away the mechanism even if this run's paths happen to match.\n  before: %s\n  after:  %s", before, after)
		}
	}

	g2AssertEditsLanded(t, produced, edits)
}

// ─── 7. the append's stale claim ─────────────────────────────────────────────

// TestSeal_G2_ApplyRoundRecord_RefusesAStaleAtIndexAndReturnsNoBytes seals the
// single most important design decision in G2: the CALLER states the index and
// the editor checks it.
//
// MEASURES SOURCE, in process.
//
// WHY THE CHECK EXISTS AT ALL, restated because a reader who has not read
// Edit.AtIndex will call it redundant: `append` cannot fail, so an editor that
// computed the index itself could never mismatch and could therefore never
// DETECT anything. iterate's d.Round is computed from load()'s read at
// main.go:523 and the append happens against appendRound's re-read at :442 —
// with execTool and an entire review panel in between, so minutes, not
// milliseconds. AtIndex is the only place that stale claim can be compared
// against the document actually being edited.
//
// IT IS DARK TODAY and must be rated by what it gates, not by what calls it.
// Wired faithfully the mismatch is unreachable, because iterate is single-writer
// and the documented pipeline is sequential. It gates the day a second writer
// exists, and the failure it catches then is two rounds silently sharing a
// number in the record every later escalation decision is computed from.
//
// FOUR LEGS:
//
//	stale AtIndex          REFUSE, and return NO bytes
//	round/AtIndex+1 skew   REFUSE — main.go:446-447 are mechanically linked
//	Record.Round skew      ACCEPT. This is the sharper half: Record.Round is
//	                       d.Round, computed from load()'s earlier read, and
//	                       refusing it would make iterate fail to record a round
//	                       it records today. That is a behaviour change wearing a
//	                       safety check, and the frozen-consumer constraint
//	                       forbids it.
//	correct list           ACCEPT — the control, without which "it refuses"
//	                       is satisfied by a function that refuses everything.
//
// Every refusal leg guards against errNotImplemented, because the scaffold
// refuses every input and would otherwise satisfy three of these four vacuously.
func TestSeal_G2_ApplyRoundRecord_RefusesAStaleAtIndexAndReturnsNoBytes(t *testing.T) {
	defer g2Red(t)

	original := g2SeedProbes(t, g2ProducedRunState(t, g2Worktree(t)))
	if n := g2ArrayLen(t, original, "rounds"); n != 1 {
		t.Fatalf("fixture has %d rounds, want 1 — the whole row is about the relationship between AtIndex and that length", n)
	}

	// CONTROL — the truthful list is accepted. Without this leg every assertion
	// below is satisfied by a function that returns an error unconditionally.
	if _, err := ApplyRoundRecord(original, g2Edits()); err != nil {
		t.Errorf("SEAL RED — the truthful edit list was refused: %v. AtIndex 1 IS len(rounds); a checker that refuses the correct case has not implemented a check, it has implemented a refusal.", err)
	}

	// LEG 1 — a stale AtIndex. The caller's claim about the document is false.
	stale := g2Edits()
	stale[0].AtIndex = 0 // as if computed from a read taken before rounds[0] landed
	stale[1].RoundNumber = 1
	out, err := ApplyRoundRecord(original, stale)
	if err == nil {
		t.Error("SEAL RED — ApplyRoundRecord accepted AtIndex 0 against a document whose rounds has 1 element. Do NOT silently append at the real length: adjusting a caller's false claim into a true one is how a stale decision becomes an invisible one.")
	} else if errors.Is(err, errNotImplemented) {
		t.Errorf("SEAL RED — the refusal is the scaffold marker, not a staleness check: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("SEAL RED — the refusal returned %d bytes. On failure ApplyRoundRecord returns an error and NO bytes; a caller that writes whatever it got back would persist a document built by a path that had already given up.", len(out))
	}

	// LEG 2 — round disagreeing with the append. main.go:446-447 are adjacent
	// and mechanically linked: state.Round = len(state.Rounds) AFTER the append.
	skew := g2Edits()
	skew[1].RoundNumber = 7
	out, err = ApplyRoundRecord(original, skew)
	if err == nil {
		t.Error("SEAL RED — ApplyRoundRecord accepted round 7 beside an append at index 1. A `round` that disagrees with len(rounds) is undetectable at every later reader, and this is a list-level property the per-edit Validate cannot see.")
	} else if errors.Is(err, errNotImplemented) {
		t.Errorf("SEAL RED — the refusal is the scaffold marker, not the list-level check: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("SEAL RED — the round/AtIndex refusal returned %d bytes", len(out))
	}

	// LEG 3 — and the one it must NOT make. Record.Round is d.Round, from
	// load()'s earlier read. When it disagrees, that IS the staleness bug showing
	// itself, and refusing it here would make iterate fail to record a round it
	// records today.
	recordSkew := g2Edits()
	rec := g2AppendedRound()
	rec.Round = 99
	recordSkew[0].Record = rec
	if _, err := ApplyRoundRecord(original, recordSkew); err != nil && !errors.Is(err, errNotImplemented) {
		t.Errorf("SEAL RED — ApplyRoundRecord refused Edit.Record.Round 99 beside AtIndex 1: %v. This is the one disagreement it must NOT refuse — Record.Round comes from decide(), and rejecting it changes what iterate records. Record the disagreement if you like; do not refuse it. The place to fix it is the unit that closes the staleness window.", err)
	}

	// LEG 4 — the container licence's reason for existing: an empty rounds[] is
	// a LEAF, and the first append REMOVES that leaf. Without the container
	// licence a correct body is reported as broken.
	empty := g2SetTopLevel(t, original, "rounds", "[]")
	if g2Flatten(t, empty)["rounds"] != "[]" {
		t.Fatal("seal bug: an explicitly empty rounds is not flattening to the leaf `[]`, and leg 4 is about that leaf")
	}
	first := g2Edits()
	first[0].AtIndex = 0
	first[1].RoundNumber = 1
	produced, err := ApplyRoundRecord(empty, first)
	if err != nil {
		t.Errorf("SEAL RED — the first append into an explicitly empty rounds was refused: %v", err)
	} else {
		if v := g2DivergencesOutside(t, empty, produced, first); len(v) > 0 {
			t.Errorf("SEAL RED — the first append into an empty rounds[] produced %d unlicensed divergence(s). The removal of the `[]` leaf is what the `rounds` CONTAINER licence exists for; if it is being reported, the licence is being built wrong.%s", len(v), g2Report(v))
		}
		g2AssertEditsLanded(t, produced, first)
	}
}

// ─── 8. shift, prepend, double append, truncation ────────────────────────────

// TestSeal_G2_VerifyPreservation_CatchesEveryArrayMalformationFromTheLicenceAlone
// seals preserve.go's claim that four distinct array malformations need no rule
// of their own, and CONFIRMS it rather than assuming it.
//
// MEASURES SOURCE, in process. The produced documents are hand-built by this
// test, not by the body, so the row measures the CHECKER and is independent of
// whether ApplyRoundRecord is implemented.
//
// THE CLAIM UNDER TEST (preserve.go, VerifyPreservation's "four things the
// licence alone already catches"):
//
//	retroactive loss   not under rounds[AtIndex], not a container match
//	prepend / shift    every path of every shifted element CHANGES
//	double append      rounds[AtIndex+1].* is ADDED and unlicensed
//	truncation         old rounds[k] compares against whatever now sits at k
//
// CONFIRMED, by computing the divergences with Diverge and Licence.Allows before
// asking VerifyPreservation anything: all four DO fall out with no extra rule,
// and the CONTROL is that the correct append falls out CLEAN through the very
// same filter. That control is what distinguishes "the licence catches these"
// from "the licence rejects everything".
//
// A FIFTH CASE IS INCLUDED because it is the one the licence catches for a
// non-obvious reason: an append at the RIGHT index whose earlier siblings were
// left alone but whose element count is right — that is the correct case — versus
// an append that also renumbered rounds[0].round, which is the single most
// tempting "tidy-up" a body author could add and is a violation.
func TestSeal_G2_VerifyPreservation_CatchesEveryArrayMalformationFromTheLicenceAlone(t *testing.T) {
	defer g2Red(t)

	original := g2SeedProbes(t, g2ProducedRunState(t, g2Worktree(t)))
	edits := g2Edits()

	// The document a CORRECT body produces: rounds[0] verbatim, the new element
	// at index 1, and the five scalar edits.
	newRound := `{"round":2,"kind":"controller","verdict":"ESCALATE","new_finding_count":0,"completed_at":"2026-08-11T12:00:00Z"}`
	correct := g2SetTopLevel(t,
		g2SetRounds(t, original, string(g2RoundAt(t, original, 0)), newRound),
		"round", "2",
		"verdict", g2Quote("escalate"),
		"updated_at", g2Quote(g2UpdatedAt),
		"status", g2Quote("escalated"),
		"escalation_reason", g2Quote(g2EscalationReason),
	)

	// CONTROL — the correct document is CLEAN through the licence filter. If it
	// is not, every malformation below would be "caught" by a filter that
	// rejects everything and this row would certify nothing.
	if v := g2DivergencesOutside(t, original, correct, edits); len(v) > 0 {
		t.Fatalf("CONTROL FAILED: the correctly-appended document reports %d unlicensed divergence(s). The malformation legs below cannot distinguish a real violation from a filter that rejects everything.%s", len(v), g2Report(v))
	}
	g2AssertEditsLanded(t, correct, edits)

	r0 := string(g2RoundAt(t, original, 0))
	malformations := map[string][]byte{
		"prepend (the new round at index 0)":         g2SetRounds(t, correct, newRound, r0),
		"shift (rounds[0] duplicated forward)":       g2SetRounds(t, correct, r0, r0, newRound),
		"double append (two new elements)":           g2SetRounds(t, correct, r0, newRound, newRound),
		"truncation (rounds[0] dropped)":             g2SetRounds(t, correct, newRound),
		"renumbering rounds[0].round as a tidy-up":   g2SetRounds(t, correct, strings.Replace(r0, `"round": 1,`, `"round": 0,`, 1), newRound),
		"re-encoding rounds[0] without its unknowns": g2SetRounds(t, correct, `{"round":1,"kind":"full","verdict":"ITERATE","new_finding_count":3}`, newRound),
	}

	for name, produced := range malformations {
		// CONFIRMATION — it really does fall out of the licence, with no rule
		// beyond Diverge + Licence.Allows.
		fromLicence := g2DivergencesOutside(t, original, produced, edits)
		if len(fromLicence) == 0 {
			t.Fatalf("CONFIRMATION FAILED for %q: the licence alone reports NO violation. preserve.go claims all four of these need no extra rule; if one of them does, that is a finding about the contract and not about the body — do not add a rule here, report it.", name)
		}

		// THE ROW — and VerifyPreservation must report it too.
		violations, err := VerifyPreservation(original, produced, edits, FidelityPathwise)
		if err != nil {
			t.Errorf("SEAL RED — %q: VerifyPreservation could not perform the check: %v", name, err)
			continue
		}
		if len(violations) == 0 {
			t.Errorf("SEAL RED — %q produced NO violations, but the licence alone finds %d. VerifyPreservation must build the Licence from the edit list and return every unlicensed divergence; an empty slice with a nil error is the ONLY passing result and this document is not a passing case.%s",
				name, len(fromLicence), g2Report(fromLicence))
		}
	}

	// And the correct document must come back clean from VerifyPreservation
	// itself — the same control, one level up.
	violations, err := VerifyPreservation(original, correct, edits, FidelityPathwise)
	if err != nil {
		t.Errorf("SEAL RED — VerifyPreservation could not check the correct document: %v", err)
	} else if len(violations) > 0 {
		t.Errorf("SEAL RED — VerifyPreservation reported %d violation(s) on a correctly-appended document. A checker that fails the correct case is not stricter, it is broken.%s", len(violations), g2Report(violations))
	}
}

// ─── 9. the licence comes from the edit list, never from what happened ───────

// TestSeal_G2_VerifyPreservation_DerivesTheLicenceFromTheEditListNotTheOutput
// seals the ban preserve.go states twice, and it is not a fine point for an
// append.
//
// MEASURES SOURCE, in process.
//
// Deriving the licensed INDEX from the produced document's rounds[] length
// licenses whatever index the body actually wrote — which is the one thing the
// append most needs checked. The implementation that reads
// `rounds[len(produced.rounds)-1]` as the licensed prefix passes every row in
// this file except this one.
//
// TWO LEGS:
//
//	appended at the wrong index   the edit list says AtIndex 1; the produced
//	                              document appended at 5, padding 2..4. A licence
//	                              derived from the output would call that clean.
//	appended when nothing was     an edit list with NO append at all, against a
//	                              asked                document that grew a round.
//	                              Nothing licenses rounds[1] and it must be
//	                              reported.
//
// CONTROL, same call: the same produced document, checked against an edit list
// that DID license index 5, must come back clean — so the row is about where the
// licence comes from and not about index 5 being unusual.
func TestSeal_G2_VerifyPreservation_DerivesTheLicenceFromTheEditListNotTheOutput(t *testing.T) {
	defer g2Red(t)

	original := g2SeedProbes(t, g2ProducedRunState(t, g2Worktree(t)))
	r0 := string(g2RoundAt(t, original, 0))
	newRound := `{"round":2,"kind":"controller","verdict":"ESCALATE","new_finding_count":0,"completed_at":"2026-08-11T12:00:00Z"}`

	atFive := g2SetTopLevel(t,
		g2SetRounds(t, original, r0, "null", "null", "null", "null", newRound),
		"round", "2", "verdict", g2Quote("escalate"), "updated_at", g2Quote(g2UpdatedAt),
		"status", g2Quote("escalated"), "escalation_reason", g2Quote(g2EscalationReason),
	)

	// LEG 1 — the edit list says index 1.
	edits := g2Edits()
	violations, err := VerifyPreservation(original, atFive, edits, FidelityPathwise)
	if err != nil {
		t.Errorf("SEAL RED — VerifyPreservation could not perform the check: %v", err)
	} else if len(violations) == 0 {
		t.Error("SEAL RED — a document that appended at rounds[5] was reported clean against an edit list that licensed rounds[1]. The licence must come from the EDIT LIST the caller intended; deriving the licensed index from the produced document licenses whatever index the body actually wrote.")
	}

	// CONTROL — the same bytes, against an edit list that really did license
	// index 5, are clean apart from the padding this test invented. The row is
	// about the SOURCE of the licence, so the control has to isolate that.
	atFiveEdits := g2Edits()
	atFiveEdits[0].AtIndex = 5
	atFiveEdits[1].RoundNumber = 6
	fromLicence := g2DivergencesOutside(t, original, atFive, atFiveEdits)
	onlyPadding := true
	for _, v := range fromLicence {
		if !strings.HasPrefix(v.At.String(), "rounds[") {
			onlyPadding = false
		}
	}
	if !onlyPadding {
		t.Fatalf("CONTROL FAILED: licensing index 5 leaves divergences outside rounds[], so leg 1's failure could be about something other than the index.%s", g2Report(fromLicence))
	}

	// LEG 2 — an edit list with no append at all, against a document that grew.
	noAppend := []Edit{
		{Kind: EditKindSetVerdict, Verdict: "escalate"},
		{Kind: EditKindSetUpdatedAt, UpdatedAt: g2UpdatedAt},
	}
	grew := g2SetTopLevel(t, g2SetRounds(t, original, r0, newRound),
		"verdict", g2Quote("escalate"), "updated_at", g2Quote(g2UpdatedAt))
	violations, err = VerifyPreservation(original, grew, noAppend, FidelityPathwise)
	if err != nil {
		t.Errorf("SEAL RED — VerifyPreservation could not perform the check: %v", err)
	} else if len(violations) == 0 {
		t.Error("SEAL RED — a round appeared in a document whose edit list contained no append, and it was reported clean. LicensedPaths licenses `rounds` as a container ONLY when the list actually contains an EditKindAppendRound; an edit list that sets only the verdict cannot quietly license the creation of an array element.")
	}
}

// ─── 10. the checker refuses what it cannot check ────────────────────────────

// TestSeal_G2_VerifyPreservation_RefusesWhatItCannotCheckOnItsOwnTerms seals the
// two-return-channel contract.
//
// MEASURES SOURCE, in process.
//
// `err` means the check could not be performed; `violations` means the check ran
// and found these. An empty violations slice with a nil error is the ONLY
// passing result. A caller that treats a nil error as "preserved" is asserting
// something this function never said — the exact `exit_code is None -> passed`
// shape skills/explicit-state.md records.
//
// FIVE LEGS, each an input this build genuinely cannot check:
//
//	FidelityByteIdentical   REFUSE. encoding/json cannot express it and this
//	                        package has no order-preserving document model.
//	                        Returning an empty violation list would report
//	                        "checked and clean" for a check that never ran.
//	FidelityUnset           REFUSE — the caller did not say what it is checking.
//	Fidelity(99)            REFUSE — never the permissive branch.
//	a produced document     REFUSE. A document that will not parse is the most
//	  that will not parse   complete failure of preservation available, and
//	                        reporting it as agreement would be the vacuous pass
//	                        this whole unit is about.
//	an edit list that       REFUSE — an Edit{} that fell out of a slice literal.
//	  will not validate
//
// CONTROL, same call: FidelityPathwise over the identical pair must NOT go down
// the error channel, so "it refuses" is not satisfied by refusing everything.
// Every leg guards against errNotImplemented for the same reason.
func TestSeal_G2_VerifyPreservation_RefusesWhatItCannotCheckOnItsOwnTerms(t *testing.T) {
	defer g2Red(t)

	original := g2SeedProbes(t, g2ProducedRunState(t, g2Worktree(t)))
	edits := g2Edits()

	// CONTROL — the normative level is not an error.
	if _, err := VerifyPreservation(original, original, edits, FidelityPathwise); err != nil && !errors.Is(err, errNotImplemented) {
		t.Errorf("CONTROL: FidelityPathwise went down the error channel: %v", err)
	}

	refusals := []struct {
		name  string
		level Fidelity
		prod  []byte
		edits []Edit
		want  string
	}{
		{"byte-identical", FidelityByteIdentical, original, edits, "byte"},
		{"unset", FidelityUnset, original, edits, "unset"},
		{"unrecognised level", Fidelity(99), original, edits, "99"},
		{"unparseable produced document", FidelityPathwise, []byte("{not json"), edits, ""},
		{"unvalidatable edit list", FidelityPathwise, original, append(g2Edits(), Edit{}), ""},
	}
	for _, r := range refusals {
		violations, err := VerifyPreservation(original, r.prod, r.edits, r.level)
		if err == nil {
			t.Errorf("SEAL RED — %s: VerifyPreservation returned no error and %d violation(s). A check that could not be performed must go down the ERROR channel; an empty violation list here reports \"checked and clean\" for a check that never ran.", r.name, len(violations))
			continue
		}
		if errors.Is(err, errNotImplemented) {
			t.Errorf("SEAL RED — %s: the refusal is the scaffold marker, not a refusal on the function's own terms: %v", r.name, err)
			continue
		}
		if len(violations) != 0 {
			t.Errorf("SEAL RED — %s: the error channel carried %d violation(s) with it. The two channels are not the same state and a caller cannot be asked to read both.", r.name, len(violations))
		}
		if r.want != "" && !strings.Contains(err.Error(), r.want) {
			t.Errorf("SEAL RED — %s: the refusal does not name %q: %v", r.name, r.want, err)
		}
	}

	// AND the only passing result. An identical pair with an edit list that
	// licenses nothing must be clean — and it must be clean with an EMPTY slice,
	// not a nil error and a populated one.
	empty := []Edit{}
	violations, err := VerifyPreservation(original, original, empty, FidelityPathwise)
	if err != nil {
		t.Errorf("SEAL RED — an empty edit list is not an error: it licenses nothing, so verification demands full pathwise identity, which is the correct demand for a write that changed nothing. Got: %v", err)
	} else if len(violations) != 0 {
		t.Errorf("SEAL RED — an identical pair reported %d violation(s):%s", len(violations), g2Report(violations))
	}
}

// ─── 11. the non-uniform Validate table is intended ──────────────────────────

// TestSeal_G2_EmptyVerdictIsAppliedAndEmptyStatusIsRefused seals preserve.go's
// deliberate asymmetry as INTENDED BEHAVIOUR, not as an oversight somebody will
// later "fix" into a regression.
//
// MEASURES SOURCE, in process.
//
// THE RULE THAT GENERATES THE TABLE is the frozen-consumer constraint, and it is
// narrower than G1's: G2 MUST NOT REFUSE A VALUE cmd/iterate CAN PRODUCE,
// because refusing one changes what iterate decides. So a payload is rejected
// only where the source PROVES iterate cannot emit it. This is exactly where
// G1's "an empty required field is did-nothing wearing succeeded" rule does not
// transfer, and copying it would have been wrong.
//
// PRODUCIBILITY IS PROVED HERE, NOT ASSERTED. recordRecheck (main.go:372-399) is
// called in this test with a cmd/recheck payload that carries no verdict field,
// and the Round it returns must have Verdict "". That is the whole argument for
// the empty-verdict leg, and it is executable.
//
// AND THE CONSEQUENCE IS SEALED AS A DEFECT, not as a feature: an empty verdict
// does not write an empty string, it DELETES the top-level `verdict` under
// `omitempty` (main.go:70), so a round whose verdict could not be determined
// deletes the PREVIOUS round's verdict rather than recording that it is unknown.
// preserve.go finding (8) records it and does not fix it. This row pins the
// behaviour so the fix, when someone writes it, is a deliberate change to what
// iterate decides and not an accident inside a preservation unit.
//
// CONTROL, same call: empty status, empty updated_at and empty escalation_reason
// must all be REFUSED, because the source proves none of them is producible —
// a total switch whose every arm assigns a non-empty literal (:450-460),
// time.Now().Format (:449), and an `if escalation != ""` guard (:457).
func TestSeal_G2_EmptyVerdictIsAppliedAndEmptyStatusIsRefused(t *testing.T) {
	defer g2Red(t)

	// PRODUCIBILITY, proved by calling production code with a producible input.
	payload := g2WriteTemp(t, "round-result.json", []byte(`{"tool":"recheck","exit_code":1,"head_sha":"abc1234","still_open":0,"new_at_floor":2}`))
	r, err := recordRecheck(decision{Round: 2, Kind: "recheck"}, payload, 1)
	if err != nil {
		t.Fatalf("recordRecheck rejected a payload with no verdict: %v — the empty-verdict leg rests on this being producible", err)
	}
	if r.Verdict != "" {
		t.Fatalf("PRODUCIBILITY FAILED: recordRecheck returned verdict %q from a payload with no verdict field. The empty verdict is only sealed here because the source can emit it; if it no longer can, Edit.Validate should refuse it and this row is the place to say so.", r.Verdict)
	}

	original := g2SeedProbes(t, g2ProducedRunState(t, g2Worktree(t)))
	if _, ok := g2Flatten(t, original)["verdict"]; ok {
		t.Fatal("fixture already carries a top-level verdict; the deletion leg needs to add one first")
	}
	// Give the document a previous verdict, so the deletion is a LOSS and not a
	// no-op. This is the state a real second round arrives in.
	original = g2SetTopLevel(t, original, "verdict", g2Quote("iterate"))

	// THE ROW — an empty verdict is applied, and applying it deletes the member.
	emptyVerdict := g2Edits()
	emptyVerdict[2].Verdict = ""
	produced, err := ApplyRoundRecord(original, emptyVerdict)
	if err != nil {
		t.Errorf("SEAL RED — ApplyRoundRecord refused an empty verdict: %v. recordRecheck can produce it (proved above), and refusing it would make iterate fail to record a round it records today — a behaviour change wearing a safety check.", err)
	} else {
		if got, ok := g2Flatten(t, produced)["verdict"]; ok {
			t.Errorf("SEAL RED — verdict = %s, want the member ABSENT. RunState.Verdict is `json:\"verdict,omitempty\"`, so an empty payload deletes it; that deletion is licensed at `verdict` because it is what iterate does today. Recorded as preserve.go finding (8): a round whose verdict could not be determined deletes the previous round's verdict rather than recording that it is unknown.", got)
		}
		if v := g2DivergencesOutside(t, original, produced, emptyVerdict); len(v) > 0 {
			t.Errorf("SEAL RED — the empty-verdict edit produced %d divergence(s) outside the licensed paths. The deletion is licensed AT `verdict` and nowhere else.%s", len(v), g2Report(v))
		}
		g2AssertEditsLanded(t, produced, emptyVerdict)
	}

	// CONTROL — the three the source proves are not producible must be refused,
	// on their own terms and not by the scaffold marker.
	notProducible := []struct {
		name string
		mut  func([]Edit)
		want string
	}{
		{"empty status", func(e []Edit) { e[4].Status = "" }, "status"},
		{"empty updated_at", func(e []Edit) { e[3].UpdatedAt = "" }, "updated_at"},
		{"empty escalation_reason", func(e []Edit) { e[5].EscalationReason = "" }, "reason"},
	}
	for _, c := range notProducible {
		bad := g2Edits()
		c.mut(bad)
		// Validate is implemented, so this half is a CONTROL on the contract as
		// it stands: the asymmetry must still be exactly where preserve.go put it.
		if err := bad[0].Validate(); err != nil {
			t.Fatalf("seal bug: %s mutated the wrong edit", c.name)
		}
		out, err := ApplyRoundRecord(original, bad)
		if err == nil {
			t.Errorf("SEAL RED — ApplyRoundRecord accepted %s. The source proves iterate cannot emit it, and writing it would erase a field while claiming to set one.", c.name)
			continue
		}
		if errors.Is(err, errNotImplemented) {
			t.Errorf("SEAL RED — %s: the refusal is the scaffold marker, not the Validate table: %v", c.name, err)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("SEAL RED — %s: the refusal does not name %q: %v", c.name, c.want, err)
		}
		if len(out) != 0 {
			t.Errorf("SEAL RED — %s: the refusal returned %d bytes", c.name, len(out))
		}
	}
}

// ─── 12. one read, two views, and no validator ───────────────────────────────

// TestSeal_G2_LoadRunStateDocument_ReturnsTheBytesItReadWithoutTightening seals
// the load half, and it is the row where G1's shape is INVERTED.
//
// MEASURES SOURCE, in process.
//
// G1's LoadRunStateDocument called cmd/gates' existing validateRunState, and
// G1's row sealed that it neither tightened nor loosened it. cmd/iterate HAS NO
// VALIDATOR: readRunState (main.go:427-437) does os.ReadFile then
// json.Unmarshal and nothing else, and the only precondition anywhere is load()'s
// `state.Classification == nil` check at :528, which lives in load() and stays
// there. So the seal here is the opposite obligation: it must NOT invent one.
// Adding validation would make iterate refuse documents it accepts today, which
// is precisely the behaviour change cmd/classify's differential exists to catch.
//
// THE ORACLE IS readRunState, and it is INDEPENDENT: preserve.go's inherited
// ruling (B) forbids either function delegating to the other, because a function
// that is its own oracle certifies nothing.
//
// THE ORDERING CONDITION, carried forward from G1 and still live: readRunState
// has two callers (:442 inside appendRound, :523 inside load()). If a later unit
// moves BOTH to LoadRunStateDocument, readRunState becomes dead code, someone
// deletes it as tidy-up, and this oracle silently vacates while the row keeps
// passing. Re-base this row on a hand-built expectation BEFORE readRunState
// loses its last caller.
//
// CONTROLS, same call: three documents readRunState accepts today — including
// two that are schema-INVALID and one that load() would reject — must all still
// be accepted. A row that only checked the happy path would go green against an
// implementation that had quietly added a schema check.
func TestSeal_G2_LoadRunStateDocument_ReturnsTheBytesItReadWithoutTightening(t *testing.T) {
	defer g2Red(t)

	doc := g2SeedProbes(t, g2ProducedRunState(t, g2Worktree(t)))
	path := g2WriteTemp(t, "run-state.json", doc)

	// CONTROL — today's reader accepts this document.
	want, err := readRunState(path)
	if err != nil {
		t.Fatalf("CONTROL FAILED: today's readRunState rejects the fixture (%v); this row is about not TIGHTENING and cannot be judged against a document that was never accepted", err)
	}

	raw, state, err := LoadRunStateDocument(path)
	if err != nil {
		t.Errorf("SEAL RED — LoadRunStateDocument rejected a document readRunState accepts: %v", err)
	}
	if !reflect.DeepEqual(raw, doc) {
		t.Errorf("SEAL RED — the returned bytes are not the file's bytes (%d returned, %d on disk). The body must edit THESE bytes; re-reading or re-marshalling for the raw return re-opens the window this function exists to close.", len(raw), len(doc))
	}
	if state == nil {
		t.Error("SEAL RED — the decoded view is nil; the contract is one read, TWO views")
	} else if !reflect.DeepEqual(state, want) {
		t.Error("SEAL RED — the decoded view differs from readRunState's, so the two views are not of the same document")
	}

	// CONTROLS — three documents this tool accepts today and must go on
	// accepting. cmd/iterate has no validator and G2 does not give it one.
	tolerated := map[string][]byte{
		"schema_version 2 (a NEWER producer's document)":                       g2SetTopLevel(t, doc, "schema_version", "2"),
		"no classification (load() rejects it at :528, readRunState does not)": g2DeleteTopLevelForRow(t, doc, "classification"),
		"a gate key the schema's propertyNames.enum forbids":                   g2SetTopLevel(t, doc, "gates", `{"lint:apps/finance-domain/wallet": {"status": "pass"}}`),
	}
	for name, d := range tolerated {
		p := g2WriteTemp(t, "tolerated.json", d)
		if _, err := readRunState(p); err != nil {
			t.Fatalf("CONTROL FAILED: today's readRunState rejects %s (%v), so this leg no longer distinguishes anything", name, err)
		}
		if _, _, err := LoadRunStateDocument(p); err != nil {
			t.Errorf("SEAL RED — LoadRunStateDocument rejected %s: %v. cmd/iterate has no validator and inventing one is a behaviour change, not a hardening — it would make iterate refuse documents it accepts today.", name, err)
		}
	}

	// …and the one thing it must still reject, so "does not validate" is not
	// "does not read".
	broken := g2WriteTemp(t, "broken.json", []byte("{not json"))
	if _, err := readRunState(broken); err == nil {
		t.Fatal("CONTROL FAILED: readRunState accepts unparseable JSON, so this leg distinguishes nothing")
	}
	if _, _, err := LoadRunStateDocument(broken); err == nil {
		t.Error("SEAL RED — LoadRunStateDocument accepted a document that is not JSON at all")
	} else if errors.Is(err, errNotImplemented) {
		t.Errorf("SEAL RED — the rejection is the scaffold marker, not a decode failure: %v", err)
	}
}

// ─── 13. the seam ────────────────────────────────────────────────────────────

// TestSeal_G2_AppendRound_PreservesTheWholeDocument seals the wiring.
//
// MEASURES SOURCE, in process. appendRound is the one function in cmd/iterate
// that writes the run-state; preserve.go deliberately did NOT rewire it, because
// wiring a raising stub into it would have taken all 27 green rows in this
// package red at the scaffold commit. The body performs the wiring, and this is
// the row that fails until it does.
//
// CONTROL, same call, and it is the sharpest in the file: the struct-shaped
// reading — re-unmarshal into RunState and check the declared fields — is
// asserted to PASS on the very same output this row rejects. That reading is
// what cmd/iterate's existing 27 green rows do, and it is green because it reads
// the document through the structs that destroyed it. It is the seventh measured
// vacuity shape, demonstrated live rather than described.
func TestSeal_G2_AppendRound_PreservesTheWholeDocument(t *testing.T) {
	defer g2Red(t)

	original := g2SeedProbes(t, g2ProducedRunState(t, g2Worktree(t)))
	path := g2WriteTemp(t, "run-state.json", original)

	if err := appendRound(path, g2AppendedRound(), "ESCALATE", g2EscalationReason); err != nil {
		t.Fatalf("appendRound: %v", err)
	}
	produced, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// CONTROL — the reading today's suite uses reports success.
	s, err := readRunState(path)
	if err != nil {
		t.Fatalf("CONTROL FAILED: the produced document is not readable at all: %v", err)
	}
	if s.TaskKey != "SMG-9001" || s.Classification == nil || s.Classification.Risk != "critical" ||
		len(s.Rounds) != 2 || s.Rounds[1].Verdict != "ESCALATE" || s.Status != "escalated" || len(s.Gates) != 9 {
		t.Fatal("CONTROL FAILED: the top-level, struct-shaped reading no longer passes. This row's argument is that such a reading passes while the document is being destroyed, and it needs that reading to pass.")
	}

	// THE ROW — the same document, measured per JSON path. updated_at is stamped
	// from the clock, so the edit list is rebuilt with what landed rather than
	// with g2UpdatedAt; that is reading ONE licensed value out of the output, not
	// deriving the licence from it.
	edits := g2Edits()
	edits[3].UpdatedAt = s.UpdatedAt
	edits[0].Record = g2AppendedRound()
	edits[0].Record.CompletedAt = s.Rounds[1].CompletedAt

	if s.UpdatedAt == "" {
		t.Error("SEAL RED — updated_at was not stamped; the merge did not run")
	}
	if v := g2DivergencesOutside(t, original, produced, edits); len(v) > 0 {
		t.Errorf("SEAL RED — appendRound destroyed %d JSON path(s) outside the licensed edits. It is not yet wired to ApplyRoundRecord; it still round-trips the whole document through this package's closed structs (main.go:441-467). The CONTROL above passed on this same output.%s",
			len(v), g2Report(v))
	}
	g2AssertEditsLanded(t, produced, edits)
}

// ─── 14. the consequence, end to end, from source ────────────────────────────

// TestSeal_G2_EndToEnd_FromSource_ThePipelineCanStillGate is the row the
// pipeline consequence gets to itself, and it is measured by running the next
// tool rather than by reasoning about it.
//
// MEASURES SOURCE, out of process. It builds cmd/iterate FROM THE TREE UNDER
// TEST to a scratch path with `go build -o` and runs THAT. It deliberately does
// not exec cmd/iterate/iterate: that binary is a committed artifact and a row
// that measured it could not see a source fix at all. Row 15 measures the
// artifact, on purpose, and says so.
//
// `go build -o <scratch>` IS LOAD BEARING, NOT HYGIENE. `go build ./...` or
// `go build .` in this directory OVERWRITES the tracked cmd/iterate/iterate —
// preserve.go finding (7), a hazard warned about nowhere before this unit, which
// the scaffold hit and survived by luck. g2FingerprintTrackedBinaries fails this
// row if anything in it writes any of the three artifacts.
//
// -buildvcs=false is passed because `go build` inside a linked git WORKTREE
// stamps the WRONG repository: Go's VCS detection wants a `.git` DIRECTORY and a
// worktree's is a FILE, so the search walks past it and stamps the first
// enclosing repo. The stamp is irrelevant to an in-test scratch binary and
// disabling it keeps the build from depending on where the worktree happens to
// live. It is NOT irrelevant for the committed artifact — see preserve.go
// finding (6), which is the body's instruction, and row 15.
//
// THE ORACLE IS THE TRACKED cmd/gates BINARY, and that is sound: gates is the
// CONSUMER whose behaviour is the regression, not the thing being fixed. Asking
// the real next tool what it does with the produced document is a stronger
// measurement than reading a key out of the JSON, and it is how the consequence
// was originally observed:
//
//	classification.changed_files destroyed -> gates finds no module owning any
//	  changed file -> exit 3 INVALID_INPUT. After one `iterate run` THE PIPELINE
//	  CANNOT GATE AT ALL.
//
// CONTROL, same call: the identical question asked of the legacy projection must
// come out exit 3. The assertion is on the EXIT CODE and on the parsed error
// line, never on a bare substring of a long report — an incidental substring is
// one of the seven measured vacuity shapes.
//
// SECOND CONTROL: g2GatesBlock's field sets are cross-checked against what the
// tracked gates binary actually writes in this call, so the measured constant
// every other row leans on cannot drift into a shape gates no longer produces.
func TestSeal_G2_EndToEnd_FromSource_ThePipelineCanStillGate(t *testing.T) {
	defer g2Red(t)
	g2FingerprintTrackedBinaries(t)

	worktree := g2Worktree(t)
	base := g2ProducedRunState(t, worktree)

	// Run the TRACKED gates binary for real, so this row's gate records are
	// produced rather than asserted — and cross-check the constant.
	realPath := g2WriteTemp(t, "real.json", base)
	gates := exec.Command(g2Abs(t, trackedGatesBinary), "-run-state", realPath, "-config", g2Abs(t, trackedGatesConfig))
	gates.Dir = worktree
	if out, err := gates.CombinedOutput(); err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() == 3 {
			t.Fatalf("the tracked gates binary could not gate the fixture (%v):\n%s", err, out)
		}
	}
	realDoc, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	g2AssertGatesBlockMatchesReality(t, realDoc)

	original := g2SeedProbes(t, base)
	runState := g2WriteTemp(t, "run-state.json", original)

	dir := t.TempDir()
	bin := filepath.Join(dir, "iterate-from-source")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building cmd/iterate from source failed: %v\n%s", err, out)
	}
	if fi, err := os.Stat(bin); err != nil || fi.Size() == 0 {
		t.Fatalf("the build produced nothing at %s (%v); this row would otherwise measure whatever else is on PATH", bin, err)
	}

	run := exec.Command(bin, "run", "-ceiling", "0", "-run-state", runState)
	if out, err := run.CombinedOutput(); err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() == exitInvalid {
			t.Fatalf("iterate (from source) failed: %v\n%s", err, out)
		}
	}
	produced, err := os.ReadFile(runState)
	if err != nil {
		t.Fatal(err)
	}

	// PROOF OF EXECUTION — a binary that exited before reaching appendRound would
	// leave the run-state untouched, and "untouched" preserves every path
	// perfectly.
	if n := g2ArrayLen(t, produced, "rounds"); n != 2 {
		t.Fatalf("the from-source binary recorded no round (rounds has %d element(s)); every assertion below would be about a document nothing happened to", n)
	}

	// CONTROL — the same question asked of today's projection comes out exit 3.
	legacyPath := g2WriteTemp(t, "legacy.json", g2LegacyProjection(t, original, g2Edits()))
	if code, line := g2GatesExit(t, worktree, legacyPath); code != 3 {
		t.Fatalf("CONTROL FAILED: cmd/gates exits %d over today's projection, want the measured 3 INVALID_INPUT. The pipeline consequence is not being exhibited and this row certifies nothing. Got: %s", code, line)
	}

	// THE ROW.
	if code, line := g2GatesExit(t, worktree, g2WriteTemp(t, "produced.json", produced)); code == 3 {
		t.Errorf("SEAL RED — after one `iterate run`, cmd/gates exits 3 INVALID_INPUT and the pipeline cannot gate at all. classification.changed_files was destroyed, so no Go module owns any changed file. This is strictly worse than the pre-G1 gates defect, which lost keys but left a gateable document. Got: %s", line)
	}

	// And the whole document, not just the consequence anyone happened to notice.
	if v := g2DivergencesOutsideIteratesLicence(t, original, produced); len(v) > 0 {
		t.Errorf("SEAL RED — a real iterate run over a real classify+gates run destroyed or altered %d JSON path(s) outside everything cmd/iterate is licensed to change:%s", len(v), g2Report(v))
	}
}

// ─── 15. the committed artifact ──────────────────────────────────────────────

// TestSeal_G2_TrackedBinary_IsRebuiltFromTheFixedSource is the ONE row that
// measures the committed artifact cmd/iterate/iterate rather than the source
// tree, and it does so on purpose.
//
// WHY. skills/iteration-protocol.md:130 execs cmd/iterate/iterate by absolute
// path, and .github/workflows/gates.yml runs `go test` per module over the
// checked-out tree and never rebuilds. So a source fix not accompanied by a
// rebuild is a fix that is absent from production, and nothing else in this repo
// would notice. It is ONE invoking document where cmd/gates had three, and one
// invoking document is still production.
//
// THE TRIGGER, and it can fire in both directions:
//
//	source unfixed, binary stale        RED  (today)
//	source fixed, binary NOT rebuilt    RED  — this is the failure the row exists
//	                                    for; row 14 will be green beside it and
//	                                    the pair says exactly what is wrong
//	source fixed, binary rebuilt        GREEN
//
// THE REBUILD BELONGS IN THE BODY COMMIT, built from a CLEAN CLONE of the branch
// carrying the fixed source and verified with `go version -m` — a `go build`
// inside a linked worktree stamps the wrong repository, and G1 committed and then
// discarded a binary built that way.
//
// PREDICTION, recorded so the body verifies it rather than trusting it: no green
// row in cmd/classify should fire. G1's rebuild fired two, because gates'
// behaviour on the differential changed; cmd/classify's differential invokes
// `iterate next`, and `iterate next` writes NOTHING — the run-state file is
// byte-identical before and after, confirmed in this worktree. 76 green / 9 red /
// 1 skip is the number to hold.
//
// CONTROL, same call: the artifact must still record a round and must still exit
// with a verdict code. Without it, "the artifact preserves the document" would go
// green against a binary that had stopped working altogether. And the SECOND
// control is the bound itself: `iterate next` must leave the file byte-identical,
// which is what makes the prediction above checkable rather than a hope.
func TestSeal_G2_TrackedBinary_IsRebuiltFromTheFixedSource(t *testing.T) {
	defer g2Red(t)
	g2FingerprintTrackedBinaries(t)

	worktree := g2Worktree(t)
	original := g2SeedProbes(t, g2ProducedRunState(t, worktree))

	// CONTROL — the bound. `iterate next` writes nothing, which is why rebuilding
	// this artifact cannot move any row that only runs `iterate next`.
	nextPath := g2WriteTemp(t, "next.json", original)
	before, err := os.ReadFile(nextPath)
	if err != nil {
		t.Fatal(err)
	}
	next := exec.Command(trackedIterateBinary, "next", "-run-state", nextPath)
	nextOut, _ := next.CombinedOutput()
	after, err := os.ReadFile(nextPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("CONTROL FAILED: `iterate next` MODIFIED the run-state (%d -> %d bytes). The rebuild prediction for cmd/classify's differential rests on it writing nothing; if that is no longer true, re-measure cmd/classify before and after the rebuild rather than trusting 76/9/1.", len(before), len(after))
	}
	if !strings.Contains(string(nextOut), "ITERATION DECISION") {
		t.Fatalf("CONTROL FAILED: the committed binary did not run at all:\n%s", nextOut)
	}

	runState := g2WriteTemp(t, "run-state.json", original)
	run := exec.Command(trackedIterateBinary, "run", "-ceiling", "0", "-run-state", runState)
	out, err := run.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() == exitInvalid {
			t.Fatalf("the committed binary %s failed: %v\n%s", trackedIterateBinary, err, out)
		}
	}
	produced, err := os.ReadFile(runState)
	if err != nil {
		t.Fatal(err)
	}

	// CONTROL — it really ran, and it really wrote.
	if n := g2ArrayLen(t, produced, "rounds"); n != 2 {
		t.Fatalf("CONTROL FAILED: the committed binary recorded no round (rounds has %d element(s)). This row's other assertions would be about a binary that did nothing.", n)
	}

	// THE ROW.
	if v := g2DivergencesOutsideIteratesLicence(t, original, produced); len(v) > 0 {
		t.Errorf("SEAL RED — the COMMITTED binary %s destroyed or altered %d JSON path(s) outside its licence. If the source is already fixed, this row is telling you the artifact production execs was not rebuilt from it — rebuild it in the body commit, from a clean clone, verified with `go version -m`. If the source is not fixed yet, this is simply the defect.%s",
			trackedIterateBinary, len(v), g2Report(v))
	}
	leaves := g2Flatten(t, produced)
	if got := leaves["classification.recheck_min_severity"]; got != g2Quote("medium") {
		t.Errorf("SEAL RED — the committed binary drops classification.recheck_min_severity (got %s); every later round's severity floor falls back to \"high\" on a critical money path until this artifact is rebuilt", got)
	}
	if _, ok := leaves["gates.coverage:apps/finance-domain/wallet.metrics.violations[0]"]; !ok {
		t.Errorf("SEAL RED — the committed binary destroys the coverage gate's violations while leaving status %s. Production's run-state says a gate failed and no longer says why.", leaves["gates.coverage:apps/finance-domain/wallet.status"])
	}
}

// ─── 16. the licence is derived from source, not from the comment ────────────

// TestSeal_G2_TheLicenceIsDerivedFromSourceAndTheCommentMustAgree seals the
// finding preserve.go rates as the one worth carrying forward: not the list, but
// that the code's OWN comment about what it owns was already wrong by two of six
// before anybody looked.
//
// MEASURES SOURCE, as text: main.go is parsed with go/parser in this call.
//
// main.go:439-440 says iterate "owns rounds[], round, verdict, status and
// nothing else". It omits updated_at (:449) and escalation_reason (:458) — a
// hand-list, written by the author of the code it describes, short by a third.
// classify/readset.go records the identical failure at larger scale. Nobody
// detected either.
//
// TWO LEGS, and they are not the same kind of thing:
//
//	THE DRIFT DETECTOR (control, green today). Every `state.<Field> =` assignment
//	in this package is extracted from the AST and must map onto exactly the six
//	EditKinds. This is the enumeration's soundness condition — "it is only sound
//	while it is still what iterate DOES" — made executable. It is green now and
//	it fires the day a seventh assignment site appears without an EditKind, which
//	is the only way this contract can silently stop describing the program.
//
//	THE COMMENT (the row, red today). A comment that says "and nothing else"
//	while naming four of six is worse than no comment: it is the artefact that
//	made the drift invisible. preserve.go finding (4)(5) assigns its correction to
//	the body, in the commit that wires appendRound. This leg is what makes that
//	assignment enforceable rather than advisory.
//
// It asserts only that the two omitted names APPEAR in appendRound's doc
// comment; it does not prescribe wording. A seal that pinned prose would be
// sealing a sentence, and the property is that the enumeration is not
// contradicted by the text next to it.
func TestSeal_G2_TheLicenceIsDerivedFromSourceAndTheCommentMustAgree(t *testing.T) {
	defer g2Red(t)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("cannot parse main.go: %v — this row derives the licence from the source and cannot fall back to asking", err)
	}

	// LEG 1, the drift detector.
	//
	// IT MUST SURVIVE THE WIRING, and getting this wrong was a real defect in
	// the first draft of this row. Today the licence is derived from
	// `state.<Field> =` assignment sites. After the body wires appendRound those
	// assignments are REPLACED by an []Edit literal — that is precisely what the
	// contract asks for — and a detector that only counted assignments would
	// fire on a correct body. Verified against a throwaway reference
	// implementation, where it did exactly that.
	//
	// So a licensed mutation is accounted for if the source EITHER still assigns
	// the RunState field OR constructs the EditKind that replaces it. The
	// detector's real job is the other direction and it is unaffected by the
	// wiring: a SEVENTH assignment site, or a seventh kind, with no counterpart
	// in the enumeration.
	assigned := map[string]bool{}
	kinds := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "state" {
					assigned[sel.Sel.Name] = true
				}
			}
		case *ast.Ident:
			if strings.HasPrefix(node.Name, "EditKind") && node.Name != "EditKindUnset" {
				kinds[node.Name] = true
			}
		}
		return true
	})

	// The six licensed mutations: the RunState field, and the EditKind that
	// replaces it once appendRound is wired.
	licensed := map[string]string{
		"Rounds":           "EditKindAppendRound",
		"Round":            "EditKindSetRound",
		"Verdict":          "EditKindSetVerdict",
		"UpdatedAt":        "EditKindSetUpdatedAt",
		"Status":           "EditKindSetStatus",
		"EscalationReason": "EditKindSetEscalationReason",
	}
	byKind := map[string]string{}
	for field, kind := range licensed {
		byKind[kind] = field
	}

	for field := range assigned {
		if _, ok := licensed[field]; !ok {
			t.Errorf("SEAL RED — cmd/iterate assigns state.%s and no EditKind licenses it. The enumeration in preserve.go is exhaustive and derived from the source; a seventh assignment site without a seventh kind means the contract has silently stopped describing the program, and \"pass it through\" is not available for a divergence the list does not name.", field)
		}
	}
	for kind := range kinds {
		if _, ok := byKind[kind]; !ok {
			t.Errorf("SEAL RED — cmd/iterate constructs %s, which is not one of the six mutations the enumeration derives from the source. Either the licence grew without the source growing, or the source grew and the enumeration was not re-derived.", kind)
		}
	}
	covered := 0
	for field, kind := range licensed {
		if assigned[field] || kinds[kind] {
			covered++
			continue
		}
		t.Errorf("SEAL RED — cmd/iterate neither assigns state.%s nor constructs %s. The licence must be the smallest set the source justifies; a kind for a mutation the program no longer makes licenses a change nobody intends.", field, kind)
	}
	if covered != 6 {
		t.Errorf("SEAL RED — %d of the 6 licensed mutations are accounted for in main.go (assignments: %v, kinds: %v)", covered, assigned, kinds)
	}

	// LEG 2, the comment. Find appendRound and read its doc comment.
	var doc string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "appendRound" || fn.Doc == nil {
			continue
		}
		doc = fn.Doc.Text()
	}
	if doc == "" {
		t.Fatal("appendRound has no doc comment at all; this leg is about a comment that contradicts the source, and there is now nothing to compare")
	}
	if !strings.Contains(doc, "nothing else") {
		// The claim has been withdrawn rather than corrected. That is an
		// acceptable outcome and the leg has nothing left to enforce.
		return
	}
	for _, name := range []string{"updated_at", "escalation_reason"} {
		if !strings.Contains(doc, name) {
			t.Errorf("SEAL RED — appendRound's doc comment claims iterate owns a list \"and nothing else\" without naming %s, which it assigns. The comment is short by two of six and has been since it was written; preserve.go finding (4)(5) assigns the correction to the body, in the commit that wires appendRound. Either name all six or drop the \"nothing else\" claim.\n  comment: %s", name, strings.TrimSpace(doc))
		}
	}
}

// ─── row helpers ─────────────────────────────────────────────────────────────

// g2DivergencesOutsideIteratesLicence is the acceptance measure for the two
// out-of-process rows, where the exact edit list is not known to the test: the
// binary stamps updated_at with the clock and builds the round record itself.
//
// It licenses the `rounds` container, the rounds[1] subtree and the five scalars
// wholesale, which is WEAKER than the contract — it forgives a wrong VALUE
// inside the appended round, and it forgives a `round` that disagrees with
// len(rounds). That weakness is bounded and deliberate: rows 7, 8 and 9 seal the
// append precisely, in process, where the edit list is known. Making this measure
// guess an edit list from the binary's own output would be deriving the licence
// from what happened, which VerifyPreservation's contract forbids for exactly the
// reason it would apply here.
//
// The licensed index is 1 because the FIXTURE has one round, not because the
// output has two. That distinction is the whole of row 9.
func g2DivergencesOutsideIteratesLicence(t *testing.T, original, produced []byte) []Divergence {
	t.Helper()
	if n := g2ArrayLen(t, original, "rounds"); n != 1 {
		t.Fatalf("this measure hard-codes the licensed index from the fixture's 1 round; the fixture has %d", n)
	}
	lic := Licence{
		Containers: []JSONPath{{{Key: "rounds"}}},
		Subtrees: []JSONPath{
			{{Key: "rounds"}, {Index: 1, IsIndex: true}},
			{{Key: "round"}}, {{Key: "verdict"}}, {{Key: "updated_at"}},
			{{Key: "status"}}, {{Key: "escalation_reason"}},
		},
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

// g2GatesExit runs the TRACKED cmd/gates binary over a run-state and returns its
// exit code and the INVALID_INPUT line if it printed one.
//
// The exit code is the assertion, not a substring of the report: 3 is
// INVALID_INPUT and every other code is a gate verdict. Matching on prose would
// be the incidental-substring vacuity, and gates' report is long.
func g2GatesExit(t *testing.T, worktree, runState string) (int, string) {
	t.Helper()
	cmd := exec.Command(g2Abs(t, trackedGatesBinary), "-run-state", runState, "-config", g2Abs(t, trackedGatesConfig))
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	line := ""
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.Contains(ln, "✗") {
			line = strings.TrimSpace(ln)
			break
		}
	}
	return code, line
}

// g2AssertGatesBlockMatchesReality cross-checks the measured g2GatesBlock
// constant against what the tracked cmd/gates binary just wrote.
//
// Without it the constant is a hand-written fixture that could drift into a shape
// gates no longer produces while every row leaning on it stayed green — the
// collapsed-input vacuity, arriving by decay rather than by design. It compares
// the SET OF FIELD NAMES per record kind, not the values, because the values are
// clocks and temp paths.
func g2AssertGatesBlockMatchesReality(t *testing.T, realDoc []byte) {
	t.Helper()
	fieldsOf := func(doc []byte, prefix string) map[string]bool {
		out := map[string]bool{}
		for path := range g2Flatten(t, doc) {
			if !strings.HasPrefix(path, prefix+".") {
				continue
			}
			rest := strings.TrimPrefix(path, prefix+".")
			if i := strings.IndexAny(rest, ".["); i >= 0 {
				rest = rest[:i]
			}
			out[rest] = true
		}
		return out
	}
	constDoc := g2SetTopLevel(t, []byte(`{}`), "gates", g2GatesBlock)
	for _, key := range []string{
		"gates.build:apps/finance-domain/wallet",
		"gates.coverage:apps/finance-domain/wallet",
		"gates.semgrep",
	} {
		want, got := fieldsOf(constDoc, key), fieldsOf(realDoc, key)
		if len(got) == 0 {
			t.Fatalf("the tracked gates binary wrote no record at %s; g2GatesBlock claims one and every in-process row leans on it", key)
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("g2GatesBlock HAS DRIFTED at %s: the constant carries %v and the tracked binary writes %v. Re-measure the constant; until then every row that leans on it is measuring a shape production does not produce.", key, want, got)
		}
	}
}

// g2DeleteTopLevelForRow removes a top-level member. Used only by row 12, where
// the point is a document today's readRunState accepts and load() rejects.
func g2DeleteTopLevelForRow(t *testing.T, doc []byte, key string) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatalf("not a JSON object: %v", err)
	}
	if _, ok := m[key]; !ok {
		t.Fatalf("seal bug: cannot delete absent member %q", key)
	}
	delete(m, key)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("re-emit: %v", err)
	}
	return append(out, '\n')
}
