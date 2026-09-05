# claude-workflow — Claude Context

Guidance for Claude and for dispatched agents working in this repository.

CLI tools under `cmd/` and libraries under `shared/` have separate Go modules.
Keep the sibling directories together when building: state writers use a local
module replacement for the shared updater. There is no root module, so run
the module loop in `.dispatcher.yaml`, not `go test ./...` at the root.

---

## A CONTRACT STATES RULES. IT DOES NOT REPORT MEASUREMENTS.

Read this before writing a scaffold. It is the most expensive lesson this repo
has recorded.

On 2026-08-30 the four `dogfood-go` scaffolds went through **15 cross-family
panel rounds and landed one unit**, at $73. The reviewers were right every
time, and the agents were fixing what they were told. The defect was in the
contracts themselves: each one asserted normative rules **and measured facts
about the tree at the moment it was written**, in the same breath.

The clearest case. `CanonicalRecipe` declared `Trimpath: false` while
`CanonicalBuild` passed `-trimpath` — a real contradiction, correctly found by
all three reviewer families. The operator ruled it and the agent fixed it. That
correct fix immediately falsified the neighbouring sentence, which asserted
that *all nine tracked binaries already stamp this exact recipe*. It had been
true when written. It could not survive the recipe being right.

That is not bad luck. **A measured fact in a contract is a claim the next
correct change will break**, so a contract full of them cannot converge: every
fix earns a new finding, forever.

So, when you write a contract, an interface docstring, or a scaffold:

* State what the code **must do** — invariants, pre/postconditions, closed
  sets, error semantics. These are checkable and they stay true.
* **Do not state what the tree currently contains.** No counts of files,
  binaries, call sites or rows. No "all nine X already do Y". No "this is the
  only caller". Those belong in the commit message, where a reviewer reads
  them once, or in a TEST, where they fail loudly when they stop being true.
* **A claim you cannot enforce is a defect, not documentation.** A doc comment
  cannot give a type a property. GO-1-1 spent three rounds asserting that
  `ExitCode`'s zero value was invalid while `exitOK` stayed `0` and stayed in
  `DeclaredExitCodes`. The sentence was false, and false is worse than absent
  because a reader can rely on it.
* **Withdrawing an over-strong claim is a real fix.** If a set is not closed,
  stop calling it closed. If byte-equality cannot distinguish two states, stop
  saying it can. Narrowing a promise to what the code honours is a legitimate
  answer to a review finding — say so explicitly when you do it.

The test: *if someone fixed the defect this contract describes, would any
sentence here become false?* If yes, that sentence is a measurement. Move it.

### Fixing a review finding: CUT, don't rewrite

Measured over twenty-one panel rounds on one contract. When a reviewer finds an
over-strong or false claim, the repair is to **delete the sentence**, and to add
nothing unless a rule would otherwise be lost.

The reason is a rate, not a preference. Across these rounds, **roughly half of
each round's findings were manufactured by the previous round's fix** — the
replacement sentence reached one clause further than the code could honour, or
contradicted a neighbour. Splitting the 974-line file into six did not change
that rate; it only made the findings local enough to see.

The pass that tested deletion is the evidence:

| repair style | edits | new findings caused |
|---|---|---|
| pure cut | 7 | **0** |
| wrote a replacement clause | 2 | 2 |

Every new defect traced to a sentence that was added. Not one traced to a
deletion. A cut cannot introduce a claim.

So: when a claim is too strong, remove it. Do not qualify it — a qualified
over-claim is a new claim, and it will be back next round wearing different
words. "No host default reaches the stamp" was narrowed to "reaches a COMPARED
field" and was still false; the fix was to stop saying it. If a rule genuinely
dies with the sentence, write the smallest replacement you can and expect it to
be the next finding.

Two corollaries:

* **Cut cleanly.** A careless deletion that leaves "It does NOT close the
  ENVIRONMENT: not closed. A variable nobody enumerated —" is worse than the
  claim it removed. Re-read the whole paragraph after cutting.
* **Grep the dependents.** Withdrawing a claim in one place leaves every
  sentence that relied on it false. Four separate rounds were spent on exactly
  this: the field left behind its doc, the enumeration left behind the rule it
  enumerated. Fixing a definition is half the work.

## Comments: purpose and constraints, not rationale

Same rule as `claude-dispatcher`'s, and it exists for the same reason — every
later agent pays for excess prose in context, and long prose is what goes
stale.

* Comments state **purpose, intent, and non-obvious constraints** — the facts
  whose absence would let someone break the code.
* **Rationale, measurements, rejected alternatives and rulings go in the commit
  message**, not inline.
* The test: *would a future agent break this code without this comment?* If it
  is justification aimed at a reviewer, it belongs in the commit message.

## The gate

`.dispatcher.yaml` runs `go test ./...` in each `cmd/*/` and `shared/*/` module. Two
deliberate omissions, both measured:

* **Not `go build`** — the compiled binaries are tracked in this repo, so
  building rewrites them and leaves the worktree dirty. The dispatcher refuses
  test evidence from a dirty tree, so a building gate blocks every task.
* **Not `go vet`** — a gate runs only checks that are GREEN ON A CLEAN
  BASELINE, or it cannot tell the change under test from the dirt it inherited.
  Whether vet meets that bar here, and what it would take, is in
  `docs/DECISIONS.md`; adding it is correct as soon as it does.

`go test` compiles every package in the pattern regardless: a type error in a
non-test file exits 1.

Note `go test` exits 0 for a module with **no test files at all**, so the gate
cannot tell "tests pass" from "there are no tests". At scaffold stage that is
expected — the seals task adds them — but do not read a green gate as coverage.

## Git commit style

Carried from `evenplay-mono`, whose convention this org follows.

**No attribution anywhere** — not in commits, not in file headers, not in
architecture docs, and **not in symbol names**. No `Co-Authored-By`, no author
names, no class or function named after whoever or whatever wrote it. Code and
docs belong to the team; attribution creates silos and a false sense of
ownership.

Naming a symbol after the thing it INTEGRATES WITH is not attribution and is
fine — `ClaudeReviewer` names the CLI it drives, the way `PostgresStore` names
a database. Naming one after its author is not.

**Keep the provenance, drop the credit.** The one thing a `Co-Authored-By`
trailer was buying is the ability to ask, months later, *how* a change was
produced — dispatched under a panel, or hand-edited — which is an audit
question, not a credit question. Record the process instead of the person:

```
type(scope): short description [TASK-KEY]

Dispatched-Task: TASK-KEY
Dispatcher-Run: <run id>
```

That travels with the commit into every clone and names nobody. It matters
because the richer provenance — journal, summaries, task rows — lives in
`runs_dir`, which is deliberately OUTSIDE the repo and disposable; lose that
directory and a commit's origin is unrecoverable without this.

The `[TASK-KEY]` in the subject is already load-bearing, not decoration:
`dispatcher audit`'s `landed-by-message` route greps for the bracketed form to
tell a landed-and-pruned branch from work that went missing. Bare mentions do
not count, so keep the brackets.

```
type(scope): short description

Optional longer description of what and why.
```

Types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`.

This is where the rationale, the measurements and the rejected alternatives go
— read once by a reviewer, rather than inline where every later agent re-reads
them forever.

## Error handling

Go, and this repo is seven CLI tools whose failures land in a dispatcher's
gate output, so a swallowed error becomes a green run that proved nothing.

**Wrap with `%w` and a sentinel. Never discard.**

```go
// CORRECT — the chain survives errors.Is / errors.As
var ErrNoStamp = errors.New("artifact has no stamp")
return fmt.Errorf("read stamp for %s: %w", path, ErrNoStamp)

// WRONG
return errors.New("artifact has no stamp")        // no sentinel to match on
return fmt.Errorf("read stamp: %v", err)          // %v drops the chain
_, _ = examine(ctx, path)                         // silently discarded
_ = err
```

An error a caller cannot match on is an error a gate cannot classify.

## Testing standards

Consider each type for every change; the first two are where this repo's
defects have actually lived.

* **Unit tests** — required for all logic.
* **Property-based tests** — for anything with an invariant: recipe/stamp
  identity, state-machine transitions, encode/decode round-trips, comparison
  oracles. `pgregory.net/rapid` is evenplay-mono's choice and is NOT yet a
  dependency of any module here, so adding it is a deliberate act; a
  table-driven test over generated inputs is a fine substitute. Either way this
  is the right home for the measured facts a contract must not assert — a test
  fails loudly when the world changes, where prose rots silently.
* **Concurrency tests** — required for shared state and anything touching a
  process-global (`os.Chdir`, `flag.CommandLine`, package-level caches).

## Risk classification and review depth

`.agent/risk-paths.json` classifies changed paths and the dispatcher reserves
the cross-family panel for what needs it. Adapted from `evenplay-mono`'s table
for this repo's surface:

| Risk | Applies to | Review depth |
|---|---|---|
| **High** | The gates themselves — `cmd/gates`, `cmd/classify`, `cmd/reviewer`; anything that decides whether other work ships | Full cross-family panel; a fail-open here ships unreviewed code everywhere |
| **Medium** | `cmd/iterate`, `cmd/recheck`, `cmd/repro`, build-artifact tooling | Panel on a real diff; single reviewer for a narrow one |
| **Low** | Docs, test helpers, config | Self-review |

**When in doubt, go one level higher.**

## Before claiming work complete

* The gate is green — `.dispatcher.yaml`'s module loop, on the
  **committed** tree. Evidence from a dirty worktree is not evidence.
* Every claim in your summary is one you measured. If you did not run
  something, say you did not run it; a placeholder where a result belongs is
  refused by the dispatcher and is worse than an honest gap.
* A review finding may be WRONG. Check it and say so with a measurement rather
  than complying silently — on 2026-08-30 a blocking finding claimed Go's
  `regexp` rejects `(?:...)`, which it does not.

## Dispatched work

Task lists live in `features/<epic>/tasks.yaml` **in this repo**, because the
dispatcher derives the repo from the tasks file's own location. A list kept
elsewhere cannot be pointed here, whatever its header says.

The contract-first protocol (scaffold → seals → bodies → adjudicate) and the
deviation model are documented in `claude-dispatcher`'s
`docs/contract-first-deviation-model.md`. The rule that matters most here: the
skeleton is authoritative, and to change a shared contract you record a
DEVIATION rather than forcing a fit.
