package main

// Shared machinery for the unit G1 seals — run-state preservation in cmd/gates.
//
// THESE FILES ARE SEALS, NOT AN IMPLEMENTATION. LoadRunStateDocument,
// ApplyGateResults and VerifyPreservation in preserve.go return
// errNotImplemented, and mergeGates is not yet wired to any of them, so every
// row in preserve_seal_test.go is RED on purpose. Red is the correct state
// until the body author implements to preserve.go's doc comments. A green row
// in this file's suite is either (a) a fact about code that already exists at
// baseline, recorded as a CONTROL inside a red row so a future edit is noticed,
// or (b) a mistake — investigate before relaxing anything.
//
// Four rules these seals hold themselves to, each of them a failure this
// pipeline has already shipped:
//
//   - Every row is judged alongside a CONTROL in the same call: the benign twin
//     that must come out the other way. A row with no control cannot tell an
//     implementation from a constant. The standard control here is
//     g1LegacyProjection — today's mergeGates, reproduced in-process from THIS
//     PACKAGE'S OWN STRUCTS — which must show the loss the row forbids. If the
//     control ever stops showing it, the fixture has stopped being able to
//     exhibit the defect and the row has become vacuous.
//
//   - Every row states whether it measures SOURCE or the COMMITTED ARTIFACT
//     cmd/gates/gates, and why. cmd/gates/gates is a tracked binary: a row that
//     execs it measures a frozen artifact and cannot see a source fix. Exactly
//     one row does that deliberately (TestSeal_G1_TrackedBinary_...), because
//     the artifact is what production runs, and it names the trigger that fires.
//
//   - Every row that a do-nothing body could satisfy carries PROOF OF
//     EXECUTION. The central hazard: ApplyGateResults returning `original`
//     unchanged preserves every path perfectly. g1AssertEditsLanded is the
//     answer and it is called from every fidelity row.
//
//   - Every input is one PRODUCTION can emit, and the row says how it knows.
//     The base document is not hand-written: it is generated at test time by
//     the pinned cmd/classify binary, the real v1 producer. Three seeded values
//     are NOT producible today — contract_version and
//     classification.zzz_future_key, which are forward-compatibility probes and
//     could not test forward compatibility if a current writer emitted them,
//     and repo.dirty:false, which carries a DISPUTE recorded on g1SeedProbes.
//     Each is named in place, beside a producible twin that carries the same
//     property, rather than being passed off as routine.

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

// g1Red turns a scaffold panic into an ordinary test failure, so one row's
// panic does not take the binary down and hide the shape of the work from the
// body author. It is not a tolerance: once the body lands nothing panics and
// this defer is inert.
func g1Red(t *testing.T) {
	t.Helper()
	if r := recover(); r != nil {
		t.Errorf("SEAL RED — panicked: %v", r)
	}
}

// trackedGatesBinary is the COMMITTED artifact. roles/tasker.md:193,
// roles/coder.md:318 and README.md:39 exec it by absolute path, so it — not the
// source tree — is what production runs.
const trackedGatesBinary = "./gates"

// pinnedClassifyBinary and pinnedClassifyConfig are the real v1 producer and
// the rule table cmd/classify's own seals classify against. They are FIXTURES,
// not build artifacts: cmd/classify/classify is pinned as the differential
// baseline and must never be rebuilt.
const (
	pinnedClassifyBinary = "../classify/classify"
	pinnedClassifyConfig = "../classify/testdata/example-monorepo.json"
)

// g1FingerprintTrackedBinary returns the SHA-256 of the committed cmd/gates
// binary and registers a cleanup that re-checks it.
//
// It exists because the two rows that shell out are one careless flag away from
// rebuilding the tracked binary in place — `go build .` writes cmd/gates/gates
// — and a seal suite that silently rebuilt the artifact it is measuring would
// be the frozen-artifact hazard with the evidence destroyed. Any row that
// builds or execs calls this first.
func g1FingerprintTrackedBinary(t *testing.T) string {
	t.Helper()
	sum := func() string {
		data, err := os.ReadFile(trackedGatesBinary)
		if err != nil {
			t.Fatalf("tracked binary %s is missing: %v — it is committed to this repo and these seals measure it; a suite that skipped here would go green on exactly the checkout where it mattered", trackedGatesBinary, err)
		}
		h := sha256.Sum256(data)
		return hex.EncodeToString(h[:])
	}
	before := sum()
	t.Cleanup(func() {
		if after := sum(); after != before {
			t.Errorf("SEAL BUG — this test rebuilt or overwrote the tracked binary %s (%s -> %s). The seals must never write it; build to a scratch path with `go build -o`.",
				trackedGatesBinary, before[:12], after[:12])
		}
	})
	return before
}

// ─── the base fixture: a run-state the real producer wrote ───────────────────

// g1WalletDiff is the one-file diff cmd/classify's own seal fixture set uses for
// its critical money-path case (seal_helpers_test.go sealFixtures, "wallet-
// critical"). It is reproduced here rather than imported because cmd/gates and
// cmd/classify are separate Go modules.
func g1WalletDiff() string {
	const f = "apps/finance-domain/wallet/service/debit.go"
	return "diff --git a/" + f + " b/" + f + "\n--- a/" + f + "\n+++ b/" + f + "\n@@ -1 +1 @@\n-old\n+new\n"
}

// g1ProducedRunState runs the PINNED cmd/classify binary and returns the
// run-state it wrote, verbatim.
//
// PRODUCIBILITY. This is the strongest form of "production can produce this
// input" available: the input is not described, reconstructed or hand-written,
// it is produced, by the frozen v1 producer, in this call. Every classification
// key these seals defend — recheck_min_severity, reviewer_args, panel,
// changed_files[].risk — is in the document because classify put it there.
//
// It FAILS rather than skips when the producer is absent, for the reason
// cmd/classify's pinnedV1 gives: a differential that quietly skips when it
// cannot find its baseline goes green on exactly the machine where it mattered
// least to run.
func g1ProducedRunState(t *testing.T, worktree string) []byte {
	t.Helper()
	if _, err := os.Stat(pinnedClassifyBinary); err != nil {
		t.Fatalf("pinned v1 producer %s is missing: %v — these seals generate their fixture with it rather than hand-writing one", pinnedClassifyBinary, err)
	}
	dir := t.TempDir()
	diffPath := filepath.Join(dir, "wallet.diff")
	if err := os.WriteFile(diffPath, []byte(g1WalletDiff()), 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "run-state.json")
	cmd := exec.Command(pinnedClassifyBinary,
		"-no-git",
		"-config", pinnedClassifyConfig,
		"-task", "SMG-9001",
		"-out", outPath,
		diffPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("pinned classify failed: %v\nstderr: %s", err, stderr.String())
	}
	doc, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("pinned classify wrote no run-state: %v", err)
	}

	// The fixture must be able to EXHIBIT the defect. A collapsed input — a
	// classification with three keys in it — would let every fidelity row go
	// green while preserving nothing, which is one of the five measured
	// vacuity shapes. Check the fixture before any row leans on it.
	g1AssertFixtureIsRich(t, doc)

	// repo.worktree must point at a tree the gates binary can discover modules
	// in, and base_sha must satisfy the schema's 7..40 hex pattern. -no-git
	// leaves both unusable for an end-to-end run.
	doc = g1SetTopLevel(t, doc, "repo", string(g1SetTopLevel(t, g1Member(t, doc, "repo"),
		"worktree", g1Quote(worktree), "base_sha", `"abc1234"`)))
	return doc
}

// g1AssertFixtureIsRich fails unless the generated document carries the keys
// every later row is about. It is the anti-collapse check.
func g1AssertFixtureIsRich(t *testing.T, doc []byte) {
	t.Helper()
	leaves := g1Flatten(t, doc)
	want := map[string]string{
		"classification.recheck_min_severity":      `"medium"`,
		"classification.reviewer_args[4]":          `"-risk"`,
		"classification.reviewer_args[5]":          `"critical"`,
		"classification.reviewer_args[6]":          `"-component"`,
		"classification.reviewer_args[7]":          `"wallet"`,
		"classification.changed_files[0].risk":     `"critical"`,
		"classification.changed_files[0].rules[0]": `"wallet-service"`,
		"classification.panel.seats":               `5`,
		"classification.human_pr_gate":             `true`,
		"classification.client_only":               `false`,
	}
	for path, lit := range want {
		got, ok := leaves[path]
		if !ok {
			t.Fatalf("fixture is collapsed: the pinned producer did not write %s, so no row here can exhibit the defect it is about", path)
		}
		if got != lit {
			t.Fatalf("fixture drifted: %s = %s, want %s — the seals were written against the producer's measured output", path, got, lit)
		}
	}
	if n := len(g1TopLevelKeys(t, g1Member(t, doc, "classification"))); n < 15 {
		t.Fatalf("fixture is collapsed: classification has %d top-level keys, want the producer's 15 — the defect is a 15→3 loss and a thin fixture cannot show it", n)
	}
}

// ─── the probes ──────────────────────────────────────────────────────────────
//
// PRODUCIBILITY, key by key, because three of these are not alike:
//
//	round: 0                    SCHEMA-LEGAL, NOT PRODUCED — CORRECTED BY P4
//	                            (adjudicate(G1)). This entry read "PRODUCIBLE AND
//	                            SCHEMA-LEGAL ... owned by the driver". Only the
//	                            second half survives. schema:236 does type `round`
//	                            as integer, minimum 0 — but top-level `round` is
//	                            declared `json:"round,omitempty"` by ALL THREE
//	                            writers (classify/main.go:108, gates/main.go:104,
//	                            iterate/main.go:69), and nothing else in this repo
//	                            creates a run-state: `classify -out` is the only
//	                            producer, and there is no driver that writes this
//	                            field. So no writer here emits round: 0 either.
//	repo.dirty: false           SCHEMA-LEGAL, NOT PRODUCED — see the DISPUTE note
//	                            on g1SeedProbes. THE SAME STANDING AS round: 0,
//	                            not a weaker one, which is the point of the
//	                            correction above: two probes in the same position
//	                            are not a probe plus a backstop.
//	                            THE ZERO-VALUE LEG THE ROWS ACTUALLY LEAN ON is
//	                            neither of them. cmd/classify declares
//	                            FinancialPathsTouched, ClientOnly, ServerSurface
//	                            and Migration WITHOUT `omitempty`
//	                            (classify/main.go:128-131), so it emits their zero
//	                            values on every run. Measured on this fixture: the
//	                            pinned producer writes
//	                            classification.client_only:false and
//	                            classification.migration:false; the pre-G1 binary
//	                            destroyed both with the rest of the classification
//	                            (15 keys -> 3); the rebuilt binary preserves both
//	                            as false. Producible, driver-written, and already
//	                            asserted by rows 1, 11 and 12.
//	deferred_findings[0].line   SCHEMA-LEGAL: the schema types `line` as an
//	                            integer with no maximum, and deferred_findings
//	                            is driver-written. Any integer above 2^53 in
//	                            `pr`, `rounds` or `deferred_findings` — all
//	                            three decoded through `any` — is corrupted the
//	                            same way. Measured: 9007199254740993 comes back
//	                            9007199254740992 from the tracked binary.
//	contract_version            FORWARD-COMPATIBILITY PROBE, by definition not
//	classification.zzz_future_key   emitted by any writer in this repo: the
//	                            property is that a key THIS BUILD DOES NOT KNOW
//	                            survives, and a key a current writer emits
//	                            cannot test it. Its producible twin is in the
//	                            document already and is asserted beside it:
//	                            classification.recheck_min_severity is a key no
//	                            struct in cmd/gates declares, the pinned
//	                            classify writes it on every critical run, and
//	                            the tracked gates destroys it.

// g1SeedProbes adds the forward-compatibility and zero-value probes to a
// produced run-state.
//
// It edits through json.RawMessage rather than through any typed struct, so the
// number literals arrive in the fixture exactly as written. A fixture that lost
// 9007199254740993 on its way IN could never seal its loss on the way out.
//
// DISPUTE, recorded rather than resolved here: the scaffold's second
// measurement lists repo.dirty:false among the losses G1 must fix, but
// cmd/classify — which config/run-state.schema.json names as the ONLY writer of
// `repo` — declares Dirty with `omitempty` too (classify/main.go:121), so no
// writer in this repo emits repo.dirty:false today. Preserving it is still
// correct and costs nothing under passthrough, and G1 must not be the unit that
// argues itself out of preserving a key. It is sealed here, and `round: 0`
// carries the zero-value property, because round: 0 IS producible.
//
// P4 RULING (adjudicate(G1)): the dispute is UPHELD on repo.dirty:false and the
// resolution is CORRECTED. Sealing the probe anyway was right. But `round: 0` is
// not producible either — all three writers declare it `omitempty` — so it
// cannot carry the zero-value property, and neither probe can. The property is
// carried by classification.client_only:false and classification.migration:false,
// which the pinned producer emits on every run because cmd/classify declares
// those fields without `omitempty`. See the corrected producibility table above
// and preserve.go's ruling (D). Nothing in this file's ASSERTIONS changes: both
// probes stay sealed, and the leg that carries the property was already being
// asserted by rows 1, 11 and 12. What changes is which leg the reader is told to
// lean on.
func g1SeedProbes(t *testing.T, doc []byte) []byte {
	t.Helper()
	doc = g1SetTopLevel(t, doc,
		"round", `0`,
		"contract_version", `2`,
		"deferred_findings", `[{"severity":"medium","summary":"unbounded debit path","file":"apps/finance-domain/wallet/service/debit.go","line":9007199254740993,"found_in_round":3}]`,
	)
	doc = g1SetTopLevel(t, doc, "repo", string(g1SetTopLevel(t, g1Member(t, doc, "repo"), "dirty", `false`)))
	doc = g1SetTopLevel(t, doc, "classification", string(g1SetTopLevel(t, g1Member(t, doc, "classification"),
		"zzz_future_key", `"a key no build in this repo declares"`)))

	// The seeding must not itself have corrupted anything: the document must
	// still parse, and it must still be readable by today's gates.
	if _, err := Diverge(doc, doc); err != nil {
		t.Fatalf("seeded fixture does not parse: %v", err)
	}
	if got := g1Flatten(t, doc)["deferred_findings[0].line"]; got != "9007199254740993" {
		t.Fatalf("the seeding lost the number literal before any row could seal it: deferred_findings[0].line = %s", got)
	}
	return doc
}

// g1GatesBlock is a `gates` member in the exact shape the TRACKED binary writes
// it: keys of the form "<gate>:<module-rel>" alongside a bare repo-scoped one,
// status plus skip_reason. Measured by running the tracked binary over this
// fixture with -only nosuchgate.
const g1GatesBlock = `{
    "lint:apps/finance-domain/wallet": {"status": "pass", "command": "golangci-lint run ./...", "ran_at": "2026-08-10T11:00:00Z", "duration_ms": 4210},
    "semgrep": {"status": "skipped", "skip_reason": "not selected by -only — this is NOT a pass"}
  }`

// ─── the canonical edit list ─────────────────────────────────────────────────

// g1UpdatedAt is the timestamp the edits set. Fixed, so a row's expectations do
// not depend on the clock.
const g1UpdatedAt = "2026-08-10T12:00:00Z"

// g1Edits is the licensed edit list for a run that recorded two gates and
// stamped the document — the three assignments preserve.go's enumeration is
// derived from (main.go:1322, :1325, :1327), with real gate keys: one carrying
// the ':' separator, one bare.
func g1Edits() []Edit {
	return []Edit{
		{Kind: EditKindSetGateResult, GateKey: "build:apps/finance-domain/wallet", Result: Gate{
			Status: "pass", Command: "go build ./...", RanAt: "2026-08-10T11:59:00Z", DurationMS: 1234,
		}},
		{Kind: EditKindSetGateResult, GateKey: "semgrep", Result: Gate{
			Status: "skipped", SkipReason: "not selected by -only — this is NOT a pass",
		}},
		{Kind: EditKindSetUpdatedAt, UpdatedAt: g1UpdatedAt},
	}
}

// ─── the measure ─────────────────────────────────────────────────────────────

// g1DivergencesOutside is the seal's own acceptance measure: every path at
// which produced differs from original and which no licensed prefix covers.
//
// It is built from Diverge and AllowedPrefixes — both implemented in the
// scaffold, both this contract's own — and NOT from VerifyPreservation, which
// is a stub. That separation is deliberate: the fidelity rows must be red
// because ApplyGateResults does not preserve, not because the checker they use
// is unimplemented, and a row that measured a stub with a stub would be red for
// no reason at all.
//
// It applies the PLAIN PREFIX rule and nothing more. That is knowingly WEAKER
// than the contract — a removal of a gate nobody edited sits under the
// unconditional `gates` prefix and this filter would forgive it — and it is
// weaker on purpose, so that these rows cannot pass or fail for reasons that
// belong to the deletion question.
// TestSeal_G1_VerifyPreservation_TreatsADeletionUnderGatesAsAViolation is where
// that question is sealed.
func g1DivergencesOutside(t *testing.T, original, produced []byte, edits []Edit) []Divergence {
	t.Helper()
	prefixes, err := AllowedPrefixes(edits)
	if err != nil {
		t.Fatalf("AllowedPrefixes: %v", err)
	}
	ds, err := Diverge(original, produced)
	if err != nil {
		t.Fatalf("Diverge: %v", err)
	}
	var out []Divergence
	for _, d := range ds {
		licensed := false
		for _, p := range prefixes {
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

// g1Report renders a divergence list for a failure message, capped so a 33-row
// loss does not bury the sentence that explains it.
func g1Report(ds []Divergence) string {
	const cap = 12
	var b strings.Builder
	for i, d := range ds {
		if i == cap {
			fmt.Fprintf(&b, "\n  ... and %d more", len(ds)-cap)
			break
		}
		b.WriteString("\n  ")
		b.WriteString(d.String())
	}
	return b.String()
}

// ─── the control: today's mergeGates, in process ─────────────────────────────

// g1LegacyProjection reproduces what mergeGates does TODAY — json.Unmarshal
// into this package's RunState, apply the edits, json.MarshalIndent back
// (main.go:1316-1335) — and is the standard control for every fidelity row.
//
// IT MEASURES SOURCE, not the committed binary: it runs THIS package's structs
// in this process. That matters for its longevity. After the body lands,
// mergeGates will no longer take this path, but the structs will still be
// closed — preserve.go's three tripwires forbid widening them — so this control
// keeps showing the loss and keeps proving the fixture can exhibit it. A
// control that execs the tracked binary would go quiet the moment the binary
// was rebuilt, which is the frozen-artifact hazard pointed the other way.
func g1LegacyProjection(t *testing.T, original []byte, edits []Edit) []byte {
	t.Helper()
	var s RunState
	if err := json.Unmarshal(original, &s); err != nil {
		t.Fatalf("legacy control: the fixture is not a run-state today's gates can read: %v", err)
	}
	if s.Gates == nil {
		s.Gates = map[string]Gate{}
	}
	for _, e := range edits {
		switch e.Kind {
		case EditKindSetGateResult:
			s.Gates[e.GateKey] = e.Result
		case EditKindSetUpdatedAt:
			s.UpdatedAt = e.UpdatedAt
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

// g1AssertEditsLanded fails unless every licensed edit is actually present in
// the produced document.
//
// This is the answer to the central vacuity in this unit: a body that returns
// `original` unchanged preserves every path perfectly and satisfies every
// fidelity assertion in the suite. Preservation and application have to be
// judged together or neither is judged at all.
func g1AssertEditsLanded(t *testing.T, produced []byte, edits []Edit) {
	t.Helper()
	leaves := g1Flatten(t, produced)
	for _, e := range edits {
		switch e.Kind {
		case EditKindSetGateResult:
			base := (JSONPath{{Key: "gates"}, {Key: e.GateKey}}).String()
			if got, ok := leaves[base+".status"]; !ok || got != g1Quote(e.Result.Status) {
				t.Errorf("PROOF OF EXECUTION FAILED: %s.status = %s (present=%v), want %s — the edit was never applied, so every preservation assertion in this row is about a document nothing happened to",
					base, got, ok, g1Quote(e.Result.Status))
			}
			if e.Result.Command != "" {
				if got := leaves[base+".command"]; got != g1Quote(e.Result.Command) {
					t.Errorf("PROOF OF EXECUTION FAILED: %s.command = %s, want %s — the gate result was written as a shell, not as the value gates computed", base, got, g1Quote(e.Result.Command))
				}
			}
			if e.Result.SkipReason != "" {
				if got := leaves[base+".skip_reason"]; got != g1Quote(e.Result.SkipReason) {
					t.Errorf("PROOF OF EXECUTION FAILED: %s.skip_reason = %s, want %s — a skip whose reason was dropped is the silent skip main.go's own header says this program exists to prevent", base, got, g1Quote(e.Result.SkipReason))
				}
			}
		case EditKindSetUpdatedAt:
			if got := leaves["updated_at"]; got != g1Quote(e.UpdatedAt) {
				t.Errorf("PROOF OF EXECUTION FAILED: updated_at = %s, want %s", got, g1Quote(e.UpdatedAt))
			}
		default:
			t.Fatalf("seal bug: unhandled edit kind %s", e.Kind)
		}
	}
}

// ─── small JSON tools ────────────────────────────────────────────────────────
//
// All of these go through json.RawMessage. None of them decodes a value into
// `any`, because that is the very corruption these seals measure.

// g1Flatten is Diverge's own walk, exposed as path -> value literal.
func g1Flatten(t *testing.T, doc []byte) map[string]string {
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

// g1Member returns one top-level member's raw bytes.
func g1Member(t *testing.T, doc []byte, key string) []byte {
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

// g1SetTopLevel sets top-level members from raw JSON literals and re-emits the
// object. Untouched members keep their original bytes, so nothing the caller
// did not name can be corrupted by the edit.
func g1SetTopLevel(t *testing.T, doc []byte, kv ...string) []byte {
	t.Helper()
	if len(kv)%2 != 0 {
		t.Fatalf("seal bug: g1SetTopLevel wants key/value pairs, got %d arguments", len(kv))
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

// g1DeleteTopLevel removes a top-level member.
func g1DeleteTopLevel(t *testing.T, doc []byte, key string) []byte {
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

// g1TopLevelKeys returns a JSON object's top-level keys, sorted.
func g1TopLevelKeys(t *testing.T, doc []byte) []string {
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

// g1Quote renders a Go string as the JSON literal Diverge would report for it,
// including encoding/json's HTML escaping — so a seal's expectation and a
// Divergence's Before/After are written in the same alphabet.
func g1Quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `"` + s + `"`
	}
	return string(b)
}

// ─── a worktree the gates binary can plan against ────────────────────────────

// g1Worktree builds the minimal tree cmd/gates needs to get past prepare(): a
// Go module owning the changed file the classification names. Without it,
// discoverModules finds nothing and gates exits 3 INVALID_INPUT before it ever
// reaches the merge this unit is about.
func g1Worktree(t *testing.T) string {
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
	write(filepath.Join(mod, "service", "debit.go"), "package service\n\nfunc Debit() {}\n")
	return root
}
