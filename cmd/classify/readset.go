// Consumer read-set generation and the v1 differential (unit B1).
//
// SCAFFOLD — CONTRACTS ONLY. Bodies panic. The doc comments are the spec.
//
// Why this file is a generator and not a list:
//
// An earlier informal enumeration of what the frozen consumers read named
// skills, config_path and panel.reasons — none of which any consumer declares —
// and OMITTED recheck_min_severity, which cmd/iterate genuinely reads as its
// severity floor (iterate/main.go:271-272). A v1 emitter built from that list
// would have dropped the floor, iterate's floorFor would have returned its
// "high" fallback (iterate/main.go:274), and every MEDIUM finding would have
// been skipped silently on exactly the critical-system runs the floor exists
// for. The list was wrong in both directions at once, and nothing detected it.
//
// So the read-set is DERIVED FROM THE CONSUMERS' SOURCE, every time, and the
// derivation carries file:line for every field so no human ever hand-cites it
// again. Citations in prose go stale; this effort has already caught several.
//
// The citations in these comments were re-verified against the worktree at
// scaffold time, not copied from the design document.
package main

// ─── what "read" means ───────────────────────────────────────────────────────

// ReadKind distinguishes the two defensible definitions of a consumer's read
// set. The design says "generated from the consumers' ASTs" and does not say
// which. CHOICE: both are computed; DECLARED is normative for emitter totality
// and REFERENCED is advisory.
type ReadKind int

const (
	// ReadKindUnset is the zero value. Illegal. Raise.
	ReadKindUnset ReadKind = iota

	// ReadKindDeclared: the field appears as a JSON tag on the consumer's own
	// closed struct declaration for the classification payload.
	//
	// This is NORMATIVE for emitter totality, for two reasons. First, a
	// declared field is what survives the consumer's unmarshal-and-marshal-back
	// cycle, so it is what the consumer PRESERVES for whoever reads the file
	// next. Second, a declared-but-currently-unreferenced field is one
	// consumer edit away from being referenced, with no producer change to
	// notice it — whereas adding a field to the struct is a visible edit that
	// this generator will pick up on its next run.
	ReadKindDeclared

	// ReadKindReferenced: the field is additionally reached by a selector
	// expression in the consumer's code (state.Classification.X, cls.X).
	//
	// ADVISORY. It answers "which fields change behaviour today", which is
	// useful for triaging a differential diff, and it is NOT the emitter's
	// obligation. An emitter built to the referenced set only would be exactly
	// the hand-list failure with an AST in front of it.
	ReadKindReferenced
)

// ─── the generated set ───────────────────────────────────────────────────────

// SourceSite is a citation the generator produced, not one a human typed.
type SourceSite struct {
	// File is repo-relative, e.g. "cmd/iterate/main.go".
	File string
	Line int
	// What names the site: "declared" for the struct field declaration,
	// "referenced" for a selector expression.
	What string
}

// ReadField is one field of the classification payload that at least one frozen
// consumer declares.
type ReadField struct {
	// JSONPath is the dotted path within the `classification` object, using
	// the JSON tag names, with [] for a slice element:
	// "risk", "components", "changed_files[].path".
	//
	// Nested structs ARE descended: cmd/gates declares FileClass with only
	// `path` (gates/main.go:128-130), while the producer's FileClass carries
	// path, risk and rules (main.go:159-163). The generator must report
	// changed_files[].path and must NOT report changed_files[].risk, or the
	// emitter's obligation would be overstated.
	JSONPath string

	// Consumers names every consumer declaring this field, sorted, e.g.
	// ["gates", "iterate"]. The union across consumers is the emitter's
	// obligation; the per-consumer attribution is what makes a regression
	// report actionable.
	Consumers []string

	// Kind is ReadKindDeclared for every entry. Referenced is recorded in
	// Sites rather than as a separate entry, so a field appears exactly once.
	Kind ReadKind

	// Referenced is true iff at least one consumer reaches this field with a
	// selector expression. Advisory. False here is NOT permission to omit the
	// field from the v1 emission.
	Referenced bool

	// Sites carries every declaration and every reference, generated.
	Sites []SourceSite
}

// ReadSet is the generator's whole output.
type ReadSet struct {
	// Fields is sorted by JSONPath so the output is deterministic and can be
	// checked in as a golden that a consumer edit visibly breaks.
	Fields []ReadField
	// Consumers is the set that was actually parsed, sorted. A ReadSet whose
	// Consumers list is short is a ReadSet that was generated against an
	// incomplete world; see the hard-failure rule on GenerateReadSet.
	Consumers []string
}

// ConsumerSource names one frozen consumer to parse.
type ConsumerSource struct {
	// Name is the short name used in ReadField.Consumers, e.g. "gates".
	Name string
	// Dir is the directory holding the consumer's package, relative to the
	// classify package directory. classify is its own Go module and does not
	// and must not import these packages; the generator reads their SOURCE.
	Dir string
	// TypeName is the consumer's closed struct for the classification payload.
	// Both consumers call it "Classification" today, but the generator takes
	// it as data rather than assuming the name, because a rename in a frozen
	// consumer must produce a hard failure and not an empty read set.
	TypeName string
}

// frozenConsumers is the world the generator parses. It is the ONLY hand-
// written part of this mechanism, and it is deliberately hand-written: which
// programs are frozen consumers of the classification is a design fact, not a
// derivable one. What must never be hand-written is which FIELDS they read.
//
// Verified against the worktree at scaffold time:
//   - cmd/gates    declares Classification{risk, components, changed_files}
//     at gates/main.go:121-126, with FileClass{path} at :128-130.
//   - cmd/iterate  declares Classification{risk, components,
//     recheck_min_severity, reviewer_args} at iterate/main.go:86-91.
//
// Adding a consumer here is a design decision. Removing one is the removal of a
// constraint on the emitter and must be justified in the commit that does it.
var frozenConsumers = []ConsumerSource{
	{Name: "gates", Dir: "../gates", TypeName: "Classification"},
	{Name: "iterate", Dir: "../iterate", TypeName: "Classification"},
}

// GenerateReadSet parses each consumer's source and returns the union read set.
//
// HARD-FAILURE RULE, which is the whole safety property: if any consumer's
// directory is missing, fails to parse, or does not declare TypeName, this
// returns an error. It must NEVER return a partial ReadSet. A generator that
// degrades to a smaller set when it cannot see a consumer reproduces the
// hand-list bug with more machinery — the omission would be invisible and the
// emitter would drop a field that a consumer still reads.
//
// Likewise it must error on a declared field whose JSON tag is absent,
// malformed, or "-": an untagged Go field marshals under its Go name, which is
// a different wire key, and guessing between the two is exactly the class of
// silent mismatch this generator exists to prevent.
//
// It parses with go/parser at the file level. It does NOT type-check and does
// not need to: it never imports the consumer modules, which is what keeps
// cmd/classify independent of them.
//
// Determinism is contractual: two runs over identical sources produce identical
// ReadSets, including Sites order. The output is meant to be committed and
// diffed.
func GenerateReadSet(consumers []ConsumerSource) (ReadSet, error) {
	panic("B1: not implemented")
}

// EmitterCovers checks that a v1 emission covers the whole generated read set.
//
// It reports every JSONPath in rs that does not appear as a key in the emitted
// v1 payload, and it must treat an omitempty-elided key as MISSING when the
// value is the type's zero — because for the consumer, an absent key and a
// zero value are the same bytes, and the emitter's totality claim is about
// bytes.
//
// EXPECTED RESULT AT BASELINE, stated so a seal can pin it: the read set is
// exactly {risk, components, changed_files[].path, recheck_min_severity,
// reviewer_args}, and the v1 emission covers all five. If a future run produces
// a sixth, someone edited a frozen consumer and the emitter must be re-checked
// before that edit ships.
func EmitterCovers(rs ReadSet, emittedV1 []byte) (missing []string, err error) {
	panic("B1: not implemented")
}

// ─── the v1 differential ─────────────────────────────────────────────────────

// volatileV1Fields is the named exclusion set for the differential against the
// pinned old binary. Byte identity is impossible and was never the goal.
//
// classified_at is the only member: the old binary stamps the wall clock at
// main.go:274 into the field declared at main.go:141-143. It is a volatile
// derived field, it is the reason the v2 envelope drops it, and excluding it is
// what makes the comparison a test that can pass.
//
// This list is a wire-contract change if it grows. Adding a field here means
// declaring that the two binaries may legitimately disagree about it, which is
// a much stronger claim than "this test is flaky". Any addition needs the same
// review as a schema change.
var volatileV1Fields = []string{"classified_at"}

// DifferentialResult is one comparison of two v1 emissions.
type DifferentialResult struct {
	// Equivalent is true iff every compared path agrees.
	Equivalent bool
	// Divergences names each disagreeing JSON path with both values, in
	// sorted path order.
	Divergences []Divergence
	// Compared names every path that was compared, so a vacuous pass — a
	// comparison that happened to look at nothing — is visible in the result
	// rather than hidden behind Equivalent: true. A seal MUST assert that
	// Compared is non-empty and contains the read set; this field exists
	// because a seal that only asserts Equivalent is a vacuous seal.
	Compared []string
	// Excluded names every path skipped as volatile, so the exclusion is
	// visible in the output rather than implied by its absence.
	Excluded []string
}

// Divergence is one disagreement.
type Divergence struct {
	JSONPath string
	Old      string
	New      string
}

// SemanticEquivalentV1 compares the pinned old binary's v1 output against the
// new binary's v1 output over the GENERATED read set, excluding volatile.
//
// Contract:
//   - The comparison domain is exactly rs, minus volatile. It is not "all keys
//     present in both", which would let a field the new binary stopped emitting
//     pass unnoticed, and it is not "all keys in old", which would fail on
//     harmless additions.
//   - A path present in rs but absent from EITHER document is a divergence, not
//     a skip. Absence is a value.
//   - Comparison is semantic, not textual: JSON numbers compare numerically,
//     slices compare element-wise in order (the producer's ordering is already
//     deterministic — components via sortedKeys at main.go:737, risk_reasons in
//     fixed tier order at main.go:741), objects compare key-wise.
//   - It errors if rs is empty, if either document fails to parse, or if
//     volatile names a path not in rs. A differential over an empty domain
//     would report Equivalent: true having compared nothing.
//
// The two documents must be produced from IDENTICAL inputs: same config bytes,
// same diff bytes, same flags. Producing them from a live worktree makes
// resolveRepo's git state an uncontrolled input; the fixture must use -no-git.
func SemanticEquivalentV1(oldOut, newOut []byte, rs ReadSet, volatile []string) (DifferentialResult, error) {
	panic("B1: not implemented")
}

// ─── sidecar survival ────────────────────────────────────────────────────────

// SidecarSurvives asserts the property the separate-file decision rests on:
// after the frozen pipeline rewrites the shared run-state, the v2 sidecar is
// untouched and still parses.
//
// It runs classify → gates → iterate over one run-state and reports whether the
// sidecar's bytes are identical before and after. Identical, not merely
// parseable: the sidecar has exactly one writer and any change to it means
// something else opened it.
//
// BASELINE FINDING THE SEAL AUTHOR MUST KNOW — measured in this worktree, not
// reasoned from line numbers:
//
// The v1 projection does NOT survive the frozen pipeline today. cmd/gates
// unmarshals the run-state into a Classification declaring only three fields
// (gates/main.go:121-126) and marshals the whole state back
// (gates/main.go:1329). Running classify then gates over one run-state reduces
// the classification object from 15 keys to 2. recheck_min_severity,
// reviewer_args, human_pr_gate, panel, financial_paths_touched, config_path and
// skills are all destroyed by the first gate round.
//
// Two consequences that are NOT hypothetical:
//   - iterate's floorFor (iterate/main.go:270-275) then finds no
//     recheck_min_severity and returns "high", skipping MEDIUM findings — the
//     exact failure the read-set generator was built to prevent, arriving by a
//     different route.
//   - README's step 3 reads .classification.reviewer_args with jq AFTER gates
//     has run, and gets nothing.
//
// So the design's claim that a fixture "asserts both projections survive every
// rewrite" is false for the v1 projection at baseline. This function must
// therefore assert the SIDECAR's survival — which is real, because nothing else
// writes that file — and must report the v1 projection's loss as a measurement
// rather than assert it away. Do not "fix" this by widening the frozen
// consumers' structs: cmd/gates and cmd/iterate are frozen, and the differential
// is only meaningful while they stay untouched.
func SidecarSurvives(runState string) (survived bool, v1KeysLost []string, err error) {
	panic("B1: not implemented")
}
