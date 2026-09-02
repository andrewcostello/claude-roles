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
   is `fmt.Println`/`fmt.Printf` (cited by symbol, not line — see H2).
   Recorded as current
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
  and `persist()` reach `log.Fatalf` — exit 1 — at seven call sites: two in
  `run()`, five in `persist()`. Cited by function rather than by line, because
  line numbers here were read off the base tree and the scaffold's own contract
  comments moved every one of them by 59; a coordinate that a comment edit
  falsifies is not a durable record. `grep -n 'log\.Fatalf' cmd/classify/main.go`
  is the reproduction. An operator scripting against the documented set receives a
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

---

## GO-1-2 — sealing the wiring mapping (2026-09-02)

Subject: the rows that judge
`(contract × -out × -json) → artifact set + exit code`. They landed in
`cmd/classify/wiring_seal_test.go`. This section records one amendment to a
GO-1-1 decision and one amendment to a live seal; everything else GO-1-1 wrote
stands and is what the rows were derived from.

### D4 — The mapping rows exec a build of the current tree. D1 is amended.

D1 ruled: *"In process, through `RunWiring`. No build, no exec, no
subprocess."* The rows do the opposite, and the reason is that D1's premise
stopped being true the moment the scaffold landed.

**Why in-process is not available.** `RunWiring` is a stub returning
`ErrWiringNotImplemented`; GO-1-3 owns its body. A row reaching production only
through it is RED today for a reason that has nothing to do with the mapping,
and stays red no matter what anyone does to `emit()`. It cannot distinguish a
correct wiring from a broken one, which is the entire job. A red row that
answers no question is worth less than a green row that answers one.

**D1's stated reason for refusing a subprocess is false, and it was measured.**
D1 says exec *"cannot observe which arm ran — a v2 request answered with v1
bytes and a v2 request answered by a broken `EmitV2` are the same subprocess."*
They are not the same subprocess. Both mutations were applied to a scratch
build of this tree and run as
`classify -json -no-git -config <table> -contract-version 2 <diff>`:

| mutation | exit | stdout | stderr |
|---|---|---|---|
| `emit()`'s `ContractV2` arm → `EmitV1` | **0** | the v1 payload, 1 JSON doc | empty |
| `EmitV2` returns an error | **1** | **0 bytes** | `classify: v2 emission: …` |
| unmutated | 0 | the response wrapper | empty |

Three distinct observations from outside the process. The arm that ran is
recoverable from the bytes it wrote, because the two arms write different
shapes — which is what the contract says they must do.

**And the suite already exec'd.** `liveClassify` (`repair_seal_test.go`) builds
the current tree to a scratch path with an explicit `-o` and eight existing
seals exec it, including every EnvGap row and
`TestSeal_Repair_LiveResolution_DifferingDualTablesMustNotResolveSilently` —
which exists *precisely* because the in-process seals over `ResolveConfigDual`
stayed green while production routed around it. D1 refused the mechanism its
own module depends on. The new rows reuse `liveClassify`, so they inherit its
guards: the frozen baseline's digest is checked before and after, the artifact
must differ from that baseline, and the build never writes to a tracked path.

**What survives from D1, unchanged.** Exec'ing a *tracked* binary is still
refused — the pinned baseline predates `emit()`'s v2 arm and cannot be asked
about it, and `./classify` is a build output. Nothing here execs either.

**The cost of this decision, stated rather than discovered later.** A
subprocess is invisible to the test binary's coverage instrumentation. Measured
before and after these rows landed, whole-suite: `run`, `emit`, `persist`,
`printReport`, `printInvalidInput`, `loadInputs`, `validateInput`,
`resolveConfigPath`, `reportConfigSearch` and `usage` are at **0.0% both
times** — package total 63.5% → 64.0%. Every statement in that 0.5 point
belongs to the amended digest row, and the diff is three functions and no
others: `readDiff` 0.0% → 31.2% (nothing in the package called it before),
`consumeCertifiedConfigRead` 66.7% → 100.0%, `certifiedConfigRead.take` 85.7% →
100.0%. The mapping rows moved the profile by nothing at all. The mapping is sealed and the coverage table cannot
see it. Evidence for these rows is mutation-kill, recorded below, and a
coverage table is not admissible against them in either direction. See H8.

**The obligation this creates for GO-1-3.** `RunWiring` clause 8 — `main()`
forwards the streams and the exit code and adds nothing — is what makes the
subprocess and the in-process seam the same subject. When the body lands, these
rows keep judging the shipped artifact and GO-1-3 owes the in-process form of
the same table plus D1's structural delegation row. The two are not
alternatives: the structural row proves `RunWiring` is the code `main()` runs,
and these rows prove what that code answers.

### D5 — `TestSeal_InstalledDigestSource_YieldsHexOrErrors` is amended, not struck

It was vacuous, and the vacuity was re-derived here rather than taken from the
report: run alone under coverage it left `hexSHA256` at **0.0%** and
`ConsumedDigests` at **90.0%**. It asked the installed source for its digests
and returned early on the error, having asserted nothing — and the error was
the only answer available, because `ConsumedDigests` raises unless both
`sawConfig` and `sawDiff` are set and no test in the package called `readDiff`.

Amended rather than struck: the property is real and nothing else seals it. The
dual digest echo is the only thing binding a v2 response to the bytes that
produced it. Striking the row would have deleted the obligation along with the
hole. The name is kept so the record stays traceable. Both halves of "hex or
errors" are now reached in one call, and the success half is driven through
`loadConfig` and `readDiff` — production's own recording call sites — with the
expected digests computed in the test from the staged bytes. Alone under
coverage it now leaves `recordConfig`, `recordDiff`, `ConsumedDigests` and
`hexSHA256` at **100.0%**.

### H8 — the wiring path stays at 0.0% coverage while being sealed

New hole, created by D4 and named so nobody reads it as the absence of a seal.
The rows exec a subprocess, so every statement they exercise in `run`, `emit`
and `persist` is counted in another process and lands in no profile. Two
readings this forbids: "0.0% means unsealed" (false — six mutations in those
three functions redden these rows), and "raise the number by adding in-process rows
over the emitters" (that is what the existing library seals already do, and it
is what left `emit()`'s arm selection unsealed in the first place). Closing H8
honestly needs either `RunWiring`'s body — which puts the mapping back in
process, where D1 wanted it — or `go build -cover` plus `GOCOVERDIR` in
`liveClassify` and a profile merge. Neither is GO-1-2's.

### What the rows do NOT decide

- **H4 stays open.** No row pins the stream `INVALID_INPUT` uses; the message
  assertions read stdout and stderr together. The one stream fact asserted is
  weaker and holds under either resolution: after an exit 3 there is no
  parseable JSON document on stdout, because the run made no verdict.
- **H2, H3, H5, H6, H7 stay open.** The rows seal that an unknown flag exits 2
  and an unaccepted contract exits 3 and that the two differ (H3's mapping
  half, which H3 assigns to GO-1-2); what `usage()` advertises is untouched.

### Measured: which mutation reddens which row

Each row was proved red by the specific defect it names, whole-suite, with the
tree restored and re-verified by digest after each. The column names the rows
this section is accountable for; where a mutation also reddens pre-existing
rows the count is given, because a mutation nothing else notices and a mutation
half the suite notices are different facts about the new row:

| mutation | row that reddens |
|---|---|
| `emit()` `ContractV2` arm → `EmitV1` | `…_JSONEmitterIsChosenByTheContract`, `…_OutAndContractDecideTheArtifactSet` |
| `emit()` `ContractV1` arm → `EmitV2` | `…_JSONEmitterIsChosenByTheContract` (and eight others) |
| `emit()` never prints the human report | `…_ReportIsTheSameUnderBothContractsAndWritesNothing` |
| `persist()`'s `ContractV1` arm writes the sidecar instead of removing it | `…_OutAndContractDecideTheArtifactSet` (and the existing repair row) |
| `run()` lenient-parses an unaccepted contract to v1 | `…_UnacceptedContractExitsInvalidAndTouchesNothing`, `…_FlagErrorAndContractErrorAreDifferentExits` |
| `persist()` ignores an empty `-out` | `…_OutAndContractDecideTheArtifactSet` (and three others) |
| `hexSHA256` returns uppercase | `TestSeal_InstalledDigestSource_YieldsHexOrErrors` |
| `recordConfig` digests the empty slice | `TestSeal_InstalledDigestSource_YieldsHexOrErrors` |

A ninth mutation is recorded as **already sealed and deliberately not
re-sealed**: swapping `ConsumedDigests`' return to
`hexSHA256(s.diff), hexSHA256(s.config)` reddens exactly
`TestSeal_Repair_ResolveConfigDual_ConsumedBytesMustBeTheCertifiedBytes`,
added by a later repair wave. Re-derived here, not carried over from the task
row that said the swap passes the suite — that claim no longer reproduces.
