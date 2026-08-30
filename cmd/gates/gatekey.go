// Contract for RUN-STATE GATE KEYS (unit GO-3).
//
// SCAFFOLD — CONTRACTS ONLY. The grammar constants, the renderers and the
// three compiled patterns are real and compile at package init; the four
// check/split functions panic. GO-3-2 derives seal rows from this comment,
// GO-3-3 implements to it, GO-3-4 rules on disputes. Change the comment
// before changing a body. Rationale and measurements live in the commit
// messages.
//
// ─── GRAMMAR ─────────────────────────────────────────────────────────────────
//
//	key        = gate-name | gate-name ":" module-rel
//	gate-name  = [a-z][a-z0-9_]*
//	module-rel = "." | segment ("/" segment)*
//	segment    = [A-Za-z0-9_~-][A-Za-z0-9_.~-]*
//
// The SHAPE gateKey() (main.go) produces. It replaces the schema's
// thirteen-name enum: names come from a user-supplied gates.json, so no
// schema owns that table and a list beside gateKey() is a second one.
//
// ─── ENFORCEMENT ─────────────────────────────────────────────────────────────
//
//   - Read: validateRunState checks every key of `gates`, reporting all bad
//     keys in its existing `gates[<key>] …` sentence shape.
//   - Write: mergeGates checks every result key BEFORE touching the file; on
//     any refusal it writes nothing and returns an error naming each bad key.
//     The write itself is atomic — a temp file in the run state's directory
//     renamed over it — so no reader ever sees a truncated or partial state
//     and a failed write leaves the original byte-identical. finish treats
//     ANY mergeGates error as terminal: a refused key is exitInvalid (3, it
//     derives from an input), an I/O failure is exitFail (1) with a message
//     naming the path and the error; both take precedence over the gate
//     tally, and 0 is never returned over an unwritten state. GO-3-3 removes
//     finish's WARNING-and-return-0 branch and amends README's exit-code row.
//   - Inputs: parseConfig refuses a gates.json key failing checkGateName;
//     discoverModules refuses a module root failing checkModuleRel as
//     INVALID_INPUT naming the path. Both closed, every key gateKey() forms
//     satisfies `key` by construction.
//   - Schema: config/run-state.schema.json declares draft 2020-12 (the first
//     dialect with propertyNames is draft-06; an older `$schema` would keep
//     the fragment and ignore it) and carries schemaGateKeyPropertyNames() at
//     gates.propertyNames with no `enum`; a seal compares file to function.
//
// ─── ENGINES (constraint on every renderer) ──────────────────────────────────
//
// Fragments use only what RE2, ECMA-262 (JSON Schema's dialect) and Python
// re read identically, and they are NEVER matched directly: unanchored,
// MatchString is a substring search and `[.]|…` accepts `..` and `../w`.
// Each boundary has its own anchored renderer — gateNameRE for
// checkGateName, moduleRelRE for checkModuleRel, gateKeyRE for checkGateKey —
// and a checker consults only its own. Go: \A…\z. Schema: ^…$ PLUS
// `"not": {"pattern": "\\s"}`, which is load-bearing — Python re (what the
// jsonschema package runs) lets $ match before one trailing LF, so ^…$ alone
// accepts "test\n" there. ECMA-262 refuses it alone, so a Node/Ajv pass is
// not evidence the keyword can be dropped.
//
// ─── CHOICES (GO-3-4 rules on any of these) ──────────────────────────────────
//
//   - segment is exactly Go's module-path element set — ASCII letters,
//     digits and `- . _ ~` (golang.org/x/mod/module, modPathOK) — so a key
//     round-trips through splitGateKey and engines agree. `@`, `+`, space,
//     ':' and every other character in a module root are INVALID_INPUT
//     naming the path, never a key.
//   - A segment may not begin with '.' (refuses `..` and dot-directories); the
//     bare "." is admitted because discoverModules emits it for a go.mod at
//     the worktree root (main_test.go's single-module fixtures). A leading '_'
//     is admitted: `go build ./...` skips such dirs walking, not as roots.
//   - gate-name is lower-case snake: derivePlan and executeOne already compare
//     p.Gate against lower-case literals.
//   - The enforcer checks SHAPE, not membership in the loaded gates.json: a
//     persisted state may carry keys from an earlier config generation.
//     Whether an unknown name deserves a warning is GO-3-4's call.
//
// ─── SEAL GUIDANCE (GO-3-2 deletes this section once the seal carries it) ────
//
//   - Every row goes through a function that panics today (checkGateName,
//     checkModuleRel, checkGateKey, splitGateKey), through validateRunState
//     or through finish. The compiled patterns and the schema fragment are
//     real at scaffold time: a row over them alone is green before any body
//     exists, so they corroborate inside a row and are never a row.
//   - Derive: every name in testdata/example-gates.json × rels {".", "m",
//     "apps/w", "apps/finance-domain/wallet"}: gateKey(...).Key passes
//     checkGateKey (corroborated by gateKeyRE and the schema fragment) and
//     splitGateKey inverts it.
//   - Refuse (checkGateKey AND the schema fragment): "", "Test", "test-x",
//     "1test", "test:", ":apps/w", "test:apps/w:x", "test::apps/w",
//     "test:/apps/w", "test:apps/w/", "test:apps//w", "test:..", "test:../w",
//     "test:./w", "test:.hidden", "test:apps/.git", "test apps/w",
//     "test:apps w", "test:apps/w@v2", "test:apps/w+x", "tést", "test:apps/wé",
//     and each of "test", "test:apps/w" followed by LF, CR, CRLF, U+2028,
//     U+2029.
//   - Schema oracle: Python `jsonschema` applying the fragment as
//     propertyNames — equivalently re.search(pattern, key) AND NOT
//     re.search(r"\s", key). The fragment WITHOUT `not` must accept "test\n"
//     under it and the fragment WITH it must refuse; that pair seals the
//     keyword. Node/Ajv may be added but does not replace it.
//   - Schema file: `$schema` is "https://json-schema.org/draft/2020-12/schema";
//     gates.propertyNames deep-equals schemaGateKeyPropertyNames() and has no
//     "enum"; and a WHOLE run-state document validated against the WHOLE file
//     (Draft202012Validator, not the fragment) is refused with gates keyed
//     "test:" and accepted keyed "coverage:apps/finance-domain/wallet" — the
//     row that goes red when a dialect ignores propertyNames.
//   - Boundaries (red under an unanchored MatchString, which accepts every
//     one of these as a substring of a legal value): parseConfig rejects
//     gates named "Build", "go build", "test-x", "test x" and "test\n";
//     discoverModules rejects module roots "..", "../w", "./w", ".hidden",
//     "apps/.git", "apps/w x", "apps/w/", "apps/w@v2" and accepts ".",
//     "apps/w", "apps/my~mod". validateRunState rejects "test:" and accepts
//     "coverage:apps/finance-domain/wallet".
//   - Write side: finish over a result keyed "Test:apps/w" returns exitInvalid,
//     leaves the file byte-identical and names the key — red against today's
//     finish (WARNING + exit 0). mergeGates over a run state whose directory
//     refuses new files returns an error and leaves the file byte-identical
//     (skip as root).
//   - Wrong implementations every refuse row stays red under: pattern ".*";
//     checking only the part before the colon; the fragment without `not`
//     under the Python oracle; MustCompile(fragment).MatchString with no
//     anchors at any of the three boundaries.
package main

import "regexp"

// Grammar fragments. No anchors, no engine-specific escapes: only what RE2,
// ECMA-262 and Python re read identically. Renderers add the anchors; nothing
// matches a fragment directly (see ENGINES).
const (
	// gateNameGrammar is the bare-name half of the key space: the keys of
	// gates.json. parseConfig is the boundary that enforces it.
	gateNameGrammar = `[a-z][a-z0-9_]*` //nolint:unused // GO-3 scaffold: wired by GO-3-3

	// moduleRelSegment is one path element of a module root: Go's module-path
	// element set, with a leading '.' refused so `.`, `..` and dot-directories
	// cannot appear inside a path.
	moduleRelSegment = `[A-Za-z0-9_~-][A-Za-z0-9_.~-]*` //nolint:unused // GO-3 scaffold: wired by GO-3-3

	// moduleRelGrammar is what discoverModules may emit as Module.Rel: the
	// bare "." for the worktree root, or slash-separated segments.
	moduleRelGrammar = `[.]|` + moduleRelSegment + `(?:/` + moduleRelSegment + `)*` //nolint:unused // GO-3 scaffold: wired by GO-3-3

	// gateKeyGrammar is the whole key: what gateKey() produces from a legal
	// name and a legal rel, and exactly what validateRunState admits.
	gateKeyGrammar = gateNameGrammar + `(?::(?:` + moduleRelGrammar + `))?` //nolint:unused // GO-3 scaffold: wired by GO-3-3
)

// goPattern renders a fragment for Go's regexp: \A and \z are RE2's text
// anchors and no flag loosens them.
func goPattern(fragment string) string { return `\A(?:` + fragment + `)\z` } //nolint:unused // GO-3 scaffold: wired by GO-3-3

func goGateNamePattern() string  { return goPattern(gateNameGrammar) }  //nolint:unused // GO-3 scaffold: wired by GO-3-3
func goModuleRelPattern() string { return goPattern(moduleRelGrammar) } //nolint:unused // GO-3 scaffold: wired by GO-3-3
func goGateKeyPattern() string   { return goPattern(gateKeyGrammar) }   //nolint:unused // GO-3 scaffold: wired by GO-3-3

// schemaGateKeyPattern renders the grammar for the JSON Schema `pattern`
// keyword. Never place this string in a schema without the `not` keyword
// schemaGateKeyPropertyNames adds beside it (see ENGINES).
func schemaGateKeyPattern() string { return `^(?:` + gateKeyGrammar + `)$` } //nolint:unused // GO-3 scaffold: wired by GO-3-3

// schemaGateKeyPropertyNames is the exact fragment config/run-state.schema.json
// must carry at gates.propertyNames. Compared, never transcribed: the seal
// reads the file and deep-equals it to this value.
func schemaGateKeyPropertyNames() map[string]any { //nolint:unused // GO-3 scaffold: wired by GO-3-3
	return map[string]any{
		"pattern": schemaGateKeyPattern(),
		"not":     map[string]any{"pattern": `\s`},
	}
}

// The three enforcers' compiled grammars, one per boundary. Package init
// compiles them, so an unsupported construct fails every test in this
// package, not one run of the tool.
var (
	gateNameRE  = regexp.MustCompile(goGateNamePattern())  //nolint:unused // GO-3 scaffold: wired by GO-3-3
	moduleRelRE = regexp.MustCompile(goModuleRelPattern()) //nolint:unused // GO-3 scaffold: wired by GO-3-3
	gateKeyRE   = regexp.MustCompile(goGateKeyPattern())   //nolint:unused // GO-3 scaffold: wired by GO-3-3
)

// checkGateName reports why name is not a legal gate name, or "" when it is.
// Exactly gateNameRE. parseConfig calls it for every key of gates.json before
// any plan exists; the message names the offending key and the grammar.
func checkGateName(name string) string { //nolint:unused // GO-3 scaffold: wired by GO-3-3
	panic("GO-3: not implemented")
}

// checkModuleRel reports why rel is not a legal Module.Rel, or "" when it is.
// Exactly moduleRelRE. discoverModules calls it for every module root it
// derives; a failure is INVALID_INPUT (exit 3) naming the path — the key is
// never formed.
func checkModuleRel(rel string) string { //nolint:unused // GO-3 scaffold: wired by GO-3-3
	panic("GO-3: not implemented")
}

// checkGateKey reports why key is not a legal run-state gate key, or "" when
// it is. Exactly gateKeyRE, phrased for a human: an empty key, a bad name, a
// bad rel and a second ':' each get their own sentence. validateRunState
// calls it per key (read side); mergeGates calls it per result key and
// writes nothing when any fails (write side; finish then exits exitInvalid).
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
