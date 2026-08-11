// Run-state preservation for cmd/gates — unit G1.
//
// SCAFFOLD: CONTRACTS AND STUBS ONLY. The doc comments are the spec. Every
// function that performs the fix returns errNotImplemented; the three things
// implemented here are named, with their reasons, under "WHAT IS IMPLEMENTED"
// below.
//
// ─── THE DEFECT ──────────────────────────────────────────────────────────────
//
// mergeGates (main.go:1316-1335) round-trips the whole run-state through this
// package's closed structs: readRunState → json.Unmarshal into RunState
// (main.go:1248-1262) → json.MarshalIndent (main.go:1329) → os.WriteFile. Any
// JSON path those structs do not declare is silently dropped, and the run-state
// is a SHARED blackboard: cmd/classify writes the classification, cmd/gates and
// cmd/iterate read it, and the driver reads it after both.
//
// MEASURED IN THIS WORKTREE at scaffold time — not inferred from the structs,
// and not copied from B1's note. The pinned cmd/classify/classify was run on
// the wallet fixture, then the TRACKED cmd/gates/gates with `-only nosuchgate`,
// and the two documents compared PER JSON PATH:
//
//	classification: 15 top-level keys before, 3 after {risk, components, changed_files}
//	29 JSON paths destroyed, of which 2 are SUB-PATHS inside a surviving key:
//	    classification.changed_files[0].risk   = "critical"
//	    classification.changed_files[0].rules[0] = "wallet-service"
//	18 JSON paths added — every one of them under `gates` — and 1 changed, updated_at.
//
// Two consequences observed by running the frozen consumer rather than reading
// it: `iterate next` then prints "Floor: high" where classify wrote
// recheck_min_severity "medium", so every MEDIUM finding is skipped on a
// critical money path; and reviewer_args is gone, so the round-1 argv carries
// no -risk and no -component and the panel runs at the generic tier — the exact
// regression cmd/classify exists to prevent.
//
// A SECOND measurement, on a document seeded with forward-compatibility and
// zero-value probes, adds three failure modes the first one cannot show:
//
//	contract_version: 2               (unknown TOP-LEVEL key)      → DROPPED
//	classification.zzz_future_key     (unknown NESTED key)         → DROPPED
//	repo.dirty: false                 (declared, `omitempty`)      → DROPPED
//	round: 0                          (declared, `omitempty`)      → DROPPED
//	deferred_findings[0].line: 9007199254740993                    → 9007199254740992
//
// The last two rows are why this file exists in the shape it does, and each
// kills one of the two candidate designs on its own — see "THE RULING" below.
//
// ─── THE CONSTRAINT ──────────────────────────────────────────────────────────
//
// cmd/gates and cmd/iterate are FROZEN CONSUMERS. cmd/classify's differential
// and its read-set generator (classify/readset.go) exist to prove their
// behaviour did not change, and the generator parses THIS PACKAGE'S SOURCE with
// go/parser on every run. So G1 must preserve keys without changing either what
// gates decides or what gates DECLARES.
//
// Three tripwires in that generator bind every future edit to this file, and
// they are stated here because the failure they produce is a red row in a
// different module and nobody will connect it back:
//
//  1. classify/readset.go jsonWireKey rejects a field tagged json:"-" and a
//     field with no tag at all, with an ERROR — not a skip. So a passthrough
//     member hidden on Classification with json:"-" does not merely widen the
//     read set, it makes GenerateReadSet fail outright and takes every row that
//     calls it red. There is no "invisible" field on Classification.
//  2. classify/readset_seal_test.go:289 asserts the read set does NOT contain
//     changed_files[].risk, changed_files[].rules or changed_files. Declaring
//     either sub-field on FileClass turns that green row red.
//  3. classify/readset_seal_test.go wantReadSet pins the union at exactly five
//     JSON paths, and names classified_at, skills, config_path, panel and
//     panel.reasons as phantoms that must NOT appear. Declaring any of them on
//     Classification turns that row red too.
//
// THE RULE THIS IMPOSES ON EVERY LATER AUTHOR: nothing in cmd/gates may add,
// remove or retag a field on Classification or FileClass, or on any struct
// reachable from Classification, and no file in this package may declare a
// second type named Classification or FileClass (readset.go's packageStructs
// indexes the package by type name, and the last declaration wins). New types
// that are not reachable from Classification are invisible to the generator.
// That is the seam G1 builds on.
//
// ─── THE RULING: PASSTHROUGH, NOT DECLARATION ────────────────────────────────
//
// Two candidate shapes. Preserve-by-declaration — widen RunState,
// Classification and FileClass until every key is declared — is REJECTED, on
// four grounds, in descending order of how hard they are to argue with:
//
//	(a) IT DOES NOT COMPILE PAST THE SEALS. Tripwires 2 and 3 above are
//	    mechanical. Declaring changed_files[].risk turns readset_seal_test.go:289
//	    red; declaring recheck_min_severity or classified_at turns wantReadSet
//	    red. This is not a preference between two workable designs. One of them
//	    is already sealed shut.
//	(b) IT DOES NOT ACTUALLY PRESERVE. Measured above: repo.dirty=false and
//	    round=0 are DECLARED fields and are still destroyed, because `omitempty`
//	    erases the zero value. Declaration would therefore also require auditing
//	    every `omitempty` on both structs — a second hand-list, on the same
//	    fields, with the same drift.
//	(c) IT CANNOT PRESERVE AN UNKNOWN KEY AT ALL. contract_version and
//	    classification.zzz_future_key are unknowable to a struct by definition.
//	    A declaration-based fix is complete only against the producer that
//	    exists today.
//	(d) IT IS THE HAND-LIST BUG WEARING A STRUCT. classify/readset.go's opening
//	    comment records the precedent in this repo: an earlier informal
//	    enumeration of what the consumers read named three fields no consumer
//	    declares and OMITTED recheck_min_severity, the severity floor. Nothing
//	    detected it. A widened struct is that same list, with a compiler
//	    checking its syntax and nothing checking its completeness.
//
// IF SOMEONE NONETHELESS CHOOSES DECLARATION, this is what would have to stop
// the list drifting, and all three are required, not any one: the struct would
// have to be GENERATED from config/run-state.schema.json rather than written;
// the generator would have to fail closed when the schema names a key the
// struct lacks (the shape of classify/readset.go's hard-failure rule); and
// every `omitempty` would have to go, because `omitempty` re-introduces the
// loss inside a declared field. At that point the struct is a slower, larger
// passthrough that still cannot carry an unknown key, which is (c).
//
// PRESERVE-BY-PASSTHROUGH, adopted: the original bytes are kept, the bounded
// set of decided fields is merged INTO them, and everything else survives by
// construction rather than by enumeration. It is invisible to the read-set
// generator because it touches no type reachable from Classification. It
// preserves unknown keys, zero values and number literals without knowing what
// they are.
//
// ─── WHAT IS IMPLEMENTED, AND WHY ────────────────────────────────────────────
//
// The rule for this scaffold is stubs only, excepting anything whose absence
// would make the contract untestable. Three things qualify:
//
//   - Diverge and its walk. This is the MEASURING INSTRUMENT, and it is what
//     the fidelity property MEANS. Stubbed, "what exactly is preserved" stays
//     prose, and the seal author would have to write their own measurement — at
//     which point the property being sealed is theirs and not this contract's.
//     It is also the whole of the answer to sub-path loss: a top-level key
//     comparison cannot see changed_files[0].risk, and Diverge can.
//   - The Validate/PathPrefix dispatches on Fidelity, EditKind and Edit. These
//     ARE the "enumerate exhaustively and raise on the unknown" obligation
//     (skills/explicit-state.md). A stub that raises unconditionally is not a
//     weaker version of a dispatch that raises on the unknown; it is a
//     different function, and it would leave the obligation untested.
//   - JSONPath's rendering and prefix test, because Diverge cannot report
//     anything without them.
//
// Everything that performs the fix — LoadRunStateDocument, ApplyGateResults,
// VerifyPreservation — returns errNotImplemented.
//
// ─── WHAT THIS SCAFFOLD DELIBERATELY DOES NOT DO ─────────────────────────────
//
// CHOICE: mergeGates is NOT rewired to call ApplyGateResults. Rejected
// alternative: wire it now, so the seam is visible. Wiring a raising stub into
// the one function that writes the run-state takes all 63 green rows in this
// package red immediately, and a scaffold whose job is to move no row cannot
// start by moving 63. The body performs the wiring. The seal author can still
// write a RED end-to-end seal today — build cmd/gates from source to a scratch
// path, run it over a seeded run-state, and assert preservation; that seal is
// red until the body wires it, which is exactly the seal that is wanted.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// errNotImplemented is the scaffold's marker. Every stub wraps it with what it
// is obliged to do, so a body author reading a failure sees the contract and
// not just the word "unimplemented".
var errNotImplemented = errors.New("G1 scaffold: not implemented")

// ─── round-trip fidelity, as a named property ────────────────────────────────

// Fidelity names what "preserved" means. There are three defensible answers and
// they differ on real documents, so the contract picks one by name rather than
// leaving a reader to infer it from an implementation.
//
// Measured, on the documents this unit is about, so the difference is not
// hypothetical: key-set equality accepts a value corruption
// (deferred_findings[0].line 9007199254740993 → 9007199254740992 is a CHANGED
// path, not a lost one); pathwise equality accepts a key REORDER (the tracked
// gates already moves `pr` and `deferred_findings` relative to each other and
// inserts `gates` in the middle); and byte-identity accepts neither, but also
// cannot accept gates writing its own results, which is the whole point of the
// program.
type Fidelity int

const (
	// FidelityUnset is the zero value and is ILLEGAL. A caller that did not
	// choose a level has not stated what it is checking, and defaulting it to
	// any of the three would be the permissive-default this repo's
	// explicit-state rule exists to remove. Validate rejects it.
	FidelityUnset Fidelity = iota

	// FidelityByteIdentical: the produced document is byte-for-byte the
	// original outside the byte spans the allowed edits occupy.
	//
	// NOT NORMATIVE. What it would cost: encoding/json cannot express it.
	// json.Marshal over a map emits keys in sorted order and over a struct in
	// field-declaration order, so neither can reproduce an arbitrary producer's
	// key order; and inserting the `gates` member shifts every byte after it,
	// so the property only means anything once it is scoped to spans, which
	// requires tracking the offset of every member through the edit. That is an
	// order-preserving JSON document model — a new dependency or a hand-rolled
	// one — bought to protect object key order, which no consumer of this file
	// reads. It is the right property for a signed or digested artifact. The
	// run-state is neither.
	//
	// One sub-property of it IS free under the adopted design and should be
	// taken: a passthrough that holds untouched members as json.RawMessage
	// re-emits their original bytes exactly, so every untouched SUBTREE is
	// byte-identical and only the key order of the objects the editor descends
	// through is lost. See ApplyGateResults.
	FidelityByteIdentical

	// FidelityPathwise is NORMATIVE for this unit.
	//
	// For every JSON path present in the original and not covered by an allowed
	// edit, the produced document carries the same path with an IDENTICAL VALUE
	// LITERAL; and the produced document introduces no path not covered by an
	// allowed edit. Array element order and count are part of the property.
	// Object key ORDER is not.
	//
	// "Identical value literal", not "equal decoded value", and the difference
	// is load-bearing: json.Unmarshal into `any` decodes every number to
	// float64, so 9007199254740993 and 9007199254740992 decode to the same
	// float64 and a decoded-value comparison calls that corruption "preserved".
	// It is a measured corruption in this very pipeline, not a contrived one.
	// Diverge therefore compares json.Number literals.
	FidelityPathwise

	// FidelityKeySet: the set of JSON paths is unchanged; values are not
	// compared.
	//
	// NOT NORMATIVE, and named because it is the reading a passing test would
	// most easily drift into. What it would cost: everything. A body that
	// re-attached every lost key with a null, an empty string or a zero would
	// satisfy it completely. recheck_min_severity is the case that matters —
	// present-and-empty and present-and-"high" both clear key-set equality, and
	// both skip every MEDIUM finding exactly as absence does. It is free to
	// implement and it certifies nothing.
	FidelityKeySet
)

// Validate is the exhaustive dispatch. A Fidelity this build does not recognise
// — a newer producer's level, a value cast from an int — is an ERROR and never
// the permissive branch. See skills/explicit-state.md: naming the states does
// nothing if a fourth can arrive and be treated as the lenient one.
func (f Fidelity) Validate() error {
	switch f {
	case FidelityByteIdentical, FidelityPathwise, FidelityKeySet:
		return nil
	case FidelityUnset:
		return fmt.Errorf("fidelity level is unset: the caller did not say what it is checking, and there is no defensible default — FidelityPathwise is normative for cmd/gates but it must be named")
	default:
		return fmt.Errorf("fidelity level %d is not one this build recognises; refusing rather than treating it as the weakest level", int(f))
	}
}

func (f Fidelity) String() string {
	switch f {
	case FidelityUnset:
		return "unset"
	case FidelityByteIdentical:
		return "byte-identical"
	case FidelityPathwise:
		return "pathwise"
	case FidelityKeySet:
		return "key-set"
	default:
		// Not "unknown": a String that renders an unrecognised value as a
		// plausible word is how an unrecognised value reaches a log and stops
		// looking wrong. Validate is the gate; this makes the value visible.
		return "fidelity(" + strconv.Itoa(int(f)) + ")"
	}
}

// ─── what cmd/gates is allowed to change ─────────────────────────────────────
//
// The enumeration is EXHAUSTIVE and it is derived from the source, not from
// intent: the only assignments to the decoded run-state anywhere in this
// package are main.go:1322 (`state.Gates = map[string]Gate{}` when absent),
// main.go:1325 (`state.Gates[r.Key] = r.Outcome`) and main.go:1327
// (`state.UpdatedAt = ...`). Nothing else in cmd/gates mutates the document.
// The 18 added paths and 1 changed path in the measurement above are all
// accounted for by exactly these.
//
// Anything else is a VIOLATION and must raise. "Pass it through" is not
// available: a divergence this list does not name is either a bug in the editor
// or a change nobody declared, and both are the same thing to a reader of the
// run-state three nodes later.

// EditKind is the closed set of mutations cmd/gates may make.
type EditKind int

const (
	// EditKindUnset is the zero value and is ILLEGAL. An Edit{} that fell out
	// of a slice literal must not silently become "set nothing" or, worse,
	// "allow everything".
	EditKindUnset EditKind = iota

	// EditKindSetGateResult creates or replaces gates.<GateKey> with the whole
	// Gate value. It covers create and replace only: mergeGates never deletes a
	// gate, so a REMOVAL under `gates` is a violation and not a licensed edit.
	// Keeping that distinction is the point — a gate that vanished from the map
	// is indistinguishable at the reader from one that never ran, which is the
	// state main.go's own header says this program exists to prevent.
	EditKindSetGateResult

	// EditKindSetUpdatedAt sets the top-level updated_at.
	EditKindSetUpdatedAt
)

func (k EditKind) String() string {
	switch k {
	case EditKindUnset:
		return "unset"
	case EditKindSetGateResult:
		return "set-gate-result"
	case EditKindSetUpdatedAt:
		return "set-updated-at"
	default:
		return "editkind(" + strconv.Itoa(int(k)) + ")"
	}
}

// Edit is one licensed mutation.
//
// CHOICE: one struct with a Kind discriminator, rather than an interface with
// one implementation per kind. Rejected because an interface makes the set OPEN
// — a third package, or a later file here, can satisfy it and be dispatched
// without appearing in any switch, which is precisely the "a fourth state
// arrives" failure. A closed int with an exhaustive switch cannot be extended
// from outside, and the switch's default arm is where the extension is caught.
type Edit struct {
	Kind EditKind

	// GateKey is the run-state's `gates` member name, valid only for
	// EditKindSetGateResult. It is the result.Key computed in execute(), which
	// is "<gate>" for repo scope and "<gate>:<module-rel>" for module scope —
	// so it routinely contains ':' and '.' (the measurement shows "build:.").
	// That is why JSONPath is a segment list and not a dotted string; see
	// JSONPath's own comment.
	GateKey string

	// Result is the value written at gates.<GateKey>, valid only for
	// EditKindSetGateResult.
	Result Gate

	// UpdatedAt is the RFC3339 timestamp written at the top-level updated_at,
	// valid only for EditKindSetUpdatedAt.
	UpdatedAt string
}

// Validate rejects an Edit whose kind is unrecognised and an Edit whose payload
// does not match its kind.
//
// Cross-field validation is not ceremony here. An EditKindSetGateResult with an
// empty GateKey would write the member "" — a gate whose name is the empty
// string, which every reader would render as blank and nobody would notice; and
// an EditKindSetUpdatedAt with an empty UpdatedAt would erase the timestamp
// while claiming to set it. Both are "did nothing" wearing "succeeded".
func (e Edit) Validate() error {
	switch e.Kind {
	case EditKindSetGateResult:
		if strings.TrimSpace(e.GateKey) == "" {
			return fmt.Errorf("edit %s: empty GateKey — it would write the run-state member \"\", which renders as a blank gate name and reads as no gate at all", e.Kind)
		}
		if strings.TrimSpace(e.Result.Status) == "" {
			return fmt.Errorf("edit %s for gate %q: empty status — config/run-state.schema.json requires it, and an absent status is the silent-pass this program exists to prevent", e.Kind, e.GateKey)
		}
		return nil
	case EditKindSetUpdatedAt:
		if strings.TrimSpace(e.UpdatedAt) == "" {
			return fmt.Errorf("edit %s: empty timestamp — erasing updated_at while claiming to set it", e.Kind)
		}
		return nil
	case EditKindUnset:
		return errors.New("edit kind is unset: a zero-valued Edit reached the editor, which means something built an edit list it did not fill in")
	default:
		return fmt.Errorf("edit kind %s is not one this build licenses; refusing rather than passing the mutation through", e.Kind)
	}
}

// PathPrefix is the single JSON path an Edit is licensed to touch. Every
// divergence at or beneath it is expected; every divergence anywhere else is a
// violation.
//
// It returns an error rather than an empty path for an unrecognised kind: an
// empty JSONPath is the prefix of EVERY path, so a "safe" empty return would
// license the entire document. That is the exact shape of the fail-open this
// contract is built to remove, and it is worth naming because it is the
// obvious, wrong implementation.
func (e Edit) PathPrefix() (JSONPath, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	switch e.Kind {
	case EditKindSetGateResult:
		return JSONPath{{Key: "gates"}, {Key: e.GateKey}}, nil
	case EditKindSetUpdatedAt:
		return JSONPath{{Key: "updated_at"}}, nil
	default:
		// Unreachable while Validate stays exhaustive. Kept, and kept as an
		// error, because the two switches drifting apart is the realistic
		// failure and the safe answer to "I do not know which path" is not
		// "all of them".
		return nil, fmt.Errorf("edit kind %s has no licensed path; refusing to return an empty prefix, which would license the whole document", e.Kind)
	}
}

// AllowedPrefixes is the licensed path set for a whole edit list, plus the one
// path that is licensed unconditionally.
//
// `gates` ITSELF is licensed, because mergeGates creates the member when it is
// absent (main.go:1321-1323) and a created container is an ADDED path at
// `gates` that no per-gate prefix covers.
//
// Nothing else is unconditional. In particular schema_version is NOT licensed:
// gates validates it (main.go:1268) and must never rewrite it.
func AllowedPrefixes(edits []Edit) ([]JSONPath, error) {
	if len(edits) == 0 {
		// Not an error and not silent. An empty edit list is a real state —
		// gates ran no gate — and its licensed set is exactly {updated_at is
		// not licensed, gates is not created}. Returning nil here means a
		// subsequent VerifyPreservation demands a byte-for-byte pathwise
		// identity, which is the correct demand for a write that changed
		// nothing.
		return nil, nil
	}
	out := []JSONPath{{{Key: "gates"}}}
	for i, e := range edits {
		p, err := e.PathPrefix()
		if err != nil {
			return nil, fmt.Errorf("edit %d: %w", i, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// ─── JSON paths ──────────────────────────────────────────────────────────────

// PathSegment is one step into a JSON document: an object member or an array
// element.
//
// CHOICE: a segment LIST rather than a dotted string as the authoritative
// identity. Rejected the dotted string because the run-state's own keys break
// it: gate result keys are "<gate>:<module-rel>", so the real document
// contains gates."build:." and the rendering "gates.build:..status" can be read
// three different ways. A path grammar that is ambiguous on the document it is
// designed for is not a grammar. The dotted form survives only as String(), for
// humans, with escaping.
type PathSegment struct {
	// Key is the object member name. Meaningful when IsIndex is false.
	Key string
	// Index is the array position. Meaningful when IsIndex is true.
	Index int
	// IsIndex distinguishes the two. A JSON object may legally have a member
	// literally named "[0]", so the discriminator cannot be inferred from the
	// rendering — which is the second reason the string form is not the
	// identity.
	IsIndex bool
}

// JSONPath is a location in a JSON document, from the root.
type JSONPath []PathSegment

// String renders a path for a human. Object keys are escaped: '\' becomes
// '\\' and '.' becomes '\.', so the rendering is injective and two different
// paths can never print the same. Array elements render as [N].
func (p JSONPath) String() string {
	if len(p) == 0 {
		return "(root)"
	}
	var b strings.Builder
	for i, s := range p {
		if s.IsIndex {
			b.WriteString("[")
			b.WriteString(strconv.Itoa(s.Index))
			b.WriteString("]")
			continue
		}
		if i > 0 {
			b.WriteString(".")
		}
		b.WriteString(escapePathKey(s.Key))
	}
	return b.String()
}

func escapePathKey(k string) string {
	k = strings.ReplaceAll(k, `\`, `\\`)
	return strings.ReplaceAll(k, ".", `\.`)
}

// HasPrefix reports whether p is at or beneath q. Comparison is on segments, so
// a member named "build:." is one segment and cannot be confused with two.
func (p JSONPath) HasPrefix(q JSONPath) bool {
	if len(q) > len(p) {
		return false
	}
	for i := range q {
		if p[i] != q[i] {
			return false
		}
	}
	return true
}

// ─── measuring divergence ────────────────────────────────────────────────────

// DivergenceKind names how two documents differ at one path.
type DivergenceKind int

const (
	// DivergenceUnset is the zero value and is ILLEGAL: a Divergence that
	// reached a report without saying how it diverged is not evidence.
	DivergenceUnset DivergenceKind = iota
	// DivergenceRemoved: the path is in the original and not in the produced
	// document. This is the classification loss.
	DivergenceRemoved
	// DivergenceAdded: the path is in the produced document and not the
	// original.
	DivergenceAdded
	// DivergenceChanged: the path is in both and the value literals differ.
	DivergenceChanged
)

func (k DivergenceKind) String() string {
	switch k {
	case DivergenceUnset:
		return "unset"
	case DivergenceRemoved:
		return "removed"
	case DivergenceAdded:
		return "added"
	case DivergenceChanged:
		return "changed"
	default:
		return "divergencekind(" + strconv.Itoa(int(k)) + ")"
	}
}

// Divergence is one measured difference between two documents.
type Divergence struct {
	// At is the location. It is a segment list; use At.String() to print it.
	At JSONPath
	// Kind is how they differ. Never DivergenceUnset in a returned value.
	Kind DivergenceKind
	// Before and After are the JSON value LITERALS as they appear in each
	// document, and the empty string when the path is absent from that side.
	// A scalar's literal is its JSON encoding, so the string value "{}" is
	// `"{}"` and an empty object is `{}` — they are distinguishable.
	Before string
	After  string
}

func (d Divergence) String() string {
	switch d.Kind {
	case DivergenceRemoved:
		return fmt.Sprintf("%s: REMOVED (was %s)", d.At, d.Before)
	case DivergenceAdded:
		return fmt.Sprintf("%s: ADDED (%s)", d.At, d.After)
	case DivergenceChanged:
		return fmt.Sprintf("%s: CHANGED %s -> %s", d.At, d.Before, d.After)
	default:
		return fmt.Sprintf("%s: %s", d.At, d.Kind)
	}
}

// Diverge is the measuring instrument for FidelityPathwise. It reports every
// path at which produced differs from original, sorted by rendered path so two
// runs over the same pair produce the same report.
//
// IMPLEMENTED, not stubbed — see this file's header for why. It is also the
// answer to the sub-path question: it descends into arrays and objects and
// reports classification.changed_files[0].risk in its own right, which is
// something no comparison of the classification object's top-level keys can do.
//
// WHAT IT COMPARES. Both documents are decoded with UseNumber, so a number is
// carried as its source literal and never through float64. That is deliberate
// and it is the difference between catching and missing a measured corruption
// in this pipeline: 9007199254740993 and 9007199254740992 are the same float64.
//
// WHAT IT DOES NOT COMPARE. Object key order, and whitespace. Those are the two
// things FidelityPathwise gives up relative to byte-identity, and giving them
// up here is the same decision as choosing FidelityPathwise — stated twice on
// purpose, because a body author reads the function and not the constant.
//
// DUPLICATE MEMBER NAMES are not representable after decoding: encoding/json
// keeps the last. A document with duplicate keys therefore compares as though
// it had only the last one, on BOTH sides, so the comparison stays sound even
// though it is blind to the duplication. Stated rather than left to be
// discovered.
//
// STRING LITERALS ARE RE-ENCODED, so json.Marshal's HTML escaping applies and a
// value containing '<', '>' or '&' is REPORTED with that character escaped to
// its six-byte \\u form even when the source spelled it literally — the
// panel.reasons entries in this very run-state do. Both sides use one encoder,
// so the comparison is unaffected; only the rendering in a report differs from
// the file. Named because a reader who diffs a Divergence against the run-state
// by eye will otherwise think they have found a second bug.
//
// An input that is not valid JSON is an ERROR, on either side. It is not "no
// divergences": a produced document that does not parse is the most complete
// failure of preservation available, and reporting it as agreement would be the
// vacuous pass this whole unit is about.
func Diverge(original, produced []byte) ([]Divergence, error) {
	before, err := flattenDocument(original)
	if err != nil {
		return nil, fmt.Errorf("original document: %w", err)
	}
	after, err := flattenDocument(produced)
	if err != nil {
		return nil, fmt.Errorf("produced document: %w", err)
	}

	var out []Divergence
	for key, b := range before {
		a, ok := after[key]
		if !ok {
			out = append(out, Divergence{At: b.at, Kind: DivergenceRemoved, Before: b.literal})
			continue
		}
		if a.literal != b.literal {
			out = append(out, Divergence{At: b.at, Kind: DivergenceChanged, Before: b.literal, After: a.literal})
		}
	}
	for key, a := range after {
		if _, ok := before[key]; !ok {
			out = append(out, Divergence{At: a.at, Kind: DivergenceAdded, After: a.literal})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := out[i].At.String(), out[j].At.String()
		if li != lj {
			return li < lj
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

// flatLeaf is one terminal value and where it lives.
type flatLeaf struct {
	at      JSONPath
	literal string
}

// flattenDocument decodes a document and returns every LEAF keyed by its
// rendered path. The rendering is injective (see JSONPath.String), so the map
// key is a faithful identity and not a collision waiting to happen.
//
// An empty object and an empty array are LEAVES, with literals "{}" and "[]".
// If they were not, `"components": []` and an absent `components` would flatten
// identically and the loss of an explicitly-empty list would be invisible —
// and an explicitly-empty list is exactly the kind of value `omitempty`
// destroys, which is one of the two measured failures this unit exists for.
func flattenDocument(doc []byte) (map[string]flatLeaf, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	// A trailing token means the file is not one JSON document. Silently
	// ignoring it would let a truncated-and-reappended run-state compare equal
	// to its own prefix.
	if dec.More() {
		return nil, errors.New("decode: trailing content after the top-level value — this is not one JSON document")
	}
	out := map[string]flatLeaf{}
	if err := flattenValue(root, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// flattenValue walks one decoded value.
//
// The type switch is EXHAUSTIVE over what encoding/json produces with
// UseNumber — map, slice, json.Number, string, bool, nil — and its default arm
// RAISES. It does not skip, and it does not stringify with %v. A value this
// walk did not recognise is a value it did not compare, and a comparison that
// silently omits a subtree reports agreement it never observed.
func flattenValue(v any, at JSONPath, out map[string]flatLeaf) error {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			return addLeaf(out, at, "{}")
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := append(append(JSONPath{}, at...), PathSegment{Key: k})
			if err := flattenValue(t[k], child, out); err != nil {
				return err
			}
		}
		return nil
	case []any:
		if len(t) == 0 {
			return addLeaf(out, at, "[]")
		}
		for i, e := range t {
			child := append(append(JSONPath{}, at...), PathSegment{Index: i, IsIndex: true})
			if err := flattenValue(e, child, out); err != nil {
				return err
			}
		}
		return nil
	case json.Number:
		// The SOURCE literal, not a reformatted number. This is the whole
		// reason for UseNumber.
		return addLeaf(out, at, t.String())
	case string:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Errorf("%s: encode string: %w", at, err)
		}
		return addLeaf(out, at, string(b))
	case bool:
		if t {
			return addLeaf(out, at, "true")
		}
		return addLeaf(out, at, "false")
	case nil:
		// JSON null is a VALUE, not an absence. A run-state member set to null
		// and a member that is gone are different states and must report as
		// different divergences.
		return addLeaf(out, at, "null")
	default:
		return fmt.Errorf("%s: encoding/json produced a %T, which this walk does not recognise; refusing to skip it, because a subtree that was not compared is not a subtree that agreed", at, v)
	}
}

func addLeaf(out map[string]flatLeaf, at JSONPath, literal string) error {
	key := at.String()
	if _, clash := out[key]; clash {
		// Unreachable while String stays injective. Kept because the injectivity
		// is an argument, and an argument is worth a check that costs nothing.
		return fmt.Errorf("path %s was produced twice; the path rendering is not injective and every comparison built on it is unsound", key)
	}
	out[key] = flatLeaf{at: at, literal: literal}
	return nil
}

// ─── the fix ─────────────────────────────────────────────────────────────────

// LoadRunStateDocument reads the run-state ONCE and returns both the raw bytes
// and the decoded, VALIDATED view.
//
// One read, two views, is the contract. Today readRunState is called twice per
// invocation — once in prepare() at main.go:253 and again inside mergeGates at
// main.go:1317, minutes later, after every gate has run — so the document that
// is validated is not provably the document that is edited. Merging into bytes
// read at a third moment would add a third window. The body must edit THESE
// bytes.
//
// CHOICE: this returns the raw bytes rather than silently re-reading inside
// ApplyGateResults. Rejected alternative: give ApplyGateResults the path and let
// it read. It reads better and it re-opens the window this function exists to
// close, and it makes the function untestable without a filesystem.
//
// The validation is the EXISTING validateRunState. G1 does not tighten it: a
// document that gates accepts today must still be accepted, or G1 has changed
// what gates decides, which is the one thing it may not do.
//
// STUB.
func LoadRunStateDocument(path string) (raw []byte, state *RunState, err error) {
	return nil, nil, fmt.Errorf("LoadRunStateDocument(%q): must read the file once, return its bytes verbatim, and return the same bytes decoded and passed through validateRunState: %w", path, errNotImplemented)
}

// ApplyGateResults produces the new run-state document: the original, with
// exactly the licensed edits applied and everything else preserved at
// FidelityPathwise.
//
// HOW, stated because the shape is the fix and a body that reaches the same
// signature by re-marshalling a struct has not done it:
//
//   - Decode the original into map[string]json.RawMessage. Every member the
//     edits do not touch stays a RawMessage and is re-emitted as its ORIGINAL
//     BYTES. Unknown keys, zero values that `omitempty` would have erased, and
//     number literals all survive without this code knowing they exist. That is
//     the "by construction" in preserve-by-passthrough, and it is also what
//     buys the byte-identity of untouched subtrees noted on
//     FidelityByteIdentical.
//   - Decode ONLY `gates` one level further, to map[string]json.RawMessage, so
//     a gate result can be replaced without disturbing its siblings. Do not
//     decode `classification` at all. Nothing in this function needs it, and
//     anything that decodes it can lose it.
//   - Apply each edit through its Kind, exhaustively, raising on unknown.
//
// WHAT IT MUST NOT DO, each of these having been the obvious shortcut at some
// point in this repo:
//
//   - It must not fall back to the old json.MarshalIndent(state) path on any
//     error. A fallback makes G1 vacuous: the failure mode it exists to prevent
//     becomes the error handler. On failure it returns an error and NO bytes.
//   - It must not return the original unchanged when the edit list is empty and
//     call that success. It returns the original unchanged and that IS success,
//     but VerifyPreservation must still be able to tell the two apart, which is
//     why the edit list is an input to verification and not an assumption.
//   - It must not sort, renumber, reindent or normalise anything outside the
//     licensed paths. Reindenting is invisible to FidelityPathwise and is
//     therefore the change that would slip through; it is banned by the
//     byte-identity of untouched subtrees, which is a stronger property the
//     design already provides for free.
//
// CHOICE: the produced document keeps the existing two-space MarshalIndent
// formatting and trailing newline for the members this function does write
// (main.go:1329, :1335). Rejected alternative: compact output. The run-state is
// read by humans and diffed in commits; changing its formatting in the commit
// that fixes preservation would bury the fix in a whitespace diff.
//
// OPEN DECISION, RECORDED AND NOT TAKEN — what gates should do when this
// returns an error. finish() currently logs "WARNING: failed to write gates
// into run state" and still returns 0 (main.go:303-305), so a run whose results
// were never persisted reports PASS. That is the same shape as a skipped gate
// with no reason, and this file's own header quotes main.go saying so. But
// changing it changes gates' EXIT BEHAVIOUR, which is the thing B1's
// differential exists to hold still, so it is not the scaffold's to take. My
// recommendation for the record: it should fail, and the change should be its
// own unit with its own differential run.
//
// STUB.
func ApplyGateResults(original []byte, edits []Edit) ([]byte, error) {
	return nil, fmt.Errorf("ApplyGateResults over %d bytes and %d edit(s): must merge the licensed edits into the original document and preserve every other JSON path at %s: %w", len(original), len(edits), FidelityPathwise, errNotImplemented)
}

// VerifyPreservation checks a produced document against the original at the
// named level, and returns every violation.
//
// TWO RETURN CHANNELS, AND THEY ARE NOT THE SAME STATE. `err` means the check
// could not be performed — a document that will not parse, an unrecognised
// Fidelity, an edit list that will not validate. `violations` means the check
// ran and found these. A caller that writes `if err != nil { log }` and ignores
// violations has re-created the implicit state this unit is about, and a caller
// that treats a nil error as "preserved" is asserting something this function
// never said. That is exactly the `exit_code is None -> passed` shape recorded
// in skills/explicit-state.md.
//
// An empty violations slice with a nil error is the ONLY passing result.
//
// WHAT IT MUST NOT DO: it must not derive its licensed path set from the
// PRODUCED document, or from the divergences it found. The licensed set comes
// from the edit list the caller intended, so an edit that was never intended is
// caught. Deriving it from what happened would certify whatever happened.
//
// CHOICE: verification is a separate exported function rather than an assertion
// inside ApplyGateResults. Rejected the internal assertion because a body that
// checks its own output with its own model of the document proves only that the
// model is self-consistent — the same reason classify/readset.go derives the
// read set from the consumers' source instead of from its own emitter. A seal
// can call these two against each other; it cannot get inside one function.
//
// STUB.
func VerifyPreservation(original, produced []byte, edits []Edit, level Fidelity) (violations []Divergence, err error) {
	if lerr := level.Validate(); lerr != nil {
		return nil, lerr
	}
	return nil, fmt.Errorf("VerifyPreservation at %s over %d edit(s): must report every divergence outside AllowedPrefixes(edits) as a violation, and must distinguish \"could not check\" from \"nothing to report\": %w", level, len(edits), errNotImplemented)
}

// ─── findings and rulings this scaffold records ──────────────────────────────
//
// (1) THE CONTRADICTION B1 LEFT, and which reading is correct.
//
// classify/readset.go:908-912 says the loss is measured PER JSON PATH and names
// changed_files[].risk and .rules. Its own contract for SidecarSurvives says
// v1KeysLost carries "top-level keys of the `classification` object". And the
// proof-of-execution block added by adjudicate(B1) to
// TestSeal_SidecarSurvives_* derives its ground truth from classificationKeys,
// which returns top-level keys only, and then requires every key the body
// REPORTED as lost to appear in that top-level ground truth. So a body that
// correctly reported sub-path loss — "changed_files[].risk" in v1KeysLost —
// would find it absent from the top-level ground truth and turn that green row
// red.
//
// That row is on another branch and is not edited here. The reading:
//
//   - FOR v1KeysLost, TOP-LEVEL IS CORRECT. That list is not a loss inventory;
//     it is a PROOF-OF-EXECUTION INSTRUMENT. Its non-vacuity comes from being
//     cross-checkable against the run-state by the same function the test uses,
//     and adjudicate(B1) says so explicitly: the row previously passed against
//     a body that executed nothing, and the cross-check is what closed that.
//     Widening the list to per-path would break the cross-check and give back
//     the vacuity. B1 chose correctly and its comment says why: the per-path
//     loss is "recorded as a finding rather than smuggled into a list whose
//     every member must be checkable against the file".
//   - FOR THE DEFECT, PER-JSON-PATH IS CORRECT, and it is not close.
//     changed_files[0].risk and .rules are genuinely destroyed inside a
//     surviving key, and a fix judged only by top-level key-set equality could
//     be satisfied by re-attaching thirteen nulls. Two of the twenty-nine paths
//     this unit must restore are invisible to the top-level reading.
//
// So the two are not in contradiction once you notice they are instruments for
// different questions, and the resolution is that G1 DOES NOT REUSE
// v1KeysLost AS ITS ACCEPTANCE MEASURE. G1's measure is Diverge, in this
// package, per JSON path. Nothing in cmd/classify needs to change for that, and
// the seal author must not reach for SidecarSurvives to seal this unit.
//
// (2) UNKNOWN KEYS, AND WHAT G1 MEANS FOR THE v2 SIDECAR'S JUSTIFICATION.
//
// FidelityPathwise makes unknown-key survival a property of the design rather
// than of the key list: contract_version and classification.zzz_future_key
// survive ApplyGateResults because nothing decodes them.
//
// classify/contract.go:466-473 gives the sidecar's justification in full, and
// it is exactly one thing: "Both frozen writers unmarshal the run-state into
// closed structs and marshal them back — cmd/gates/main.go:1248 → :1329, and
// cmd/iterate/main.go:427 → :461 — so any key those structs do not declare is
// silently dropped by the first gate round." The v2 envelope is a separate file
// because it could not survive as a second key in the shared one.
//
// G1 HALVES THAT JUSTIFICATION AND DOES NOT RETIRE IT, and the reason is
// measured, not argued. cmd/iterate has the identical shape and G1 does not
// touch it. `iterate run` was executed over a run-state in this worktree and it
// destroyed classification.changed_files outright — iterate's Classification
// (iterate/main.go:86-91) declares risk, components, recheck_min_severity and
// reviewer_args and no changed_files. Running gates again on the result exits 3
// INVALID_INPUT, "no Go modules own any changed file": after one `iterate run`
// the pipeline cannot gate at all. iterate's Gate struct (iterate/main.go:93-96)
// declares only status and skip_reason, so the same write also destroys
// exit_code, command, ran_at, duration_ms, output_path and metrics for every
// gate cmd/gates just recorded.
//
// So: the sidecar's justification stands until cmd/iterate is fixed too, and
// the honest statement after G1 is "one of the two frozen writers preserves the
// document". A G2 against cmd/iterate would retire it, and the design here is
// deliberately reusable for that — but see the CHOICE below on sharing.
//
// CHOICE: this code is a FILE IN cmd/gates and not a shared package. Rejected
// alternative: a package both consumers import. cmd/gates and cmd/iterate are
// separate Go modules with no shared module between them, so sharing means
// creating one and adding a require+replace to two frozen consumers — a
// visible, structural edit to both, in a unit whose entire constraint is not to
// disturb them. Duplicating ~200 lines into cmd/iterate later is the cheaper
// and more honest cost, and the read-set generator will keep both honest about
// what they DECLARE, which is the part that matters.
//
// (3) THE v2 ENVELOPE HAS NO changed_files, AND G1 DOES NOT CHANGE THAT.
//
// classify/contract.go:606-612 and TestSeal_Finding_V2EnvelopeCannotFeedGates
// record it: ClassificationV2 carries no changed_files, cmd/gates declares it
// (main.go:124) and reads it (main.go:444-445), so cmd/gates cannot be migrated
// to the v2 sidecar without extending the envelope.
//
// G1 adds no field to ClassificationV2 and removes no field from this package's
// Classification, so that finding is untouched and its seal stays green. What
// G1 changes is the ARGUMENT AROUND it: the cut-over was one of the reasons to
// want the envelope extended, and after G1 the v1 run-state carries the whole
// classification losslessly through cmd/gates. A consumer that needs the full
// classification can read the run-state. That weakens the case for extending
// the v2 envelope rather than strengthening it, and it should be said out loud
// before someone extends §3.3 on the grounds that gates needs it.
//
// (4) REBUILDING cmd/gates/gates — A RECOMMENDATION, NOT A DECISION TAKEN HERE.
//
// The tracked binary cmd/gates/gates IS rebuilt and committed alongside source
// in this repo (bdecc7f), unlike cmd/classify/classify, which is pinned as the
// v1 differential baseline and must never be rebuilt in this line of work.
// adjudicate(B1-repair) verified the tracked gates binary is faithful to its
// source today.
//
// MY RECOMMENDATION: REBUILD, in the BODY commit, together with the source fix.
// Without the rebuild this unit ships a fix that is absent from production —
// roles/tasker.md:193, roles/coder.md:318 and README.md:39 all exec the
// committed binary by absolute path, and .github/workflows/gates.yml runs
// `go test` over the checked-out tree and never rebuilds, so CI cannot notice.
// That is precisely the shape adjudicate(B1-repair) named as the one way
// TestSeal_Recorded_V1ProjectionDoesNotSurviveGates could rot: green, accurate
// about the artifact, silently stale about the source.
//
// WHAT THE REBUILD COSTS, stated so nobody is surprised into skipping it. The
// rebuild is the event that fires two GREEN rows in cmd/classify, by design:
// TestSeal_Recorded_V1ProjectionDoesNotSurviveGates ("THIS IS GOOD NEWS AND
// THIS SEAL IS SUPPOSED TO CATCH IT") and TestSeal_SidecarSurvives_* via
// SidecarSurvives returning an empty v1KeysLost. Both rows name the follow-up
// in their own text: update recordedV1KeysLost and the row together, and say
// which unit did it. A body author is forbidden from editing seals, so this
// needs the operator to authorise the amendment or to route it to a seal
// author.
//
// THE FAILURE MODE TO REFUSE: leaving the binary stale so those two rows stay
// green. That trades a fixed production for a green suite, and it is the exact
// trade this repo has already been burned by twice.
//
// (5) OUT OF SCOPE, RECORDED SO IT IS NOT LOST. cmd/gates writes gate keys of
// the form "<gate>:<module-rel>" — "build:." in the measurement — but
// config/run-state.schema.json constrains `gates` with propertyNames.enum to
// the bare gate names. The tracked binary already writes documents its own
// schema rejects. G1 preserves that behaviour exactly, because changing it
// would change what gates writes. It is a real divergence between the schema
// and both implementations and it wants its own unit.
