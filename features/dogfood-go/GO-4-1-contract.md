# GO-4-1 — scaffold contract: a function contracted, implemented and sealed, and called by nothing

Status: **DELIVERED.** The base-revision question this contract was written
under is settled: the operator ruled that GO-4 builds on its lineage rather
than on `main`, and this branch is based on the B1+G2 merge, which carries
`cmd/gates/preserve.go`, `cmd/iterate/preserve.go` and `VerifyPreservation`,
and whose suite is green across all seven modules. The stub files are
COMMITTED on this branch; they are not a patch awaiting a decision.

## 1. The base

This branch builds on the merge that joins the B1 and G2 lineages, not on
`main`, and not on either lineage tip alone:

* `main` does not contain the subject at all.
* The G2 tip this contract originally named has a red `cmd/classify`, so no
  task could pass its mechanical gate there.
* The merge carries both subjects and is green.

The lineages stay diverged; converging them is a separate decision with its own
conflict in `cmd/classify/main.go`, and nothing in this unit depends on it. The
concrete revisions are in this branch's git history and in the run journal,
deliberately not restated here — a contract that names the tree it was written
against is false the moment the tree moves, and that failure mode cost this
feature 15 panel rounds. See CLAUDE.md, "A CONTRACT STATES RULES. IT DOES NOT
REPORT MEASUREMENTS."

> **Sections 2, 3 and 11 are DATED EVIDENCE, not contract terms.** They record
> what was measured, at the revision named in each heading, because this unit's
> task is a defect re-measurement. Nothing downstream may treat them as rules:
> they describe a tree at a moment, and any correct fix moves that tree. The
> enforceable contract is sections 4 through 10.

## 2. Re-derived at `83b0b97` (git-archive copy, never a worktree copy; host go1.26.0)

**Counting method, stated because it is the whole disagreement.** Two counts
give different numbers in these files:

- *names it*: every occurrence of the identifier. Both doc comments open with
  the function's own name (Go's convention), so a name count certifies nothing.
- *calls it*: `VerifyPreservation(` call expressions, attributed to the
  enclosing `func`. This is the analyzer's count and the one used below.

| module | non-test occurrences | of which declaration / comments / **call expressions** | production callers |
|---|---|---|---|
| `cmd/gates` (`preserve.go:997`) | 9 | 1 / 8 / **0** | none — `main.go` never mentions it |
| `cmd/iterate` (`preserve.go:1553`) | 9 | 1 / 8 / **0** | none — `preserve.go:2013` says so in words |

Seals that **call** it, by the caller's own body (no call is routed through a
helper — both `preserve_seal_helpers_test.go` files mention it only in comments):

| module | seal | calls in body |
|---|---|---|
| `cmd/gates` | `TestSeal_G1_VerifyPreservation_ReportsEditsOutsideTheLicensedPaths` (`:358`) | 2 |
| `cmd/gates` | `TestSeal_G1_VerifyPreservation_TreatsADeletionUnderGatesAsAViolation` (`:438`) | 4 |
| `cmd/gates` | `TestSeal_G1_VerifyPreservation_RefusesWhatItCannotCheck` (`:588`) | 2 |
| `cmd/iterate` | `TestSeal_G2_Licence_GatesIsLicensedForGatesAndForbiddenForIterate` (`:429`) | 1 |
| `cmd/iterate` | `TestSeal_G2_VerifyPreservation_CatchesEveryArrayMalformationFromTheLicenceAlone` (`:751`) | 2 |
| `cmd/iterate` | `TestSeal_G2_VerifyPreservation_DerivesTheLicenceFromTheEditListNotTheOutput` (`:844`) | 2 |
| `cmd/iterate` | `TestSeal_G2_VerifyPreservation_RefusesWhatItCannotCheckOnItsOwnTerms` (`:929`) | 3 |

**SEVEN (seal, subject) pairs: 3 + 4.** The recorded "5 × iterate" counted a
seal twice under two descriptions (the D6 adjudication already corrected it;
`tests/fixtures/d6_g2_preserve/PROVENANCE.md` in claude-dispatcher). Reproduced
here by the sweep itself: seven findings over two subject keys.

Both modules: `gofmt -l` empty, `go vet` clean, `go test ./...` **ok** (gates
5.2 s, iterate 2.6 s) on this host. Unlike GO-1's module, nothing here is
environment-red, so "unchanged" below means green = green.

## 3. The sweep, reproduced (not trusted)

`call_site_reachability.check_tree` at `claude-dispatcher @ 40624c3`
(`feat/D1-role-protocol` lineage, `ANALYZERS` = the Go row), over
`git archive 83b0b97` extracted to a scratch directory, host go1.26.0:

    5.6 s
    258 seals examined · 273 roots · 317 findings
    OK 257 · BREACH 37 · REPORT 4 · ACCEPTED 0 · ABSTAIN 19
    13 unresolved calls · 68 unanalyzed paths · 3 subject gaps

Every figure in the task reproduces. Two notes on the figures themselves:

- `13 unresolved calls` is `unresolved_call_count`, the non-test holes.
  `build_call_graph` lists **16**; the other three are in test files
  (`cmd/classify/repair_seal_test.go:1451`, `cmd/classify/seal_helpers_test.go:327`,
  `cmd/iterate/preserve_seal_test.go:1065`) and are outside every production
  closure by construction.
- `68 unanalyzed paths` is for a tree with no `.git`. The same tree inside a
  throwaway `git init` reports 211 — the object store is counted. Compare
  sweeps on archives only.

The seven pairs, verbatim from the report:

| subject | seal | `reach` | reason | disposition |
|---|---|---|---|---|
| `cmd/gates.VerifyPreservation` | `…_RefusesWhatItCannotCheck` | UNDECIDED | DYNAMIC_EDGE | **ABSTAIN** |
| `cmd/gates.VerifyPreservation` | `…_ReportsEditsOutsideTheLicensedPaths` | UNDECIDED | DYNAMIC_EDGE | **ABSTAIN** |
| `cmd/gates.VerifyPreservation` | `…_TreatsADeletionUnderGatesAsAViolation` | UNDECIDED | DYNAMIC_EDGE | **ABSTAIN** |
| `cmd/iterate.VerifyPreservation` | `…_Licence_GatesIsLicensedForGatesAndForbiddenForIterate` | FROM_TESTS_ONLY | — | **BREACH** |
| `cmd/iterate.VerifyPreservation` | `…_CatchesEveryArrayMalformationFromTheLicenceAlone` | FROM_TESTS_ONLY | — | **BREACH** |
| `cmd/iterate.VerifyPreservation` | `…_DerivesTheLicenceFromTheEditListNotTheOutput` | FROM_TESTS_ONLY | — | **BREACH** |
| `cmd/iterate.VerifyPreservation` | `…_RefusesWhatItCannotCheckOnItsOwnTerms` | FROM_TESTS_ONLY | — | **BREACH** |

The gates detail, verbatim: *"2 call(s) inside the production closure could not
be resolved, first at cmd/gates/main.go:1468 (call through the func-typed
variable "cancel", whose value this walk cannot pin to a single literal); one of
them may be the missing call site. the hole set is SCOPED to this subject's
import component"*. The iterate detail: *"… is reached from
cmd/iterate.TestSeal_G2_… and from no production root; the seal proves it
behaves and nothing proves it runs"*.

## 4. WHY `cmd/gates` abstains where `cmd/iterate` does not — named to the call

`check_subject` reaches the FROM_TESTS_ONLY verdict (step 4) only past step 3,
which abstains when the subject's production closure, scoped to its import
component (`holes_in_scope`), contains any entry of `unresolved_calls`.

**The two holes that silence `cmd/gates`**, both in the subject's own package and
both in the production closure from `main`:

| site | function | binding | production chain |
|---|---|---|---|
| `cmd/gates/main.go:754` `defer cancel()` | `runOne` | `:753 ctx, cancel := context.WithTimeout(context.Background(), timeout)` | `main → run → execute → executeOne → runOne` (`:197`, `:244`, `:643`, `:679`) |
| `cmd/gates/main.go:1468` `defer cancel()` | `runCmd` | `:1467 ctx, cancel := context.WithTimeout(context.Background(), timeout)` | `main → run → execute → executeOne → evaluateBenchRelative → runCmd` (`:691`, `:1153/:1157/:1173/:1178`) |

The walk cannot name `cancel`'s target because it fails the SOLE-BINDING FUNC
LITERAL rule's obligation 2: the binding is a **tuple binding** from a
multi-valued call, so no literal is named, and the value is produced out of
tree. The "out-of-tree provenance" argument for clearing it was refused by the
D6 adjudication with three measured counterexamples (`sync.OnceFunc`,
`iter.Pull`, `httptest.NewServer`), so the hole is honest and stays.

**Why `cmd/iterate` does not abstain.** Its five holes at `83b0b97`'s fixture
(`setMember` at `preserve.go:1445/:1458/:1462/:1466/:1470`, a closure in
`ApplyRoundRecord`) were all cleared by that same rule — `setMember := func…`
is a sole-binding literal, read everywhere, address never taken. With its
component's hole set empty, step 3 passes and step 4 reports the dark
function. **And `cmd/gates`' holes do not leak into `cmd/iterate`** because
`holes_in_scope` scopes the hole set to the subject's import component; before
that landed the answer was 0 of 7, measured in D5's own record.

**The whole tree's 13 holes are ONE idiom, twelve times.** Measured over the
non-test `.go` files at `83b0b97`:

| site | binding |
|---|---|
| `cmd/deepseek/main.go:46` (`main`), `:256` (`toolGrep`) | `ctx, cancel := context.WithTimeout(context.Background(), …)` |
| `cmd/gates/main.go:754` (`runOne`), `:1468` (`runCmd`) | same |
| `cmd/recheck/main.go:417` (`runVerifier`) | same (`:398`) |
| `cmd/repro/main.go:330` (`cmdRun`) | same (`:322`) |
| `cmd/reviewer/main.go:315` (`gitOutput`), `:449` (`dispatchReviewers`), `:1235` (`buildSiblingTraces`) | same, parent `context.Background()` |
| `cmd/reviewer/main.go:2138`, `:2179` (`runScouts`), `:2523` (`runDeepseekScouts`) | same shape, parent is a **`ctx` parameter** |
| `cmd/reviewer/main.go:1405` (`preloadSourceFiles`) | `filter`, a func-typed variable |

Twelve of thirteen are `context.WithTimeout` + `defer cancel()`, in five of the
seven modules. That is the fact the "honest fix" question turns on.

## 5. What a correct analyzer must report for each of the seven pairs

Three columns, because "today", "correct today" and "after the body" differ:

| pair | today (measured) | correct answer today | after GO-4-3 (predicted, unmeasured) |
|---|---|---|---|
| gates × 3 | UNDECIDED / DYNAMIC_EDGE → ABSTAIN | FROM_TESTS_ONLY → BREACH: the function is dark; the abstention is the mechanism declining, not a verdict about the code | FROM_PRODUCTION / RESOLVED → OK via `main → run → finish → mergeGates → admitForWrite → VerifyPreservation`, all `direct` edges |
| iterate × 4 | FROM_TESTS_ONLY → BREACH | as reported | FROM_PRODUCTION / RESOLVED → OK via `main → cmdRun → recordAndReport → appendRound → admitForWrite → VerifyPreservation` (and the `d.Stop` branch of `cmdRun`) |

Two consequences the body and the adjudicator need:

- **After the caller lands, the gates abstention stops mattering for this
  subject.** Step 1 (a found production path) precedes step 3; a found path is
  found regardless of holes. So GO-4-3's pass condition — "stops being
  FROM_TESTS_ONLY" — is reachable for both modules without touching the
  analyzer. The abstention remains a live finding about the mechanism's ability
  to report *absence* for any other `cmd/gates` subject, and for four more
  modules.
- **A row driven only through the report is green in `cmd/gates` for the wrong
  reason today.** A GO-4-2 row must read `reach`, and treat UNDECIDED as
  neither pass nor fail (the task's second trap).

## 6. The honest fix: analyzer, module, or abstention rule

**Abstention rule — refused.** Narrowing step 3 is claiming an unresolved call
is harmless. That is the fail-open shape this effort refuses and the standing
ruling ("better to resolve the calls than to excuse them") already says so.
Nothing here proposes it.

**Module — available, and not the honest first answer.** `runOne`/`runCmd`
could drop the func-value call (`exec.Command` + `time.AfterFunc(timeout,
func() { _ = cmd.Process.Kill() })` + `timer.Stop()`: a literal handed to an
out-of-tree call is walked under its owner, and `Stop` is a named out-of-tree
method — no hole). Cost: production code reshaped to an analyzer's limit, a
subtle semantic change to how a timed-out gate is killed, and it clears 2 of 13
holes — the same idiom stays in `deepseek`, `recheck`, `repro` and `reviewer`.
Recorded as the fallback GO-4-4 may choose; not recommended.

**Analyzer — recommended, as a resolution and not an excuse.** The refused
provenance claim failed because in-tree values flowed INTO the out-of-tree call
(`OnceFunc(inTreeA)`, `Pull(seq)`, `NewServer(h{})`) and out again as the func
value. The discriminator in all three counterexamples is the **arguments**.
Proposed rule, *Predicted (unmeasured)*, for D5/D6 to rule — both files are on
`FLOOR_GLOBS` in claude-dispatcher and are not this repository's to edit:

> A func-typed local whose sole binding is a tuple binding from a call to a
> named out-of-tree function, where every argument is a basic-typed value or
> itself a named out-of-tree call with no in-tree mention, and obligations 3 and
> 3b hold, is a fully answered question ("this reaches nothing in the tree"),
> not a hole.

Under it, `context.WithTimeout(context.Background(), d)` clears — 9 of the 12
`cancel` sites, including both in `cmd/gates` — and the three whose parent is
a `ctx` **parameter** DECLINE (an interface-typed parameter may carry an in-tree
implementation whose methods the cancel closure invokes). `filter` declines.
Soundness rests on the invariant the walk already relies on: an in-tree func
value that escapes into out-of-tree code is mentioned at the escape site and
carries a `reference` edge. Whether that invariant holds for *every* escape
route is D6's to measure, the way it measured the refusal.

## 7. The seam GO-4-2 seals and GO-4-3 fills

**Subject.** The write path in each module: today `LoadRunStateDocument →
Apply{GateResults,RoundRecord} → os.WriteFile`, with the checker never asked
(`cmd/gates/main.go:1331-1352`, `cmd/iterate/main.go:465-504`). The seam is
one function per module, identical in shape:

```go
func admitForWrite(original, produced []byte, edits []Edit) (admitted []byte, violations []Divergence, err error)
```

**Contract:**

| input | admitted | violations | err |
|---|---|---|---|
| `VerifyPreservation(original, produced, edits, FidelityPathwise)` → `([]{}, nil)` | `produced`, byte-identical | nil | nil |
| → `(vs, nil)` with `len(vs) > 0` | nil | `vs`, unchanged | wraps `errUnverifiedWrite` only |
| → `(nil, cerr)` (could not check: unparseable document, bad edit list, refused level) | nil | nil | wraps `errUnverifiedWrite` AND `cerr` |

- The level is fixed at `FidelityPathwise`, not a parameter (a parameter is a
  way to pass `FidelityKeySet` and certify a null-reattached document).
- `err != nil` on **every** refusal, so a caller that checks only `err` never
  writes what the checker refused — the implicit-state trap
  (`skills/explicit-state.md`) is closed at the type.
- The wiring is `Apply… → admitForWrite → os.WriteFile`; on refusal nothing is
  written and the file is byte-identical, because the decision precedes the
  write.
- **What a refusal changes about the tools, stated so nobody adds it
  carelessly** (the G1/G2 rulings: "calling the verifier on the write path
  changes what gates and iterate do"). Today `finish` turns a `mergeGates`
  error into a WARNING and lets the gate tally decide the exit — exit 0 over an
  unwritten state is possible; `recordAndReport` does the same with
  `verdictExit`. A refusal here is the tool's own defect (the apply step
  produced what its own licence forbids), not an input error. GO-3-1's contract
  on the sibling branch already rules that `finish` treats any `mergeGates`
  error as terminal and never returns 0 over an unwritten state (GO-3-3 owns
  that change). **GO-4-3 does not change either caller's exit rule**: it wires
  the seam and reports the interaction; GO-4-4 rules whether iterate gets the
  GO-3 treatment.
- No exported surface changes; no type with methods (see §11 for why).

**Rows (for the seal author), each judged in the same call and each with the
mutation that must redden it:**

| row | module | input | must | reddened by |
|---|---|---|---|---|
| A | iterate | the recorded example: `edits = [Append@0 …]` (`g2Edits()`), `produced` hand-built with the record landed at `rounds[1]` | refuse; `violations` equal, element for element, to a direct `VerifyPreservation` call in the row; `errors.Is(err, errUnverifiedWrite)`; file untouched | admitting without calling the checker; calling it and discarding `violations` |
| B | both | the correct document (the licensed edits themselves diverge from `original`) | admit; bytes byte-identical to `produced` | an inline `Diverge`-only check with no licence (it refuses the licensed edit) |
| C | gates | `gates: {}` → populated (the container licence); and a gate record deleted under `gates` | admit the first; refuse the second naming the deleted path | an inline check that treats `gates` as a subtree licence |
| D | both | `produced` that does not parse | refuse with `violations == nil`, `errors.Is(err, errUnverifiedWrite)` and `err` also wrapping the checker's error | collapsing the two channels (reporting "could not check" as zero violations, or as violations) |
| E | both | a CHANGED-only corruption (`evidence_id` 2^53+1 → 2^53, the G2 measured loss) | refuse | deciding at `FidelityKeySet` |
| F | both, end-to-end from source after GO-4-3 | `mergeGates`/`appendRound` over a temp run-state | the sweep reports all seven pairs FROM_PRODUCTION; the row reads `reach`, and UNDECIDED is neither pass nor fail | un-wiring the seam |

**Residual, declared.** An implementation that calls `VerifyPreservation`,
discards its answer, and re-implements the licence faithfully inline passes
A–E. Rows B and C narrow that mutant to "reproduces `AllowedPrefixes`/
`LicensedPaths` and the container distinction exactly" — at which point it is a
second copy of the checker, which is a review finding and not a row's.

## 8. Caller or declaration

Both honest answers are open to GO-4-3; this contract recommends **a caller in
both modules** and sets the declaration form in case one module is declared:

- A `StagedDeclaration` (claude-dispatcher `call_site_reachability`) needs
  `test_id` and `subject_key` matching a finding exactly, a `wiring` naming the
  commit/ticket that WILL add the call, and a reason. It turns BREACH into
  ACCEPTED only — **it cannot touch an abstention**, so it is unavailable for
  the three `cmd/gates` pairs today. A declaration for `cmd/iterate` with no
  `wiring` is not a declaration, by the protocol's own ruling.
- The case for a caller is the measured one: the checker exists because the
  apply step's own refusals are "the only thing standing between a malformed
  edit list and a written document" (`cmd/iterate/preserve.go:2011-2017`,
  G2 ruling P1). A declaration would record that as intended.

## 9. Holes (for the plan author's `declares.holes`; Go is not mechanically hole-checked by `scaffold_shape.py`)

- `cmd/gates/checked_write.go::admitForWrite`
- `cmd/iterate/checked_write.go::admitForWrite`

Both return `errNotImplemented` (the module's own scaffold marker, as G1/G2's
stubs did); nothing calls them; the suite is unchanged; the sweep is unchanged
(§11).

## 10. Corrections and gaps in the record

1. "cmd/iterate 5 × its own" — four, by calls in the caller's body (§2).
2. The abstention detail names the COUNT and the FIRST hole only (`:1468`);
   `:754` appears in no finding. Already recorded in D6's enrolment note;
   still true at `40624c3`.
3. The task says "`Apply` writes via `os.WriteFile`". The writers are
   `mergeGates` and `appendRound`; `ApplyGateResults`/`ApplyRoundRecord`
   return bytes and never touch the disk.

## 11. The scaffold, as delivered

COMMITTED on this branch — it is no longer a patch awaiting a base decision,
and the runs-directory patch file it was once delivered as is gone with the
run that produced it.

The branch adds `cmd/gates/checked_write.go` and `cmd/iterate/checked_write.go`
and amends `preserve.go` in both modules. An earlier draft of this section said
the change "touches nothing else", which was true of the patch and false of the
branch; git is the authority on what it touches, and this sentence is not.

Verification recorded when it was written:

- `gofmt -l` empty; `go vet ./...` clean; `go test ./...` **ok** in both
  modules (5.2 s / 2.6 s) — green = green.
- `git apply --check` and `git apply` clean in a throwaway `git init` of a
  fresh archive; `go vet` clean after apply.
- **The sweep is unchanged**: 317 findings before and after, the same set by
  (seal, subject, reach); OK 257 · BREACH 37 · REPORT 4 · ACCEPTED 0 ·
  ABSTAIN 19; 13 holes; the seven pairs identical. `admitForWrite` appears in
  the graph as two symbols and produces no finding (no seal calls it).
  Output: `dispatcher-runs/…/GO-4-1/sweep-83b0b97-with-scaffold.txt`.

**A first shape moved the sweep, and that is why the seam is method-free.**
The first draft returned a `*writeRefusal` error type with an `Error()` method.
Measured: 317 → **322** findings, BREACH 37 → 39, ABSTAIN 19 → 22 — five new
(seal, `(*writeRefusal).Error`) pairs, because the walk emits one `interface`
edge per in-tree method named `M` for every `x.M()` on an interface-typed
receiver, and five seals call `.Error()` on an `error`. Neither module carries
any `Error()` method today, so the scaffold would have been the first. A
scaffold that adds three abstentions and two breaches to the report it is
judged by is not a scaffold; the shape was changed to `(admitted, violations,
err)` with no new types, and the sweep re-measured identical. Recorded for
GO-4-4 and for D5: **any `error` type added to a judged Go package costs one
finding per seal that calls `.Error()` on an interface**, which is a property of
the mechanism and not of the code.

The patch, verbatim (`cmd/iterate/checked_write.go` differs only in the names of
its callers — `ApplyRoundRecord`, `appendRound`, `recordAndReport / cmdRun`):

```go
// GO-4-1 SCAFFOLD — the door a produced run-state passes through on its way to
// disk. Contract and one stub; nothing calls it yet. The measurements behind
// every constraint here are in features/dogfood-go/GO-4-1-contract.md.
//
// CONSTRAINTS:
//
//   - admitForWrite is the ONLY path from ApplyGateResults' output to
//     os.WriteFile. GO-4-3 wires mergeGates as LoadRunStateDocument ->
//     ApplyGateResults -> admitForWrite -> os.WriteFile. A second route to the
//     file, or a caller that writes on a non-nil error, is a finding.
//   - It decides by VerifyPreservation(original, produced, edits,
//     FidelityPathwise) and by nothing else. The level is fixed, not a
//     parameter: a parameter is a way to pass FidelityKeySet and certify a
//     document that re-attached every lost key with a null.
//   - The ONLY admitting result is (produced, nil, nil), with produced
//     byte-identical. Every other outcome returns nil bytes and a non-nil
//     error wrapping errUnverifiedWrite — so a caller that checks only err
//     never writes what the checker refused.
//   - A refusal keeps VerifyPreservation's two channels apart. A check that
//     RAN and found violations returns them, unchanged, as the second value,
//     with err wrapping errUnverifiedWrite only. A check that COULD NOT run
//     returns nil violations and err wrapping BOTH errUnverifiedWrite and
//     VerifyPreservation's own error. Neither may be reported as the other.
//   - On refusal the run-state on disk is untouched, because the write comes
//     after the decision. The refusal's exit code is the caller's rule
//     (finish; GO-3-3 owns it) and is not decided here.
//   - No type with methods is added here: an in-tree `Error()` method is
//     attributed to every seal that calls `.Error()` on an interface value
//     and would add findings to the reachability report this unit is judged
//     by. Keep the seam method-free.
//   - No exported surface changes. The stub and the sentinel are unexported.
package main

import (
	"errors"
	"fmt"
)

// errUnverifiedWrite is wrapped by every refusal, so a caller can distinguish
// "the checker said no" from an I/O error with errors.Is and never by prose.
var errUnverifiedWrite = errors.New("run-state write refused: preservation not verified")

// admitForWrite returns (produced, nil, nil), byte-identical, iff
// VerifyPreservation(original, produced, edits, FidelityPathwise) returns an
// empty violation list and a nil error. Otherwise it returns nil bytes, the
// violations the check found (nil when the check could not run), and an error
// wrapping errUnverifiedWrite.
//
// HOLE — GO-4-3 fills it and routes mergeGates through it.
func admitForWrite(original, produced []byte, edits []Edit) (admitted []byte, violations []Divergence, err error) { //nolint:unused // GO-4 scaffold: wired by GO-4-3
	return nil, nil, fmt.Errorf("%w: admitForWrite must decide by VerifyPreservation(original, produced, edits, FidelityPathwise) and admit only an empty violation list with a nil error: %w", errUnverifiedWrite, errNotImplemented)
}
```
