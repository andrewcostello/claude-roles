# Comment Style — what code comments say, in every language

**Scope:** every comment and doc comment in every repo — Go, TypeScript,
Python. Loaded by the Tasker when writing or rewriting code, and by
reviewer seats as a review dimension. Sibling of `review-language.md`:
that file governs how findings read; this one governs how comments read.

**Why:** machine-generated and legacy comments narrate change history and
cite tickets, spec sections, and plan documents no reader has. They are so
verbose that engineers — especially ESL readers — skip them and read the
functions instead. A comment nobody reads still costs review time, and it
rots.

**Canonical copy:** this file. Repos that run dispatcher epics vendor a
copy (for example `live-gaming-platform/docs/comment-style.md`) so
worktrees, CI, and human PR reviewers have it in-repo; a vendored copy
names this file as its source in its header. Change this file first, then
refresh vendored copies.

---

## 1. What a comment may say

A comment earns its place only by stating something the code cannot:

- **Purpose**: what this thing is for, in one or two sentences.
- **Contract**: what the caller must guarantee, what is guaranteed back,
  error semantics, nil/None/undefined behavior.
- **Invariants**: "the mutex guards both maps", "keys are never reused",
  units, ordering requirements.
- **Concurrency**: what is safe to call from multiple threads/goroutines/
  async contexts, what is not.
- **Why, when surprising**: the one-line reason the code does the
  non-obvious thing. Only when a maintainer would otherwise "fix" it.

## 2. What a comment must never say

Delete on sight:

- **Change history.** "X was added with the TimerService landing",
  "firing moved to the singleton", "mirrors the pre-refactor shape".
  Version control is the history. Comments describe the code as it is
  today.
- **Ticket, plan, and spec references.** `(T22-T26)`, `§8.6.1`,
  `plan 2026-05-14-…`, PR numbers. A decision that needs a durable record
  goes in the repo's `docs/`, not inline.
- **Restating the code.** `// increment the counter` above `counter++`.
- **Narrating other modules.** Describe this side of the contract; name
  the other module and stop.
- **Speculation and alternatives considered.** "We could instead have…"
  is not documentation.

The mechanical check — must return nothing for files you own (works for
`//` and `#` comments alike):

```sh
grep -rlnE '§[0-9]|plan 20[0-9]{2}-|\(T[0-9]+(-T[0-9]+)?\)|was added|pre-refactor' <paths>
```

## 3. Plain English (write for ESL readers)

Same contract as `review-language.md`, applied to comments:

- **Short sentences.** Aim for under 20 words. One idea per sentence.
- **Active voice, present tense, named actor.** "BuildBinding rejects a
  nil client", not "a nil client will be rejected".
- **Plain words.** use, not utilize/leverage; start, not bootstrap;
  create, not instantiate; because, not owing to the fact that.
- **No idioms, jokes, or metaphors.** No "foot-gun", no "load-bearing".
- **No Latin abbreviations.** Write "for example" and "that is", not
  "e.g." and "i.e.".
- **One name per concept.** If the module doc says "binding", every
  comment says "binding" — never a synonym for the same thing.
- **Expand an acronym once** per package/module doc, then use it freely.

Complex logic in simple English: name the actors, then state what happens
in order, one step per sentence. If a comment needs more than three
sentences, use a list — or the logic deserves a better function name.

## 4. Length budgets

| Comment | Budget |
|---|---|
| Function or method doc | 1–3 sentences |
| Type/class doc | 1–4 sentences |
| Field, variable, or parameter doc | 1–2 sentences |
| Inline comment | 1–2 lines |
| Package/module doc | up to ~15 lines |

Going over budget is allowed only for a real contract: a concurrency
protocol, a state machine, a wire format. Never for history or narrative.

## 5. Directive comments are code. Never touch them.

Compilers and tools read these; editing one silently changes what
compiles, lints, or ships:

- **Go**: `//go:build`, `// +build`, `//go:generate`, `//go:embed`,
  `//go:noinline` and friends, `//nolint:…`, `//export`, cgo preambles.
- **TypeScript**: `// eslint-disable*`, `// @ts-ignore`,
  `// @ts-expect-error`, `// @ts-nocheck`, triple-slash directives
  (`/// <reference …>`), `// prettier-ignore`.
- **Python**: `# type: ignore`, `# noqa`, `# pragma: no cover`,
  `# fmt: off/on`, `# pylint: disable=…`, encoding declarations, shebangs.

A cleanup task leaves every directive byte-identical.

## 6. Tests

- A test gets a comment only when its name cannot carry the intent alone;
  then it is one sentence: what the test proves.
- Table-driven / parametrized cases carry their intent in the case name.
- Test helpers follow the same doc rules as production code.

## 7. Per-language format

### Go — godoc convention

The standard: [Go Doc Comments](https://go.dev/doc/comment),
[Effective Go — Commentary](https://go.dev/doc/effective_go#commentary).

- Every exported identifier gets a doc comment, directly above the
  declaration, no blank line, **beginning with the identifier's name**:
  `// BuildBinding validates deps and returns the domain binding.`
- The first sentence stands alone — `go doc` and pkg.go.dev show it in
  lists.
- One package comment per package (`doc.go` when the package has more
  than a few files), starting `// Package name …`. It defines the
  package's domain terms once.
- Complete sentences, period at the end. gofmt formats doc comments;
  stay gofmt-clean. Doc comments support lists and links — use a list
  for a lifecycle or state machine.

### TypeScript — TSDoc

- Exported symbols get a `/** … */` doc comment. First sentence is the
  standalone summary the editor tooltip shows.
- Let the types speak: no `@param x - the x` that restates a typed
  signature. Add `@param`/`@returns` only for meaning the type cannot
  carry (units, valid ranges, ordering, who owns the object).
- `@throws` for error contracts; `@deprecated` with the replacement
  named. No `@author`, no `@since` — that is history.
- Inline comments use `//`, same content rules as everywhere.

### Python — PEP 257, Google style

- Public modules, classes, and functions get a docstring. First line is
  a one-sentence summary that stands alone, imperative mood
  ("Return the pool…", not "Returns the pool…" is fine either way —
  pick one per repo and keep it).
- Google-style sections (`Args:`, `Returns:`, `Raises:`) — they read as
  plain English, which suits ESL readers better than reST field lists.
- Type information lives in annotations, not docstrings; an `Args:`
  entry adds meaning (units, ranges, ownership), never repeats the type.
- `#` inline comments follow the same content rules as everywhere.

## 8. Cleanup-task protocol

A comment-cleanup task changes comments and nothing else:

- No renames, no code movement, no logic changes, no import changes.
- The diff shows only comment lines and blank lines adjacent to them.
- Directives (section 5) stay byte-identical.
- A real bug or wrong name found while reading is reported as a deferred
  finding in the task summary, never fixed in the cleanup change.

## 9. Worked example (real code, Go)

Before:

```go
// PoolProvider was added with the host TimerService landing (T22-T26): the
// resolved *pgxpool.Pool backs pm_timers persistence (§8.6.1) and the
// EnsureSchema closure returned in BuildBindingResult. It is a closure rather
// than a captured *pgxpool.Pool because the production scaffold populates the
// pool only at cqrsapp Start (well after adapter construction at buildApp
// time); BuildBinding runs at registrar Start, after pgConn Start, so calling
// the provider there resolves the live, post-Start pool. Production wiring
// passes the *PGConn.Pool method value (`pgConn.Pool`).
```

After:

```go
// PoolProvider returns the database pool that stores process manager timers.
// It is a function, not a pool: the pool exists only after the app starts,
// and BuildBinding runs after that point. Production passes pgConn.Pool.
```

Everything the reader needs survives. The history, ticket numbers, and
section references are gone; version control still has them.
