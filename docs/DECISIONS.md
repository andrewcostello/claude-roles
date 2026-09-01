# DECISIONS

Decisions this repository has made, one section per unit, newest last. A
decision lands here when it constrains a later unit's code and is not derivable
from the code itself.

---

## GO-1-1 — the classify wiring contract (scaffold, 2026-08-30)

Base: `fix/live-config-seal @ 0cfdb57` + `9418fc1` (the `.dispatcher.yaml`
gate). Operator ruling of 2026-08-30 named this base; `main` and the B1 lineage
stay diverged and that is accepted.

Subject: `cmd/classify`'s entry spine — the mapping
`(contract × -out × -json) → artifact set + exit code`. The contract itself is
`cmd/classify/wiring.go`; this file records only the three decisions the task
row demanded and the holes the scaffold could not close.

### What was measured, and where the record is wrong

Re-derived on this branch by copying `cmd/` to a scratch directory (never
`cp -a` of the linked worktree — that inherits the `.git` **pointer file** and a
git command inside the copy mutates the real repository) and running
`go test -covermode=set -coverprofile ./...` in `cmd/classify`:

| claim | task row | measured here |
|---|---|---|
| functions at 0.0% on the wiring path | 7 | **13** |
| `func Test` in `cmd/classify` | 97 | **97** (7/10/21/4/19/9 seals + 27 `main_test.go`) |
| package statement coverage | 58.3% | **64.3%** in the scratch copy |
| `emit()` v2 arm → `EmitV1` reddens | nothing | **nothing** — same two environment-dependent scratch failures as the unmutated baseline |

The row named the entry spine (`main`, `parseFlags`,
`registerContractVersionFlag`, `run`, `resolveConfigPath`, `persist`, `emit`).
The re-derivation adds six more at 0.0%: `usage`, `reportConfigSearch`,
`loadInputs`, `validateInput`, `printInvalidInput`, `printReport`. The whole
**reporting** half — every function that decides what an operator or a
downstream consumer is told — is unexecuted alongside the spine. Use 13, at
this revision.

The suite is **green in the real worktree**; the two scratch failures are
artefacts of the copy (`TestSeal_GenerateReadSet_CitationsResolveToRealLines`
needs the repo's line numbers, `TestSeal_Repair_NoFallbackToAnotherCheckoutsConfig`
needs a `.agent/risk-paths.json` above the module).

### D1 — What an end-to-end row may execute

**In process, through `RunWiring`. No build, no exec, no subprocess.**

- Exec'ing a tracked binary is refused. There are **two** such files, not one:
  `pinnedBinary` = `PinnedBaselineV1Path` = `testdata/baseline/classify-v1-ad289891e9c7`
  (the digest-pinned differential reference), and `./classify` =
  `deployedClassifyPath` (this module's default `go build` output). The
  baseline predates `emit()`'s v2 arm and therefore cannot be asked whether
  that arm is correct.
- Building a fresh binary into a temp dir and exec'ing it is refused for a
  **weaker** reason, stated as weaker rather than dressed up as the first: with
  an explicit `-o` it neither rewrites a tracked file nor dirties the tree. It
  is refused because a subprocess can observe stdout, stderr and the exit code
  but cannot observe *which arm ran* — a v2 request answered with v1 bytes and a
  v2 request answered by a broken `EmitV2` are the same subprocess.
- **Measured, 2026-08-30:** a bare `go build ./...` inside `cmd/classify`
  rewrote the tracked `./classify` in place (4,284,063 → 4,443,059 bytes) and
  left the worktree dirty; restored with `git checkout --`. In a scratch copy,
  replacing `./classify` with a fresh build of the same source **reddens
  nothing** — same two environment-dependent failures as the unmutated
  baseline. Use `go build -o /dev/null .` or `go test`.

The obligation this decision creates: `RunWiring` must be the code `main()`
runs, not a parallel spine. GO-1-2 owes a **structural** row for the
delegation, on the pattern of `baseline_seal_test.go`'s `scanPackageSource`
(control leg included).

### D2 — `registerContractVersionFlag`, and what "unexecuted" proves

The claim splits into two facts with different owners:

1. **This source registers `-contract-version` and threads it to
   `options.contractVersion`.** Provable in process, and GO-1-2's to seal. It
   is not provable through `parseFlags()`, which reads `flag.CommandLine` and
   `os.Args` and calls `flag.Parse()`. Both are assignable package variables,
   so a row COULD save, replace and restore them — the reason there is no such
   row is a REFUSAL, not a capability limit: a row that mutates process globals
   cannot run beside a parallel one, and the restore is one defect away from
   leaking into every later row. That refusal is a rule GO-1-2 must obey, which
   an "impossible" framing would erase. `parseInvocationFlags` exists so the
   seam can be driven without the mutation.
   A row through `registerContractVersionFlag` **alone** is not enough: it
   proves the helper registers a flag, not that the FlagSet the classify path
   actually parses is the one it registered on. (`parseFlags()` itself does not
   survive GO-1-3 — `RunWiring` clause 1 — so it is named here as the state of
   the tree the scaffold was written against, not as a seam that persists.)
2. **The pinned baseline accepts `-contract-version`.** Not provable, not owed,
   and asserting it would assert that the baseline is not the baseline. The
   correct row is the negative: nothing probes `./classify` or the pinned
   baseline for v2 capability, and no row rebuilds either to acquire the flag.

Third state, currently unreached: when `contractFlagRegistrar` is nil the
function registers **no flag** and returns `defaultContractVersion.String()` —
`"1"`, which `ParseContractVersion` accepts. A binary built without B1's
`init()` therefore runs v1 silently and never exits 3 for a missing flag;
passing `-contract-version` to it fails inside the `flag` package
(`flag provided but not defined`), not at exit 3. `capability.go` already
reports this as `contract_version_flag: false`. It is a legal named state and
GO-1-2 owes it a row.

### D3 — The exit-code contract for `ParseContractVersion → printInvalidInput → exit 3`

Closed set: `DeclaredExitCodes` in `wiring.go`. It is enumerated THERE and
deliberately not repeated here — a second copy is a second edit, and the
edit that only happened once is this project's whole failure class. For
this path:

1. **Trigger.** Exit 3 iff `ParseContractVersion` rejects the raw flag value.
   Accepted: exactly `"1"` and `"2"`. Rejected, each exit 3: `"0"`, `"3"`,
   `"v1"`, `"02"`, `" 2"`, `""`. An **absent** flag is not exit 3 — the
   registrar defaults it to `defaultContractVersion.String()`.
2. **Code.** Exactly `exitInvalid` (3), never 1. `log.Fatalf` exits 1 and is
   the wrong instrument here.
3. **Message.** Names the received value and the accepted set, from
   `ParseContractVersion`'s own format string, wrapped in
   `printInvalidInput`'s `INVALID_INPUT` block.
4. **Stream.** That block goes to **stdout**, not stderr — `printInvalidInput`
   is `fmt.Println`/`fmt.Printf` (`main.go:1241-1255`). Recorded as current
   behaviour and deliberately **not** blessed; see hole H4. GO-1-3 must not
   change it while turning rows green.
5. **Ordering.** The contract is validated first, before `resolveConfigPath`
   and before any input is read. `-contract-version 3` against a worktree with
   no config table exits 3 reporting the **contract** problem only. A row that
   supplies a valid config to reach this path is testing something weaker than
   the contract says.
6. **Artifact set.** Because the rejection precedes everything, a run that
   exits 3 here writes nothing and removes nothing. Given `-out P` where `P`
   and `V2SidecarPath(P)` both exist, both are byte-identical afterwards:
   `ArtifactStale` on both. Stale is **correct** here — the run made no
   verdict, so it may disturb none — and is a **defect** one arm over, in
   `persist()`, where a v1 run that leaves a v2 sidecar in place republishes a
   superseded verdict. One state, two verdicts, decided by which path produced
   it. That is why `ArtifactState` separates `Stale` from `Written` at all.

### Holes — proposed, not gated

The GO-1-1 row carries no `declares.holes`, and `scaffold_shape.py` hole-checks
Python only, so these are mechanically ungated. Stated here so a later unit
inherits them explicitly rather than by silence.

- **H1 — `main()` is unsealable behaviourally, permanently.** It calls
  `os.Exit` unconditionally and reads `os.Args`. Even after GO-1-3 lands the
  delegation, `main` stays at 0.0%, and the delegation is checkable only by
  scanning source. Named so that nobody reads the post-GO-1-3 coverage table as
  "the spine is covered".
- **H2 — exit 1 is unadvertised by `usage()` and reachable from seven call
  sites.** (Titled on H3's pattern: `DeclaredExitCodes` DOES list it, so
  "undeclared" would assert the negation of the set wiring.go defines. The gap
  is advertisement, not declaration.) `usage()`
  advertises "0 classified, 3 INVALID_INPUT, 4 CAPABILITY_INCOMPLETE". `run()`
  and `persist()` reach `log.Fatalf` — exit 1 — at `main.go:282, 293, 391, 415,
  427, 433, 435`. An operator scripting against the documented set receives a
  code that set does not contain. Listed in `DeclaredExitCodes` because it is
  real, not because it is intended.
- **H3 — an unknown flag exits 2, which `usage()` does not advertise.**
  `flag.CommandLine` is `ExitOnError`, which is `os.Exit(2)`.
  `DeclaredExitCodes` DOES list it, as `exitFlagError`, because it is
  observable; `usage()` does not, which is the gap. The MAPPING is not a hole
  and is not GO-1-3's to choose: `parseInvocationFlags` returns the parse error
  and `RunWiring` maps it to `exitFlagError`, decided in the scaffold
  (`parseInvocationFlags` clause 3) and sealed by GO-1-2. What remains open is
  only whether `usage()` should advertise the code.
- **H4 — `INVALID_INPUT` goes to stdout, including under `-json`.** A consumer
  that runs `classify -json` and parses stdout gets a human-readable block on
  exit 3. Exit code and stream disagree about who the audience is. WHICH stream
  it should use is consumer-visible and is its own decision; that it must reach
  that stream through a writer `RunWiring` supplied is not open — `RunWiring`
  clause 3 requires it, and a `fmt` call naming no writer is a defect GO-1-3
  removes either way.
- **H5 — `persist()`'s exhaustive contract switch is skipped entirely when
  `-out` is empty.** `persist` returns 0 before the `switch contract` when
  `opts.out == ""`, so `ContractVersionUnset` and any out-of-set value reach a
  silent 0 rather than the fatal arms the switch documents. Unreachable today
  because `run()` validates first — a second decision point protected by a
  first, which is the arrangement this project's whole failure class is made of.
- **H6 — a failed `emit` skips sidecar teardown.** `run()` calls `emit` before
  `persist`. Under `-json -contract-version 1` with an `-out` that has a v2
  sidecar beside it, an `EmitV1` failure exits 1 and `RemoveV2Sidecar` never
  runs, so the superseded v2 verdict survives a run that reported failure.
  Same class as the defect `persist`'s own comment describes, reached by
  ordering rather than by a missing arm.
- **H7 — nothing detects a rebuilt `./classify`.** Measured above: replacing it
  with a fresh build of the same source reddens none of the 97 tests. The
  digest pin covers `PinnedBaselineV1Path` only. This is GO-2's subject ("nine
  tracked binaries, warned about nowhere") and is recorded here because GO-1-1
  tripped it by accident.
