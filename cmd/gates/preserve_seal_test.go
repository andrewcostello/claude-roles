package main

// Unit G1 seals — cmd/gates must stop destroying the classification.
//
// Twelve rows. All twelve are RED at the scaffold commit, and the failure list
// is the body author's checklist. preserve_seal_helpers_test.go carries the
// rules these rows hold themselves to; read its header first.
//
// WHAT EACH ROW MEASURES — source, or the committed artifact cmd/gates/gates.
// The distinction matters because cmd/gates/gates is TRACKED, and a row that
// execs it measures a frozen artifact that cannot see a source fix.
//
//	rows 1-9   SOURCE, in process. They call the package's own functions.
//	row 10     SOURCE, in process. mergeGates itself — the seam the body wires.
//	row 11     SOURCE, out of process: `go build -o <scratch>` and run THAT.
//	           The end-to-end consequence, measured on a binary built from the
//	           tree under test. It uses the frozen cmd/iterate as its ORACLE,
//	           which is sound — iterate is the consumer whose behaviour is the
//	           regression, not the thing being fixed.
//	row 12     THE COMMITTED ARTIFACT, deliberately, with its trigger stated.
//
// WHY THE ACCEPTANCE MEASURE IS Diverge AND NOT SidecarSurvives/v1KeysLost.
// preserve.go's finding (1) rules on it and this suite obeys the ruling: for
// v1KeysLost, top-level is correct, because it is a proof-of-execution
// instrument whose non-vacuity comes from being cross-checkable by the same
// function the test uses. For THE DEFECT, per-JSON-path is correct, and it is
// not close: classification.changed_files[0].risk and .rules[0] are destroyed
// INSIDE a key that survives, so two of the twenty-nine lost paths are
// invisible to any top-level reading. Row 3 makes that argument executable.

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ─── 1. round-trip fidelity ──────────────────────────────────────────────────

// TestSeal_G1_ApplyGateResults_PreservesEveryPathOutsideTheLicensedEdits is the
// core row. Everything else in this file is a named consequence of it.
//
// MEASURES SOURCE, in process.
//
// INPUT: a run-state written by the pinned cmd/classify binary in this call,
// plus the probes documented on g1SeedProbes.
//
// CONTROL, judged in the same call: g1LegacyProjection — today's mergeGates
// reproduced from this package's own structs — must show a large loss over the
// identical input and edit list. If the control ever comes out clean, the
// fixture has stopped being able to exhibit the defect and this row has become
// vacuous; investigate the fixture, do not relax the assertion.
//
// PROOF OF EXECUTION: g1AssertEditsLanded. A body that returned `original`
// unchanged would satisfy every preservation assertion here perfectly, so
// preservation and application are judged together.
func TestSeal_G1_ApplyGateResults_PreservesEveryPathOutsideTheLicensedEdits(t *testing.T) {
	defer g1Red(t)

	original := g1SeedProbes(t, g1ProducedRunState(t, g1Worktree(t)))
	edits := g1Edits()

	// CONTROL FIRST: prove the fixture can exhibit the defect before asking
	// anything of the body.
	legacyViolations := g1DivergencesOutside(t, original, g1LegacyProjection(t, original, edits), edits)
	if len(legacyViolations) < 25 {
		t.Fatalf("CONTROL FAILED: today's round-trip destroys only %d paths outside the licensed edits, and the measured defect is 29+. Either the fixture collapsed or the structs were widened — this row cannot certify anything until the control fires.%s",
			len(legacyViolations), g1Report(legacyViolations))
	}

	produced, err := ApplyGateResults(original, edits)
	if err != nil {
		t.Fatalf("ApplyGateResults: %v", err)
	}
	if len(produced) == 0 {
		t.Fatal("ApplyGateResults returned no bytes and no error — a successful merge that produced nothing would erase the run-state at the call site")
	}

	if v := g1DivergencesOutside(t, original, produced, edits); len(v) > 0 {
		t.Errorf("SEAL RED — %d JSON path(s) outside the licensed edits diverged. FidelityPathwise means every path present in the original and not covered by an allowed edit carries an IDENTICAL VALUE LITERAL in the produced document:%s",
			len(v), g1Report(v))
	}

	g1AssertEditsLanded(t, produced, edits)

	// gates must still be able to read what it wrote: it runs more than once in
	// a pipeline, and a preserved document that its own validator rejects has
	// traded one failure for a worse one.
	p := filepath.Join(t.TempDir(), "produced.json")
	if err := os.WriteFile(p, produced, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunState(p); err != nil {
		t.Errorf("SEAL RED — gates cannot re-read its own output: %v", err)
	}
}

// ─── 2. unknown keys ─────────────────────────────────────────────────────────

// TestSeal_G1_ApplyGateResults_PreservesUnknownKeysTopLevelAndNested seals the
// forward-compatibility property the v2 sidecar exists to work around
// (classify/contract.go:466-473).
//
// MEASURES SOURCE, in process.
//
// THREE LEGS, and the third is the one that makes the row producible rather
// than hypothetical:
//
//	top-level  contract_version           — a key a NEWER writer emits
//	nested     classification.zzz_future_key
//	nested     classification.recheck_min_severity — a key NO STRUCT in
//	           cmd/gates declares, that the pinned classify writes on every
//	           critical run, and that the tracked gates destroys today. The
//	           unknown-key property is not a future concern here; production is
//	           already losing keys to it.
//
// CONTROL, same call: all three must be destroyed by the legacy projection.
func TestSeal_G1_ApplyGateResults_PreservesUnknownKeysTopLevelAndNested(t *testing.T) {
	defer g1Red(t)

	original := g1SeedProbes(t, g1ProducedRunState(t, g1Worktree(t)))
	edits := g1Edits()

	unknown := map[string]string{
		"contract_version":                    "2",
		"classification.zzz_future_key":       g1Quote("a key no build in this repo declares"),
		"classification.recheck_min_severity": g1Quote("medium"),
	}

	before := g1Flatten(t, original)
	for path, lit := range unknown {
		if got, ok := before[path]; !ok || got != lit {
			t.Fatalf("fixture does not carry %s = %s (got %s, present=%v); the row cannot seal a key its input does not have", path, lit, got, ok)
		}
	}

	legacy := g1Flatten(t, g1LegacyProjection(t, original, edits))
	for path := range unknown {
		if _, ok := legacy[path]; ok {
			t.Fatalf("CONTROL FAILED: today's round-trip PRESERVES %s. This row cannot distinguish a fix from the status quo — check whether a struct was widened, which preserve.go's tripwires forbid.", path)
		}
	}

	produced, err := ApplyGateResults(original, edits)
	if err != nil {
		t.Fatalf("ApplyGateResults: %v", err)
	}
	after := g1Flatten(t, produced)
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

	g1AssertEditsLanded(t, produced, edits)
}

// ─── 3. sub-paths inside a surviving key ─────────────────────────────────────

// TestSeal_G1_ApplyGateResults_PreservesSubPathsInsideASurvivingKey is the row
// that makes preserve.go finding (1) executable.
//
// MEASURES SOURCE, in process.
//
// classification survives the legacy round-trip. changed_files survives it.
// changed_files[0].path survives it. And changed_files[0].risk and
// changed_files[0].rules[0] are destroyed inside them. A measure that compares
// the classification object's TOP-LEVEL KEYS reports success on that document,
// which is exactly why G1's acceptance measure is Diverge and not v1KeysLost.
//
// CONTROL, same call: the top-level reading is computed on the same legacy
// document and asserted to be CLEAN for these keys — the row proves its own
// necessity rather than asserting it in prose.
func TestSeal_G1_ApplyGateResults_PreservesSubPathsInsideASurvivingKey(t *testing.T) {
	defer g1Red(t)

	original := g1SeedProbes(t, g1ProducedRunState(t, g1Worktree(t)))
	edits := g1Edits()

	subPaths := map[string]string{
		"classification.changed_files[0].risk":     g1Quote("critical"),
		"classification.changed_files[0].rules[0]": g1Quote("wallet-service"),
	}

	legacyDoc := g1LegacyProjection(t, original, edits)

	// CONTROL A — the top-level reading sees nothing wrong.
	legacyClassification := g1Member(t, legacyDoc, "classification")
	legacyTop := g1TopLevelKeys(t, legacyClassification)
	if !g1Contains(legacyTop, "changed_files") {
		t.Fatalf("CONTROL FAILED: changed_files did not survive the legacy round-trip, so this row is no longer about a loss INSIDE a surviving key. Got %v", legacyTop)
	}
	if got := g1Flatten(t, legacyDoc)["classification.changed_files[0].path"]; got != g1Quote("apps/finance-domain/wallet/service/debit.go") {
		t.Fatalf("CONTROL FAILED: changed_files[0].path did not survive either (%s), so the element is gone rather than hollowed out and the sub-path argument does not apply", got)
	}

	// CONTROL B — and yet the sub-paths are gone.
	legacyLeaves := g1Flatten(t, legacyDoc)
	for path := range subPaths {
		if _, ok := legacyLeaves[path]; ok {
			t.Fatalf("CONTROL FAILED: %s survives today. The measured defect is that it does not; check the fixture before touching the assertion.", path)
		}
	}

	produced, err := ApplyGateResults(original, edits)
	if err != nil {
		t.Fatalf("ApplyGateResults: %v", err)
	}
	after := g1Flatten(t, produced)
	for path, lit := range subPaths {
		got, ok := after[path]
		if !ok {
			t.Errorf("SEAL RED — %s was DROPPED. It is destroyed inside a key that survives, so no top-level key-set comparison can see it, and a fix judged by one could be satisfied by re-attaching nulls.", path)
			continue
		}
		if got != lit {
			t.Errorf("SEAL RED — %s = %s, want %s", path, got, lit)
		}
	}

	// Array order and count are part of FidelityPathwise. reviewer_args is the
	// case with teeth: the panel tier is decided by argv ORDER, and a preserved
	// set in the wrong order is a different command.
	for i, want := range []string{"-cwd", ".", "-base", "origin/main", "-risk", "critical", "-component", "wallet"} {
		path := (JSONPath{{Key: "classification"}, {Key: "reviewer_args"}, {Index: i, IsIndex: true}}).String()
		if got := after[path]; got != g1Quote(want) {
			t.Errorf("SEAL RED — %s = %s, want %s (array order and count are part of FidelityPathwise)", path, got, g1Quote(want))
		}
	}
	if _, extra := after["classification.reviewer_args[8]"]; extra {
		t.Error("SEAL RED — reviewer_args grew an element; count is part of the property")
	}

	g1AssertEditsLanded(t, produced, edits)
}

// ─── 4. number literals and omitempty zero values ────────────────────────────

// TestSeal_G1_ApplyGateResults_PreservesNumberLiteralsAndZeroValues seals the
// two failure modes that kill preserve-by-declaration on its own terms.
//
// MEASURES SOURCE, in process.
//
// NUMBER LITERAL. deferred_findings and pr are decoded through `any`, so every
// number becomes a float64 and every integer above 2^53 comes back a different
// integer. Measured on the tracked binary: 9007199254740993 -> 9007199254740992.
// This row asserts on LITERALS, and one of its controls demonstrates in place
// why: it shows that the two literals are the SAME float64, so a decoded-value
// comparison would call the corruption "preserved".
//
// ZERO VALUE. `round: 0` is schema-legal (config/run-state.schema.json:236,
// integer, minimum 0, driver-owned) and `omitempty` erases it. repo.dirty:false
// is the second probe and carries the dispute recorded on g1SeedProbes.
//
// CONTROL, same call: a small integer in the same object must round-trip
// intact through the legacy path, so the row is about the 2^53 boundary and not
// about "numbers break".
func TestSeal_G1_ApplyGateResults_PreservesNumberLiteralsAndZeroValues(t *testing.T) {
	defer g1Red(t)

	original := g1SeedProbes(t, g1ProducedRunState(t, g1Worktree(t)))
	edits := g1Edits()

	const bigLiteral = "9007199254740993"
	const corrupted = "9007199254740992"

	// CONTROL A — a decoded-value comparison cannot tell these apart. This is
	// the argument for comparing literals, executed rather than asserted.
	var a, b float64
	if err := json.Unmarshal([]byte(bigLiteral), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(corrupted), &b); err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("CONTROL FAILED: %s and %s are distinguishable as float64 on this platform, so the literal comparison this row rests on is no longer load-bearing here", bigLiteral, corrupted)
	}

	legacy := g1Flatten(t, g1LegacyProjection(t, original, edits))

	// CONTROL B — today's path corrupts the big literal...
	if got := legacy["deferred_findings[0].line"]; got != corrupted {
		t.Fatalf("CONTROL FAILED: today's round-trip returns deferred_findings[0].line = %s, want the measured corruption %s. The fixture is not exhibiting the defect.", got, corrupted)
	}
	// ...and CONTROL C — leaves a small integer in the same object alone, so
	// the property is about the 2^53 boundary, not about numbers in general.
	if got := legacy["deferred_findings[0].found_in_round"]; got != "3" {
		t.Fatalf("CONTROL FAILED: a small integer in the same object came back %s, want 3. Something broader than the float64 round-trip is at work and this row would be attributing it to the wrong cause.", got)
	}
	// CONTROL D — and destroys both zero values.
	for _, path := range []string{"round", "repo.dirty"} {
		if _, ok := legacy[path]; ok {
			t.Fatalf("CONTROL FAILED: %s survives today; omitempty is supposed to erase it and this row is about that erasure", path)
		}
	}

	produced, err := ApplyGateResults(original, edits)
	if err != nil {
		t.Fatalf("ApplyGateResults: %v", err)
	}
	after := g1Flatten(t, produced)

	if got := after["deferred_findings[0].line"]; got != bigLiteral {
		if got == corrupted {
			t.Errorf("SEAL RED — deferred_findings[0].line = %s, want %s. This is the measured corruption: the value went through float64. A body that re-marshals a decoded document cannot fix it; the original bytes have to survive as bytes.", got, bigLiteral)
		} else {
			t.Errorf("SEAL RED — deferred_findings[0].line = %q, want the source literal %s", got, bigLiteral)
		}
	}
	for _, tc := range []struct{ path, lit string }{
		{"round", "0"},
		{"repo.dirty", "false"},
	} {
		got, ok := after[tc.path]
		if !ok {
			t.Errorf("SEAL RED — %s was DROPPED. It is a DECLARED field, and `omitempty` erased its zero value anyway — which is why preserve-by-declaration does not actually preserve, and why the zero value has to survive as bytes rather than as a struct member.", tc.path)
			continue
		}
		if got != tc.lit {
			t.Errorf("SEAL RED — %s = %s, want %s", tc.path, got, tc.lit)
		}
	}

	g1AssertEditsLanded(t, produced, edits)
}

// ─── 5. only the licensed edits ──────────────────────────────────────────────

// TestSeal_G1_VerifyPreservation_ReportsEditsOutsideTheLicensedPaths seals the
// checker rather than the editor.
//
// MEASURES SOURCE, in process.
//
// The documents here are HAND-BUILT fixtures, not ApplyGateResults output, so
// this row is red because VerifyPreservation is a stub and for no other reason.
// A row that fed one stub's output to another stub would be red without
// measuring anything.
//
// TWO RETURN CHANNELS. `err` means the check could not be performed;
// `violations` means it ran and found these. An empty violations slice with a
// nil error is the only passing result, and this row asserts on both channels
// every time — a caller that reads only `err` has re-created the implicit state
// the unit is about.
//
// CONTROL, same call: the clean document must produce zero violations and a nil
// error. Without it, "reports a violation" is satisfied by a function that
// reports every document.
func TestSeal_G1_VerifyPreservation_ReportsEditsOutsideTheLicensedPaths(t *testing.T) {
	defer g1Red(t)

	original := g1SeedProbes(t, g1ProducedRunState(t, g1Worktree(t)))
	edits := g1Edits()
	clean := g1HandBuiltProduced(t, original, edits)

	// CONTROL — the correct document is accepted.
	v, err := VerifyPreservation(original, clean, edits, FidelityPathwise)
	if err != nil {
		t.Errorf("SEAL RED (control) — VerifyPreservation could not check a correctly-produced document: %v", err)
	}
	if len(v) > 0 {
		t.Errorf("SEAL RED (control) — a correctly-produced document reported %d violation(s); a checker that flags everything certifies nothing:%s", len(v), g1Report(v))
	}

	// THE ROW — three unlicensed edits, each a real hazard.
	for _, tc := range []struct {
		name    string
		doc     []byte
		atPath  string
		because string
	}{
		{
			name:    "schema_version rewritten",
			doc:     g1SetTopLevel(t, clean, "schema_version", "2"),
			atPath:  "schema_version",
			because: "gates VALIDATES schema_version (main.go:1268) and must never rewrite it; preserve.go's AllowedPrefixes says so explicitly",
		},
		{
			name:    "classification.risk downgraded",
			doc:     g1SetTopLevel(t, clean, "classification", string(g1SetTopLevel(t, g1Member(t, clean, "classification"), "risk", g1Quote("low")))),
			atPath:  "classification.risk",
			because: "cmd/classify owns the classification and no later node may recompute it; a gates run that lowered the risk tier would silently drop the panel",
		},
		{
			name:    "created_at restamped",
			doc:     g1SetTopLevel(t, clean, "created_at", g1Quote("2099-01-01T00:00:00Z")),
			atPath:  "created_at",
			because: "only updated_at is licensed (main.go:1327); created_at is not, and the two are one typo apart",
		},
	} {
		v, err := VerifyPreservation(original, tc.doc, edits, FidelityPathwise)
		if err != nil {
			t.Errorf("SEAL RED — %s: VerifyPreservation returned err=%v. This document parses; the check CAN be performed and the divergence belongs in the violations channel, not the error one.", tc.name, err)
			continue
		}
		found := false
		for _, d := range v {
			if d.At.String() == tc.atPath {
				found = true
			}
		}
		if !found {
			t.Errorf("SEAL RED — %s: no violation reported at %s. %s. Got %d violation(s):%s", tc.name, tc.atPath, tc.because, len(v), g1Report(v))
		}
	}
}

// ─── 6. deletion under gates ─────────────────────────────────────────────────

// TestSeal_G1_VerifyPreservation_TreatsADeletionUnderGatesAsAViolation is the
// row that separates a correct checker from the obvious one.
//
// MEASURES SOURCE, in process.
//
// `gates` is licensed unconditionally, because mergeGates creates the member
// when it is absent. The naive checker therefore forgives EVERY divergence
// under `gates` — including a gate result that vanished. preserve.go rules the
// other way and says why: mergeGates never deletes a gate, and a gate that
// vanished from the map is indistinguishable at the reader from one that never
// ran, which is the state main.go's own header says this program exists to
// prevent.
//
// The rule the three legs pin down, jointly:
//
//	a removal at exactly `gates`              LICENSED (the {} leaf stops
//	                                          being a leaf when a gate is added)
//	anything at or under `gates.<edited-key>` LICENSED (create or replace)
//	a removal under `gates.<other-key>`       VIOLATION
func TestSeal_G1_VerifyPreservation_TreatsADeletionUnderGatesAsAViolation(t *testing.T) {
	defer g1Red(t)

	base := g1SeedProbes(t, g1ProducedRunState(t, g1Worktree(t)))
	withGates := g1SetTopLevel(t, base, "gates", g1GatesBlock)
	edits := []Edit{
		{Kind: EditKindSetGateResult, GateKey: "build:apps/finance-domain/wallet", Result: Gate{Status: "pass", Command: "go build ./...", DurationMS: 1234}},
		{Kind: EditKindSetUpdatedAt, UpdatedAt: g1UpdatedAt},
	}
	clean := g1HandBuiltProduced(t, withGates, edits)

	// CONTROL A — adding a gate beside two existing ones is clean.
	if v, err := VerifyPreservation(withGates, clean, edits, FidelityPathwise); err != nil || len(v) > 0 {
		t.Errorf("SEAL RED (control A) — creating a gate beside existing ones must be licensed: err=%v, %d violation(s):%s", err, len(v), g1Report(v))
	}

	// CONTROL B — REPLACING an edited gate is licensed even though the
	// replacement drops sub-paths the old value had. Without this leg the row
	// would be satisfied by the over-strict rule "no removal under gates ever",
	// which would fail every legitimate re-run.
	replaceEdits := []Edit{
		{Kind: EditKindSetGateResult, GateKey: "lint:apps/finance-domain/wallet", Result: Gate{Status: "fail", ExitCode: 1}},
		{Kind: EditKindSetUpdatedAt, UpdatedAt: g1UpdatedAt},
	}
	replaced := g1HandBuiltProduced(t, withGates, replaceEdits)
	if v, err := VerifyPreservation(withGates, replaced, replaceEdits, FidelityPathwise); err != nil || len(v) > 0 {
		t.Errorf("SEAL RED (control B) — replacing an EDITED gate must be licensed at or beneath its own prefix, including the sub-paths the new value does not carry: err=%v, %d violation(s):%s", err, len(v), g1Report(v))
	}

	// CONTROL C — the `gates` member going from {} to populated is licensed.
	// This is the case AllowedPrefixes' unconditional `gates` prefix exists
	// for, and it is why the rule cannot simply be "removals under gates are
	// violations".
	emptyGates := g1SetTopLevel(t, base, "gates", `{}`)
	fromEmpty := g1HandBuiltProduced(t, emptyGates, edits)
	if v, err := VerifyPreservation(emptyGates, fromEmpty, edits, FidelityPathwise); err != nil || len(v) > 0 {
		t.Errorf("SEAL RED (control C) — an empty `gates` object becoming populated must be licensed by the unconditional `gates` prefix: err=%v, %d violation(s):%s", err, len(v), g1Report(v))
	}

	// THE ROW — a gate nobody edited disappeared.
	deleted := g1SetTopLevel(t, clean, "gates",
		string(g1DeleteTopLevel(t, g1Member(t, clean, "gates"), "semgrep")))
	v, err := VerifyPreservation(withGates, deleted, edits, FidelityPathwise)
	if err != nil {
		t.Fatalf("SEAL RED — VerifyPreservation could not check a parseable document: %v", err)
	}
	found := false
	for _, d := range v {
		if d.Kind == DivergenceRemoved && d.At.HasPrefix(JSONPath{{Key: "gates"}, {Key: "semgrep"}}) {
			found = true
		}
	}
	if !found {
		t.Errorf("SEAL RED — the removal of gates.semgrep, a gate no edit named, was not reported as a violation. `gates` is licensed as a CONTAINER, not as a region where anything may happen: EditKindSetGateResult covers create and replace only. Got %d violation(s):%s",
			len(v), g1Report(v))
	}
}

// ─── 7. raise on unknown: the editor ─────────────────────────────────────────

// TestSeal_G1_ApplyGateResults_RefusesAnUnlicensedEditAndReturnsNoBytes seals
// the exhaustive-dispatch obligation at the point where it is not yet met.
//
// MEASURES SOURCE, in process.
//
// WHY THE errNotImplemented CHECKS. Every leg here would be VACUOUSLY GREEN
// against the scaffold: ApplyGateResults returns an error for every input, so
// "it errors on an unknown kind" is satisfied by a function that has never
// looked at the kind. Each leg therefore requires that the error is NOT
// errNotImplemented and that it NAMES the thing it refused. That is what makes
// these rows red today and what will make them mean something tomorrow.
//
// CONTROLS, same call: the dispatches preserve.go already implements —
// Edit.Validate, Edit.PathPrefix, Fidelity.Validate, AllowedPrefixes — are
// judged here rather than in a green row of their own, so that a body author
// who weakens one (in particular, PathPrefix returning an empty path, which is
// the prefix of EVERY path and would license the whole document) turns this row
// red instead of quietly widening the licence.
func TestSeal_G1_ApplyGateResults_RefusesAnUnlicensedEditAndReturnsNoBytes(t *testing.T) {
	defer g1Red(t)

	original := g1SeedProbes(t, g1ProducedRunState(t, g1Worktree(t)))

	// CONTROLS on the implemented dispatches.
	if err := Fidelity(99).Validate(); err == nil {
		t.Error("SEAL RED (control) — Fidelity(99).Validate() accepted a level this build does not recognise; an unrecognised level must never take the permissive branch")
	}
	if err := FidelityUnset.Validate(); err == nil {
		t.Error("SEAL RED (control) — FidelityUnset.Validate() accepted the zero value; a caller that did not name a level has not said what it is checking")
	}
	if err := FidelityPathwise.Validate(); err != nil {
		t.Errorf("SEAL RED (control) — the NORMATIVE level was rejected: %v", err)
	}
	unknownEdit := Edit{Kind: EditKind(99), GateKey: "build:.", Result: Gate{Status: "pass"}}
	if err := unknownEdit.Validate(); err == nil {
		t.Error("SEAL RED (control) — Edit.Validate accepted EditKind(99)")
	}
	if p, err := unknownEdit.PathPrefix(); err == nil || len(p) != 0 {
		t.Errorf("SEAL RED (control) — PathPrefix for an unrecognised kind returned path=%v err=%v. An EMPTY JSONPath is the prefix of every path, so returning one licenses the whole document — the exact fail-open this contract removes.", p, err)
	}
	if _, err := AllowedPrefixes([]Edit{unknownEdit}); err == nil {
		t.Error("SEAL RED (control) — AllowedPrefixes accepted an edit list containing an unrecognised kind")
	}

	// THE ROW.
	for _, tc := range []struct {
		name  string
		edits []Edit
		names string
	}{
		{"unrecognised kind", []Edit{unknownEdit}, "editkind(99)"},
		{"zero-valued Edit", []Edit{{}}, "unset"},
		{"gate result with no key", []Edit{{Kind: EditKindSetGateResult, GateKey: "   ", Result: Gate{Status: "pass"}}}, "GateKey"},
		{"gate result with no status", []Edit{{Kind: EditKindSetGateResult, GateKey: "build:.", Result: Gate{}}}, "status"},
		{"updated_at with no timestamp", []Edit{{Kind: EditKindSetUpdatedAt}}, "timestamp"},
	} {
		produced, err := ApplyGateResults(original, tc.edits)
		if err == nil {
			t.Errorf("SEAL RED — %s: ApplyGateResults ACCEPTED it. Passing an unlicensed mutation through is not available: a mutation nobody declared is indistinguishable, three nodes later, from a bug in the editor.", tc.name)
			continue
		}
		if errors.Is(err, errNotImplemented) {
			t.Errorf("SEAL RED — %s: refused with the scaffold's not-implemented marker, which every input gets. The dispatch must refuse ON ITS OWN TERMS and say what it refused; %q must appear in the message.", tc.name, tc.names)
			continue
		}
		if !strings.Contains(err.Error(), tc.names) {
			t.Errorf("SEAL RED — %s: the refusal does not name %q: %v", tc.name, tc.names, err)
		}
		if produced != nil {
			t.Errorf("SEAL RED — %s: returned %d byte(s) alongside the error. On failure it must return NO bytes: a caller that writes whatever came back would persist a document the editor disowned, and falling back to the old marshal path on error would make this whole unit vacuous by turning the failure mode into the error handler.", tc.name, len(produced))
		}
	}
}

// ─── 8. raise on unknown: the checker ────────────────────────────────────────

// TestSeal_G1_VerifyPreservation_RefusesWhatItCannotCheck seals the OTHER
// return channel: the difference between "the check ran and found nothing" and
// "the check could not run".
//
// MEASURES SOURCE, in process.
//
// The two red legs are again guarded against errNotImplemented, for the reason
// row 7 gives: a stub that errors on everything satisfies "it errors" without
// having dispatched on anything.
//
// CONTROLS, same call: the Fidelity legs, which preserve.go already wires into
// VerifyPreservation, must refuse and must NOT be reported as violations — an
// unrecognised level arriving as an empty violation list would read as "checked
// and clean".
func TestSeal_G1_VerifyPreservation_RefusesWhatItCannotCheck(t *testing.T) {
	defer g1Red(t)

	original := g1SeedProbes(t, g1ProducedRunState(t, g1Worktree(t)))
	edits := g1Edits()
	clean := g1HandBuiltProduced(t, original, edits)

	// CONTROLS — the level dispatch.
	for _, level := range []Fidelity{FidelityUnset, Fidelity(99)} {
		v, err := VerifyPreservation(original, clean, edits, level)
		if err == nil {
			t.Errorf("SEAL RED (control) — VerifyPreservation accepted fidelity level %s", level)
		}
		if len(v) > 0 {
			t.Errorf("SEAL RED (control) — level %s produced %d violation(s) as well as an error; a caller cannot tell a refusal from a finding", level, len(v))
		}
	}

	// THE ROW.
	for _, tc := range []struct {
		name     string
		produced []byte
		edits    []Edit
		names    string
	}{
		{"edit list that will not validate", clean, []Edit{{Kind: EditKindSetGateResult, GateKey: "", Result: Gate{Status: "pass"}}}, "GateKey"},
		{"edit list with an unrecognised kind", clean, []Edit{{Kind: EditKind(99)}}, "editkind(99)"},
		{"produced document that will not parse", []byte(`{"schema_version": 1,`), edits, "produced"},
		{"produced document with trailing content", append(append([]byte{}, clean...), []byte("{}\n")...), edits, "produced"},
	} {
		v, err := VerifyPreservation(original, tc.produced, tc.edits, FidelityPathwise)
		if err == nil {
			t.Errorf("SEAL RED — %s: VerifyPreservation returned no error. A document that does not parse is the most complete failure of preservation available, and reporting it as agreement is the vacuous pass this unit is about.", tc.name)
			continue
		}
		if errors.Is(err, errNotImplemented) {
			t.Errorf("SEAL RED — %s: refused with the scaffold's not-implemented marker rather than on its own terms; %q must appear in the message.", tc.name, tc.names)
			continue
		}
		if !strings.Contains(err.Error(), tc.names) {
			t.Errorf("SEAL RED — %s: the refusal does not name %q: %v", tc.name, tc.names, err)
		}
		if len(v) > 0 {
			t.Errorf("SEAL RED — %s: returned %d violation(s) alongside the error. `err` means the check could not be performed; `violations` means it ran and found these. Returning both makes the two states one.", tc.name, len(v))
		}
	}
}

// ─── 9. one read, two views ──────────────────────────────────────────────────

// TestSeal_G1_LoadRunStateDocument_ReturnsTheBytesItValidated seals the window
// preserve.go's contract closes: today readRunState is called twice per
// invocation, in prepare() and again inside mergeGates minutes later, so the
// document that was validated is not provably the document that gets edited.
//
// MEASURES SOURCE, in process.
//
// CONTROLS, same call: G1 may not TIGHTEN validation — a document gates accepts
// today must still be accepted, or G1 has changed what gates decides, which is
// the one thing it may not do — and a document gates rejects today must still
// be rejected, with an error that is not the scaffold marker.
func TestSeal_G1_LoadRunStateDocument_ReturnsTheBytesItValidated(t *testing.T) {
	defer g1Red(t)

	doc := g1SeedProbes(t, g1ProducedRunState(t, g1Worktree(t)))
	dir := t.TempDir()
	path := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		t.Fatal(err)
	}

	// CONTROL — today's reader accepts this document. If it did not, the accept
	// leg below would be asserting that G1 must LOOSEN validation, which is a
	// different and much larger claim.
	want, err := readRunState(path)
	if err != nil {
		t.Fatalf("CONTROL FAILED: today's readRunState rejects the fixture (%v); this row is about not TIGHTENING validation and cannot be judged against a document that was never accepted", err)
	}

	raw, state, err := LoadRunStateDocument(path)
	if err != nil {
		t.Errorf("SEAL RED — LoadRunStateDocument rejected a document readRunState accepts: %v. G1 must not tighten what gates accepts.", err)
	}
	if !reflect.DeepEqual(raw, doc) {
		t.Errorf("SEAL RED — the returned bytes are not the file's bytes (%d returned, %d on disk). The body must edit THESE bytes; re-reading or re-marshalling for the raw return re-opens the window this function exists to close.", len(raw), len(doc))
	}
	if state == nil {
		t.Error("SEAL RED — the decoded view is nil; the contract is one read, TWO views")
	} else if !reflect.DeepEqual(state, want) {
		t.Error("SEAL RED — the decoded view differs from readRunState's, so the two views are not of the same document")
	}

	// CONTROL — a document gates rejects today must still be rejected, and on
	// its own terms. Without the errNotImplemented guard this leg is vacuously
	// green against the scaffold, which errors on every input.
	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, g1SetTopLevel(t, doc, "schema_version", "2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunState(badPath); err == nil {
		t.Fatal("CONTROL FAILED: today's readRunState accepts schema_version 2, so this leg no longer distinguishes anything")
	}
	_, _, err = LoadRunStateDocument(badPath)
	if err == nil {
		t.Error("SEAL RED — LoadRunStateDocument accepted a document readRunState rejects; the validation is the EXISTING validateRunState and G1 does not loosen it either")
	} else if errors.Is(err, errNotImplemented) {
		t.Errorf("SEAL RED — the rejection is the scaffold marker, not a validation failure: %v", err)
	} else if !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("SEAL RED — the rejection does not name schema_version: %v", err)
	}
}

// ─── 10. the seam ────────────────────────────────────────────────────────────

// TestSeal_G1_MergeGates_PreservesTheWholeDocument seals the wiring.
//
// MEASURES SOURCE, in process. mergeGates is the one function in cmd/gates that
// writes the run-state; preserve.go deliberately did NOT rewire it, because
// wiring a raising stub into it would have taken all 63 green rows in this
// package red at the scaffold commit. The body performs the wiring, and this is
// the row that fails until it does.
//
// CONTROL, same call, and it is the sharpest one in the file: the assertion
// TestMergeGates_PreservesClassificationAndRounds makes today — re-unmarshal
// into RunState and check the declared fields — is reproduced here and asserted
// to PASS on the very same output this row rejects. A green row already exists
// over this function. It is green because it reads the document through the
// structs that destroyed it.
func TestSeal_G1_MergeGates_PreservesTheWholeDocument(t *testing.T) {
	defer g1Red(t)

	original := g1SeedProbes(t, g1ProducedRunState(t, g1Worktree(t)))
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	results := []result{
		{Key: "build:apps/finance-domain/wallet", Gate: "build", Module: "apps/finance-domain/wallet",
			Outcome: Gate{Status: "pass", Command: "go build ./...", DurationMS: 1234}},
		{Key: "semgrep", Gate: "semgrep",
			Outcome: Gate{Status: "skipped", SkipReason: "not selected by -only — this is NOT a pass"}},
	}
	if err := mergeGates(path, results); err != nil {
		t.Fatalf("mergeGates: %v", err)
	}
	produced, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// CONTROL — the reading today's suite uses reports success.
	var s RunState
	if err := json.Unmarshal(produced, &s); err != nil {
		t.Fatalf("CONTROL FAILED: the produced document is not readable at all: %v", err)
	}
	if s.TaskKey != "SMG-9001" || s.Classification == nil || s.Classification.Risk != "critical" ||
		s.Gates["build:apps/finance-domain/wallet"].Status != "pass" || s.Gates["semgrep"].Status != "skipped" {
		t.Fatal("CONTROL FAILED: the top-level, struct-shaped reading no longer passes; this row's argument is that such a reading passes while the document is being destroyed, and it needs that reading to pass")
	}

	// THE ROW — the same document, measured per JSON path.
	edits := []Edit{
		{Kind: EditKindSetGateResult, GateKey: results[0].Key, Result: results[0].Outcome},
		{Kind: EditKindSetGateResult, GateKey: results[1].Key, Result: results[1].Outcome},
		{Kind: EditKindSetUpdatedAt, UpdatedAt: s.UpdatedAt},
	}
	if v := g1DivergencesOutside(t, original, produced, edits); len(v) > 0 {
		t.Errorf("SEAL RED — mergeGates destroyed %d JSON path(s) outside the licensed edits. It is not yet wired to ApplyGateResults; it still round-trips the whole document through this package's closed structs (main.go:1316-1335). The row above passed on this same output.%s",
			len(v), g1Report(v))
	}
	g1AssertEditsLanded(t, produced, edits)

	if s.UpdatedAt == "" {
		t.Error("SEAL RED — updated_at was not stamped; the merge did not run")
	}
}

// ─── 11. the consequences, end to end, from source ───────────────────────────

// TestSeal_G1_EndToEnd_FromSource_TheTwoLiveRegressions is the row the two live
// regressions get to themselves.
//
// MEASURES SOURCE, out of process. It builds cmd/gates FROM THE TREE UNDER TEST
// to a scratch path with `go build -o` and runs THAT. It deliberately does not
// exec cmd/gates/gates: that binary is a committed artifact, and a row that
// measured it could not see a source fix at all. Row 12 measures the artifact,
// on purpose, and says so.
//
// THE ORACLE IS THE FROZEN cmd/iterate BINARY, and that is sound: iterate is
// the CONSUMER whose behaviour is the regression, not the thing being fixed.
// Asking the real consumer what it does with the produced document is a
// stronger measurement than reading a key out of the JSON, and it is how both
// consequences were originally observed:
//
//	recheck_min_severity gone -> floorFor (iterate/main.go:270-275) returns its
//	  "high" fallback and every MEDIUM finding is skipped on a critical money path
//	reviewer_args gone -> buildArgv (iterate/main.go:292) emits no -risk and no
//	  -component and the panel runs at the generic tier
//
// CONTROL, same call: the identical assertions are run against the legacy
// projection, where iterate must report Floor: high and an argv with no -risk.
// The assertions are on the exact contiguous argv substring and on the parsed
// Floor token, never on a bare "critical" — "critical" also appears in the
// header line, and an incidental substring is one of the five measured vacuity
// shapes.
func TestSeal_G1_EndToEnd_FromSource_TheTwoLiveRegressions(t *testing.T) {
	defer g1Red(t)
	g1FingerprintTrackedBinary(t)

	worktree := g1Worktree(t)
	original := g1SeedProbes(t, g1ProducedRunState(t, worktree))

	dir := t.TempDir()
	runState := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(runState, original, 0o600); err != nil {
		t.Fatal(err)
	}

	// Build from source to a SCRATCH path. `go build .` with no -o would write
	// cmd/gates/gates, the tracked artifact; g1FingerprintTrackedBinary above
	// fails this row if anything here does.
	bin := filepath.Join(dir, "gates-from-source")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building cmd/gates from source failed: %v\n%s", err, out)
	}
	if fi, err := os.Stat(bin); err != nil || fi.Size() == 0 {
		t.Fatalf("the build produced nothing at %s (%v); this row would otherwise measure whatever else is on PATH", bin, err)
	}

	run := exec.Command(bin, "-run-state", runState,
		"-config", "testdata/example-gates.json", "-only", "nosuchgate")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("gates (from source) failed: %v\n%s", err, out)
	}
	produced, err := os.ReadFile(runState)
	if err != nil {
		t.Fatal(err)
	}

	// PROOF OF EXECUTION — a binary that had exited before reaching the merge
	// would leave the run-state untouched, and "untouched" preserves every path
	// perfectly. The gate results have to be in there.
	if got := g1Flatten(t, produced)["gates.semgrep.status"]; got != g1Quote("skipped") {
		t.Fatalf("the from-source binary recorded no gate results (gates.semgrep.status = %s); every assertion below would be about a document nothing happened to", got)
	}

	// CONTROL — the same two questions, asked of the legacy projection, must
	// come out the broken way. If they do not, this fixture cannot exhibit the
	// regression and the row certifies nothing.
	legacyPath := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(legacyPath, g1LegacyProjection(t, original, g1Edits()), 0o600); err != nil {
		t.Fatal(err)
	}
	lFloor, lArgv := g1IterateNext(t, legacyPath)
	if lFloor != "high" {
		t.Fatalf("CONTROL FAILED: iterate reports Floor %q over today's projection, want the measured fallback \"high\"", lFloor)
	}
	if strings.Contains(lArgv, "-risk critical -component wallet") {
		t.Fatalf("CONTROL FAILED: today's projection still yields a risk-aware argv (%q); the reviewer_args regression is not being exhibited", lArgv)
	}

	// THE ROW.
	floor, argv := g1IterateNext(t, runState)
	if floor != "medium" {
		t.Errorf("SEAL RED — `iterate next` reports Floor %q, want \"medium\". classify wrote recheck_min_severity \"medium\"; floorFor found nothing and returned its \"high\" fallback, so every MEDIUM finding is skipped on a critical money path.", floor)
	}
	if !strings.Contains(argv, "-risk critical -component wallet") {
		t.Errorf("SEAL RED — the round-1 argv carries no -risk and no -component, so the panel runs at the generic tier — the exact regression cmd/classify exists to prevent. Got:\n  %s", argv)
	}

	// And the whole document, not just the two keys anyone happened to notice.
	if v := g1DivergencesOutsideGatesAndUpdatedAt(t, original, produced); len(v) > 0 {
		t.Errorf("SEAL RED — a real gates run over a real classify run destroyed %d JSON path(s) outside `gates` and `updated_at`:%s", len(v), g1Report(v))
	}
}

// ─── 12. the committed artifact ──────────────────────────────────────────────

// TestSeal_G1_TrackedBinary_IsRebuiltFromTheFixedSource is the ONE row in this
// file that measures the committed artifact cmd/gates/gates rather than the
// source tree, and it does so on purpose.
//
// WHY. roles/tasker.md:193, roles/coder.md:318 and README.md:39 exec
// cmd/gates/gates by absolute path, and .github/workflows/gates.yml runs
// `go test` over the checked-out tree and never rebuilds. So a source fix that
// is not accompanied by a rebuild is a fix that is absent from production, and
// nothing else in this repo would notice. preserve.go's finding (4) names that
// failure mode and calls refusing it the trade this repo has already been burned
// by twice.
//
// THE TRIGGER, and it can fire in both directions:
//
//	source unfixed, binary stale        RED  (today)
//	source fixed, binary NOT rebuilt    RED  — this is the failure the row
//	                                    exists for; row 11 will be green beside
//	                                    it and the pair says exactly what is
//	                                    wrong
//	source fixed, binary rebuilt        GREEN
//
// The rebuild belongs in the BODY commit. It will also fire two GREEN rows in
// cmd/classify by design (TestSeal_Recorded_V1ProjectionDoesNotSurviveGates and
// TestSeal_SidecarSurvives_*, via recordedV1KeysLost); both name the follow-up
// in their own text, and a body author may not edit seals, so that amendment
// needs the operator to route it to a seal author.
//
// CONTROL, same call: the artifact must still exit 0 and must still record its
// gate results. Without it, "the artifact preserves the classification" would
// go green against a binary that had stopped working altogether.
func TestSeal_G1_TrackedBinary_IsRebuiltFromTheFixedSource(t *testing.T) {
	defer g1Red(t)
	g1FingerprintTrackedBinary(t)

	worktree := g1Worktree(t)
	original := g1SeedProbes(t, g1ProducedRunState(t, worktree))
	dir := t.TempDir()
	runState := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(runState, original, 0o600); err != nil {
		t.Fatal(err)
	}

	run := exec.Command(trackedGatesBinary, "-run-state", runState,
		"-config", "testdata/example-gates.json", "-only", "nosuchgate")
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the committed binary %s failed: %v\n%s", trackedGatesBinary, err, out)
	}
	produced, err := os.ReadFile(runState)
	if err != nil {
		t.Fatal(err)
	}

	// CONTROL — it really ran, and it really wrote.
	leaves := g1Flatten(t, produced)
	if got := leaves["gates.semgrep.status"]; got != g1Quote("skipped") {
		t.Fatalf("CONTROL FAILED: the committed binary did not record its gate results (gates.semgrep.status = %s). This row's other assertions would be about a binary that did nothing.", got)
	}

	// THE ROW.
	if v := g1DivergencesOutsideGatesAndUpdatedAt(t, original, produced); len(v) > 0 {
		t.Errorf("SEAL RED — the COMMITTED binary %s destroyed %d JSON path(s). If the source is already fixed, this row is telling you the artifact production execs was not rebuilt from it — rebuild it in the body commit and update the two cmd/classify rows named in preserve.go finding (4). If the source is not fixed yet, this is simply the defect.%s",
			trackedGatesBinary, len(v), g1Report(v))
	}
	if got := leaves["classification.recheck_min_severity"]; got != g1Quote("medium") {
		t.Errorf("SEAL RED — the committed binary drops classification.recheck_min_severity (got %s); production's severity floor falls back to \"high\" on every critical money path until this artifact is rebuilt", got)
	}
}

// ─── row helpers ─────────────────────────────────────────────────────────────

// g1DivergencesOutsideGatesAndUpdatedAt is the acceptance measure for the two
// out-of-process rows, where the exact edit list is not known to the test: the
// binary decides which gates to record and stamps updated_at with the clock.
//
// It licenses `gates` and `updated_at` wholesale, which is WEAKER than the
// contract — it forgives a deletion under `gates`, and it forgives a rewritten
// gate the run did not produce. That weakness is bounded and deliberate: rows 5
// and 6 seal the licence precisely, in process, where the edit list is known.
// Making this measure guess an edit list from the binary's own output would be
// deriving the licensed set from what happened, which VerifyPreservation's
// contract forbids for exactly the reason it would apply here — it would
// certify whatever happened.
func g1DivergencesOutsideGatesAndUpdatedAt(t *testing.T, original, produced []byte) []Divergence {
	t.Helper()
	ds, err := Diverge(original, produced)
	if err != nil {
		t.Fatalf("Diverge: %v", err)
	}
	allowed := []JSONPath{{{Key: "gates"}}, {{Key: "updated_at"}}}
	var out []Divergence
	for _, d := range ds {
		licensed := false
		for _, p := range allowed {
			if d.At.HasPrefix(p) {
				licensed = true
				break
			}
		}
		if !licensed {
			out = append(out, d)
		}
	}
	return out
}

// g1IterateNext runs the frozen cmd/iterate binary over a run-state and returns
// the severity floor it computed and the argv it would run.
//
// It parses the two lines rather than substring-matching the whole output,
// because "critical" appears in the header as well as the argv and a bare
// substring assertion would be satisfied by the wrong occurrence.
//
// The exit code is NOT an error channel here: `iterate next` exits with its
// VERDICT (0 approve, 1 iterate, 2 escalate, 3 invalid input — iterate/main.go
// :42-45), and 1 is the ordinary answer for a run with no rounds yet. Treating
// a non-zero exit as a failure would have taken this row red for a reason that
// has nothing to do with preservation. Exit 3 is caught by the parse below:
// INVALID_INPUT prints no decision.
func g1IterateNext(t *testing.T, runState string) (floor, argv string) {
	t.Helper()
	const bin = "../iterate/iterate"
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("the frozen consumer %s is missing: %v — it is the oracle for both live regressions and this row cannot run without it", bin, err)
	}
	cmd := exec.Command(bin, "next", "-run-state", runState)
	out, _ := cmd.CombinedOutput()
	lines := strings.Split(string(out), "\n")
	for i, ln := range lines {
		if idx := strings.Index(ln, "Floor:"); idx >= 0 && floor == "" {
			floor = strings.TrimSpace(ln[idx+len("Floor:"):])
		}
		if strings.HasPrefix(strings.TrimSpace(ln), "Next command:") && i+1 < len(lines) {
			argv = strings.TrimSpace(lines[i+1])
		}
	}
	if floor == "" || argv == "" {
		t.Fatalf("could not read iterate's decision (floor=%q argv=%q, exit %d) from:\n%s",
			floor, argv, cmd.ProcessState.ExitCode(), out)
	}
	return floor, argv
}

// g1HandBuiltProduced builds the document a CORRECT ApplyGateResults would
// produce, by splicing raw JSON.
//
// IT IS A FIXTURE BUILDER, NOT A REFERENCE IMPLEMENTATION, and the difference
// is why it is allowed to exist in a seal file. It performs no validation, has
// no error channel, raises on anything it was not handed, and is used only to
// give VerifyPreservation documents whose divergences from the original are
// known BY CONSTRUCTION. Rows 5, 6 and 8 need that: feeding one stub's output
// to another stub would make them red without measuring anything.
func g1HandBuiltProduced(t *testing.T, original []byte, edits []Edit) []byte {
	t.Helper()
	gates := json.RawMessage(`{}`)
	var top map[string]json.RawMessage
	if err := json.Unmarshal(original, &top); err != nil {
		t.Fatal(err)
	}
	if g, ok := top["gates"]; ok {
		gates = g
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(gates, &members); err != nil {
		t.Fatalf("`gates` is not an object: %v", err)
	}
	out := original
	for _, e := range edits {
		switch e.Kind {
		case EditKindSetGateResult:
			b, err := json.Marshal(e.Result)
			if err != nil {
				t.Fatal(err)
			}
			members[e.GateKey] = b
		case EditKindSetUpdatedAt:
			out = g1SetTopLevel(t, out, "updated_at", g1Quote(e.UpdatedAt))
		default:
			t.Fatalf("seal bug: g1HandBuiltProduced was handed edit kind %s", e.Kind)
		}
	}
	b, err := json.MarshalIndent(members, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return g1SetTopLevel(t, out, "gates", string(b))
}

// g1Contains reports whether a string slice holds s.
func g1Contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ─── disputes and gaps this seal author records ──────────────────────────────
//
// (i) repo.dirty:false IS NOT PRODUCIBLE, and the scaffold's second measurement
// lists it as a loss G1 must fix. config/run-state.schema.json names cmd/classify
// as the only writer of `repo`, and classify's own Repo.Dirty carries
// `omitempty` (classify/main.go:121), so classify cannot emit `false` either.
// The probe is sealed anyway — under passthrough it costs nothing, and G1 must
// not be the unit that argues itself out of preserving a key — but `round: 0`
// is the leg the zero-value property rests on, because round: 0 is
// schema-legal (schema:236, integer, minimum 0) and driver-written. Recorded
// rather than resolved: if anyone wants repo.dirty:false to appear in a real
// run-state, that is a change to cmd/classify and its own unit.
//
// (ii) BYTE-IDENTITY OF UNTOUCHED SUBTREES IS NOT SEALED, deliberately.
// FidelityByteIdentical's doc comment says the sub-property is "free under the
// adopted design" — a passthrough holding untouched members as json.RawMessage
// re-emits their original bytes. It is not quite free: json.MarshalIndent runs
// json.Indent over the whole buffer, so a RawMessage the editor never descended
// into is still RE-INDENTED on the way out. On this document the re-indent
// happens to be a no-op, because the producer already emits two-space indent at
// the same depths — but that is a coincidence of the current producer, not a
// property. Sealing byte-identity would therefore seal something stronger than
// FidelityPathwise, which is the level preserve.go makes normative, so this
// suite does not. Flagged because ApplyGateResults' "must not reindent" bullet
// reads as though the design already guarantees it, and it does not.
//
// (iii) THE OUT-OF-PROCESS ROWS LICENSE `gates` AND `updated_at` WHOLESALE,
// which is weaker than the contract — see g1DivergencesOutsideGatesAndUpdatedAt
// for why tightening it there would mean deriving the licensed set from what
// happened. Rows 5 and 6 hold the precise licence, in process. The gap is
// bounded and stated rather than closed.
//
// (iv) OUT OF SCOPE, NOT SEALED HERE, CONFIRMED STILL BROKEN IN THIS WORKTREE:
// `iterate run` destroys classification.changed_files outright, after which
// gates exits 3 INVALID_INPUT and the pipeline cannot gate at all; iterate's
// Gate struct also destroys exit_code, command, ran_at, duration_ms,
// output_path and metrics. That is unit #20. Also out of scope: gates writes
// `gates` keys like "build:." that config/run-state.schema.json's
// propertyNames.enum rejects — the tracked binary already writes documents its
// own schema fails. Row 11 and row 12 both produce such keys, and neither seals
// them, because G1 must not change what gates writes.
