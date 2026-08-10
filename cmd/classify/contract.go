// Contract surface for the classification→gating wire (unit B1).
//
// SCAFFOLD — CONTRACTS ONLY. Every function in this file panics. The doc
// comments ARE the specification: the seal author derives rulings from this
// prose, and the body author implements to it. Where the normative design
// (docs/plans/2026-08-02-classification-gating-design.md §3.3, held in the
// claude-dispatcher checkout) is silent, the choice is marked CHOICE and the
// rejected alternative is named. Do not resolve a CHOICE by editing the body;
// change the comment first, or the two definitions drift — which is the
// failure class that cost this design twenty review rounds.
//
// The dispatcher's signature gate is Python-only. Nothing mechanically holds
// a Go body to these comments. They are held by the seal author reading them.
package main

import (
	"encoding/json"
	"io"
)

// ─── contract version ────────────────────────────────────────────────────────

// ContractVersion is the classification wire contract in force for one
// invocation. It is a genesis decision recorded by the caller, never inferred
// per-parse: the pinned v1 producer emits no version field at all (see the
// Classification struct at main.go:125 — there is no contract_version member),
// so an absent field is not a negotiation signal and must never be read as one.
//
// The zero value is ContractVersionUnset and is NEVER legal at any entry point.
// It exists so that "nobody decided" is a named state that raises, rather than
// silently meaning 1. Every function taking a ContractVersion must handle all
// four cases exhaustively and return an error on Unset and on any value outside
// the closed set — including future integers. Do not write a default arm that
// falls through to v1.
type ContractVersion int

const (
	// ContractVersionUnset is the zero value. Illegal everywhere. Raise.
	ContractVersionUnset ContractVersion = 0
	// ContractV1 is the legacy envelope: the exact shape the pinned binary
	// emits today, byte-for-byte, minus nothing and plus nothing. It remains
	// the emission default for the whole coexistence period.
	ContractV1 ContractVersion = 1
	// ContractV2 is the canonical envelope of §3.3. Opt-in only, until
	// cmd/gates and cmd/iterate migrate.
	ContractV2 ContractVersion = 2
)

// flagContractVersion is the CLI flag name. It is a constant so the capability
// probe and the flag registrar cannot disagree about what the flag is called.
const flagContractVersion = "contract-version"

// defaultContractVersion is ContractV1 and must stay ContractV1 until both
// frozen consumers have migrated. This is not a stylistic default: a v2-only
// run-state strands cmd/gates' changed_files dependency (gates/main.go:124,
// read at gates/main.go:444) and cmd/iterate's recheck_min_severity floor
// (iterate/main.go:89, read at iterate/main.go:271). Changing this constant is
// the cut-over, not a tweak.
const defaultContractVersion = ContractV1

// ParseContractVersion converts the -contract-version flag value to the typed
// contract.
//
// Accepts exactly "1" and "2". Every other input — including "", "0", "v1",
// " 2", "02", and any integer outside the set — is an error naming the value
// received and the accepted set. There is no lenient parse and no default on
// failure: a caller that mistypes the contract must be told, not silently
// given v1, because the difference between contracts is whether an absent
// config_scaffold is legal.
//
// The error is INVALID_INPUT (exit 3), not INVALID_SCHEMA: it is the operator's
// argv that is wrong, not a producer's output.
func ParseContractVersion(s string) (ContractVersion, error) {
	panic("B1: not implemented")
}

// Valid reports whether v is one of the closed set {ContractV1, ContractV2}.
// ContractVersionUnset is NOT valid. Callers use this to raise early; it is
// never a licence to substitute a default for an invalid value.
func (v ContractVersion) Valid() bool {
	panic("B1: not implemented")
}

// String renders the contract for messages and for the capability probe's
// contract_versions list. It must render ContractVersionUnset as "unset" and
// any out-of-set value as a form that visibly contains the raw integer, so an
// unknown contract cannot be mistaken for a known one in a log line.
func (v ContractVersion) String() string {
	panic("B1: not implemented")
}

// ─── config_scaffold: the named, total desugaring rule ───────────────────────

// ScaffoldPresence is the tri-state of the config_scaffold key as it appeared
// on the wire. It exists because "absent" and "present and false" are DIFFERENT
// facts and this project's explicit-state discipline forbids collapsing them at
// a decision boundary. The v1 struct tag is `config_scaffold,omitempty`
// (main.go:141), so v1 output genuinely cannot distinguish them without this
// type — which is exactly why the type exists rather than a *bool.
//
// The zero value is ScaffoldPresenceUnknown and is never legal. A caller that
// has not determined presence must not call the desugar.
type ScaffoldPresence int

const (
	// ScaffoldPresenceUnknown is the zero value. Illegal. Raise.
	ScaffoldPresenceUnknown ScaffoldPresence = iota
	// ScaffoldAbsent: the key was not present in the object at all.
	ScaffoldAbsent
	// ScaffoldPresentFalse: the key was present with the value false.
	ScaffoldPresentFalse
	// ScaffoldPresentTrue: the key was present with the value true.
	ScaffoldPresentTrue
)

// DesugarConfigScaffold is the ONE total rule that turns wire presence into the
// boolean the §3.2 equations consume. It is a named rule, not a fallback, and
// it is the only place absence may become false.
//
// Exhaustive by contract — every (contract, presence) pair below has a stated
// result, and any pair not listed is a programming error that must panic-free
// return an error rather than fall through:
//
//	(V1_COMPAT, Absent)        → false, nil   — the named desugar
//	(V1_COMPAT, PresentFalse)  → false, nil
//	(V1_COMPAT, PresentTrue)   → true,  nil
//	(V2,        Absent)        → _, error     — INVALID_SCHEMA: presence required
//	(V2,        PresentFalse)  → false, nil
//	(V2,        PresentTrue)   → true,  nil
//	(Unset,     *)             → _, error
//	(*,         Unknown)       → _, error
//
// Why the asymmetry is deliberate, restated so nobody "simplifies" it later:
// requiring presence in the legacy shape would turn EVERY non-scaffold classify
// into INVALID_SCHEMA, because the v1 tag omits the field when false. And a
// soft default under v2 would re-open absent-means-false, which is the implicit
// state this whole unit exists to close. Neither direction may be relaxed.
//
// The V2 error must name the field and the contract, because this is the error
// an operator will hit first when they flip -contract-version 2 against a
// producer that has not been rebuilt.
//
// DUAL-DEFINITION HAZARD: the design states this rule for the dispatcher's
// Python parse_classification. This Go copy exists because the T10 fixture
// generator and the producer's own re-read path need it in-process. Two
// implementations of one normative rule is precisely the edit-propagation class
// §12 mandated PR0 to eliminate. When PR0's machine-readable artifact lands,
// this function must become generated, not hand-maintained.
func DesugarConfigScaffold(contract ContractVersion, presence ScaffoldPresence) (bool, error) {
	panic("B1: not implemented")
}

// ─── panel intensity and its two-way projection ──────────────────────────────

// PanelIntensity is the v2 representation of panel shape. The v1 envelope has
// no such field; it carries the three derived booleans/ints instead.
//
// The producer emits only PanelFULL and PanelSINGLE. PanelSKIP exists in the
// type because the closed sum is shared with the consumer, where it is
// reachable in LEGACY and EMPTY modes — neither of which ever projects to v1.
// The producer must never emit it.
type PanelIntensity int

const (
	// PanelIntensityUnset is the zero value. Illegal. Raise.
	PanelIntensityUnset PanelIntensity = iota
	// PanelSKIP has NO v1 emission. Projecting it is an error, not an
	// empty panel: a panel is always required by contract.
	PanelSKIP
	// PanelSINGLE is the reduced single-reviewer carve-out.
	PanelSINGLE
	// PanelFULL is the mandatory five-seat panel.
	PanelFULL
)

// ProjectPanelToV1 is the v2→v1 panel projection. It is total over the values
// the producer can emit and errors on every other value.
//
//	FULL   → Panel{Required: true, Seats: 5, Reduced: false}
//	SINGLE → Panel{Required: true, Seats: 1, Reduced: true}
//	SKIP   → error   (no v1 emission exists; reachable only in LEGACY/EMPTY)
//	Unset  → error
//	other  → error   (raise on unknown; never approximate to FULL here — the
//	                  at-least-FULL treatment of an unknown intensity is the
//	                  CONSUMER's demand rule, not the producer's emission rule)
//
// Required is true in both live cases and is never false. reasons are carried
// through verbatim from the v2 panel; the projection neither adds nor edits
// them. v2-only fields are forbidden in the v1 Panel — do not add intensity to
// the v1 struct as a convenience, and do not populate Panel.Reasons from
// anything but the v2 reasons.
func ProjectPanelToV1(intensity PanelIntensity, reasons []string) (Panel, error) {
	panic("B1: not implemented")
}

// LiftPanelFromV1 is the v1→v2 direction, used when building the v2 envelope
// from the classification the existing classify() already computed.
//
//	{Required: true, Seats: 5, Reduced: false} → FULL
//	{Required: true, Seats: 1, Reduced: true}  → SINGLE
//	anything else                              → error (INVALID_SCHEMA)
//
// The "anything else" arm is unreachable against today's decidePanel
// (main.go:847), which constructs only those two shapes. It is REQUIRED anyway:
// an unreachable arm that raises is the difference between a contract and a
// coincidence, and decidePanel is one edit away from a third shape.
//
// Note in particular that Required == false must error rather than project to
// SKIP. A future decidePanel that emits Required:false has changed the mandatory
// -panel rule, and that must surface as a schema failure, not as a silent SKIP.
func LiftPanelFromV1(p Panel) (PanelIntensity, error) {
	panic("B1: not implemented")
}

// ─── the v2 envelope ─────────────────────────────────────────────────────────

// PanelV2 is the v2 panel object. It has exactly two members. The v1 trio
// (required/seats/reduced) MUST NOT appear here: one representation per fact,
// and a panel is always required by contract.
type PanelV2 struct {
	// Intensity is the string form: "FULL" or "SINGLE". The producer never
	// emits "SKIP". Marshalling must reject PanelIntensityUnset rather than
	// emitting an empty string.
	Intensity string `json:"intensity"`
	// Reasons is the same human-auditable derivation the v1 panel carries.
	// It is non-omitted: an empty reasons list is a real, distinguishable
	// state (the reduced carve-out always carries one reason today, but an
	// omitted key would make "no reasons" and "reasons dropped" identical).
	Reasons []string `json:"reasons"`
}

// ClassificationV2 is the canonical v2 wire envelope. Its key set is EXACTLY
// the key set of the design's §3.3 fixture — no more, no less. Every field is
// non-omitted: `omitempty` anywhere in this struct re-opens the absent-means-
// zero implicit state that this unit exists to close, so a nil slice must
// marshal as [] and a false bool must marshal as false.
//
// Deliberately ABSENT, each with its reason:
//
//	classified_at   — a volatile derived field (main.go:143, stamped at
//	                  main.go:274). It is also the named exclusion in the v1
//	                  differential, and a wire that carries a clock cannot have
//	                  a deterministic golden.
//	config_sha256   — digests live in the response wrapper, computed over the
//	                  bytes the producer actually consumed, never in the payload.
//	config_path     — a local filesystem path is not a portable wire fact and
//	                  is not a gate input.
//	skills          — a routing hint for the driver, recomputed by the consumer.
//	reviewer_args   — the frozen workflow reviewer is advisory-only during
//	                  coexistence; the dispatcher's PanelPlan runner is the sole
//	                  PanelSatisfaction input and builds its own argv.
//	changed_files   — see the KNOWN GAP note on EmitV2 below. This omission is
//	                  faithful to §3.3 and is a live problem for any future
//	                  migration of cmd/gates, which reads it.
type ClassificationV2 struct {
	// ContractVersion is always 2 in this struct. It is emitted, unlike v1
	// which carries no version field at all.
	ContractVersion int `json:"contract_version"`

	Risk                  string `json:"risk"`
	FinancialPathsTouched bool   `json:"financial_paths_touched"`
	ClientOnly            bool   `json:"client_only"`
	ServerSurface         bool   `json:"server_surface"`
	Migration             bool   `json:"migration"`
	HumanPRGate           bool   `json:"human_pr_gate"`
	RecheckMinSeverity    string `json:"recheck_min_severity"`

	Components []string `json:"components"`
	Panel      PanelV2  `json:"panel"`
	// GateSignals reuses the v1 GateHit shape unchanged (main.go:146). Its
	// `sample,omitempty` tag is inherited and is a KNOWN INCONSISTENCY with
	// this struct's no-omitempty rule; see the report. Changing GateHit would
	// change v1 bytes, which P1 must not do.
	GateSignals    []GateHit `json:"gate_signals"`
	RiskReasons    []string  `json:"risk_reasons"`
	UnmatchedFiles []string  `json:"unmatched_files"`

	// ConfigScaffold is REQUIRED and NON-OMITTED. This is the whole point of
	// the field's appearance here: under v2 its absence is INVALID_SCHEMA, so
	// the producer must always write it. Do not add omitempty. The v1 struct
	// keeps its omitempty tag deliberately — see EmitV1.
	ConfigScaffold bool `json:"config_scaffold"`
}

// ─── the response wrapper ────────────────────────────────────────────────────

// responseVersion is the wrapper's own major. A missing or unknown
// response_version major is INCOMPATIBLE_CONTRACT at the consumer.
const responseVersion = 1

// ResponseWrapper is the version-independent carrier the envelope rides inside.
// It exists because the dual digest echo that gates every money-driving
// classification has no home in either payload: v1 has no digest fields and v2
// deliberately carries none.
//
// There is deliberately NO request_id. The request frame carries none, and the
// two digests already bind this response to the exact request bytes. An
// unsourced echo field would be the copy-back class one level up.
//
// Classification is held as RawMessage so the wrapper is genuinely version-
// independent: it carries a v1 or a v2 payload without the wrapper type
// knowing which. Only this member projects into run-state.
type ResponseWrapper struct {
	ResponseVersion int `json:"response_version"`
	// ComputedConfigSHA256 and ComputedDiffSHA256 are lowercase hex, computed
	// by the producer over the bytes IT CONSUMED — never copied from an input
	// field, because the request frame carries no digests at all, which makes
	// a copy-back echo impossible by construction. Both are always present.
	ComputedConfigSHA256 string          `json:"computed_config_sha256"`
	ComputedDiffSHA256   string          `json:"computed_diff_sha256"`
	Classification       json.RawMessage `json:"classification"`
}

// ─── the v2 sidecar ──────────────────────────────────────────────────────────

// v2SidecarSchemaVersion versions the SIDECAR FILE ENVELOPE. It is orthogonal
// to ContractVersion (which versions the classification payload) and to the
// run-state schemaVersion (main.go:41, which versions the shared run-state
// file). Three independent version numbers whose current values are 1, 2 and 1
// is a live footgun; each is named separately here so a reader can tell which
// one they are looking at.
const v2SidecarSchemaVersion = 1

// V2Sidecar is the whole content of <run-state>.v2.json.
//
// The v2 envelope lands in a SEPARATE FILE and NEVER as a second key in the
// shared run-state. This is not tidiness. Both frozen writers unmarshal the
// run-state into closed structs and marshal them back —
// cmd/gates/main.go:1248 (readRunState) → :1329 (MarshalIndent), and
// cmd/iterate/main.go:427 → :461 — so any key those structs do not declare is
// silently dropped by the first gate round. Verified empirically at baseline,
// not inferred: see the note on GenerateReadSet in readset.go, and be aware
// that the same mechanism already destroys v1 keys today.
//
// Staleness of this sidecar relative to the tool-rewritten v1 file is harmless
// because NEITHER is a gate input. Both are mirrors. No dispatcher gate reads
// either one; correct bytes on an agent-writable carrier are still a mirror.
type V2Sidecar struct {
	SchemaVersion int `json:"schema_version"`
	// Response is the full wrapper, not the bare envelope, so that a reader
	// of the sidecar alone can still check the dual digest echo.
	Response ResponseWrapper `json:"response"`
}

// V2SidecarPath derives the sidecar path from the run-state path.
//
// CHOICE (the design writes only "<run-state>.v2.json" and is silent on
// whether the existing extension is replaced): this is a LITERAL APPEND —
// "/tmp/run.json" yields "/tmp/run.json.v2.json". Rejected alternative:
// replacing a trailing ".json", which yields the prettier "/tmp/run.v2.json"
// but requires case analysis on the input path and produces a different answer
// for a run-state that has no ".json" suffix. "No reader guesses" is the design
// constraint, and a total append needs no rules; the ugly name is the price.
//
// Returns the empty string for an empty runState, which callers must treat as
// "no sidecar is written" — writing "./.v2.json" would be worse than nothing.
func V2SidecarPath(runState string) string {
	panic("B1: not implemented")
}

// ─── emission ────────────────────────────────────────────────────────────────

// EmitV1 writes the legacy payload.
//
// It must be BYTE-IDENTICAL to what the pinned binary emits today: the same
// struct, the same field order, the same `omitempty` behaviour, two-space
// indent, trailing newline from Encoder.Encode (main.go:314-318). This is the
// output the v1 differential compares, and it is what the frozen consumers
// parse. Any change here is a wire break dressed as a refactor.
//
// In particular: the v1 Classification KEEPS `config_scaffold,omitempty`
// (main.go:141). Removing omitempty here would change v1 bytes for every
// non-scaffold run. The absent-means-false problem is closed on the v1 side by
// DesugarConfigScaffold at the consumer, which is a named total rule — not by
// mutating the legacy shape.
//
// The v1 emission is total by construction: the producer holds every fact the
// frozen readers consume, so there is no field it cannot fill. That totality is
// what GenerateReadSet exists to keep true.
func EmitV1(w io.Writer, cls *Classification) error {
	panic("B1: not implemented")
}

// BuildV2 lifts the classification classify() already computed into the v2
// envelope. It is a pure projection with no policy of its own: every value
// comes from cls, and the §3.2 equations must still hold over the result.
//
// It errors (INVALID_SCHEMA) if cls.Panel does not lift (see LiftPanelFromV1),
// and it must never invent a value for a field cls does not carry.
//
// Nil slices in cls become empty slices here, not null, because every field in
// ClassificationV2 is non-omitted and `null` is a third state.
func BuildV2(cls *Classification) (ClassificationV2, error) {
	panic("B1: not implemented")
}

// EmitV2 writes the v2 envelope inside the response wrapper.
//
// The digests come from the installed DigestSource (capability.go). If none is
// installed, EmitV2 must FAIL — it must not emit empty strings, and it must not
// emit the envelope bare without its wrapper. A wrapper with absent digests is
// exactly the unimplementable-echo state that round 12 rejected.
//
// KNOWN GAP, stated plainly: the §3.3 envelope has no changed_files, and
// cmd/gates reads changed_files (declared gates/main.go:124, read
// gates/main.go:444-445). So the v2 sidecar alone cannot feed cmd/gates. That
// is consistent during coexistence, where gates reads the v1 run-state, but it
// means "migrate gates to v2" is not currently possible without extending the
// envelope. This unit does not extend it; §3.3 is normative and this is a
// finding against §3.3, not a licence to add the field.
func EmitV2(w io.Writer, cls *Classification) error {
	panic("B1: not implemented")
}

// WriteV2Sidecar writes <run-state>.v2.json.
//
// Contract:
//   - It is written only when the contract is ContractV2 AND -out was given.
//   - It fully replaces any prior sidecar; it never merges. Unlike the shared
//     run-state, this file has exactly one writer, so the merge-preserving
//     dance of writeRunState (main.go:996) does not apply and must not be
//     imitated.
//   - It NEVER touches the run-state file. If this function is ever seen to
//     open opts.out, that is the bug the separate-file rule exists to prevent.
//   - Failure to write the sidecar is a hard error, not a warning. A silently
//     missing sidecar is indistinguishable at the consumer from an old run,
//     which routes it down the in-flight mirror path and loses the v2 facts.
func WriteV2Sidecar(runState string, cls *Classification) error {
	panic("B1: not implemented")
}

// ─── config path resolution ──────────────────────────────────────────────────

// ResolveConfigDual applies the §3.3 config-path rule to the searched
// candidates:
//
//	.agent/risk-paths.json alone            → use it
//	.claude/risk-paths.json alone           → use it (compatibility)
//	both present, identical content         → use .agent/ (preferred)
//	both present, DIFFERING content         → INVALID_SCHEMA
//	neither                                 → the existing missing-config
//	                                          INVALID_INPUT path, unchanged
//
// CHOICE (the design says "differing dual" and does not define differing):
// difference is SHA-256 OVER THE RAW BYTES, not over the parsed config.
// Rationale: it is total and needs no canonical form; it is the same digest the
// wrapper echoes for the config channel, so one notion of "this config" serves
// both; and two copies of a project's money table that differ only in
// formatting are already a drift signal worth failing on. Rejected alternative:
// comparing parsed Config values, which requires defining equality over
// compiled regexps and would silently accept a reordered rules array that
// changes nothing today and something tomorrow.
//
// SCOPE: this rule applies ONLY to the directory search. An explicit -config
// names exactly one file and bypasses the dual check — naming the rule table is
// that flag's whole contract.
//
// SECURITY NOTE, deliberately left as a finding rather than silently fixed:
// configCandidates (main.go:330) places $RISK_PATHS_CONFIG AHEAD of both
// directories, so an agent that can set an environment variable redirects the
// entire money-path table and this dual check never runs. That is an
// authority-channel problem, it is out of B1's scope, and it must not be closed
// by quietly reordering the candidate list here.
func ResolveConfigDual(worktree string) (path string, err error) {
	panic("B1: not implemented")
}
