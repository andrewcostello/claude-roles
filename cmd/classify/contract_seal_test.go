package main

// Seals for contract.go — the version type, the config_scaffold desugar, the
// two-way panel projection, the v2 envelope shape, the sidecar and the dual
// config rule.
//
// Everything that calls a scaffold function is RED today. The rows that are
// green are marked RECORDED and say what would turn them red.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ─── ContractVersion ─────────────────────────────────────────────────────────

// ParseContractVersion accepts exactly "1" and "2". Everything else raises.
//
// The rejects are not decoration. "" is what an unset flag yields, "0" is the
// zero value spelled out, "v1"/"V1" are the two forms a human writes from
// memory, " 2" is a shell quoting accident and "02" is a zero-padded field from
// a generated argv — all of which a lenient parse would silently turn into a
// contract the caller did not choose. The difference between contracts is
// whether an absent config_scaffold is legal, so a wrong guess here is a wrong
// answer about whether a money-path table was ever reviewed.
//
// CONTROL: "1" and "2" are judged in the same call. A body that rejected
// everything would fail the first two rows.
func TestSeal_ParseContractVersion_ClosedSet(t *testing.T) {
	defer red(t)

	accepted := []struct {
		in   string
		want ContractVersion
	}{
		{"1", ContractV1},
		{"2", ContractV2},
	}
	for _, tt := range accepted {
		got, err := ParseContractVersion(tt.in)
		if err != nil {
			t.Errorf("CONTROL ParseContractVersion(%q) errored: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseContractVersion(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}

	for _, in := range []string{"", "0", "v1", "V1", " 2", "2 ", "02", "3", "-1", "1.0", "true", "1\n", "١"} {
		got, err := ParseContractVersion(in)
		if err == nil {
			t.Errorf("ParseContractVersion(%q) = %v, nil — every value outside {1,2} must raise; there is no lenient parse and no default on failure", in, got)
			continue
		}
		// The message must name what was received and what is accepted. An
		// operator who mistyped the flag gets one line, not a hunt.
		msg := err.Error()
		// Either the raw value or its quoted rendering: a message that %q's the
		// input is naming it, and for a value containing a newline that is the
		// better message.
		if in != "" && !strings.Contains(msg, in) && !strings.Contains(msg, strconv.Quote(in)) {
			t.Errorf("ParseContractVersion(%q) error %q does not name the value received", in, msg)
		}
		if !strings.Contains(msg, "1") || !strings.Contains(msg, "2") {
			t.Errorf("ParseContractVersion(%q) error %q does not name the accepted set {1,2}", in, msg)
		}
	}

	// Unset must never be produced by a parse, on any path.
	if got, err := ParseContractVersion("0"); err == nil || got == ContractVersionUnset && err == nil {
		t.Errorf(`ParseContractVersion("0") produced %v — "nobody decided" is a state that raises, never a value a parse returns`, got)
	}
}

// Valid() is the closed-set predicate. Unset is NOT valid, and neither is any
// future integer: the type must not acquire members by arithmetic.
func TestSeal_ContractVersion_Valid(t *testing.T) {
	defer red(t)
	for _, v := range []ContractVersion{ContractV1, ContractV2} { // CONTROL
		if !v.Valid() {
			t.Errorf("%d.Valid() = false, want true", int(v))
		}
	}
	for _, v := range []ContractVersion{ContractVersionUnset, ContractVersion(-1), ContractVersion(3), ContractVersion(99)} {
		if v.Valid() {
			t.Errorf("ContractVersion(%d).Valid() = true — Unset and out-of-set values are never valid", int(v))
		}
	}
}

// String() has two jobs and they constrain each other.
//
// It renders the probe's and the log's view of a contract, AND it supplies the
// -contract-version flag's default (capability.go:48-49 requires the registrar
// to default to defaultContractVersion.String()). That default is then parsed
// by ParseContractVersion. So String and Parse must ROUND-TRIP, which is what
// stops a body author rendering "ContractV1" or "v1" — either would make the
// binary reject its own default flag value.
//
// Separately, an out-of-set value must render so the raw integer is visible: a
// log line saying "unknown contract" that swallowed the number is the state
// this package spent twenty review rounds closing.
func TestSeal_ContractVersion_StringRoundTripsAndShowsUnknowns(t *testing.T) {
	defer red(t)

	for _, v := range []ContractVersion{ContractV1, ContractV2} {
		s := v.String()
		back, err := ParseContractVersion(s)
		if err != nil {
			t.Errorf("ParseContractVersion(%v.String() = %q) errored: %v — the flag default would be rejected by its own parser", int(v), s, err)
			continue
		}
		if back != v {
			t.Errorf("round trip %v -> %q -> %v", int(v), s, int(back))
		}
	}

	if got := ContractVersionUnset.String(); got != "unset" {
		t.Errorf("ContractVersionUnset.String() = %q, want %q", got, "unset")
	}

	for _, raw := range []int{3, 7, -1} {
		s := ContractVersion(raw).String()
		if !strings.Contains(s, strconv.Itoa(raw)) {
			t.Errorf("ContractVersion(%d).String() = %q — an unknown contract must visibly carry its raw integer, or it reads as a known one in a log line", raw, s)
		}
		// CONTROL: and it must not collide with a known rendering.
		if s == ContractV1.String() || s == ContractV2.String() {
			t.Errorf("ContractVersion(%d).String() = %q collides with a known contract", raw, s)
		}
	}
}

// defaultContractVersion is v1 for the whole coexistence period. Flipping it is
// the cut-over, not a tweak: gates still needs changed_files and iterate still
// needs recheck_min_severity, and neither is in the v2 envelope.
//
// RECORDED. Green today. It turns red on the cut-over commit, which is exactly
// when someone should be made to re-read the two consumer dependencies.
func TestSeal_EmissionDefaultStaysV1DuringCoexistence(t *testing.T) {
	t.Parallel()
	if defaultContractVersion != ContractV1 {
		t.Errorf("defaultContractVersion = %d, want ContractV1. Changing this is the cut-over: cmd/gates reads changed_files and cmd/iterate reads recheck_min_severity, and the v2 envelope carries neither. Migrate them first.", int(defaultContractVersion))
	}
}

// ─── DesugarConfigScaffold ───────────────────────────────────────────────────

// The one total desugaring rule, sealed exhaustively including its illegal
// arms.
//
// PRODUCTION REACHABILITY of each presence value:
//   - Absent under V1: every non-scaffold classify. The v1 tag is
//     `config_scaffold,omitempty` (main.go:141), so the pinned binary omits the
//     key on every run where the config is not a scaffold. Verified in
//     TestSeal_V1Golden_ConfigScaffoldOmitFalseTrue.
//   - PresentTrue under V1: any run against a `classify init` config. Verified
//     against testdata/scaffold-config.json, which IS `classify init` output.
//   - PresentFalse under V1: NOT PRODUCIBLE BY THE PINNED PRODUCER — omitempty
//     elides it. It is reachable at the consumer, which is where this rule runs:
//     the dispatcher's parse sees whatever a producer sent, and a V1_COMPAT
//     adapter (a new-capability producer emitting the legacy shape) can emit the
//     key explicitly. The row is sealed because the rule is a consumer rule; the
//     non-producibility is sealed separately, as a fact about EmitV1.
//   - Absent/False/True under V2: all three producible — absence is what a
//     producer that was never rebuilt sends, and false/true are the two values
//     BuildV2 writes.
func TestSeal_DesugarConfigScaffold_TotalTable(t *testing.T) {
	defer red(t)

	type row struct {
		contract ContractVersion
		presence ScaffoldPresence
		want     bool
		wantErr  bool
	}
	rows := []row{
		// The named desugar. Absence becomes false HERE and nowhere else.
		{ContractV1, ScaffoldAbsent, false, false},
		{ContractV1, ScaffoldPresentFalse, false, false},
		{ContractV1, ScaffoldPresentTrue, true, false},

		// Under v2 presence is required. This asymmetry is deliberate and must
		// not be relaxed in either direction: requiring presence under v1 would
		// turn every non-scaffold classify into INVALID_SCHEMA, and a soft
		// default under v2 would re-open absent-means-false.
		{ContractV2, ScaffoldAbsent, false, true},
		{ContractV2, ScaffoldPresentFalse, false, false},
		{ContractV2, ScaffoldPresentTrue, true, false},

		// "Nobody decided" is a named state that raises.
		{ContractVersionUnset, ScaffoldAbsent, false, true},
		{ContractVersionUnset, ScaffoldPresentFalse, false, true},
		{ContractVersionUnset, ScaffoldPresentTrue, true, true},

		// Presence not determined: the caller must not call the desugar.
		{ContractV1, ScaffoldPresenceUnknown, false, true},
		{ContractV2, ScaffoldPresenceUnknown, false, true},
		{ContractVersionUnset, ScaffoldPresenceUnknown, false, true},

		// No default arm falls through to v1.
		{ContractVersion(3), ScaffoldAbsent, false, true},
		{ContractVersion(3), ScaffoldPresentTrue, true, true},
		{ContractV1, ScaffoldPresence(9), false, true},
	}

	for _, r := range rows {
		got, err := DesugarConfigScaffold(r.contract, r.presence)
		if r.wantErr {
			if err == nil {
				t.Errorf("Desugar(%v, %v) = %v, nil — want an error", r.contract, r.presence, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Desugar(%v, %v) errored: %v", r.contract, r.presence, err)
			continue
		}
		if got != r.want {
			t.Errorf("Desugar(%v, %v) = %v, want %v", r.contract, r.presence, got, r.want)
		}
	}
}

// The V2/Absent error is the first thing an operator hits when they flip
// -contract-version 2 against a producer nobody rebuilt, so it must name the
// field and the contract rather than saying "invalid".
//
// CONTROL: the same call under v1 must succeed and yield false. A body that
// made absence an error everywhere would satisfy the message assertion and fail
// the control.
func TestSeal_DesugarConfigScaffold_V2AbsentMessageNamesFieldAndContract(t *testing.T) {
	defer red(t)

	if got, err := DesugarConfigScaffold(ContractV1, ScaffoldAbsent); err != nil || got != false {
		t.Errorf("CONTROL Desugar(V1, Absent) = %v, %v — want false, nil", got, err)
	}

	_, err := DesugarConfigScaffold(ContractV2, ScaffoldAbsent)
	if err == nil {
		t.Fatal("Desugar(V2, Absent) did not raise")
	}
	msg := err.Error()
	if !strings.Contains(msg, "config_scaffold") {
		t.Errorf("error %q does not name the field config_scaffold", msg)
	}
	if !strings.Contains(msg, "2") {
		t.Errorf("error %q does not name the contract it applies to", msg)
	}
}

// ─── panel projection, both directions ───────────────────────────────────────

// ProjectPanelToV1 is total over what the producer emits and raises on
// everything else — including SKIP, which has no v1 emission at all.
//
// The failure this closes: approximating an unknown intensity to FULL here.
// At-least-FULL is the CONSUMER's demand rule for an unknown intensity
// (design §3.3); the producer has no licence to invent a panel.
func TestSeal_ProjectPanelToV1(t *testing.T) {
	defer red(t)

	reasons := []string{"financial path touched", "component preset applies: wallet"}

	full, err := ProjectPanelToV1(PanelFULL, reasons)
	if err != nil {
		t.Fatalf("CONTROL ProjectPanelToV1(FULL) errored: %v", err)
	}
	if full.Required != true || full.Seats != 5 || full.Reduced != false {
		t.Errorf("FULL -> %+v, want {Required:true Seats:5 Reduced:false}", full)
	}
	if !sameStrings(full.Reasons, reasons) {
		t.Errorf("FULL reasons = %v, want the v2 reasons verbatim %v — the projection neither adds nor edits them", full.Reasons, reasons)
	}

	single, err := ProjectPanelToV1(PanelSINGLE, reasons)
	if err != nil {
		t.Fatalf("CONTROL ProjectPanelToV1(SINGLE) errored: %v", err)
	}
	if single.Required != true || single.Seats != 1 || single.Reduced != true {
		t.Errorf("SINGLE -> %+v, want {Required:true Seats:1 Reduced:true}", single)
	}
	if !sameStrings(single.Reasons, reasons) {
		t.Errorf("SINGLE reasons = %v, want %v", single.Reasons, reasons)
	}

	for _, bad := range []PanelIntensity{PanelSKIP, PanelIntensityUnset, PanelIntensity(42), PanelIntensity(-1)} {
		got, err := ProjectPanelToV1(bad, reasons)
		if err == nil {
			t.Errorf("ProjectPanelToV1(%d) = %+v, nil — SKIP has no v1 emission and an unknown intensity must raise, never approximate to FULL", int(bad), got)
		}
	}
}

// LiftPanelFromV1 accepts exactly the two shapes decidePanel constructs.
//
// The Required:false row is the important one. A future decidePanel that
// emitted Required:false would have changed the mandatory-panel rule, and that
// must surface as INVALID_SCHEMA rather than quietly becoming SKIP — because
// SKIP is the one intensity that means "no panel", and this project's rule is
// that no PR is raised with zero AI review.
func TestSeal_LiftPanelFromV1(t *testing.T) {
	defer red(t)

	got, err := LiftPanelFromV1(Panel{Required: true, Seats: 5, Reduced: false})
	if err != nil || got != PanelFULL {
		t.Errorf("CONTROL lift{true,5,false} = %d, %v — want FULL, nil", int(got), err)
	}
	got, err = LiftPanelFromV1(Panel{Required: true, Seats: 1, Reduced: true})
	if err != nil || got != PanelSINGLE {
		t.Errorf("CONTROL lift{true,1,true} = %d, %v — want SINGLE, nil", int(got), err)
	}

	bad := []struct {
		p   Panel
		why string
	}{
		{Panel{}, "the zero Panel"},
		{Panel{Required: false, Seats: 5}, "Required:false must raise, NOT become SKIP — it means the mandatory-panel rule changed"},
		{Panel{Required: false, Seats: 1, Reduced: true}, "Required:false with the reduced shape"},
		{Panel{Required: true, Seats: 5, Reduced: true}, "5 seats marked reduced is a contradiction"},
		{Panel{Required: true, Seats: 1, Reduced: false}, "1 seat not marked reduced is a contradiction"},
		{Panel{Required: true, Seats: 3}, "a third seat count decidePanel does not construct today"},
		{Panel{Required: true, Seats: 0}, "zero seats"},
	}
	for _, b := range bad {
		if got, err := LiftPanelFromV1(b.p); err == nil {
			t.Errorf("lift(%+v) = %d, nil — %s", b.p, int(got), b.why)
		}
	}
}

// The round trip, and the production-reachability evidence for both intensities
// in one call: the two panels are taken from real classify() output over the
// fixture set, not hand-built. This is how the seal knows production can emit
// the inputs it is judging.
func TestSeal_PanelProjection_RoundTripsOverRealClassifications(t *testing.T) {
	defer red(t)

	seen := map[PanelIntensity]string{}
	for _, f := range sealFixtures() {
		cls := f.classification(t)
		intensity, err := LiftPanelFromV1(cls.Panel)
		if err != nil {
			t.Errorf("%s: real classify() panel %+v does not lift: %v — LiftPanelFromV1 must be total over what decidePanel constructs", f.Name, cls.Panel, err)
			continue
		}
		seen[intensity] = f.Name

		back, err := ProjectPanelToV1(intensity, cls.Panel.Reasons)
		if err != nil {
			t.Errorf("%s: projecting %d back errored: %v", f.Name, int(intensity), err)
			continue
		}
		if back.Required != cls.Panel.Required || back.Seats != cls.Panel.Seats || back.Reduced != cls.Panel.Reduced {
			t.Errorf("%s: round trip %+v -> %d -> %+v", f.Name, cls.Panel, int(intensity), back)
		}
		if !sameStrings(back.Reasons, cls.Panel.Reasons) {
			t.Errorf("%s: reasons not carried verbatim: %v -> %v", f.Name, cls.Panel.Reasons, back.Reasons)
		}
	}

	// Both live intensities must actually have been exercised, or the fixture
	// set has drifted and this seal is passing on half the domain.
	for _, want := range []PanelIntensity{PanelFULL, PanelSINGLE} {
		if seen[want] == "" {
			t.Errorf("no fixture produced intensity %d — the fixture set no longer covers both panels the producer can emit", int(want))
		}
	}
	// And SKIP must never appear: the producer must never emit it.
	if f, ok := seen[PanelSKIP]; ok {
		t.Errorf("fixture %s lifted to SKIP — the producer must never emit SKIP", f)
	}
}

// ─── the v2 envelope's shape ─────────────────────────────────────────────────

// v2EnvelopeKeys is the EXACT key set of §3.3, in the struct's declaration
// order. Written out here so that a tag edit in contract.go is a two-file
// change and cannot pass as a rename.
var v2EnvelopeKeys = []string{
	"contract_version",
	"risk",
	"financial_paths_touched",
	"client_only",
	"server_surface",
	"migration",
	"human_pr_gate",
	"recheck_min_severity",
	"components",
	"panel",
	"gate_signals",
	"risk_reasons",
	"unmatched_files",
	"config_scaffold",
}

// v2AbsentKeys are the v1 keys §3.3 deliberately drops, each for a stated
// reason. Their absence is contractual, not incidental.
var v2AbsentKeys = []string{
	"classified_at",  // volatile: a wire that carries a clock has no deterministic golden
	"config_sha256",  // digests live in the wrapper, over the bytes actually consumed
	"config_path",    // a local path is not a portable wire fact
	"skills",         // a routing hint the consumer recomputes
	"reviewer_args",  // the frozen reviewer is advisory-only during coexistence
	"changed_files",  // see TestSeal_Finding_V2EnvelopeCannotFeedGates
	"schema_version", // the run-state's version is not the payload's
}

// The zero ClassificationV2 must still emit EVERY key. This is the no-omitempty
// rule, and it is the whole reason the struct exists rather than reusing the v1
// one: `omitempty` re-opens the absent-means-zero implicit state.
//
// RECORDED-plus: the struct is declared today, so this row is green at
// baseline. It is here because the Go signature gate freezes signatures, not
// struct TAGS — nothing else in this repo stops a body author adding omitempty
// to make a diff smaller. Mutation-verified: adding `,omitempty` to any field
// turns this red.
func TestSeal_ClassificationV2_NoOmitEmptyAnywhere(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(ClassificationV2{})
	if err != nil {
		t.Fatal(err)
	}
	got := topKeys(t, b)
	if missing := containsAll(got, v2EnvelopeKeys); len(missing) > 0 {
		t.Errorf("the ZERO ClassificationV2 omits %v — every field is non-omitted; a false bool must marshal as false and an absent key must never mean zero", missing)
	}
	if extra := containsAll(v2EnvelopeKeys, got); len(extra) > 0 {
		t.Errorf("ClassificationV2 carries keys outside the §3.3 set: %v", extra)
	}
	for _, k := range v2AbsentKeys {
		if hasKey(t, b, k) {
			t.Errorf("ClassificationV2 carries %q — §3.3 drops it deliberately; adding it back is a wire change, not a convenience", k)
		}
	}
	if !hasKey(t, b, "config_scaffold") {
		t.Error("config_scaffold is omitted from the zero value — under v2 it is REQUIRED and NON-OMITTED, which is the whole point of the field appearing here")
	}
}

// PanelV2 has exactly two members and its reasons are non-omitted: an empty
// reasons list is a real state, distinguishable from "reasons dropped".
func TestSeal_PanelV2_ShapeAndNonOmittedReasons(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(PanelV2{})
	if err != nil {
		t.Fatal(err)
	}
	got := topKeys(t, b)
	if !sameStrings(got, []string{"intensity", "reasons"}) {
		t.Errorf("PanelV2 keys = %v, want exactly [intensity reasons] — the v1 trio required/seats/reduced must not appear (one representation per fact; a panel is always required by contract)", got)
	}
}

// RECORDED FINDING — the one omitempty inside a no-omitempty envelope.
//
// ClassificationV2 reuses the v1 GateHit, which carries `sample,omitempty`
// (main.go:148). P1 could not fix it without changing v1 bytes, so the
// inconsistency is real and is carried deliberately. This seal pins it so it is
// a fact under test rather than a sentence in a comment: the day GateHit
// changes, this row goes red and whoever changed it is told, in the same
// breath, that they have changed v1's bytes too.
func TestSeal_Recorded_GateHitSampleOmitEmptyLeaksIntoV2(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(GateHit{Signal: "dev-flag", File: "a.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	if hasKey(t, b, "sample") {
		t.Error("GateHit no longer elides an empty sample. That is a v1 WIRE CHANGE (main.go:148 is the pinned binary's shape) as well as a v2 envelope change. If it was intentional, the v1 differential and the v1 goldens must be re-derived.")
	}
	// CONTROL: a populated sample is emitted, so the row above is about
	// omission and not about the field being absent altogether.
	b2, err := json.Marshal(GateHit{Signal: "dev-flag", File: "a.tsx", Sample: "+const show = __DEV__"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasKey(t, b2, "sample") {
		t.Error("CONTROL: a populated GateHit.sample is not emitted at all")
	}
}

// ─── BuildV2 ─────────────────────────────────────────────────────────────────

// BuildV2 is a pure projection: every value comes from cls, nil slices become
// empty slices (never null, because null is a third state), and a panel that
// does not lift is INVALID_SCHEMA.
func TestSeal_BuildV2_PureProjectionOverRealClassifications(t *testing.T) {
	defer red(t)

	for _, f := range sealFixtures() {
		cls := f.classification(t)
		v2, err := BuildV2(cls)
		if err != nil {
			t.Errorf("%s: BuildV2 errored: %v", f.Name, err)
			continue
		}

		if v2.ContractVersion != 2 {
			t.Errorf("%s: contract_version = %d, want 2", f.Name, v2.ContractVersion)
		}
		if v2.Risk != cls.Risk {
			t.Errorf("%s: risk = %q, want %q", f.Name, v2.Risk, cls.Risk)
		}
		if v2.FinancialPathsTouched != cls.FinancialPathsTouched ||
			v2.ClientOnly != cls.ClientOnly ||
			v2.ServerSurface != cls.ServerSurface ||
			v2.Migration != cls.Migration ||
			v2.HumanPRGate != cls.HumanPRGate {
			t.Errorf("%s: a boolean was invented or dropped: %+v vs %+v", f.Name, v2, cls)
		}
		if v2.RecheckMinSeverity != cls.RecheckMinSeverity {
			t.Errorf("%s: recheck_min_severity = %q, want %q", f.Name, v2.RecheckMinSeverity, cls.RecheckMinSeverity)
		}
		if v2.ConfigScaffold != cls.ConfigScaffold {
			t.Errorf("%s: config_scaffold = %v, want %v", f.Name, v2.ConfigScaffold, cls.ConfigScaffold)
		}
		if !sameStrings(v2.Components, cls.Components) {
			t.Errorf("%s: components = %v, want %v", f.Name, v2.Components, cls.Components)
		}
		if !sameStrings(v2.RiskReasons, cls.RiskReasons) {
			t.Errorf("%s: risk_reasons = %v, want %v", f.Name, v2.RiskReasons, cls.RiskReasons)
		}
		if !sameStrings(v2.UnmatchedFiles, cls.UnmatchedFiles) {
			t.Errorf("%s: unmatched_files = %v, want %v", f.Name, v2.UnmatchedFiles, cls.UnmatchedFiles)
		}
		if len(v2.GateSignals) != len(cls.GateSignals) {
			t.Errorf("%s: gate_signals length %d, want %d", f.Name, len(v2.GateSignals), len(cls.GateSignals))
		}

		// Nil in, empty-and-non-nil out. Marshalled, this is the difference
		// between [] and null, and null is a third state the envelope forbids.
		for name, s := range map[string][]string{
			"components":      v2.Components,
			"risk_reasons":    v2.RiskReasons,
			"unmatched_files": v2.UnmatchedFiles,
			"panel.reasons":   v2.Panel.Reasons,
		} {
			if s == nil {
				t.Errorf("%s: %s is nil — it must marshal as [], never null", f.Name, name)
			}
		}
		if v2.GateSignals == nil {
			t.Errorf("%s: gate_signals is nil — it must marshal as []", f.Name)
		}

		// The panel is the lifted one, and never the v1 trio.
		wantIntensity := "FULL"
		if cls.Panel.Reduced {
			wantIntensity = "SINGLE"
		}
		if v2.Panel.Intensity != wantIntensity {
			t.Errorf("%s: panel.intensity = %q, want %q", f.Name, v2.Panel.Intensity, wantIntensity)
		}
		if !sameStrings(v2.Panel.Reasons, cls.Panel.Reasons) {
			t.Errorf("%s: panel.reasons = %v, want the v1 reasons verbatim %v", f.Name, v2.Panel.Reasons, cls.Panel.Reasons)
		}
	}
}

// A classification whose panel does not lift is INVALID_SCHEMA, not a v2
// envelope with a guessed panel.
//
// CONTROL: a liftable panel on the same call path succeeds.
func TestSeal_BuildV2_UnliftablePanelIsSchemaError(t *testing.T) {
	defer red(t)

	ok := &Classification{Risk: "low", Panel: Panel{Required: true, Seats: 5}} // CONTROL
	if _, err := BuildV2(ok); err != nil {
		t.Errorf("CONTROL BuildV2 on a liftable panel errored: %v", err)
	}
	bad := &Classification{Risk: "low", Panel: Panel{Required: false, Seats: 5}}
	if got, err := BuildV2(bad); err == nil {
		t.Errorf("BuildV2 with Required:false = %+v, nil — it must fail rather than invent a panel", got)
	}
}

// The config_scaffold triple for v2: false and true both emitted, absent never.
//
// PRODUCTION REACHABILITY: false comes from any reviewed config
// (testdata/example-monorepo.json), true from `classify init` output
// (testdata/scaffold-config.json). "Absent" is not an emission the producer may
// make at all, which is what this row seals.
func TestSeal_V2_ConfigScaffoldAlwaysWrittenFalseAndTrue(t *testing.T) {
	defer red(t)

	seen := map[bool]bool{}
	for _, f := range sealFixtures() {
		cls := f.classification(t)
		v2, err := BuildV2(cls)
		if err != nil {
			t.Errorf("%s: BuildV2: %v", f.Name, err)
			continue
		}
		b, err := json.Marshal(v2)
		if err != nil {
			t.Fatal(err)
		}
		if !hasKey(t, b, "config_scaffold") {
			t.Errorf("%s: v2 emission omits config_scaffold — under v2 its absence is INVALID_SCHEMA, so the producer must always write it", f.Name)
		}
		seen[v2.ConfigScaffold] = true
	}
	if !seen[false] || !seen[true] {
		t.Errorf("the fixture set no longer covers both config_scaffold values (saw %v) — the omit/false/true triple is only two thirds sealed", seen)
	}
}

// ─── EmitV2 and the response wrapper ─────────────────────────────────────────

// EmitV2 with no DigestSource installed must FAIL, and must write nothing.
//
// This is round 12's rejected state made unreachable: a wrapper with absent or
// empty digests is the unimplementable echo, and a bare envelope with no
// wrapper has nowhere to carry the digests at all.
//
// CONTROL: the same call with a source installed succeeds and writes the
// wrapper.
func TestSeal_EmitV2_RequiresAnInstalledDigestSource(t *testing.T) {
	defer red(t)

	cls := sealFixtures()[0].classification(t)

	withHooks(t, nil, nil, nil)
	var buf bytes.Buffer
	if err := EmitV2(&buf, cls); err == nil {
		t.Errorf("EmitV2 with no DigestSource returned nil — it must fail rather than emit empty digests or a bare envelope. Wrote: %s", buf.String())
	}
	if buf.Len() != 0 {
		t.Errorf("EmitV2 wrote %d bytes on the no-digest path; it must write nothing:\n%s", buf.Len(), buf.String())
	}

	// A source that errors is the same story: no partial wrapper.
	withHooks(t, nil, fakeDigests{err: errors.New("no bytes consumed on the diff channel")}, nil)
	buf.Reset()
	if err := EmitV2(&buf, cls); err == nil {
		t.Error("EmitV2 with a failing DigestSource returned nil")
	}
	if buf.Len() != 0 {
		t.Errorf("EmitV2 wrote %d bytes when the DigestSource failed", buf.Len())
	}

	// CONTROL.
	withHooks(t, nil, fakeDigests{config: strings.Repeat("a", 64), diff: strings.Repeat("b", 64)}, nil)
	buf.Reset()
	if err := EmitV2(&buf, cls); err != nil {
		t.Fatalf("CONTROL EmitV2 with a DigestSource installed errored: %v", err)
	}
	var w ResponseWrapper
	if err := json.Unmarshal(buf.Bytes(), &w); err != nil {
		t.Fatalf("EmitV2 output is not a ResponseWrapper: %v\n%s", err, buf.String())
	}
	if w.ResponseVersion != responseVersion {
		t.Errorf("response_version = %d, want %d", w.ResponseVersion, responseVersion)
	}
	if w.ComputedConfigSHA256 != strings.Repeat("a", 64) || w.ComputedDiffSHA256 != strings.Repeat("b", 64) {
		t.Errorf("wrapper digests = %q/%q — they must come from the installed DigestSource", w.ComputedConfigSHA256, w.ComputedDiffSHA256)
	}
	if len(w.Classification) == 0 {
		t.Fatal("wrapper carries no classification")
	}
	// The payload inside is the v2 envelope, not the v1 one.
	if !hasKey(t, w.Classification, "contract_version") {
		t.Error("the wrapped payload has no contract_version — EmitV2 must wrap the v2 envelope")
	}
	if hasKey(t, w.Classification, "classified_at") {
		t.Error("the wrapped payload carries classified_at — EmitV2 wrapped a v1 payload")
	}
	// And the wrapper has no request_id: the request frame carries none, and an
	// unsourced echo field is the copy-back class one level up.
	if hasKey(t, buf.Bytes(), "request_id") {
		t.Error("the response wrapper carries request_id — there is deliberately none")
	}
}

// ─── the sidecar ─────────────────────────────────────────────────────────────

// V2SidecarPath is a LITERAL APPEND. The prettier ".json"-replacing form is the
// scaffold's named rejected alternative, and it is the control here: for
// "/tmp/run.json" the answer must be "/tmp/run.json.v2.json" and must NOT be
// "/tmp/run.v2.json".
func TestSeal_V2SidecarPath_IsALiteralAppend(t *testing.T) {
	defer red(t)

	cases := map[string]string{
		"/tmp/run.json":       "/tmp/run.json.v2.json",
		"/tmp/run":            "/tmp/run.v2.json",
		"run.json":            "run.json.v2.json",
		"/tmp/run.JSON":       "/tmp/run.JSON.v2.json", // no case analysis
		"/tmp/a.json/b.json":  "/tmp/a.json/b.json.v2.json",
		"/tmp/run.json.v2.js": "/tmp/run.json.v2.js.v2.json",
		"":                    "", // no sidecar; "./.v2.json" would be worse than nothing
	}
	for in, want := range cases {
		if got := V2SidecarPath(in); got != want {
			t.Errorf("V2SidecarPath(%q) = %q, want %q", in, got, want)
		}
	}
	if got := V2SidecarPath("/tmp/run.json"); got == "/tmp/run.v2.json" {
		t.Error("V2SidecarPath replaced the .json extension — that is the REJECTED alternative. A total append needs no case analysis and gives one answer for a run-state with no .json suffix; the ugly name is the price.")
	}
}

// WriteV2Sidecar writes the sidecar and NEVER the run-state.
//
// The three properties, each with the failure it closes:
//   - it never opens the run-state (the separate-file rule exists because the
//     frozen writers destroy unknown keys in the shared file);
//   - it fully REPLACES, never merges — this file has exactly one writer, so
//     imitating writeRunState's merge dance would resurrect stale facts;
//   - a write failure is a hard error, because a silently missing sidecar is
//     indistinguishable at the consumer from an old run and routes it down the
//     in-flight mirror path, losing the v2 facts.
func TestSeal_WriteV2Sidecar(t *testing.T) {
	defer red(t)

	dir := t.TempDir()
	runState := filepath.Join(dir, "run.json")
	runStateBytes := []byte(`{"schema_version":1,"task_key":"SMG-1","status":"in_progress"}` + "\n")
	if err := os.WriteFile(runState, runStateBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	cls := sealFixtures()[0].classification(t)
	if cls.Panel.Seats == 0 {
		t.Fatal("the fixture classification has no panel; the sidecar would carry nothing to check")
	}

	// A prior sidecar with a key nothing in V2Sidecar declares. After a
	// replacing write it must be GONE. If it survives, the writer merged.
	sidecar := V2SidecarPath(runState)
	if err := os.WriteFile(sidecar, []byte(`{"schema_version":1,"stale_marker":"from an earlier run"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	withHooks(t, nil, fakeDigests{config: strings.Repeat("c", 64), diff: strings.Repeat("d", 64)}, nil)
	if err := WriteV2Sidecar(runState, cls); err != nil {
		t.Fatalf("WriteV2Sidecar errored: %v", err)
	}

	after, err := os.ReadFile(runState)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, runStateBytes) {
		t.Errorf("WriteV2Sidecar modified the RUN-STATE. That is the bug the separate-file rule exists to prevent.\nbefore: %s\nafter:  %s", runStateBytes, after)
	}

	got, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("no sidecar at %s: %v", sidecar, err)
	}
	if bytes.Contains(got, []byte("stale_marker")) {
		t.Errorf("the prior sidecar's content survived — this file has exactly one writer and the write must fully replace, never merge:\n%s", got)
	}
	var sc V2Sidecar
	if err := json.Unmarshal(got, &sc); err != nil {
		t.Fatalf("sidecar does not parse as V2Sidecar: %v\n%s", err, got)
	}
	if sc.SchemaVersion != v2SidecarSchemaVersion {
		t.Errorf("sidecar schema_version = %d, want %d", sc.SchemaVersion, v2SidecarSchemaVersion)
	}
	if sc.Response.ResponseVersion != responseVersion {
		t.Errorf("sidecar response_version = %d, want %d", sc.Response.ResponseVersion, responseVersion)
	}
	// The sidecar carries the FULL wrapper, so a reader of the sidecar alone
	// can still check the dual digest echo.
	if sc.Response.ComputedConfigSHA256 == "" || sc.Response.ComputedDiffSHA256 == "" {
		t.Error("sidecar carries no digests — it must hold the whole wrapper, not the bare envelope")
	}
	if len(sc.Response.Classification) == 0 {
		t.Error("sidecar carries no classification payload")
	}

	// A write that cannot happen is a hard error, not a warning.
	if err := WriteV2Sidecar(filepath.Join(dir, "no-such-dir", "run.json"), cls); err == nil {
		t.Error("WriteV2Sidecar into a nonexistent directory returned nil — failure to write is a hard error")
	}
}

// An empty run-state must never produce "./.v2.json" in the process's working
// directory.
//
// DISPUTE (recorded, see the report): the scaffold says V2SidecarPath returns
// "" for an empty run-state and that callers must treat that as "no sidecar is
// written", but it does not say whether WriteV2Sidecar("") errors or is a
// silent no-op. The seal therefore pins only the part the scaffold does state —
// the file must not appear — and accepts either outcome, so that P4's ruling
// can land without rewriting this row.
func TestSeal_WriteV2Sidecar_EmptyRunStateWritesNoFile(t *testing.T) {
	defer red(t)

	cls := sealFixtures()[0].classification(t)
	withHooks(t, nil, fakeDigests{config: strings.Repeat("c", 64), diff: strings.Repeat("d", 64)}, nil)

	stray := ".v2.json"
	_ = os.Remove(stray)
	t.Cleanup(func() { _ = os.Remove(stray) })

	_ = WriteV2Sidecar("", cls)
	if _, err := os.Stat(stray); err == nil {
		t.Errorf("WriteV2Sidecar(\"\") created %q in the package directory — writing \"./.v2.json\" would be worse than nothing", stray)
	}
}

// ─── ResolveConfigDual ───────────────────────────────────────────────────────

// The dual-config rule, including its CHOICE: difference is SHA-256 over the
// RAW BYTES, not over the parsed config.
//
// The formatting-only case is the seal that pins the CHOICE. Two copies of a
// project's money table that parse identically but differ in whitespace are
// already a drift signal: they are two files a human has been editing
// separately. The rejected alternative — comparing parsed Configs — would
// accept them, and would also accept a reordered rules array that changes
// nothing today and something tomorrow.
//
// PRODUCTION REACHABILITY of "both present": commit 2b18e02 moved agent tooling
// config from .claude/ to .agent/. Every project mid-migration has both.
func TestSeal_ResolveConfigDual(t *testing.T) {
	defer red(t)

	good, err := os.ReadFile(exampleConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	// Same config, different bytes: one extra level of indentation. Parses to
	// the identical Config; the raw bytes differ.
	var anyCfg map[string]any
	if err := json.Unmarshal(good, &anyCfg); err != nil {
		t.Fatal(err)
	}
	reindented, err := json.MarshalIndent(anyCfg, "", "    ")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(good, reindented) {
		t.Fatal("fixture setup: the reindented config is byte-identical, so the CHOICE row would be vacuous")
	}
	if _, err := parseConfig(reindented); err != nil {
		t.Fatalf("fixture setup: the reindented config must still PARSE, or this tests the wrong thing: %v", err)
	}

	mk := func(t *testing.T, agent, claude []byte) string {
		t.Helper()
		wt := t.TempDir()
		if agent != nil {
			if err := os.MkdirAll(filepath.Join(wt, ".agent"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wt, ".agent", "risk-paths.json"), agent, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if claude != nil {
			if err := os.MkdirAll(filepath.Join(wt, ".claude"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wt, ".claude", "risk-paths.json"), claude, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return wt
	}

	t.Run("agent_alone", func(t *testing.T) {
		defer red(t)
		wt := mk(t, good, nil)
		got, err := ResolveConfigDual(wt)
		if err != nil {
			t.Fatalf("errored: %v", err)
		}
		if got != filepath.Join(wt, ".agent", "risk-paths.json") {
			t.Errorf("resolved %q, want the .agent copy", got)
		}
	})

	t.Run("claude_alone_is_still_accepted", func(t *testing.T) {
		defer red(t)
		wt := mk(t, nil, good)
		got, err := ResolveConfigDual(wt)
		if err != nil {
			t.Fatalf("errored: %v — .claude/ alone stays supported; projects already have one", err)
		}
		if got != filepath.Join(wt, ".claude", "risk-paths.json") {
			t.Errorf("resolved %q, want the .claude copy", got)
		}
	})

	t.Run("both_identical_prefers_agent", func(t *testing.T) {
		defer red(t)
		wt := mk(t, good, good)
		got, err := ResolveConfigDual(wt)
		if err != nil {
			t.Fatalf("errored: %v", err)
		}
		if got != filepath.Join(wt, ".agent", "risk-paths.json") {
			t.Errorf("resolved %q, want the preferred .agent copy", got)
		}
	})

	t.Run("both_differing_is_schema_error", func(t *testing.T) {
		defer red(t)
		other, err := os.ReadFile(scaffoldConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		wt := mk(t, good, other)
		got, err := ResolveConfigDual(wt)
		if err == nil {
			t.Fatalf("resolved %q with no error — two differing money tables must be INVALID_SCHEMA, not a silent preference", got)
		}
		msg := err.Error()
		if !strings.Contains(msg, ".agent") || !strings.Contains(msg, ".claude") {
			t.Errorf("error %q does not name both candidates the operator must reconcile", msg)
		}
	})

	t.Run("CHOICE_formatting_only_difference_still_fails", func(t *testing.T) {
		defer red(t)
		wt := mk(t, good, reindented)
		if got, err := ResolveConfigDual(wt); err == nil {
			t.Errorf("resolved %q with no error. Difference is SHA-256 OVER THE RAW BYTES: two copies of a project's money table that differ only in formatting are two files a human has been editing separately, and that is a drift signal worth failing on. Comparing PARSED Configs is the named rejected alternative — it also silently accepts a reordered rules array that changes nothing today and something tomorrow.", got)
		}
	})

	t.Run("neither_is_the_existing_missing_config_path", func(t *testing.T) {
		defer red(t)
		wt := mk(t, nil, nil)
		if got, err := ResolveConfigDual(wt); err == nil {
			t.Errorf("resolved %q in an empty worktree — a missing config is INVALID_INPUT, not a default", got)
		}
	})
}

// RECORDED SECURITY FINDING, deliberately left as a finding rather than fixed.
//
// configCandidates (main.go:330-341) places $RISK_PATHS_CONFIG AHEAD of both
// directories. An agent that can set an environment variable therefore
// redirects the entire money-path table, and the new dual-config check never
// runs at all — ResolveConfigDual is not reached, so a differing pair is never
// detected. This is an authority-channel problem and is out of B1's scope; it
// must NOT be closed by quietly reordering the candidate list.
//
// Sealed by EXEC rather than by mutating this process's environment: the
// pinned binary in a child process is both stronger evidence — it is the real
// end-to-end behaviour, config_path in its own output names the file it used —
// and free of any risk of racing main_test.go's parallel
// TestConfigCandidates_PrefersVendorNeutralDir, which reads the same variable.
//
// Green today. It turns red when someone fixes the ordering, which is the
// moment the finding should be re-read and the fix credited.
func TestSeal_Recorded_EnvVarOutranksBothConfigDirectories(t *testing.T) {
	t.Parallel()

	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, ".agent"), 0o750); err != nil {
		t.Fatal(err)
	}
	trusted, err := os.ReadFile(exampleConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	trustedPath := filepath.Join(wt, ".agent", "risk-paths.json")
	if err := os.WriteFile(trustedPath, trusted, 0o600); err != nil {
		t.Fatal(err)
	}
	// The attacker-supplied table: a scaffold that names no money paths at all.
	attacker, err := os.ReadFile(scaffoldConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	attackerPath := filepath.Join(wt, "elsewhere.json")
	if err := os.WriteFile(attackerPath, attacker, 0o600); err != nil {
		t.Fatal(err)
	}

	diffPath := filepath.Join(wt, "fixture.diff")
	if err := os.WriteFile(diffPath, []byte(diffFor("apps/finance-domain/wallet/service/debit.go")), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(env []string) map[string]any {
		t.Helper()
		cmd := exec.Command(pinnedBinary, "-json", "-no-git", "-worktree", wt, diffPath)
		cmd.Env = append(os.Environ(), env...)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			t.Fatalf("pinned binary failed: %v\n%s", err, errb.String())
		}
		var m map[string]any
		if err := json.Unmarshal(out.Bytes(), &m); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, out.String())
		}
		return m
	}

	// CONTROL: with no env var the worktree's .agent/ table is used and the
	// money path is correctly critical and financial.
	base := run([]string{"RISK_PATHS_CONFIG="})
	if base["config_path"] != trustedPath {
		t.Fatalf("CONTROL used config_path %v, want the worktree's .agent table %q", base["config_path"], trustedPath)
	}
	if base["financial_paths_touched"] != true {
		t.Fatalf("CONTROL: the wallet path is not financial under the trusted table — the fixture is wrong")
	}

	redirected := run([]string{"RISK_PATHS_CONFIG=" + attackerPath})
	if redirected["config_path"] != attackerPath {
		t.Errorf("$RISK_PATHS_CONFIG no longer outranks the config directories (config_path = %v).\n"+
			"If that is a deliberate fix, good — but it is a change to the authority channel: re-read the SECURITY NOTE on ResolveConfigDual (contract.go:458-463) and delete this recorded finding in the same commit.",
			redirected["config_path"])
	}
	if redirected["financial_paths_touched"] == true {
		t.Errorf("the redirected table still reports the wallet path as financial — the fixture no longer demonstrates the redirect")
	}
	// The point, stated so the finding is legible in the failure output rather
	// than only in this comment: an environment variable silenced the money
	// gate, and ResolveConfigDual never ran.
	if redirected["human_pr_gate"] == true && redirected["financial_paths_touched"] != true {
		// human_pr_gate is forced true by the scaffold flag here, which is the
		// only thing that stops this being a clean bypass. That is luck, not
		// design: a non-scaffold attacker table would not set it.
		t.Logf("note: human_pr_gate survived only because the redirected table is itself a scaffold; a hand-written attacker table would not set it")
	}
}
