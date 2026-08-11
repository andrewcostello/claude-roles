// Run-state preservation for cmd/iterate — unit G2, the sibling of G1.
//
// BODY LANDED. LoadRunStateDocument, ApplyRoundRecord and VerifyPreservation are
// implemented and appendRound (main.go) is wired to them; the section "WHAT THIS
// SCAFFOLD IMPLEMENTED" is kept as the record of which declarations the scaffold
// wrote and why, not as a statement about what is implemented today.
//
// TWO DECISIONS THE CONTRACT DID NOT MAKE AND THE BODY DID, both stated at the
// point of deviation rather than here: EditKindSetRound with RoundNumber 0 writes
// the literal `0` and does not delete `round` (see the CHOICE above
// ApplyRoundRecord), and a second EditKindAppendRound in one list is REFUSED (see
// list-level check 1). Neither is reachable from cmd/iterate today.
//
// STILL OPEN, and it is the whole of row 15: the TRACKED cmd/iterate/iterate is
// the pre-fix artifact. Finding (6) is the instruction and it is a separate
// commit.
//
// This file deliberately reuses cmd/gates/preserve.go's vocabulary — Fidelity,
// EditKind, Edit, JSONPath, Divergence, Diverge, flattenDocument, and the
// LoadRunStateDocument → Apply → VerifyPreservation triple. Where it DEVIATES
// it says so at the point of deviation and gives the source-derived reason.
// There are four such deviations and none of them is taste: LicensedPaths
// replaces AllowedPrefixes+splitLicence, the `rounds` container licence is
// conditional, Edit.Validate's table is deliberately non-uniform, and
// LoadRunStateDocument has no validator to call.
//
// ─── THE DEFECT ──────────────────────────────────────────────────────────────
//
// appendRound (main.go:441-467) reads the run-state, decodes it into this
// package's closed structs (:427-437 readRunState → RunState), mutates six
// fields, and marshals the WHOLE state back (:461-466). Every JSON path those
// structs do not declare is destroyed. The run-state is a shared blackboard:
// cmd/classify writes the classification, cmd/gates writes gate results, and
// cmd/iterate writes rounds — into the same file.
//
// MEASURED IN THIS WORKTREE at scaffold time. Not inherited from the brief, not
// inferred from the structs, and re-run rather than copied from G1's note. The
// pinned cmd/classify/classify produced the base document on the wallet
// fixture; the TRACKED cmd/gates/gates (the G1-rebuilt artifact) then ran a full
// gate pass over it; probes were seeded through json.RawMessage; and the
// TRACKED cmd/iterate/iterate was run with `run -ceiling 0`, which reaches
// appendRound through cmdRun's d.Stop branch (main.go:584). The two documents
// were compared PER JSON PATH:
//
//	REMOVED 60   ADDED 7   CHANGED 5
//
//	classification   15 top-level keys -> 4. ELEVEN destroyed. The four that
//	                 survive are exactly the four Classification declares
//	                 (:86-91): risk, components, recheck_min_severity,
//	                 reviewer_args. changed_files is destroyed outright,
//	                 including its sub-paths changed_files[0].risk = "critical"
//	                 and changed_files[0].rules[0] = "wallet-service".
//	gates            all 9 records survive BY NAME and 33 paths are destroyed
//	                 INSIDE them, because Gate here declares two fields (:93-96)
//	                 and cmd/gates' Gate declares eight (gates/main.go:131-140).
//	                 Gone: command, ran_at, duration_ms, output_path, exit_code
//	                 and metrics — for every gate cmd/gates has just written.
//	rounds[0]        THREE paths destroyed RETROACTIVELY by an append whose only
//	                 intended effect was to add rounds[1]: zzz_round_future,
//	                 evidence_id (an integer above 2^53), and
//	                 reviewers[0].zzz_reviewer_future. See ruling (3).
//	repo.dirty       destroyed (`omitempty` on a declared field, :82).
//	contract_version destroyed (unknown top-level key).
//	CHANGED          deferred_findings[0].line and pr.big, both
//	                 9007199254740993 -> 9007199254740992. PR is
//	                 map[string]any (:73) and DeferredFindings is []any (:74),
//	                 so a declared field still routes every number through
//	                 float64. Declaring a field does not save its values.
//
// THE PIPELINE CONSEQUENCE, observed by running the next tool rather than
// reasoning about it: cmd/gates on the resulting document exits
//
//	3 INVALID_INPUT — "no Go modules own any changed file — nothing this tool
//	                   can gate."
//
// After one `iterate run` THE PIPELINE CANNOT GATE AT ALL. That is strictly
// worse than the pre-G1 gates defect, which lost keys but left a gateable
// document, and it is worse in a second way: iterate destroys the results of
// the tool that ran immediately before it. See ruling (2).
//
// TWO FACTS THAT BOUND THE DEFECT, measured, and recorded because both are
// easy to assume the wrong way round:
//
//   - `iterate next` writes NOTHING. The run-state file is byte-identical
//     before and after (sha256 069db68d…). cmdNext (:548-568) has no call to
//     appendRound. So a pipeline that only ever invokes `iterate next` is not
//     affected by this defect at all — which is exactly the pipeline
//     cmd/classify's TestSeal_SidecarSurvives_* differential runs, and is why
//     that row can be honest today. See ruling (4).
//   - The collapse is a ONE-SHOT FIXED POINT, not a drift. A second
//     `iterate run` over the already-collapsed document removes nothing further
//     (REMOVED 0). There is nothing left to destroy. So the damage is done by
//     round 1 and does not compound per round — which matters, because it means
//     the severity does not depend on how long the loop ran.
//
// ─── THE CONSTRAINT ──────────────────────────────────────────────────────────
//
// cmd/iterate is a FROZEN CONSUMER. cmd/classify's read-set generator
// (classify/readset.go) parses THIS PACKAGE'S SOURCE with go/parser on every
// run, and cmd/classify's differential exists to prove this tool's behaviour did
// not change. So G2 must preserve keys without changing either what iterate
// DECIDES or what iterate DECLARES.
//
// The three tripwires cmd/gates/preserve.go enumerates bind this file too, and
// the first is the one that matters most here: classify/readset.go jsonWireKey
// rejects a field tagged json:"-" and a field with no tag AT ALL, with an ERROR
// rather than a skip. There is no invisible field on this package's
// Classification. Nothing in this file may add, remove or retag a field on
// Classification, or on any struct reachable from it, and no file in this
// package may declare a second type named Classification.
//
// The types this file DOES introduce — Fidelity, Edit, JSONPath, Divergence,
// Licence — are not reachable from Classification and are therefore invisible
// to the generator. That is the seam, and it is the same seam G1 built on.
//
// ─── THE RULINGS INHERITED FROM G1, NOT RELITIGATED ──────────────────────────
//
// PASSTHROUGH, NOT DECLARATION. Widening RunState, Classification, Gate and
// Round until every key is declared is rejected. G1 gives four grounds; three
// of them are re-confirmed by the measurement above and one is STRENGTHENED by
// it:
//
//	(a) it is sealed shut from three directions in classify/readset.go;
//	(b) it does not actually preserve — repo.dirty:false is a DECLARED field
//	    (:82) and is destroyed anyway, because `omitempty` erases zero values;
//	(c) it cannot carry an unknown key at all — contract_version and
//	    classification.zzz_future_key are unknowable to a struct by definition;
//	(d) it is the hand-list bug wearing a struct.
//
// AND (b) IS WORSE IN cmd/iterate THAN IT WAS IN cmd/gates, which is a G2
// finding and not an inherited one. `omitempty` is only one of two mechanisms
// here. PR is map[string]any and DeferredFindings is []any, so pr.big and
// deferred_findings[0].line are destroyed as VALUES while their paths survive —
// measured, both 9007199254740993 -> ...992. Declaring the field did not help,
// because the declaration bottoms out in `any`. A declaration-based fix would
// have to declare not just every key but every key's full TYPE, all the way
// down, for two members whose schema says a driver owns them and whose contents
// this package has no business knowing. That is not a wider struct; it is a
// second copy of the schema.
//
// FidelityPathwise IS NORMATIVE — same path, same VALUE LITERAL. The two
// corrupted integers above are the reason: a decoded-value comparison calls
// 9007199254740993 -> ...992 "preserved", because they are the same float64.
//
// BYTE-IDENTITY IS NOT FREE AND NOTHING MAY BE BUILT ON IT. G1 measured two
// independent mechanisms, both of which apply verbatim to any implementation
// written here: json.MarshalIndent re-indents the whole buffer, and
// json.Marshal COMPACTS every json.RawMessage with escapeHTML ON, so an
// untouched "a < b && c > d" is emitted with the six-byte \u escapes. The escape
// is a FIXED POINT — byte-identical on a second pass — so it cannot compound.
// FidelityPathwise is unaffected by both, because Diverge decodes and re-encodes
// through one encoder on both sides.
//
// WHAT A TOOL MAY CHANGE IS EXHAUSTIVE, DERIVED FROM SOURCE, AND RAISES ON
// ANYTHING ELSE. Deletion inside a licensed container is a violation, not a
// licensed edit. Ruling (3) below is what that rule turns into for an append,
// and it is the sharpest new thing in this file.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// errNotImplemented is the scaffold's marker. Every stub wraps it with what it
// is obliged to do, so a body author reading a failure sees the contract and not
// just the word "unimplemented".
//
// It is kept after the body lands, deliberately: seal rows assert that a refusal
// is NOT this marker, which is what stops "it errors on bad input" being
// satisfied by a function that never looked at its input.
var errNotImplemented = errors.New("G2 scaffold: not implemented")

// ═════════════════════════════════════════════════════════════════════════════
// BEGIN SHARED FIDELITY REGION
//
// Everything between this marker and END SHARED FIDELITY REGION is the
// measuring instrument, and the BODIES of its functions are textually identical
// to cmd/gates/preserve.go. The doc comments are not, because G1's cite
// cmd/gates by name.
//
// CHOICE: duplicated into this package rather than extracted into a shared Go
// module. This is the moment G1's deferred CHOICE gets paid, so it is
// re-examined here rather than inherited. UPHELD, and the reason is unchanged
// and structural: cmd/gates and cmd/iterate are separate Go modules with no
// shared module between them, so sharing means creating one and adding a
// require+replace to TWO frozen consumers — a visible edit to both, in a unit
// whose entire constraint is not to disturb either. The read-set generator
// keeps both honest about what they DECLARE, which is the part that can break a
// consumer.
//
// THE RISK THIS ACCEPTS, named rather than waved at: two copies of Diverge can
// drift, and if they drift they will report different divergences for the same
// pair of documents while both suites stay green. G1 did not have to price this
// because there was one copy.
//
// THE MITIGATION THAT IS AVAILABLE AND THAT I RECOMMEND TO THE SEAL AUTHOR, and
// it does not need a shared module: the modules are separate for COMPILATION,
// but both files are in one repo and one checkout, and cmd/gates' own seals
// already read across the module boundary (they exec ../classify/classify). So a
// row in EITHER package can open ../gates/preserve.go, extract the declarations
// named below by go/parser, and require their bodies to match this file's. That
// is a real drift detector and it costs one test.
//
// The declarations in this region: Fidelity and its constants, Validate, String;
// PathSegment, JSONPath, String, escapePathKey, HasPrefix; DivergenceKind and
// its String; Divergence and its String; Diverge; flatLeaf; flattenDocument;
// flattenValue; addLeaf.
//
// A THIRD duplication is where this CHOICE flips. Two copies with a drift
// detector is a trade; three copies is a shared module that someone has not
// written yet.
// ═════════════════════════════════════════════════════════════════════════════

// Fidelity names what "preserved" means. There are three defensible answers and
// they differ on real documents, so the contract picks one by name rather than
// leaving a reader to infer it from an implementation.
//
// Measured on the documents this unit is about: key-set equality accepts a value
// corruption (deferred_findings[0].line and pr.big are CHANGED paths, not lost
// ones); pathwise equality accepts a key reorder; and byte-identity accepts
// neither, but also cannot accept iterate appending a round, which is the whole
// point of the program.
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
	// NOT NORMATIVE, AND NOT ACHIEVABLE WITH encoding/json. G1 measured two
	// independent mechanisms that break it and both apply here: json.MarshalIndent
	// re-indents the whole buffer, and json.Marshal compacts every
	// json.RawMessage with escapeHTML on. Scoping the property to the edited
	// spans would need an order-preserving JSON document model this package does
	// not have. It is the right property for a signed or digested artifact; the
	// run-state is neither.
	//
	// DO NOT restate G1's since-withdrawn claim that byte-identity of untouched
	// subtrees is "free under the adopted design". It is not free and nothing in
	// this repo may be built on it — see G1's ruling (A).
	FidelityByteIdentical

	// FidelityPathwise is NORMATIVE for this unit.
	//
	// For every JSON path present in the original and not covered by a licensed
	// edit, the produced document carries the same path with an IDENTICAL VALUE
	// LITERAL; and the produced document introduces no path not covered by a
	// licensed edit. Array element order and count are part of the property.
	// Object key ORDER is not.
	//
	// "Identical value literal", not "equal decoded value". json.Unmarshal into
	// `any` decodes every number to float64, so 9007199254740993 and
	// 9007199254740992 decode to the same float64 and a decoded-value comparison
	// calls that corruption preserved. It is a corruption measured TWICE in this
	// package's own output — deferred_findings[0].line and pr.big — not a
	// contrived one. Diverge therefore compares json.Number literals.
	FidelityPathwise

	// FidelityKeySet: the set of JSON paths is unchanged; values are not
	// compared.
	//
	// NOT NORMATIVE, and named because it is the reading a passing test would
	// most easily drift into. It certifies nothing: a body that re-attached
	// every lost key with a null, an empty string or a zero satisfies it
	// completely. recheck_min_severity is the case that matters —
	// present-and-empty and present-and-"high" both clear key-set equality, and
	// both skip every MEDIUM finding exactly as absence does, because floorFor
	// (:270-275) falls back to "high" on an empty string.
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
		return fmt.Errorf("fidelity level is unset: the caller did not say what it is checking, and there is no defensible default — FidelityPathwise is normative for cmd/iterate but it must be named")
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

// ─── JSON paths ──────────────────────────────────────────────────────────────

// PathSegment is one step into a JSON document: an object member or an array
// element.
//
// CHOICE: a segment LIST rather than a dotted string as the authoritative
// identity. The dotted string is not a grammar on this document: cmd/gates
// writes gate keys of the form "<gate>:<module-rel>", so the real run-state
// contains gates."build:apps/finance-domain/wallet" — measured — and the
// rendering "gates.build:apps/finance-domain/wallet.status" can be read several
// ways. The dotted form survives only as String(), for humans, with escaping.
//
// The ARRAY case is why this matters more in G2 than it did in G1. iterate's one
// structural edit lands at rounds[N], and an index is not a key; conflating the
// two would make rounds[0] and a member literally named "[0]" the same path.
type PathSegment struct {
	// Key is the object member name. Meaningful when IsIndex is false.
	Key string
	// Index is the array position. Meaningful when IsIndex is true.
	Index int
	// IsIndex distinguishes the two. A JSON object may legally have a member
	// literally named "[0]", so the discriminator cannot be inferred from the
	// rendering — which is the second reason the string form is not the identity.
	IsIndex bool
}

// JSONPath is a location in a JSON document, from the root.
type JSONPath []PathSegment

// String renders a path for a human. Object keys are escaped: '\' becomes '\\'
// and '.' becomes '\.', so the rendering is injective and two different paths
// can never print the same. Array elements render as [N].
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
// a member named "build:apps/finance-domain/wallet" is one segment and cannot be
// confused with two, and rounds[1] is not beneath rounds[0].
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
	// document. This is the classification loss, and the gate-field loss.
	DivergenceRemoved
	// DivergenceAdded: the path is in the produced document and not the original.
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
	// document, and the empty string when the path is absent from that side. A
	// scalar's literal is its JSON encoding, so the string value "{}" is `"{}"`
	// and an empty object is `{}` — they are distinguishable.
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
// IMPLEMENTED, not stubbed. It IS the fidelity property: stubbed, "what exactly
// is preserved" stays prose, and a seal author would have to write their own
// measurement — at which point the property being sealed is theirs and not this
// contract's. It is also the only thing that can see the two losses this unit is
// most about, because neither is visible at the top level: the 33 destroyed
// paths INSIDE surviving gate records, and the 3 destroyed paths inside a
// surviving rounds[0].
//
// WHAT IT COMPARES. Both documents are decoded with UseNumber, so a number is
// carried as its source literal and never through float64. Measured necessity:
// this package corrupts two integers that way.
//
// WHAT IT DOES NOT COMPARE. Object key order, and whitespace. Those are the two
// things FidelityPathwise gives up relative to byte-identity, and giving them up
// here is the same decision as choosing FidelityPathwise — stated twice on
// purpose, because a body author reads the function and not the constant.
//
// ARRAY COMPARISON IS POSITIONAL, and this is the one behaviour a G2 reader must
// understand before reading a report. A round INSERTED or REMOVED in the middle
// of rounds[] does not report as one insertion; it reports as a CHANGED at every
// path of every shifted element, plus ADDED or REMOVED at the tail. The report
// is noisy and it is SOUND — every one of those divergences is real, because
// rounds[3] genuinely now holds what rounds[2] held. It is named here so a body
// author who shifts the array does not read a wall of CHANGED as a Diverge bug.
//
// DUPLICATE MEMBER NAMES are not representable after decoding: encoding/json
// keeps the last. A document with duplicate keys therefore compares as though it
// had only the last one, on BOTH sides, so the comparison stays sound even
// though it is blind to the duplication.
//
// STRING LITERALS ARE RE-ENCODED, so json.Marshal's HTML escaping applies and a
// value containing '<', '>' or '&' is REPORTED with that character escaped even
// when the source spelled it literally. Both sides use one encoder, so the
// comparison is unaffected; only the rendering in a report differs from the file.
// This is live in G2's inputs, not hypothetical: cmd/gates writes coverage-floor
// text like "<pkg> at 10.0% < floor 95%" into gates[key].metrics.violations
// (gates/main.go:1016), and that is a member cmd/iterate destroys today.
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
// If they were not, `"rounds": []` and an absent `rounds` would flatten
// identically and the loss of an explicitly-empty list would be invisible. That
// case is not decorative in G2: it is exactly the transition the `rounds`
// container licence exists for — see LicensedPaths.
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
// The type switch is EXHAUSTIVE over what encoding/json produces with UseNumber
// — map, slice, json.Number, string, bool, nil — and its default arm RAISES. It
// does not skip, and it does not stringify with %v. A value this walk did not
// recognise is a value it did not compare, and a comparison that silently omits
// a subtree reports agreement it never observed.
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

// ═════════════════════════════════════════════════════════════════════════════
// END SHARED FIDELITY REGION
// ═════════════════════════════════════════════════════════════════════════════

// ─── what cmd/iterate is licensed to change ──────────────────────────────────
//
// THE ENUMERATION IS EXHAUSTIVE AND IT IS DERIVED FROM THE SOURCE, NOT FROM
// INTENT. Every assignment to the decoded run-state anywhere in cmd/iterate,
// found by reading every `state.<Field> =` in main.go, all of them inside
// appendRound:
//
//	main.go:446  state.Rounds = append(state.Rounds, r)
//	main.go:447  state.Round = len(state.Rounds)
//	main.go:448  state.Verdict = strings.ToLower(verdict)
//	main.go:449  state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
//	main.go:452  state.Status = "in_progress"      (APPROVE)
//	main.go:454  state.Status = "in_progress"      (ITERATE)
//	main.go:456  state.Status = "escalated"        (default)
//	main.go:458  state.EscalationReason = escalation   (default arm, guarded)
//
// There is nothing else. cmdNext, decide, buildArgv, execTool and
// recordAndReport only READ. `state.Repo.Worktree` at :618 and :673 is read into
// exec.Cmd.Dir. So six edit kinds, and one of them conditional.
//
// THE COMMENT ABOVE appendRound IS ALREADY WRONG, AND THAT IS THE ARGUMENT FOR
// DERIVING RATHER THAN ASKING. main.go:439-440 says "preserving every field
// other nodes own. iterate owns rounds[], round, verdict, status and nothing
// else." It omits updated_at and escalation_reason — two of the six. It is a
// hand-list, written by the author of the code it describes, and it drifted by a
// third. classify/readset.go records the identical failure at larger scale: an
// informal enumeration of what the consumers read named three fields no consumer
// declares and OMITTED recheck_min_severity, the severity floor. Nobody detected
// either. This enumeration is only sound while it is still what iterate DOES,
// and that is the property to hold when this list is next touched.
//
// EVERYTHING NOT IN THE LIST IS A VIOLATION AND MUST RAISE. Named explicitly,
// because each one is currently destroyed and a reader should be able to check
// the list against the measurement: schema_version, task_key, created_at, repo
// (including repo.dirty), classification (all fifteen keys), gates (every
// record and every field inside every record), pr, deferred_findings,
// contract_version and any other unknown key — and rounds[0..N-1], which is
// ruling (3) and is the one a prefix licence would silently forgive.
//
// "Pass it through" is not available for a divergence this list does not name: it
// is either a bug in the editor or a change nobody declared, and both are the
// same thing to a reader of the run-state three nodes later.

// EditKind is the closed set of mutations cmd/iterate may make.
type EditKind int

const (
	// EditKindUnset is the zero value and is ILLEGAL. An Edit{} that fell out of
	// a slice literal must not silently become "set nothing" or, worse, "allow
	// everything".
	EditKindUnset EditKind = iota

	// EditKindAppendRound appends ONE element to `rounds`, at AtIndex, which
	// must equal the length of `rounds` in the ORIGINAL document (main.go:446).
	//
	// It licenses rounds[AtIndex] AND NOTHING ELSE IN THE ARRAY. It is not a
	// licence over `rounds`. See ruling (3): the measurement shows an append
	// destroying three paths inside rounds[0], and a subtree licence over
	// `rounds` would forgive exactly that.
	//
	// It covers APPEND only. A replacement of an existing element, a deletion,
	// an insertion in the middle and a reorder are all violations and not
	// licensed edits, because appendRound does none of them.
	EditKindAppendRound

	// EditKindSetRound sets the top-level `round` (main.go:447).
	EditKindSetRound

	// EditKindSetVerdict sets the top-level `verdict` (main.go:448).
	//
	// ITS PAYLOAD MAY BE EMPTY, and that is not a defect in this contract — see
	// Edit.Validate, which is where the source derivation for it lives, and
	// finding (8), which is the defect it exposes in iterate.
	EditKindSetVerdict

	// EditKindSetUpdatedAt sets the top-level `updated_at` (main.go:449).
	EditKindSetUpdatedAt

	// EditKindSetStatus sets the top-level `status` (main.go:452/:454/:456).
	EditKindSetStatus

	// EditKindSetEscalationReason sets the top-level `escalation_reason`
	// (main.go:458).
	//
	// CONDITIONAL: appendRound emits it only in the default (escalating) arm of
	// its switch AND only when the reason is non-empty. So an edit list that does
	// not contain it is the normal case, not an omission, and `escalation_reason`
	// is then NOT licensed — a divergence at it is a violation like any other.
	EditKindSetEscalationReason
)

func (k EditKind) String() string {
	switch k {
	case EditKindUnset:
		return "unset"
	case EditKindAppendRound:
		return "append-round"
	case EditKindSetRound:
		return "set-round"
	case EditKindSetVerdict:
		return "set-verdict"
	case EditKindSetUpdatedAt:
		return "set-updated-at"
	case EditKindSetStatus:
		return "set-status"
	case EditKindSetEscalationReason:
		return "set-escalation-reason"
	default:
		return "editkind(" + strconv.Itoa(int(k)) + ")"
	}
}

// Edit is one licensed mutation.
//
// CHOICE: one struct with a Kind discriminator, rather than an interface with
// one implementation per kind. Inherited from G1 and re-affirmed: an interface
// makes the set OPEN — a third package, or a later file here, can satisfy it and
// be dispatched without appearing in any switch, which is precisely the "a
// fourth state arrives" failure. A closed int with an exhaustive switch cannot
// be extended from outside, and the switch's default arm is where the extension
// is caught.
type Edit struct {
	Kind EditKind

	// Record is the round appended by EditKindAppendRound. It is this package's
	// own Round type, because appendRound's caller has already built one and
	// re-describing it here would be a second copy of a struct in the same
	// package.
	Record Round

	// AtIndex is the array position EditKindAppendRound claims to be appending
	// at: the length of `rounds` in the original document.
	//
	// WHY THE CALLER STATES IT RATHER THAN THE EDITOR COMPUTING IT — this is the
	// single most important design decision in G2 and it is the answer to "an
	// append raises questions a set does not".
	//
	// `append` cannot fail. An editor that computed the index itself could never
	// mismatch and therefore could never DETECT anything. But iterate's own
	// d.Round is computed from a read taken in load() (main.go:523) and the
	// append happens in appendRound's re-read (main.go:442) — with execTool, a
	// whole review panel, in between. AtIndex is the only place that stale claim
	// can be compared against the document actually being edited.
	//
	// It is DARK TODAY and it must be rated by what it gates, not by what calls
	// it. Wired faithfully — AtIndex taken from the bytes appendRound itself just
	// loaded — the mismatch is unreachable, because iterate is single-writer and
	// the documented pipeline (roles/tasker.md) is sequential. It gates the day a
	// second writer exists, and the failure it catches then is two rounds
	// silently sharing a number in the record every later escalation decision is
	// computed from. See the OPEN DECISION on ApplyRoundRecord.
	AtIndex int

	// RoundNumber is the value written at the top-level `round` by
	// EditKindSetRound.
	RoundNumber int

	// Verdict is the value written at `verdict` by EditKindSetVerdict. May be
	// empty; see Edit.Validate.
	Verdict string

	// UpdatedAt is the RFC3339 timestamp written at `updated_at` by
	// EditKindSetUpdatedAt.
	UpdatedAt string

	// Status is the value written at `status` by EditKindSetStatus.
	Status string

	// EscalationReason is the value written at `escalation_reason` by
	// EditKindSetEscalationReason.
	EscalationReason string
}

// Validate rejects an Edit whose kind is unrecognised and an Edit whose payload
// does not match its kind.
//
// THE TABLE IS DELIBERATELY NON-UNIFORM AND EVERY ASYMMETRY HAS A SOURCE
// CITATION. This is a deviation from G1, which rejected an empty Result.Status
// on the grounds that an empty required field is "did nothing wearing
// succeeded". The rule that generates the table here is narrower and it is the
// frozen-consumer constraint: G2 MUST NOT REFUSE A VALUE cmd/iterate CAN
// PRODUCE, because refusing one changes what iterate decides. So a payload is
// rejected only where the source proves iterate cannot emit it.
//
//	AppendRound             AtIndex >= 0 ONLY. Nothing about Record is checked.
//	                        Record.Verdict can be "" — recordRecheck copies
//	                        rr.Verdict straight from cmd/recheck's -out payload
//	                        (:392) and a payload without a verdict yields "".
//	                        Record.Status can be "". Rejecting either would make
//	                        iterate fail to record a round it records today, which
//	                        is a behaviour change wearing a safety check. This is
//	                        exactly where G1's rule does NOT transfer and copying
//	                        it would have been wrong.
//	SetRound                RoundNumber >= 0. Not >= 1: that would couple this
//	                        edit to the presence of an append, and list-level
//	                        coupling belongs where the whole list is visible.
//	SetVerdict              NOTHING. The empty string is producible (above) and
//	                        `Verdict` is `json:"verdict,omitempty"` (:70), so an
//	                        empty payload DELETES the member. That deletion is
//	                        licensed at `verdict` — it is what iterate does today
//	                        — and it is recorded as finding (8), not fixed here.
//	SetUpdatedAt            non-empty. time.Now().Format never returns "" (:449),
//	                        so an empty payload cannot come from iterate, and
//	                        erasing updated_at while claiming to set it is the
//	                        "did nothing wearing succeeded" shape G1 named.
//	SetStatus               non-empty. The switch at :450-460 is TOTAL over three
//	                        arms and every arm assigns a non-empty literal, so an
//	                        empty status is not producible.
//	SetEscalationReason     non-empty. main.go:457 guards it — `if escalation !=
//	                        ""` — so the source itself proves empty is not
//	                        producible, and an empty payload would erase a reason
//	                        while claiming to set one.
func (e Edit) Validate() error {
	switch e.Kind {
	case EditKindAppendRound:
		if e.AtIndex < 0 {
			return fmt.Errorf("edit %s: AtIndex %d is negative — there is no such array position, and a negative index would render as a path no document can contain", e.Kind, e.AtIndex)
		}
		return nil
	case EditKindSetRound:
		if e.RoundNumber < 0 {
			return fmt.Errorf("edit %s: RoundNumber %d is negative — config/run-state.schema.json types `round` as an integer with minimum 0", e.Kind, e.RoundNumber)
		}
		return nil
	case EditKindSetVerdict:
		// No check, on purpose. See the table above: the empty string is
		// producible and means "delete the member" under `omitempty`.
		return nil
	case EditKindSetUpdatedAt:
		if strings.TrimSpace(e.UpdatedAt) == "" {
			return fmt.Errorf("edit %s: empty timestamp — erasing updated_at while claiming to set it, and main.go:449 cannot produce one", e.Kind)
		}
		return nil
	case EditKindSetStatus:
		if strings.TrimSpace(e.Status) == "" {
			return fmt.Errorf("edit %s: empty status — main.go:450-460 is a total switch whose every arm assigns a non-empty literal, so this did not come from iterate", e.Kind)
		}
		return nil
	case EditKindSetEscalationReason:
		if strings.TrimSpace(e.EscalationReason) == "" {
			return fmt.Errorf("edit %s: empty reason — main.go:457 guards this assignment with `if escalation != \"\"`, so an empty one is not producible, and writing it would erase a reason while claiming to set one", e.Kind)
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
// contract is built to remove, and it is worth naming because it is the obvious,
// wrong implementation.
//
// THE APPEND'S PREFIX IS rounds[AtIndex], A TWO-SEGMENT PATH ENDING IN AN INDEX.
// That is what keeps G1's signature — no document argument — while still
// licensing a position: the position is carried on the Edit, and whether the
// Edit's claim about the document is TRUE is checked once, in ApplyRoundRecord,
// where the document is in hand.
func (e Edit) PathPrefix() (JSONPath, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	switch e.Kind {
	case EditKindAppendRound:
		return JSONPath{{Key: "rounds"}, {Index: e.AtIndex, IsIndex: true}}, nil
	case EditKindSetRound:
		return JSONPath{{Key: "round"}}, nil
	case EditKindSetVerdict:
		return JSONPath{{Key: "verdict"}}, nil
	case EditKindSetUpdatedAt:
		return JSONPath{{Key: "updated_at"}}, nil
	case EditKindSetStatus:
		return JSONPath{{Key: "status"}}, nil
	case EditKindSetEscalationReason:
		return JSONPath{{Key: "escalation_reason"}}, nil
	default:
		// Unreachable while Validate stays exhaustive. Kept, and kept as an
		// error, because the two switches drifting apart is the realistic failure
		// and the safe answer to "I do not know which path" is not "all of them".
		return nil, fmt.Errorf("edit kind %s has no licensed path; refusing to return an empty prefix, which would license the whole document", e.Kind)
	}
}

// Licence is the licensed path set for a whole edit list, with the two KINDS of
// licence kept apart by the TYPE rather than by position in a slice.
//
// CHOICE: this replaces G1's AllowedPrefixes([]JSONPath) + splitLicence pair.
// Rejected G1's flat slice, deliberately, and the reason is in G1's own file:
// splitLicence exists because "a flat prefix list cannot express" the
// distinction, it knows the container is element 0, and it has to re-check that
// coupling at runtime because "an empty JSONPath or a differently-shaped first
// element would otherwise silently make the container licence into a
// whole-document licence". That is a hazard defended against; here it is a
// hazard removed. The two fields cannot be confused for one another and there is
// no positional contract to drift.
//
// It matters more in G2 than it did in G1 because the container/subtree
// distinction is now load-bearing over an ARRAY WITH SIBLINGS, and the sibling
// destruction is measured rather than hypothetical: three paths inside rounds[0]
// destroyed by an append that only intended to add rounds[1].
type Licence struct {
	// Containers are licensed AT EXACTLY THEMSELVES and nowhere beneath. A
	// container licence says "this member may be CREATED, or may stop being a
	// leaf" — nothing about its contents.
	Containers []JSONPath

	// Subtrees are licensed at themselves and at every path beneath them.
	Subtrees []JSONPath
}

// Allows reports whether a divergence at `at` is licensed.
//
// IMPLEMENTED, not stubbed, and it is the second exception to stubs-only after
// Diverge. This function IS ruling (3). Stubbed, the container/subtree
// distinction would be prose, a seal author sealing "rounds[0] is not licensed"
// would have to implement their own version of it, and the single most
// consequential decision in this contract would be measured by the wrong author.
//
// The container test is an EXACT path match and deliberately not a prefix test.
// `rounds` stops being a leaf the moment the first round is appended to an empty
// `[]`, which is a REMOVED divergence AT `rounds` and is licensed; a removal
// UNDER rounds[0] is a violation. Only the per-edit prefixes license a subtree.
func (l Licence) Allows(at JSONPath) bool {
	for _, c := range l.Containers {
		if len(at) == len(c) && at.HasPrefix(c) {
			return true
		}
	}
	for _, s := range l.Subtrees {
		if at.HasPrefix(s) {
			return true
		}
	}
	return false
}

// LicensedPaths builds the Licence for an edit list.
//
// `rounds` is licensed AS A CONTAINER, and CONDITIONALLY — only when the list
// actually contains an EditKindAppendRound.
//
// CHOICE: conditional, where G1 licensed `gates` unconditionally on any
// non-empty edit list. G1 could get away with the looser rule because mergeGates
// always wrote gates; the tighter rule is free here, is derived from the same
// place (main.go:446 is the only thing that creates `rounds`), and means an edit
// list that sets only the verdict cannot quietly license the creation of an
// array. The general principle: the licence should be the smallest set the
// source justifies, and "the caller always passes an append anyway" is a reason
// the check never fires, not a reason to drop it.
//
// WHY `rounds` NEEDS A CONTAINER LICENCE AT ALL. If `rounds` is absent, creating
// it produces no divergence at `rounds` itself — an absent key contributes no
// leaf — and only ADDED paths under rounds[0], covered by the subtree. But if
// `rounds` is present and EMPTY it is a LEAF with the literal `[]`, and the
// first append REMOVES that leaf. Without the container licence that removal is
// a violation and a correct body is reported as broken.
//
// NOTHING ELSE IS UNCONDITIONAL, and two absences are worth naming because a
// later author will be tempted by both. `schema_version` is NOT licensed:
// nothing in cmd/iterate writes it and a rewrite of it would be a violation.
// `classification` is NOT licensed even though load() (:528-531) refuses a
// document without one: reading a member is not a licence to write it, and the
// eleven destroyed classification keys are the entire reason this unit exists.
//
// An empty edit list is not an error and not silent: it licenses nothing, so a
// subsequent VerifyPreservation demands full pathwise identity, which is the
// correct demand for a write that changed nothing. iterate itself cannot produce
// one — appendRound always emits at least the five unconditional edits — so a
// seal exercising this path is exercising the checker, not the tool, and should
// say so.
func LicensedPaths(edits []Edit) (Licence, error) {
	var l Licence
	for i, e := range edits {
		p, err := e.PathPrefix()
		if err != nil {
			return Licence{}, fmt.Errorf("edit %d: %w", i, err)
		}
		if e.Kind == EditKindAppendRound && len(l.Containers) == 0 {
			l.Containers = append(l.Containers, JSONPath{{Key: "rounds"}})
		}
		l.Subtrees = append(l.Subtrees, p)
	}
	return l, nil
}

// ─── the fix ─────────────────────────────────────────────────────────────────

// LoadRunStateDocument reads the run-state ONCE and returns both the raw bytes
// and the decoded view.
//
// One read, two views, is the contract. The body must edit THESE bytes.
//
// CHOICE: it returns the raw bytes rather than silently re-reading inside
// ApplyRoundRecord. Rejected alternative: give ApplyRoundRecord the path and let
// it read. It reads better, it re-opens the window this function exists to
// close, and it makes the function untestable without a filesystem.
//
// IT DOES NOT VALIDATE, AND THAT IS A DELIBERATE DIVERGENCE FROM G1. G1's
// LoadRunStateDocument called the EXISTING validateRunState in cmd/gates.
// cmd/iterate HAS NO VALIDATOR: readRunState (:427-437) does os.ReadFile then
// json.Unmarshal and nothing else, and the only precondition anywhere is
// load()'s `state.Classification == nil` check at :528, which lives in load()
// and stays there. Adding validation here would make iterate refuse documents it
// accepts today — a behaviour change, and precisely the one cmd/classify's
// differential exists to catch. If a later unit wants iterate to validate, that
// is its own unit with its own exit-code contract.
//
// CHOICE: this does NOT delegate to readRunState, and readRunState is NOT
// rewritten to delegate to this. Inherited from G1's ruling (B) and it holds
// here for the same reason: the seal for this function uses readRunState as its
// INDEPENDENT oracle — a document readRunState accepts must still be accepted,
// and one it rejects must still be rejected — and a function that is its own
// oracle certifies nothing. G1's ORDERING CONDITION transfers with it:
// readRunState has two callers here (:442 inside appendRound, :523 inside
// load()). If a later unit moves BOTH to LoadRunStateDocument, readRunState
// becomes dead code, someone deletes it as tidy-up, and the oracle silently
// vacates while the row keeps passing. Re-base that row on a hand-built
// expectation BEFORE readRunState loses its last caller.
//
// IMPLEMENTED.
func LoadRunStateDocument(path string) (raw []byte, state *RunState, err error) {
	// #nosec G304 -- path is the -run-state flag, exactly as in readRunState.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var s RunState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, nil, err
	}
	// AND NOTHING ELSE. No validator is called and none is invented: readRunState
	// (main.go:427-437) does os.ReadFile then json.Unmarshal and stops, load()'s
	// `state.Classification == nil` check at :528 stays in load(), and a document
	// iterate accepts today must still be accepted. A refusal added here would be
	// a behaviour change wearing a hardening.
	//
	// `data` is returned VERBATIM — not re-marshalled from `s`, which is the round
	// trip this whole unit exists to remove.
	return data, &s, nil
}

// ApplyRoundRecord produces the new run-state document: the original, with
// exactly the licensed edits applied and everything else preserved at
// FidelityPathwise.
//
// The name is for the tool's contribution — iterate records a round — but the
// EDIT LIST is authoritative and the function must apply exactly it. It is the
// G2 analogue of G1's ApplyGateResults.
//
// HOW, stated because the shape IS the fix and a body that reaches the same
// signature by re-marshalling a struct has not done it:
//
//   - Decode the original into map[string]json.RawMessage. Every member the
//     edits do not touch stays a RawMessage and is re-emitted as its ORIGINAL
//     BYTES. Unknown keys, zero values that `omitempty` would have erased, and
//     number literals all survive without this code knowing they exist. That
//     single decision is what preserves the eleven classification keys, the 33
//     gate-record paths and both corrupted integers, and it does so without this
//     file knowing what a gate or a classification is.
//   - Decode ONLY `rounds` one level further, to []json.RawMessage, so an
//     element can be appended without disturbing its siblings. EACH EXISTING
//     ELEMENT STAYS A RawMessage AND IS RE-EMITTED VERBATIM. That is ruling (3)
//     made mechanical: rounds[0].zzz_round_future survives because nothing
//     decodes rounds[0].
//   - Do not decode `classification`, `gates`, `pr` or `deferred_findings` at
//     all. Nothing in this function needs them, and anything that decodes them
//     can lose them.
//   - Apply each edit through its Kind, exhaustively, raising on unknown.
//
// TWO LIST-LEVEL CHECKS THE PER-EDIT Validate CANNOT MAKE, because neither is a
// property of one Edit. Both are refusals, and both are DARK TODAY — see
// Edit.AtIndex on how to rate them:
//
//  1. AtIndex MUST EQUAL len(rounds) IN THE ORIGINAL. A mismatch means the
//     caller's claim about the document is false, which is the stale-decision
//     hazard. Refuse. Do NOT silently append at the real length: adjusting a
//     caller's false claim into a true one is how a stale decision becomes an
//     invisible one.
//  2. IF the list contains both an append and an EditKindSetRound, then
//     RoundNumber MUST EQUAL AtIndex+1, because main.go:446-447 are adjacent and
//     mechanically linked: state.Round = len(state.Rounds) AFTER the append.
//     A `round` that disagrees with len(rounds) is undetectable at every later
//     reader.
//
// AND ONE THIS FUNCTION MUST NOT MAKE, which is the sharper half of the same
// point: Edit.Record.Round is NOT checked against AtIndex+1. It is tempting —
// they should agree, and when they disagree it is the staleness bug showing
// itself. But Record.Round is d.Round, computed by decide() from load()'s read,
// and refusing it would make iterate fail to record a round it records today.
// That is a behaviour change. Record the disagreement if you like; do not refuse
// it. The place to fix it is the unit that closes the staleness window.
//
// WHAT IT MUST NOT DO, each of these having been the obvious shortcut at some
// point in this repo:
//
//   - It must not fall back to the old json.MarshalIndent(state) path on any
//     error. A fallback makes G2 vacuous: the failure mode it exists to prevent
//     becomes the error handler. On failure it returns an error and NO bytes.
//   - It must not sort, renumber, reindent or normalise anything outside the
//     licensed paths.
//   - It must not touch rounds[0..AtIndex-1]. Not to renumber a `round` field,
//     not to backfill a missing `status`, not for anything. See ruling (3).
//
// CHOICE: the produced document keeps the existing two-space MarshalIndent
// formatting and trailing newline (main.go:461, :466). Rejected alternative:
// compact output. The run-state is read by humans and diffed in commits;
// changing its formatting in the commit that fixes preservation would bury the
// fix in a whitespace diff.
//
// OPEN DECISION, RECORDED AND NOT TAKEN — what iterate should do when this
// returns an error. recordAndReport currently logs "WARNING: failed to record
// round %d" (main.go:639) and then prints the round and returns the VERDICT exit
// code, so a run whose round was never persisted still exits 0 APPROVE. cmdRun's
// stop branch does the same at :588. That is the same shape as G1's finish(),
// and it is worse here, because the next `iterate run` will decide from a
// rounds[] that is missing a round and can therefore re-run a round or miss a
// convergence escalation. My recommendation for the record: it should fail, and
// the change should be its own unit with its own differential run, because it
// changes iterate's exit behaviour.
//
// CHOICE TAKEN BY THE BODY, BECAUSE THE CONTRACT DOES NOT MAKE IT —
// EditKindSetRound WITH RoundNumber 0 WRITES THE LITERAL `0`; IT DOES NOT DELETE
// `round`.
//
// The dispute is real and the contract leaves it open. Edit.Validate permits
// RoundNumber == 0 (it rejects only a negative, citing the schema's minimum 0),
// and RunState.Round is `json:"round,omitempty"` (main.go:69) — so a body that
// reproduced iterate's marshal faithfully would DELETE the member on a zero. The
// contract reasons about `omitempty` in exactly one place, EditKindSetVerdict,
// and it reasons about it there to LICENSE a deletion. It says nothing about
// `round`. Four grounds for writing the zero:
//
//	(a) THE CONTRACT'S OWN ASYMMETRY IS DELIBERATE AND CITED. Edit.Validate's
//	    table says "every asymmetry has a source citation", and the deletion at
//	    `verdict` has one: recordRecheck can emit "" (main.go:392), so the
//	    deletion is what iterate DOES and finding (8) records it. There is no
//	    such citation at `round`, because there is no such behaviour to preserve.
//	(b) IT IS UNREACHABLE FROM iterate, SO NEITHER CHOICE CHANGES WHAT iterate
//	    DECIDES. main.go:447 is `state.Round = len(state.Rounds)` AFTER an append,
//	    so the value is >= 1 always; and this file's own list-level check requires
//	    RoundNumber == AtIndex+1, which is >= 1 for AtIndex >= 0. The
//	    frozen-consumer constraint — the rule that generates Validate's table — is
//	    therefore silent here, and the choice is about what a SECOND caller gets.
//	(c) EXPLICIT STATE. `round: 0` and an absent `round` are different states to
//	    every later reader, and Diverge is built to report them as different
//	    divergences (a member set to a value and a member that is gone). A set
//	    edit that silently becomes a delete at one payload value is the implicit
//	    state at a decision boundary that skills/explicit-state.md exists to
//	    remove. A caller that wants `round` gone should have to say so — and there
//	    is no EditKind that says so, which is itself the answer.
//	(d) PRESERVATION. The document may already carry `round: 5`. Turning a SET
//	    into a DELETE destroys a path the schema declares, inside the one unit
//	    whose subject is not destroying paths.
//
// A deletion here would need its own EditKind, its own source citation and its
// own row. It has none of the three, so it is not this body's to invent.
//
// IMPLEMENTED.
func ApplyRoundRecord(original []byte, edits []Edit) ([]byte, error) {
	// THE LICENCE FIRST, before a single byte is touched. An edit list that does
	// not validate is not a document problem, and refusing before the merge means
	// a rejected list can never leave a half-edited document behind — which is
	// also how "returns NO bytes on failure" is made structural rather than
	// remembered at each return.
	for i, e := range edits {
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("edit %d: %w", i, err)
		}
	}

	// THE TOP LEVEL AS RAW MEMBERS. Every member this function does not name stays
	// a json.RawMessage and is re-emitted as the bytes it arrived as.
	// `classification`, `gates`, `pr`, `deferred_findings` and `repo` are four of
	// those members and nothing below decodes any of them: anything that decodes
	// them can lose them, and two of them lose their VALUES rather than their
	// paths because their declarations bottom out in `any`.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(original, &top); err != nil {
		return nil, fmt.Errorf("original document is not a JSON object: %w", err)
	}

	if len(edits) == 0 {
		// Licensed set empty, so nothing may move. Returning the original bytes IS
		// the success here; re-emitting them would be a change nobody licensed.
		// iterate cannot produce this list — appendRound always emits at least five
		// edits — so a caller arriving here is exercising the editor.
		out := make([]byte, len(original))
		copy(out, original)
		return out, nil
	}

	// `rounds` ONE LEVEL FURTHER, AND NO FURTHER, so an element can be appended
	// without disturbing its siblings. EACH EXISTING ELEMENT STAYS A RawMessage
	// AND IS RE-EMITTED VERBATIM — that is ruling (3b) made mechanical:
	// rounds[0].zzz_round_future survives because nothing decodes rounds[0].
	var rounds []json.RawMessage
	if raw, ok := top["rounds"]; ok {
		if err := json.Unmarshal(raw, &rounds); err != nil {
			return nil, fmt.Errorf("run-state member `rounds` is not a JSON array: %w", err)
		}
	}
	originalLen := len(rounds)

	// LIST-LEVEL CHECK 1 — AtIndex MUST EQUAL len(rounds) IN THE ORIGINAL. The
	// length is taken BEFORE any edit is applied, which is what "in the original"
	// means and is why the loop below runs first and separately.
	appendAt := -1
	for i, e := range edits {
		if e.Kind != EditKindAppendRound {
			continue
		}
		if appendAt >= 0 {
			// EditKindAppendRound appends ONE element. Two of them cannot both be
			// at len(rounds) in the original, so the second one's claim about the
			// document is false by construction — and appendRound emits one.
			// Refusing beats picking an interpretation nobody wrote down.
			return nil, fmt.Errorf("edit %d: a second %s in one list — an append licenses exactly one new index and both of these claim to be at len(rounds); appendRound (main.go:446) appends once per call and this list did not come from it", i, e.Kind)
		}
		if e.AtIndex != originalLen {
			return nil, fmt.Errorf("edit %d: %s claims AtIndex %d and `rounds` has %d element(s) in the document being edited — the caller's claim about the document is false, which is the stale-decision hazard (iterate's index comes from load()'s read at main.go:523 and the append happens against appendRound's re-read at :442, with a whole review panel in between). Refusing rather than appending at the real length: adjusting a caller's false claim into a true one is how a stale decision becomes an invisible one", i, e.Kind, e.AtIndex, originalLen)
		}
		appendAt = e.AtIndex
	}

	// LIST-LEVEL CHECK 2 — `round` MUST EQUAL AtIndex+1 when the list carries both,
	// because main.go:446-447 are adjacent and mechanically linked:
	// state.Round = len(state.Rounds) AFTER the append. A `round` that disagrees
	// with len(rounds) is undetectable at every later reader.
	//
	// AND THE ONE THIS FUNCTION MUST NOT MAKE: Edit.Record.Round is NOT checked
	// against AtIndex+1. It is d.Round, computed by decide() from load()'s earlier
	// read, and refusing it would make iterate fail to record a round it records
	// today. The place to fix that is the unit that closes the staleness window.
	if appendAt >= 0 {
		for i, e := range edits {
			if e.Kind == EditKindSetRound && e.RoundNumber != appendAt+1 {
				return nil, fmt.Errorf("edit %d: %s writes round %d beside an append at index %d, and main.go:446-447 make them mechanically linked — round must be %d", i, e.Kind, e.RoundNumber, appendAt, appendAt+1)
			}
		}
	}

	setMember := func(i int, key string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("edit %d: encode %s: %w", i, key, err)
		}
		top[key] = b
		return nil
	}

	roundsTouched := false
	for i, e := range edits {
		switch e.Kind {
		case EditKindAppendRound:
			// The ONE element this function encodes. Everything already in `rounds`
			// stays the bytes it arrived as.
			b, err := json.Marshal(e.Record)
			if err != nil {
				return nil, fmt.Errorf("edit %d: encode round %d: %w", i, e.Record.Round, err)
			}
			rounds = append(rounds, json.RawMessage(b))
			roundsTouched = true
		case EditKindSetRound:
			// The literal, including 0 — see the CHOICE above.
			if err := setMember(i, "round", e.RoundNumber); err != nil {
				return nil, err
			}
		case EditKindSetVerdict:
			if e.Verdict == "" {
				// NOT a set of the empty string. RunState.Verdict is
				// `json:"verdict,omitempty"` (main.go:70), so what iterate does
				// today with an undetermined verdict is DELETE the member —
				// preserve.go finding (8). The deletion is licensed at `verdict`
				// because it is what iterate does; it is recorded, not fixed.
				delete(top, "verdict")
				continue
			}
			if err := setMember(i, "verdict", e.Verdict); err != nil {
				return nil, err
			}
		case EditKindSetUpdatedAt:
			if err := setMember(i, "updated_at", e.UpdatedAt); err != nil {
				return nil, err
			}
		case EditKindSetStatus:
			if err := setMember(i, "status", e.Status); err != nil {
				return nil, err
			}
		case EditKindSetEscalationReason:
			if err := setMember(i, "escalation_reason", e.EscalationReason); err != nil {
				return nil, err
			}
		default:
			// Unreachable while Edit.Validate above stays exhaustive. Kept, and kept
			// as a refusal that NAMES the kind, because the two switches drifting
			// apart is the realistic failure and "pass the mutation through" is not
			// an available answer to it.
			return nil, fmt.Errorf("edit %d: kind %s reached the editor's dispatch without being licensed by Edit.Validate; the two switches have drifted and refusing is the only safe answer", i, e.Kind)
		}
	}

	if roundsTouched {
		// Only re-encoded when an edit actually appended. An untouched `rounds`
		// keeps its original bytes like any other member. Marshalling
		// []json.RawMessage re-emits each element's own bytes; it does not decode
		// them, so no number literal inside an existing round is routed through
		// float64 and no `omitempty` erases a zero this function never saw.
		b, err := json.Marshal(rounds)
		if err != nil {
			return nil, fmt.Errorf("encode `rounds`: %w", err)
		}
		top["rounds"] = b
	}

	// Two-space indent and a trailing newline, matching main.go:461 and :466 — see
	// the CHOICE above. Note what this does NOT do: it never re-marshals a decoded
	// value, so a number reaches the output as the literal it arrived as and
	// 9007199254740993 does not become 9007199254740992.
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode run state: %w", err)
	}
	return append(out, '\n'), nil
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
// THE LEVEL DISPATCH IS EXHAUSTIVE AND FidelityByteIdentical IS REFUSED IN THE
// ERROR CHANNEL. This build cannot perform that check — encoding/json cannot
// express byte-identity and this package has no order-preserving document model
// — and returning an empty violation list would report "checked and clean" for a
// check that never ran.
//
// WHAT IT MUST NOT DO: it must not derive its licensed path set from the
// PRODUCED document, or from the divergences it found. The licence comes from
// the edit list the caller intended, so an edit that was never intended is
// caught. Deriving it from what happened would certify whatever happened. For an
// APPEND this is not a fine point: deriving the licensed index from the produced
// document's rounds[] length licenses whatever index the body actually wrote,
// which is the one thing the append most needs checked.
//
// FOUR THINGS THE LICENCE ALONE ALREADY CATCHES, listed so nobody adds a
// separate check for them and gets a second, drifting rule:
//
//	retroactive loss   rounds[0].zzz_round_future REMOVED is not under
//	                   rounds[AtIndex] and is not a container match -> violation.
//	prepend / shift    every path of every shifted element CHANGES, and none of
//	                   them is at rounds[AtIndex] -> violations.
//	double append      rounds[AtIndex+1].* is ADDED and unlicensed -> violation.
//	truncation         old rounds[k] paths compare against whatever now sits at
//	                   index k -> CHANGED, unlicensed -> violation.
//
// CHOICE: verification is a separate exported function rather than an assertion
// inside ApplyRoundRecord. A body that checks its own output with its own model
// of the document proves only that the model is self-consistent — the same
// reason classify/readset.go derives the read set from the consumers' source
// instead of from its own emitter. A seal can call these two against each other;
// it cannot get inside one function.
//
// IMPLEMENTED.
func VerifyPreservation(original, produced []byte, edits []Edit, level Fidelity) (violations []Divergence, err error) {
	if lerr := level.Validate(); lerr != nil {
		return nil, lerr
	}

	// THE LEVEL DISPATCH, exhaustive, before any work. Validate above has already
	// refused the unset and the unrecognised; what is left is the three named
	// levels, and they are not all checkable here.
	countChanged := true
	switch level {
	case FidelityPathwise:
		// Normative for cmd/iterate. Every divergence counts: removed, added AND
		// changed — the two number corruptions this unit measures are CHANGED, and
		// a level that skipped them would call them preserved.
	case FidelityKeySet:
		// The path set only. Explicitly NOT normative — a body that re-attached
		// every lost key with a null satisfies it — and implemented only so that a
		// caller who names it gets what the constant says rather than a silently
		// stricter answer.
		countChanged = false
	case FidelityByteIdentical:
		// REFUSED, and refused in the ERROR channel, which is the honest place for
		// it: this build cannot perform the check. encoding/json cannot express
		// byte-identity — MarshalIndent re-indents the whole buffer and Marshal
		// compacts every RawMessage with escapeHTML on — and scoping it to the
		// edited spans needs an order-preserving document model this package does
		// not have. Returning an empty violation list here would report "checked
		// and clean" for a check that never ran.
		return nil, fmt.Errorf("cannot check at %s: this build has no order-preserving JSON document model, and encoding/json cannot express byte-identity — %s is the normative level for cmd/iterate", level, FidelityPathwise)
	default:
		// Unreachable while Fidelity.Validate stays exhaustive.
		return nil, fmt.Errorf("cannot check at %s: the level dispatch and Fidelity.Validate have drifted apart", level)
	}

	// THE LICENSED SET COMES FROM THE EDIT LIST THE CALLER INTENDED, never from the
	// produced document and never from the divergences found. For an APPEND that is
	// not a fine point: deriving the licensed index from the produced document's
	// rounds[] length licenses whatever index the body actually wrote, which is the
	// one thing the append most needs checked.
	lic, lperr := LicensedPaths(edits)
	if lperr != nil {
		return nil, fmt.Errorf("cannot check: the licensed path set does not build from the edit list: %w", lperr)
	}

	ds, derr := Diverge(original, produced)
	if derr != nil {
		// "Could not check", not "nothing to report". A document that does not
		// parse is the most complete failure of preservation available.
		return nil, fmt.Errorf("cannot check: %w", derr)
	}

	// An empty slice, not nil, so the only passing result is one a caller can read
	// as "the check ran and found nothing" rather than as "nothing happened".
	violations = []Divergence{}
	for _, d := range ds {
		if !countChanged && d.Kind == DivergenceChanged {
			continue
		}
		if lic.Allows(d.At) {
			continue
		}
		violations = append(violations, d)
	}
	return violations, nil
}

// ─── WHAT THIS SCAFFOLD IMPLEMENTED, AND WHY ─────────────────────────────────
//
// The rule was stubs only, excepting anything whose absence would make the
// contract untestable. Four things qualified, and the first three are G1's
// precedent applied unchanged:
//
//   - Diverge and its walk. This is the MEASURING INSTRUMENT and it is what the
//     fidelity property MEANS. Stubbed, a seal author writes their own, and the
//     property being sealed is theirs and not this contract's. It is also the
//     only thing that can see the two losses G2 is most about, both of which are
//     invisible at the top level: 33 destroyed paths inside surviving gate
//     records, and 3 inside a surviving rounds[0].
//   - The Validate/PathPrefix dispatches on Fidelity, EditKind and Edit. These
//     ARE the "enumerate exhaustively and raise on the unknown" obligation
//     (skills/explicit-state.md). A stub that raises unconditionally is not a
//     weaker version of a dispatch that raises on the unknown; it is a different
//     function, and it would leave the obligation untested.
//   - JSONPath's rendering and prefix test, because Diverge cannot report
//     anything without them.
//   - NEW IN G2: Licence.Allows and LicensedPaths. The container-versus-subtree
//     distinction is ruling (3), it is the whole difference between a correct
//     append and one that silently destroys its siblings, and it is measured
//     rather than argued. A seal author asked to seal "rounds[0] is not
//     licensed" against a stubbed Allows would have to write the distinction
//     themselves, which is the same failure as a stubbed Diverge one level up.
//
// The three stubs are exactly G1's three, renamed for this tool:
// LoadRunStateDocument, ApplyRoundRecord, VerifyPreservation.
//
// ─── WIRING: NOT DONE BY THE SCAFFOLD, DELIBERATELY — DONE BY THE BODY ───────
//
// The scaffold did NOT wire appendRound (main.go) to ApplyRoundRecord. Wiring a
// raising stub into the one function that writes the run-state would have taken
// green rows in this package red at the scaffold commit, and a scaffold whose
// job is to move no row cannot start by moving any. cmd/iterate was 27 green / 0
// red at that commit and 27 green / 0 red after it.
//
// THE BODY PERFORMED THE WIRING, and the edit list below is what it built —
// verbatim, with the assignments each edit replaced:
//
//	Edit{Kind: EditKindAppendRound,   Record: r, AtIndex: len(state.Rounds)}   :446
//	Edit{Kind: EditKindSetRound,      RoundNumber: len(state.Rounds) + 1}      :447
//	Edit{Kind: EditKindSetVerdict,    Verdict: strings.ToLower(verdict)}       :448
//	Edit{Kind: EditKindSetUpdatedAt,  UpdatedAt: time.Now()...}                :449
//	Edit{Kind: EditKindSetStatus,     Status: <the switch's chosen literal>}   :452/:454/:456
//	Edit{Kind: EditKindSetEscalationReason, ...}  ONLY in the default arm and
//	                                             ONLY when escalation != ""    :457-459
//
// Note that AtIndex and RoundNumber are both computed from the length BEFORE the
// append, which is why they differ by one, and the length is taken from the
// document LoadRunStateDocument just returned, not from load()'s earlier read.
//
// ─── FINDINGS AND RULINGS THIS SCAFFOLD RECORDS ──────────────────────────────
//
// (1) WHAT cmd/iterate IS LICENSED TO CHANGE — see the enumeration above. Six
// kinds, one conditional, derived from eight assignment sites, all inside
// appendRound. The finding worth carrying forward is not the list; it is that
// the code's OWN comment about what it owns (main.go:439-440) was already wrong
// by two of six before anybody looked. Derived, not asked.
//
// (2) GATE-FIELD DESTRUCTION IS THE SAME PROPERTY AS CLASSIFICATION LOSS, NOT A
// SECOND ONE — AND THE TEMPTATION TO SPLIT THEM IS THE DECLARATION DESIGN
// WEARING A DIFFERENT HAT.
//
// THE RULING: one property. FidelityPathwise is defined per JSON path over the
// whole document, and gates.lint:….command is a path like any other. There is no
// property boundary at "who wrote it", and there is no second mechanism: the
// passthrough that preserves classification.changed_files preserves
// gates.*.command by the same act of not decoding it, with no extra line of
// code. Making it a second property would require cmd/iterate to know cmd/gates'
// Gate schema — that is preserve-by-declaration, in another module's vocabulary,
// with a hand-list that would drift the day cmd/gates adds a ninth field.
//
// WHY IT NONETHELESS DESERVES ITS OWN PARAGRAPH: it is the same property with a
// strictly worse blast radius, for three reasons, and a body author who treats
// it as "and also the gates block" will under-test it.
//
//	(a) NO TEST IN EITHER MODULE CAN SEE IT. cmd/iterate's suite round-trips
//	    cmd/iterate's structs and is green. cmd/gates' suite measures documents
//	    cmd/gates wrote. The loss only exists in the composition, and the two
//	    packages are separate Go modules with no shared test. It has been green
//	    on both sides the whole time.
//	(b) IT DESTROYS EVIDENCE, NOT CONFIGURATION. command, ran_at, duration_ms,
//	    output_path, exit_code and metrics are the audit trail of whether a gate
//	    ACTUALLY RAN. After `iterate run`, {"status":"pass"} is indistinguishable
//	    from a hand-typed pass. cmd/gates' own config comment says the thing it
//	    exists to prevent is "a status that means 'we did not check but it is
//	    fine'" — and iterate manufactures exactly that status from a real one.
//	    Measured: gates[coverage].metrics.violations[0] = "pkg at 10.0% < floor
//	    95%" is destroyed while status:"fail" survives, so the document says a
//	    gate failed and no longer says why.
//	(c) THE ASYMMETRY THAT PROVES THE LICENCE IS PER-TOOL. `gates` is a LICENSED
//	    path for cmd/gates and a FORBIDDEN one for cmd/iterate. Same path, same
//	    document, opposite verdicts. The property is per-document; the licence is
//	    per-tool; and that is why each tool needs its own enumeration derived
//	    from its own source rather than a shared "run-state ownership" table
//	    somebody would have to keep true.
//
// (3) ROUNDS ARE AN APPEND, AND AN APPEND RAISES THREE THINGS A SET DOES NOT.
// This is the substance of G2 and none of it transfers from G1.
//
// (3a) A SET NAMES ITS PATH; AN APPEND DOES NOT. `gates.<key>` is on the edit.
// `rounds[N]` is determined by the length of the array in the document, which is
// a fact about the document and not about the edit. Two ways out: let the editor
// compute N, or make the caller state it. THE CALLER STATES IT (Edit.AtIndex),
// and ApplyRoundRecord refuses a mismatch. The reason is not symmetry with the
// set case — it is that an editor which computes N can never be wrong and can
// therefore never DETECT that the caller's N was stale. iterate's N comes from
// a read taken before a whole review panel ran. Deriving the licensed index from
// the produced document, the other tempting shortcut, is banned for G1's reason:
// it licenses whatever happened.
//
// (3b) ARE PREVIOUS ROUNDS FROZEN? TODAY, NO — MEASURED. `state.Rounds =
// append(...)` followed by MarshalIndent(state) re-marshals EVERY element
// through the Round struct. An append whose only intended effect was to add
// rounds[1] destroyed three paths inside rounds[0]: zzz_round_future,
// reviewers[0].zzz_reviewer_future, and evidence_id (an integer above 2^53,
// destroyed rather than corrupted because Round does not declare it).
//
// THE RULING: rounds[0..N-1] ARE NOT LICENSED. An append licenses exactly one
// new index and nothing else in the array. Concretely this forbids a body from
// renumbering an earlier round's `round` field, from backfilling a missing
// `status`, and from re-encoding an element it did not change.
//
// AND THE OBVIOUS OBJECTION, ANSWERED: "iterate wrote every element of rounds[]
// itself, from its own struct, so there is nothing in there it can lose."
// That is true of the rounds iterate wrote and it is not the property. The
// retroactive loss bites the moment ANY other writer annotates a round — a
// driver attaching an evidence id, a later iterate declaring a field this build
// does not, a human adding a note. That is precisely what contract_version
// probes for, and it is why the probe is in the fixture. G1 sealed
// repo.dirty:false on the same reasoning: a unit whose subject is preservation
// must not be the unit that argues itself out of preserving a key.
//
// (3c) WHAT DOES "PRESERVED" MEAN FOR AN ELEMENT THAT DID NOT EXIST BEFORE?
// Nothing — every path under rounds[N] is ADDED, and additions under the
// licensed subtree are licensed. The trap is the containing array: the licensed
// prefix must be rounds[N] EXACTLY and never `rounds`. A subtree licence over
// `rounds` forgives every divergence beneath it, which is precisely the three
// measured rounds[0] losses. That is G1's `gates` container ruling, and the
// reason is stronger here because the array has siblings that can move, where
// an object's members cannot.
//
// (3d) AND ONE MORE, WHICH IS WHY POSITIONAL FLATTENING IS THE RIGHT CHOICE
// AND NOT MERELY THE EASY ONE. Diverge compares arrays by index. A shift
// therefore reports as a wall of CHANGED rather than as one insertion. That is
// noisy and it is exactly right: a shifted rounds[] is not "the same rounds
// moved", it is a document in which every later reader's rounds[2] now means
// something different, and every one of those paths genuinely diverged. An
// identity-based array diff would report one insertion and call the rest
// preserved, which is the vacuous reading.
//
// (4) WHAT CLAIM BECOMES AVAILABLE ONCE G2 LANDS, AND WHAT STILL WOULD NOT.
//
// TODAY'S CLAIM, and cmd/classify's amended rows are careful to say only this:
// COMMITTED cmd/gates preserves the run-state at FidelityPathwise across a
// merge. Not the pipeline. readset_seal_test.go says so in as many words —
// "what this row measures is honestly 'the classification survives every WRITER
// in this pipeline', and after G1 gates is the only writer in it", the pipeline
// in question invoking `iterate next`, which I measured writes nothing.
//
// THE CLAIM G2 MAKES AVAILABLE — one sentence, and it is the one that matters:
// EVERY TOOL IN THIS REPO THAT WRITES THE RUN-STATE PRESERVES EVERY JSON PATH
// IT IS NOT LICENSED TO CHANGE, so a classification key written by cmd/classify
// survives an arbitrary interleaving of gate rounds and iterate rounds. The
// writer set is closed and I checked it rather than assuming it: cmd/recheck
// writes only its -out payload (recheck/main.go:230) and cmd/reviewer writes
// only findings and dumps; neither opens the run-state. So after G2 the writers
// are cmd/classify (the producer), cmd/gates and cmd/iterate.
//
// THE CONCRETE DELIVERABLE OF THAT CLAIM, and it should be named because
// otherwise the claim is a sentence nobody can run:
// TestSeal_SidecarSurvives_AndSoDoesTheV1Projection currently runs
// classify → gates → `iterate next`. After G2 it can be widened to `iterate
// run`, which is the differential §3.3 actually asked for ("a differential
// fixture runs classify→gates→iterate and asserts BOTH projections survive every
// rewrite"). That widening is a seal edit in another module and belongs to
// whoever may make one. It is not the body's and it is not mine.
//
// WHAT STILL WOULD NOT BE AVAILABLE, five things, and each is a claim somebody
// will be tempted to make on G2's back:
//
//  1. NOT "THE PIPELINE PRESERVES IT" — only "these three tools do". The
//     schema names a driver as the owner of `deferred_findings` and `pr`; no
//     driver in this repo writes a run-state today, but the day one does it is a
//     fourth writer with no preservation contract and the claim narrows again
//     without anyone editing a word of it.
//  2. NOT FOR THE TRACKED BINARY, until cmd/iterate/iterate is rebuilt. The
//     fix would be in the source and skills/iteration-protocol.md:130 execs the
//     artifact. See the rebuild recommendation below.
//  3. NOT THE STALENESS WINDOW, and it is wider here than in cmd/gates. iterate
//     reads the run-state twice per `run`: load() at :523, and appendRound at
//     :442 — with execTool and an entire review panel in between, so minutes, not
//     milliseconds. G2 makes appendRound preserve WHATEVER IT READS; it does not
//     make what it reads the document iterate decided against. Unlike G1's
//     residue this one has a visible symptom — rounds[N].round disagreeing with
//     N+1 — and Edit.AtIndex is where the check would live. It is a staleness
//     bug, not a preservation one, it needs a decision about what iterate does
//     when the document moved under it, and that decision is a new user-visible
//     failure path. Its own unit, and it inherits G1 ruling (B)'s ordering
//     condition.
//  4. NOT BYTE-IDENTITY. Both of G1's mechanisms apply unchanged.
//  5. NOT "iterate writes only what it owns" IN THE SENSE ITS OWN COMMENT MEANS.
//     main.go:439-440's list is short by two. G2's licence is the correction, and
//     that comment should be updated by the body in the same commit that wires
//     appendRound.
//
// (5) THE v2 SIDECAR: G2 DISCHARGES ITS STATED JUSTIFICATION IN FULL AND DOES
// NOT RETIRE IT. THE REPO'S PRIOR RULING THAT "RETIRING THE SIDECAR NEEDS A G2
// AGAINST cmd/iterate" IS TRUE AND INCOMPLETE — G2 IS NECESSARY, NOT SUFFICIENT.
//
// THE STATED JUSTIFICATION, in full, is one thing (classify/contract.go:467-473):
// "Both frozen writers unmarshal the run-state into closed structs and marshal
// them back — cmd/gates/main.go:1248 → :1329, and cmd/iterate/main.go:427 →
// :461 — so any key those structs do not declare is silently dropped by the
// first gate round." G1 falsified the first half. G2 falsifies the second. After
// G2 that sentence is false of both writers, measurably, and the reason the
// envelope was put in a separate file no longer exists.
//
// THAT IS A FACT, AND "THE REASON WE CREATED X NO LONGER HOLDS" IS NOT "X SHOULD
// BE REMOVED". Only the first is G2's to assert. Three things a proposal to
// retire has to price, none of which G2 pays:
//
//	(a) THE SEALED PROPERTY WOULD BE DOWNGRADED FROM BYTE TO PATHWISE. The
//	    sidecar survives BYTE-FOR-BYTE, and readset_seal_test.go is explicit that
//	    this is because nothing else writes that file. G1's ruling (A) establishes
//	    that byte-identity is NOT achievable through encoding/json — two measured
//	    mechanisms — so a second key in the shared run-state survives at
//	    FidelityPathwise and no stronger. G1's ruling (A) also records that the
//	    sidecar's byte comparison is THE ONLY BYTE COMPARISON IN THIS PROJECT.
//	    Retiring the sidecar deletes it. Whether pathwise is sufficient for the v2
//	    envelope is a question nobody has been asked; it should be asked before,
//	    not after.
//	    Not an argument, and I checked rather than assuming it: the dual digest
//	    echo is NOT a digest of the envelope. ComputedConfigSHA256 and
//	    ComputedDiffSHA256 are computed over the bytes the producer CONSUMED
//	    (contract.go:446-451), so a pathwise-preserved copy carries the same echo
//	    values and the echo is unaffected. The cost is the seal, not the digest.
//	(b) PRESERVATION IS A PROPERTY OF SOURCE; PRODUCTION RUNS ARTIFACTS. Both
//	    cmd/gates/gates and cmd/iterate/iterate are tracked binaries. Until both
//	    carry the fix, the justification is discharged in the repo and live in
//	    production.
//	(c) THE WRITER SET IS NOT CLOSED FOREVER — see (4)(1). The sidecar is robust
//	    to a fourth writer appearing; a second key in the shared file is not.
//
// WHAT G2 DOES SETTLE FOR §3.3, AND SETTLES COMPLETELY: the argument for
// EXTENDING the v2 envelope on preservation grounds is dead.
// TestSeal_Finding_V2EnvelopeCannotFeedGates records that ClassificationV2 has no
// changed_files while cmd/gates reads it, and G1 already observed that after G1 a
// consumer needing the full classification can read the run-state. G2 completes
// it: after G2 the full classification survives EVERY writer in the pipeline, so
// "extend §3.3 because gates or iterate need the classification" has no remaining
// force at all. G2 adds no field to ClassificationV2 and removes none from this
// package, so that row stays green and the finding is untouched. Anyone extending
// §3.3 now needs a new reason, and it must be a wire reason.
//
// AND ONE PIECE OF STALE TEXT G2 WILL CREATE, flagged so it is not discovered by
// accident: readset_seal_test.go:1040-1051 says "The sidecar's justification is
// NOT retired by G1 ... Retiring the sidecar needs a G2 against cmd/iterate", and
// "cmd/iterate still declares Classification{...} and marshals the whole state
// back from it (:461-466), so `iterate run` still destroys changed_files and the
// other ten keys." Every clause of that becomes false when G2 lands. It is a
// COMMENT on a green row, so nothing goes red and no seal is forced. It should
// still be amended, by whoever may amend a seal, in the commit that lands G2 —
// together with the widening in (4). A comment that describes a fixed defect in
// the present tense is how the next author concludes the defect is still there.
//
// (6) REBUILDING cmd/iterate/iterate — A RECOMMENDATION, NOT A DECISION TAKEN
// HERE, FOLLOWING G1's PRECEDENT.
//
// THE FACTS, checked rather than assumed. cmd/iterate/iterate is TRACKED
// (08c7b29bdbab41a7bcbc9dfd24d219179a8f7062). It is exec'd by absolute path in
// skills/iteration-protocol.md:130 — ONE document, where cmd/gates had three.
// .github/workflows/gates.yml runs `go test` per module over the checked-out
// tree and never rebuilds, so CI cannot notice a stale artifact.
//
// MY RECOMMENDATION: REBUILD, in the BODY commit, together with the source fix.
// One invoking document is still production. Without the rebuild this unit ships
// a fix that is absent from the thing that runs, which is the exact shape
// adjudicate(B1-repair) named as the way a green row rots: accurate about the
// artifact, silently stale about the source.
//
// WHAT THE REBUILD COSTS, checked so nobody is surprised into skipping it — and
// it is CHEAPER THAN G1's. G1's rebuild fired two green rows in cmd/classify
// because gates' behaviour on the differential changed. cmd/classify's
// differential invokes `iterate next`, and I measured that `iterate next` writes
// NOTHING: the run-state file is byte-identical before and after. So rebuilding
// cmd/iterate/iterate cannot change any row that only runs `iterate next`. I
// expect NO green row to fire. That is a prediction, derived from a measurement,
// and the body or P4 must verify it by running cmd/classify's suite before and
// after rather than trusting it — 76 green / 9 red / 1 skip is the number to hold.
//
// HOW TO BUILD IT, and this is not optional. `go build` inside a linked git
// WORKTREE does not stamp this repository. Go's VCS detection looks for a `.git`
// DIRECTORY; a worktree's `.git` is a FILE, so the search walks past it and
// stamps the first enclosing repo it finds. G1 committed and then discarded a
// binary built that way. Build from a CLEAN CLONE of the branch that carries the
// fixed source, and verify with `go version -m cmd/iterate/iterate | grep vcs.`
// before committing. And note G1's corollary: a committed artifact can never be
// byte-reproducible from the commit that contains it, because that commit also
// touches source and Go's action ID hashes every source byte, comments included.
// It is reproducible from the commit it NAMES.
//
// THE FAILURE MODE TO REFUSE: leaving the binary stale because the suite is
// green either way. That trades a fixed production for a green suite.
//
// (7) TWO ARTIFACTS THIS UNIT MUST NOT TOUCH, and they are not alike.
// cmd/classify/classify is PINNED as the v1 differential baseline and must stay
// 9542fe1ba7bcfada1ae2c2a53fa3499a4931edf0 — `go build ./...` inside cmd/classify
// overwrites it. cmd/gates/gates was rebuilt by adjudicate(G1) and is
// a4bd147fc2f479b3fa7ca3f2e1f87582a4ffbe43; G2 changes no cmd/gates source and
// must not rebuild it. Both were verified unchanged at this commit.
//
// AND A THIRD, WHICH THE UNIT BRIEF DID NOT NAME AND WHICH I HIT: `go build
// ./...` inside cmd/iterate OVERWRITES cmd/iterate/iterate, exactly as it does
// in cmd/classify. It is the same hazard, it is one keystroke away from any
// author working in this directory, and unlike cmd/classify's there is no note
// anywhere warning about it. I hit it at this scaffold; the build happened to
// fail on an unused import before Go wrote the file, which is luck and not a
// safeguard. USE `go vet ./...` AND `go test ./...` IN THIS DIRECTORY. If you
// must build, build elsewhere: `go build -o /tmp/iterate-check .`. A seal author
// should fingerprint this artifact the way cmd/gates'
// g1FingerprintTrackedBinary does, with a t.Cleanup that re-checks the hash, and
// for the same stated reason.
//
// (7b) golangci-lint MUST BE RUN FROM THE MODULE ROOT — RE-REPRODUCED HERE, AND
// THE FALSE PASS IS SILENT. Each cmd/ tool is its own Go module, so the module
// root for this file is cmd/iterate. Measured at this commit, with a deliberate
// `declared and not used` injected into Fidelity.String:
//
//	from cmd/iterate   ./preserve.go:290:2: declared and not used (typecheck)
//	from cmd/           level=error ... "directory prefix . does not contain
//	                    main module or its selected dependencies"   0 issues.
//	from the repo root  the same error line, and                    0 issues.
//
// The parent-directory runs print `0 issues.` while a real typecheck failure is
// present. The error line is a `level=error` on stderr above the summary and it
// is easy to scroll past. Anyone reporting a clean lint for this package must
// say which directory they ran it from.
//
// (8) A DEFECT IN iterate THIS CONTRACT EXPOSES AND DOES NOT FIX: AN
// UNDETERMINED VERDICT DELETES THE PREVIOUS ONE.
//
// recordRecheck copies rr.Verdict straight from cmd/recheck's -out payload
// (main.go:392). A payload without a verdict field yields "". appendRound then
// writes state.Verdict = strings.ToLower("") = "" (:448), and RunState.Verdict is
// `json:"verdict,omitempty"` (:70) — so the top-level `verdict` member is not
// written as empty, it is REMOVED. A round whose verdict could not be determined
// therefore deletes the previous round's verdict rather than recording that it is
// unknown. It is the implicit-state shape skills/explicit-state.md is about,
// arriving as a deletion.
//
// G2 DOES NOT FIX IT, and Edit.Validate deliberately does not refuse the empty
// verdict, because refusing it would make iterate fail to record a round it
// records today. The deletion is licensed at `verdict` because it is what iterate
// does. Recorded here so the fix has somewhere to start.
//
// (9) A SECOND ONE, SAME SHAPE, OPPOSITE DIRECTION: A STALE escalation_reason
// SURVIVES A RECOVERY. appendRound sets escalation_reason only in the escalating
// arm (:456-459) and never clears it in the APPROVE or ITERATE arms. A run that
// escalated at round 2 and then recorded an ITERATE at round 3 carries round 2's
// escalation_reason forward beside a non-escalated status. Out of scope for the
// same reason as (8): clearing it is a change to what iterate decides.
//
// (10) OUT OF SCOPE, INHERITED AND RE-CONFIRMED. cmd/gates writes gate keys of
// the form "<gate>:<module-rel>" — I measured "build:apps/finance-domain/wallet"
// — while config/run-state.schema.json constrains `gates` with propertyNames.enum
// to bare gate names. Both implementations already write documents the schema
// rejects. G2 preserves that behaviour exactly, because changing it would change
// what the tools write. It wants its own unit.
