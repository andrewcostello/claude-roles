// Contract for RUN-STATE GATE KEYS (unit GO-3).
//
// SCAFFOLD — CONTRACTS ONLY. The grammar and its two renderings below are
// real declarations (they compile at package init); every function with a
// body panics. The doc comments ARE the specification: the seal author
// (GO-3-2) derives rows from this prose, the body author (GO-3-3) implements
// to it, GO-3-4 rules on disputes. Decisions that were mine rather than
// measured are marked CHOICE with the rejected alternative named; change the
// comment before changing a body, or the two drift.
//
// ─── THE DEFECT ──────────────────────────────────────────────────────────────
//
// config/run-state.schema.json says `gates` is "Written by cmd/gates" and
// limits its property names to an enum of thirteen bare gate names. cmd/gates
// writes `<gate>:<module-rel>` for every module-scoped gate (gateKey, main.go)
// — `coverage:apps/finance-domain/wallet` is asserted in this module's own
// suite — so the schema rejects most of what the tool it documents writes.
// Nothing noticed, because nothing validates a run state against the schema
// file: validateRunState checks status and skip_reason per gate and never
// looks at the key. The enum is unenforced prose that the writer contradicts.
//
// ─── THE RULING (the three questions the unit asks) ──────────────────────────
//
//  1. THE ENUM IS STRUCK, REPLACED BY A PATTERN. Bare gate NAMES come from a
//     user-supplied gates.json; a schema that lists names claims to know a
//     table it does not own, and the list beside gateKey() is the second
//     hand-maintained table this repository has already named twice. What the
//     schema CAN own is the SHAPE of a key, which is fixed by gateKey():
//
//     key       = gate-name | gate-name ":" module-rel
//     gate-name = [a-z][a-z0-9_]*
//     module-rel = "." | segment ("/" segment)*
//     segment   = [A-Za-z0-9_@+-][A-Za-z0-9_.@+-]*
//
//     This is NOT "allow anything": `Test`, `test:`, `:apps/w`, `test:apps/w:x`,
//     `test:../w`, `test:apps//w`, `test apps/w` and every key carrying a line
//     terminator are refused. The thirteen names in the enum all satisfy
//     gate-name; the seven module-scoped literals in main_test.go all satisfy
//     key.
//
//  2. validateRunState IS THE ENFORCER; the schema is a rendering of the same
//     grammar. The enforcer is the only reader with an exit code. The schema
//     stays because two other nodes (cmd/classify, cmd/iterate) read the same
//     file and a human reads the schema; it is kept honest by derivation, not
//     by hand: schemaGateKeyPropertyNames() is the exact JSON fragment the
//     schema must carry at gates.propertyNames, and a seal compares the file
//     to it. One source (gateKeyGrammar), two renderings, one comparison.
//
//  3. THE KEY SPACE IS CLOSED AT ITS INPUTS, so the relationship between what
//     gateKey() produces and what the enforcer admits is provable rather than
//     sampled: gateKey concatenates a name and a rel with ":", so if every
//     name that reaches it satisfies gate-name (parseConfig refuses others —
//     the config boundary is the only source of bare names) and every rel
//     satisfies module-rel (discoverModules refuses others as INVALID_INPUT,
//     naming the path), every key it emits satisfies key. The writer also
//     checks its own output: mergeGates refuses a result whose key fails
//     checkGateKey instead of writing it. There is no path by which a key
//     outside the grammar reaches disk with exit 0.
//
// ─── ENGINES: WHY THERE ARE TWO RENDERINGS, NOT ONE STRING ───────────────────
//
// gateKeyGrammar uses only constructs RE2 (Go), ECMA-262 (the dialect JSON
// Schema 2020-12 specifies) and Python's re (what the common jsonschema
// package runs) all read identically: ASCII literals, positive bracket
// classes, `?`, `*`, `+` and non-capturing groups `(?:…)`, which RE2 supports
// (regexp/syntax: "(?:re) non-capturing group"). Anchors are NOT shared and are
// added per engine:
//
//   - Go: `\A…\z`. RE2's text anchors, unaffected by any flag.
//   - Schema: `^…$`. In ECMA-262 without the m flag these are strict text
//     anchors. In Python's re, `$` ALSO matches before one trailing LF, so
//     `^test$` accepts "test\n" — the schema-versus-validator disagreement in
//     a new coat. The schema rendering therefore carries a second keyword,
//     `"not": {"pattern": "\\s"}`, which every engine reads as "no whitespace
//     anywhere" and which closes the LF case in Python. Under Go and ECMA it
//     is redundant with the anchors and harmless.
//
// The engine facts above were MEASURED, not recalled: the three renderings
// were run over the 44-row corpus in the seal guidance below on go1.26.0,
// node and python3 3.14 on 2026-08-30 and returned identical verdicts; the
// commit message carries the invocation and the one gap the not-keyword closes.
//
// ─── CHOICES (GO-3-4 rules on any of these) ──────────────────────────────────
//
//   - CHOICE: module-rel is ASCII and admits no whitespace or ':'. A repository
//     whose module root has another character is refused by name at
//     discoverModules, never keyed. REJECTED: "any character but '/' and
//     ':'", because engines disagree on Unicode classes and on `\s`, and a key
//     is an identifier that must round-trip through splitGateKey, not a
//     display string. No module in this repository or evenplay-mono needs it.
//   - CHOICE: a segment may not begin with '.', so `.`, `..` and dot-directories
//     are refused inside a path; the bare "." (the worktree root itself) is the
//     one exception because discoverModules emits it. REJECTED: refusing "."
//     — main_test.go's `test:m` fixture and every single-module repo key
//     through it. A leading '_' is admitted: `go build ./...` skips such
//     directories when WALKING, but a module rooted in one builds fine.
//   - CHOICE: gate-name is lower-case snake. All thirteen shipped names fit;
//     derivePlan's ordering table and the p.Gate == "test" special cases in
//     executeOne compare against lower-case literals, so a `Test` gate would
//     already misbehave silently. REJECTED: case-insensitive names.
//   - CHOICE: the enforcer reports every bad key, not the first, in the
//     `gates[<key>] …` sentence shape validateRunState already uses.
//
// ─── SEAL GUIDANCE (GO-3-2; non-vacuity is the task's own rule) ──────────────
//
// Seal the RELATIONSHIP, never the list:
//
//   - Derive: for every gate name in testdata/example-gates.json crossed with
//     rels {".", "m", "apps/w", "apps/finance-domain/wallet"}, gateKey(...)
//     .Key passes checkGateKey, gateKeyRE, and the schema file's
//     propertyNames (pattern AND not-pattern) — and splitGateKey inverts it.
//   - Refuse: "", "Test", "test-x", "1test", "test:", ":apps/w",
//     "test:apps/w:x", "test::apps/w", "test:/apps/w", "test:apps/w/",
//     "test:apps//w", "test:..", "test:../w", "test:./w", "test:.hidden",
//     "test:apps/.git", "test apps/w", "test:apps w", "tést", "test:apps/wé",
//     and each of "test", "test:apps/w" followed by LF, CR, CRLF, U+2028,
//     U+2029 — refused by checkGateKey AND by the schema fragment. Run the
//     fragment under at least one non-Go engine; the LF row is the one that
//     distinguishes a correct schema from a merely pretty one.
//   - The schema file: gates.propertyNames deep-equals
//     schemaGateKeyPropertyNames() and has no "enum". gateKeyRE compiles
//     (package init is exercised by any test in this package).
//   - Boundaries: parseConfig rejects a gate named "Build" and "go build";
//     discoverModules rejects a module root containing a space;
//     validateRunState rejects a state carrying "test:" and accepts one
//     carrying "coverage:apps/finance-domain/wallet"; mergeGates refuses a
//     result with key "Test:apps/w" and leaves the file unchanged.
//   - The wrong implementation to report against (GO-3-3): a pattern of ".*",
//     or an enforcer that only checks the part before the colon. Every
//     "refuse" row above must stay red under both.
package main

import "regexp"

// Grammar fragments. No anchors, no engine-specific escapes: only what RE2,
// ECMA-262 and Python re read identically. Renderers add the anchors.
const (
	// gateNameGrammar is the bare-name half of the key space: the keys of
	// gates.json. parseConfig is the boundary that enforces it.
	gateNameGrammar = `[a-z][a-z0-9_]*` //nolint:unused // GO-3 scaffold: wired by GO-3-3

	// moduleRelSegment is one path element of a module root. A leading '.'
	// is refused so `.`, `..` and dot-directories cannot appear inside a path.
	moduleRelSegment = `[A-Za-z0-9_@+-][A-Za-z0-9_.@+-]*` //nolint:unused // GO-3 scaffold: wired by GO-3-3

	// moduleRelGrammar is what discoverModules may emit as Module.Rel: the
	// bare "." for the worktree root, or slash-separated segments.
	moduleRelGrammar = `[.]|` + moduleRelSegment + `(?:/` + moduleRelSegment + `)*` //nolint:unused // GO-3 scaffold: wired by GO-3-3

	// gateKeyGrammar is the whole key: what gateKey() produces from a legal
	// name and a legal rel, and exactly what validateRunState admits.
	gateKeyGrammar = gateNameGrammar + `(?::(?:` + moduleRelGrammar + `))?` //nolint:unused // GO-3 scaffold: wired by GO-3-3
)

// goGateKeyPattern renders the grammar for Go's regexp: \A and \z are RE2's
// text anchors and no flag loosens them.
func goGateKeyPattern() string { return `\A(?:` + gateKeyGrammar + `)\z` } //nolint:unused // GO-3 scaffold: wired by GO-3-3

// schemaGateKeyPattern renders the grammar for the JSON Schema `pattern`
// keyword. ^ and $ are strict under ECMA-262; Python's re lets $ match before
// one trailing LF, which schemaGateKeyPropertyNames closes with a second
// keyword — never use this string in a schema without that keyword.
func schemaGateKeyPattern() string { return `^(?:` + gateKeyGrammar + `)$` } //nolint:unused // GO-3 scaffold: wired by GO-3-3

// schemaGateKeyPropertyNames is the exact fragment config/run-state.schema.json
// must carry at gates.propertyNames. Compared, never transcribed: the seal
// reads the file and deep-equals it to this value. The `not` keyword refuses
// any whitespace anywhere, which is the only portable way to refuse a
// trailing LF under Python's `$`.
func schemaGateKeyPropertyNames() map[string]any { //nolint:unused // GO-3 scaffold: wired by GO-3-3
	return map[string]any{
		"pattern": schemaGateKeyPattern(),
		"not":     map[string]any{"pattern": `\s`},
	}
}

// gateKeyRE is the enforcer's compiled grammar. Package init compiles it, so
// an unsupported construct fails every test in this package, not one run of
// the tool.
var gateKeyRE = regexp.MustCompile(goGateKeyPattern()) //nolint:unused // GO-3 scaffold: wired by GO-3-3

// checkGateName reports why name is not a legal gate name, or "" when it is.
// parseConfig calls it for every key of gates.json before any plan exists;
// the message names the offending key and the grammar so a config author can
// fix it without reading this file.
func checkGateName(name string) string { //nolint:unused // GO-3 scaffold: wired by GO-3-3
	panic("GO-3: not implemented")
}

// checkModuleRel reports why rel is not a legal Module.Rel, or "" when it is.
// discoverModules calls it for every module root it derives; a failure is
// INVALID_INPUT (exit 3) naming the path — the key is never formed.
func checkModuleRel(rel string) string { //nolint:unused // GO-3 scaffold: wired by GO-3-3
	panic("GO-3: not implemented")
}

// checkGateKey reports why key is not a legal run-state gate key, or "" when
// it is. Exactly gateKeyRE, phrased for a human: an empty key, a bad name, a
// bad rel and a second ':' each get their own sentence. validateRunState calls
// it for every key of gates (the read side); mergeGates calls it for every
// result key and refuses to write when any fails (the write side).
func checkGateKey(key string) string { //nolint:unused // GO-3 scaffold: wired by GO-3-3
	panic("GO-3: not implemented")
}

// splitGateKey is the inverse of gateKey: "test:apps/w" → ("test", "apps/w",
// true); "semgrep" → ("semgrep", "", true); anything checkGateKey refuses →
// ("", "", false). For every legal plan p, splitGateKey(gateKey(p).Key)
// returns (p.Gate, gateKey(p).ModRel, true). The split is at the FIRST ':',
// which is unambiguous because gate-name admits none.
func splitGateKey(key string) (gate, modRel string, ok bool) { //nolint:unused // GO-3 scaffold: wired by GO-3-3
	panic("GO-3: not implemented")
}
