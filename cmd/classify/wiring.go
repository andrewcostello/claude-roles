// Wiring contract — GO-1-1 (scaffold). CONTRACT AND STUBS ONLY.
//
// Nothing in this file has a body. It states, as compilable signatures and as
// doc comments, the contract that GO-1-2's seals judge and GO-1-3's bodies
// satisfy. A scaffold that implemented the mapping its own seals will judge
// would destroy the point of sealing first, so RunWiring returns
// ErrWiringNotImplemented and parseInvocationFlags returns it too.
//
// ─── the defect, re-derived rather than quoted ───────────────────────────────
//
// Measured on this branch (0cfdb57 + .dispatcher.yaml), by copying cmd/ to a
// scratch directory — never `cp -a` of the linked worktree, which inherits the
// `.git` POINTER FILE and lets a git command inside the copy mutate the real
// repository — and running `go test -covermode=set -coverprofile ./...` in
// cmd/classify:
//
//	main.go:182  main                         0.0%
//	main.go:200  usage                        0.0%
//	main.go:215  parseFlags                   0.0%
//	main.go:255  registerContractVersionFlag  0.0%
//	main.go:263  run                          0.0%
//	main.go:319  resolveConfigPath            0.0%
//	main.go:342  reportConfigSearch           0.0%
//	main.go:362  loadInputs                   0.0%
//	main.go:382  persist                      0.0%
//	main.go:442  validateInput                0.0%
//	main.go:461  emit                         0.0%
//	main.go:1241 printInvalidInput            0.0%
//	main.go:1257 printReport                  0.0%
//
// THIRTEEN, not the seven the task row records. The row named the entry spine;
// the re-derivation finds the whole REPORTING half unexecuted too — every
// function that decides what an operator or a downstream consumer is told
// (printInvalidInput, printReport, reportConfigSearch, validateInput) is at
// 0.0% alongside it. Package coverage is 64.3% of statements in that scratch
// run, so this is not a package without tests; it is a package whose 97 tests
// all enter BELOW the wiring.
//
// 97 `func Test` in cmd/classify at this revision, re-counted here: 70 in the
// six *_seal_test.go files (baseline 7, capability 10, contract 21, envgap 4,
// readset 19, repair 9) and 27 in main_test.go.
//
// MUTATION-PROVED, re-run in the scratch copy rather than trusted. emit()'s v2
// arm rewritten to answer a v2 request with v1 bytes:
//
//	case ContractV2:
//	-	return EmitV2(os.Stdout, cls)
//	+	return EmitV1(os.Stdout, cls)
//
// came back with EXACTLY the two failures the unmutated scratch baseline
// already had, both environment-dependent artefacts of the copy
// (TestSeal_GenerateReadSet_CitationsResolveToRealLines and
// TestSeal_Repair_NoFallbackToAnotherCheckoutsConfig). Not one seal reddens.
// The same tree is GREEN in the real worktree, mutation absent.
//
// So: every seal in this package calls EmitV1 / EmitV2 / WriteV2Sidecar /
// ParseContractVersion as a LIBRARY, and no test in this package decides
// whether the binary CALLS the right one. The behaviour with no test is the
// mapping
//
//	(contract x -out x -json) -> artifact set + exit code
//
// and that mapping is what this file's contract is about.
//
// ─── Q1. What an end-to-end row is allowed to execute ────────────────────────
//
// ANSWER: the wiring of THIS source tree, IN PROCESS, through RunWiring. No
// build, no exec, no subprocess.
//
// Three candidates were on the table; two are refused and the refusals are
// different strengths, which matters because the weaker one is the tempting one.
//
//	(a) exec a tracked binary — REFUSED, absolutely, and it is TWO files, not
//	    one. `pinnedBinary` is PinnedBaselineV1Path ==
//	    "testdata/baseline/classify-v1-ad289891e9c7" (baseline.go:43), the
//	    differential's frozen v1 reference, pinned by sha256 against
//	    baselineDigestOfRecord and re-verified before its bytes are touched
//	    (pinnedV1, seal_helpers_test.go). "./classify" is a DIFFERENT tracked
//	    file, deployedClassifyPath, and is this module's default `go build`
//	    output path. Both are FIXTURES, not build artifacts. Running the
//	    baseline answers questions about 2026-08 — and the baseline PREDATES
//	    emit()'s v2 arm, so it cannot be asked whether that arm is correct.
//	    Rebuilding either to make a row pass would destroy the differential and
//	    is a separate operator call, not a test's decision.
//
//	    MEASURED HERE, 2026-08-30, because it is easier to do by accident than
//	    the prose suggests: a bare `go build ./...` run inside cmd/classify to
//	    check that this file compiles rewrote the tracked ./classify in place
//	    (4,284,063 -> 4,443,059 bytes) and left the worktree dirty. Restored
//	    with `git checkout -- cmd/classify/classify`. Use `go build -o /dev/null .`
//	    or `go test`; .dispatcher.yaml's gate uses `go test` for exactly this
//	    reason, and nothing in the 97-test suite reddens when ./classify is
//	    replaced by a fresh build of the same source.
//
//	(b) `go build -o $TMPDIR/classify .` and exec that — REFUSED, but for a
//	    weaker reason that must be stated rather than dressed up as (a). With
//	    an explicit -o it does NOT rewrite the tracked binary and does NOT
//	    dirty the worktree, so the "must not be rebuilt" rule does not by
//	    itself forbid it. It is refused because it buys nothing this contract
//	    needs and costs three things it cannot afford: a Go toolchain at test
//	    time (the gate in .dispatcher.yaml runs `go test`, never `go build`,
//	    precisely to keep the tracked binaries out of the loop); seconds of
//	    link time per row across the ~20 rows this mapping needs; and, the
//	    real objection, a subprocess can observe stdout, stderr and the exit
//	    code but cannot observe WHICH arm ran — a v2 request answered with v1
//	    bytes and a v2 request answered by a broken EmitV2 are the same
//	    subprocess. In process, the seal can assert on the artifact set AND on
//	    the state transition that produced it.
//
//	(c) call RunWiring in process — CHOSEN.
//
// The obligation (b) and (c) share, and the one that makes or breaks the whole
// unit: RunWiring must be the SAME CODE main() runs. If GO-1-3 implements
// RunWiring as a second, parallel spine, every row below is vacuous by
// construction — it would seal a copy. main() must become
//
//	os.Exit(RunWiring(...).ExitCode)   // or equivalent one-line delegation
//
// and GO-1-2 owes a STRUCTURAL row for that, not a behavioural one: scan this
// package's non-test source and assert main()'s classify path reaches
// RunWiring. baseline_seal_test.go's scanPackageSource already does exactly
// this kind of source-level check and is the pattern to copy, control leg
// included.
//
// ─── Q2. registerContractVersionFlag, and what "unexecuted" does and does not
//
//	mean ────────────────────────────────────────────────────────────────
//
// ANSWER: correct, nothing today proves any binary accepts -contract-version.
// But the claim splits into two facts with different owners, and collapsing
// them is how this row goes wrong.
//
//	(i)  THIS SOURCE registers -contract-version on the FlagSet it then parses,
//	     and the parsed value reaches options.contractVersion. PROVABLE, in
//	     process, and it is GO-1-2's to seal. It is not provable TODAY only
//	     because parseFlags() reads the two globals flag.CommandLine and
//	     os.Args and calls flag.Parse(), which a test cannot drive twice and
//	     cannot drive with its own argv. That is the entire reason
//	     parseInvocationFlags exists below: same registration, same threading,
//	     caller-supplied FlagSet and argv.
//
//	     Note what a test of registerContractVersionFlag ALONE would not prove.
//	     Calling it on a fresh FlagSet and asserting Lookup("contract-version")
//	     is non-nil says the registrar works; it says nothing about whether the
//	     FlagSet parseFlags actually parses is the one it registered on. The
//	     row must go through parseInvocationFlags end to end, argv in, options
//	     out, or it seals the helper and not the wiring.
//
//	(ii) THE PINNED BINARY accepts -contract-version. NOT provable and NOT
//	     owed — it is the frozen v1 baseline and predates the flag. Asserting
//	     it accepts the flag would be asserting the baseline is not the
//	     baseline. The correct row here is the NEGATIVE one: nothing may probe
//	     ./classify for v2 capability, and no row may rebuild it to acquire
//	     the flag.
//
// One further state this contract must name, because registerContractVersionFlag
// has an arm nothing reaches: when contractFlagRegistrar is nil the function
// registers NO flag and returns defaultContractVersion.String(). That is "1",
// which ParseContractVersion accepts, so a binary built without B1's init()
// silently runs v1 and NEVER exits 3 for a missing flag. It is a legal, named
// state — capability.go reports it as ContractVersionFlag:false — and the seal
// owes a row for it, because it is the one configuration in which
// -contract-version is not a flag at all and passing it is a `flag provided but
// not defined` failure from the flag package rather than this binary's exit 3.
//
// ─── Q3. The exit-code contract for ParseContractVersion -> printInvalidInput
//
//	-> exit 3 ───────────────────────────────────────────────────────────
//
// The closed set is DeclaredExitCodes below. For this path specifically, six
// claims, each independently sealable:
//
//  1. TRIGGER. Exit 3 iff ParseContractVersion rejects the raw flag value.
//     It accepts exactly "1" and "2"; "0", "3", "v1", "02", " 2" and "" are
//     each exit 3. Absent flag is NOT exit 3: the registrar defaults it to
//     defaultContractVersion.String() == "1".
//  2. CODE. Exactly exitInvalid (3), never 1. log.Fatalf exits 1 and is the
//     wrong instrument here; run() returns the code and main() exits with it.
//  3. MESSAGE. Names the value received and the accepted set, from
//     ParseContractVersion's own format string — `-contract-version %q is not
//     a classification contract this binary emits; accepted values are 1 and 2`
//     — wrapped in printInvalidInput's INVALID_INPUT block.
//  4. STREAM. That block goes to STDOUT today, not stderr: printInvalidInput
//     is fmt.Println/fmt.Printf, which write to os.Stdout (main.go:1241-1255).
//     Recorded as the CURRENT behaviour, deliberately not blessed — see hole
//     H4 in docs/DECISIONS.md. GO-1-3 must not "fix" it while turning rows
//     green; changing it is its own decision with its own consumers.
//  5. ORDERING. The contract is validated FIRST, before resolveConfigPath and
//     before any input is read. So `-contract-version 3` against a worktree
//     with NO config table exits 3 reporting the CONTRACT problem only. A row
//     that supplies a valid config to test this path is testing something
//     weaker than the contract says.
//  6. ARTIFACT SET — the claim this whole file exists for. Because the
//     rejection precedes everything, a run that exits 3 here writes NOTHING
//     and REMOVES NOTHING. Given -out P where P exists and V2SidecarPath(P)
//     exists, both must be byte-identical afterwards: RunState.State ==
//     ArtifactStale and V2Sidecar.State == ArtifactStale. Stale is the
//     CORRECT answer here — the run made no verdict, so it may not disturb a
//     previous one — and it is the same state that is a DEFECT one arm over,
//     in persist(), where a v1 run that leaves a v2 sidecar in place publishes
//     a superseded verdict. One state, two verdicts, decided by which run
//     produced it; that is why ArtifactState distinguishes Stale from Written
//     at all.
package main

import (
	"errors"
	"flag"
	"io"
)

// ErrWiringNotImplemented is what every stub in this file returns.
//
// It is a distinct sentinel and not a generic error so that GO-1-2's seals can
// tell "the body is not written yet" (expected RED) from "the body is written
// and wrong" (a finding). A seal that cannot tell those apart goes green the
// day the stub is replaced by a broken body.
var ErrWiringNotImplemented = errors.New(
	"classify wiring: stub from GO-1-1 (scaffold); the body is GO-1-3's and is not written yet")

// DeclaredExitCodes is the closed set of exit codes cmd/classify may produce.
//
// Enumerated once, here, for the same reason contractVersionSet is enumerated
// once: three hand-written lists need three edits to stay honest and the edit
// that only happened twice is this project's whole failure class. An exit code
// outside this set is a finding, not a new feature — the header of main.go
// advertises "0 classified, 3 INVALID_INPUT" and usage() adds
// "4 CAPABILITY_INCOMPLETE (probe only)"; neither mentions 1, and 1 is
// nonetheless reachable from seven log.Fatalf call sites inside run() and
// persist(). It is listed because it is REAL, not because it is intended; see
// hole H2 in docs/DECISIONS.md.
var DeclaredExitCodes = []int{
	exitOK,
	exitInternal,
	exitInvalid,
	exitCapabilityIncomplete,
}

const (
	// exitOK is a completed classification. Named rather than written as a
	// bare 0 so that "the run succeeded" and "the zero value of an int nobody
	// set" stop being the same token in this package's signatures.
	exitOK = 0
	// exitInternal is what log.Fatalf produces. It is UNDOCUMENTED in usage()
	// and it is the code an operator actually receives when the config is
	// unreadable, the diff cannot be read, emit fails, the run state cannot be
	// written, or persist's sidecar write or teardown fails. Naming it does not
	// bless it.
	exitInternal = 1
)

// ─── the artifact set ────────────────────────────────────────────────────────

// ArtifactState is what a single run did to a single file on disk.
//
// FOUR states, not a bool, and this is the design decision the unit turns on.
// "The sidecar is not there" is two different facts — it was never there, or
// this run tore down a previous run's — and "the sidecar is there" is two more:
// this run wrote it, or a previous run did and this run left it alone. persist()
// already documents the fourth as a live defect ("a v1 re-run over an -out that
// a v2 run had already used left the PREVIOUS run's sidecar in place, still
// asserting the superseded verdict"), and a bool cannot express the difference
// between that and a correct write. A test whose oracle is os.Stat cannot see it
// either, which is why RunWiring snapshots before and after rather than
// reporting existence.
//
// This is `skills/explicit-state.md` applied to a file: absence is a state and
// it must be nameable, and here absence is TWO states.
type ArtifactState int

const (
	// ArtifactStateUnset is the zero value and is ILLEGAL at every boundary.
	// It exists so that "nobody looked" raises instead of silently reading as
	// "absent" — the same discipline ContractVersionUnset enforces for the
	// contract. Every switch over ArtifactState must be exhaustive over all
	// five members with no default arm falling through to Absent.
	ArtifactStateUnset ArtifactState = iota
	// ArtifactAbsent: not present before the run, not present after. The run
	// neither wrote nor removed it.
	ArtifactAbsent
	// ArtifactWritten: present after, and its bytes are this run's. Covers
	// both creation and overwrite; the caller compares Bytes if it cares which.
	ArtifactWritten
	// ArtifactRemoved: present before, absent after, because this run removed
	// it. The correct outcome for a v2 sidecar under ContractV1 with -out.
	ArtifactRemoved
	// ArtifactStale: present before AND after, byte-identical, NOT written by
	// this run. Correct on the exit-3 paths, which make no verdict and so may
	// disturb nothing. A DEFECT on the persist() paths, where it means a
	// superseded verdict is still readable beside a fresh run state. The state
	// is the same; only the path decides the verdict, which is exactly why it
	// must be reported rather than folded into ArtifactWritten.
	ArtifactStale
)

// String renders the state for failure messages. Total: it must render
// ArtifactStateUnset as a visibly-wrong token rather than as "absent", and any
// out-of-set value in a form that keeps the raw integer visible, for the reason
// ContractVersion.String gives.
func (s ArtifactState) String() string {
	// Stub. GO-1-3 owns the body.
	_ = s
	return ""
}

// Valid reports membership in the closed set {Absent, Written, Removed, Stale}.
// ArtifactStateUnset is NOT valid.
func (s ArtifactState) Valid() bool {
	// Stub. GO-1-3 owns the body.
	_ = s
	return false
}

// FileArtifact is one on-disk output of a run, with the state that produced it.
//
// Bytes carries the file's content AFTER the run, or nil when State is
// ArtifactAbsent or ArtifactRemoved. Path is always populated — including when
// the file is absent — because "which path did you look at" is the first
// question a failed row must answer, and a seal that cannot print the path it
// checked is one that has been passing against the wrong directory.
type FileArtifact struct {
	Path  string
	State ArtifactState
	Bytes []byte
}

// Artifacts is the COMPLETE, CLOSED set of things one classify invocation may
// leave behind. Closed is the operative word: if the wiring grows a third
// output file, it goes here, and a row that asserts on this struct starts
// failing to mention it rather than silently ignoring it.
//
// ExitCode is a member of DeclaredExitCodes.
type Artifacts struct {
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	RunState  FileArtifact
	V2Sidecar FileArtifact
}

// Invocation is one classify run stated as data: the argv an operator would
// type, the stdin they would pipe, and the directory it runs in.
//
// Args is argv[1:] — WITHOUT the program name — so a row reads exactly like the
// command line it stands for. Stdin may be nil, which means "no diff on stdin"
// and is a different fact from an empty reader: nil is the operator who passed
// a file argument, an empty reader is the operator who piped nothing.
//
// Dir anchors -worktree, -out and -config resolution. A row that leaves it
// empty is running against the test process's working directory, which is the
// package directory and is a live git repository — resolveRepo would then shell
// out to git against the repo under review. Rows pass t.TempDir() and -no-git
// unless git state is the subject.
//
// There is deliberately no Env field. $RISK_PATHS_CONFIG was removed from
// configCandidates because an agent that can set an environment variable could
// redirect the whole money-path table; giving the wiring seam an env channel
// would hand that back through the test surface.
type Invocation struct {
	Args  []string
	Stdin io.Reader
	Dir   string
}

// RunWiring executes one classify invocation in process and reports the exit
// code together with every artifact it produced.
//
// THIS IS THE UNIT'S SUBJECT. Contract:
//
//  1. IT IS THE CODE main() RUNS. Not a parallel spine, not a
//     reimplementation. main() delegates to it. See Q1 above: if this is a
//     copy, every row that calls it is vacuous by construction.
//  2. IT NEVER EXITS THE PROCESS. Every os.Exit and every log.Fatalf on the
//     classify path becomes a returned Artifacts.ExitCode. os.Exit survives in
//     main() and nowhere else.
//  3. IT WRITES NOTHING TO os.Stdout OR os.Stderr. Stdout and Stderr on the
//     returned Artifacts are the complete streams. That is what forces emit,
//     printReport, printInvalidInput and reportConfigSearch to take an
//     io.Writer instead of calling fmt.Println.
//  4. IT REPORTS ARTIFACT STATE, NOT EXISTENCE. It snapshots -out and
//     V2SidecarPath(-out) BEFORE running and compares after, so
//     ArtifactWritten, ArtifactStale and ArtifactRemoved are distinguishable.
//     With no -out, both FileArtifacts are ArtifactAbsent with the Path field
//     empty.
//  5. THE ERROR RETURN IS NOT THE RUN'S FAILURE. A classify run that fails is
//     a non-zero ExitCode with err == nil. A non-nil err means RunWiring
//     ITSELF could not run the invocation — it could not snapshot the -out
//     path, could not chdir, was handed an Invocation it cannot honour. Rows
//     must not conflate them: `if err != nil { t.Fatal }` then assert on
//     ExitCode.
//  6. SUBCOMMANDS ARE IN SCOPE. Args[0] of "init", "capabilities", "help",
//     "-h" or "--help" takes main()'s pre-flag-parse branch, and its exit code
//     is this function's answer too. The capabilities probe dispatches AHEAD
//     of flag parsing on purpose and that ordering is part of the mapping.
//
// STUB. Returns ErrWiringNotImplemented. GO-1-3 owns the body.
func RunWiring(inv Invocation) (Artifacts, error) {
	_ = inv
	return Artifacts{}, ErrWiringNotImplemented
}

// parseInvocationFlags turns argv into options against a CALLER-SUPPLIED
// FlagSet, so the flag half of the wiring is observable without touching
// flag.CommandLine or os.Args.
//
// Unexported, matching registerContractVersionFlag, which it must call: the
// point is not a new public surface but a seam a package-main test can drive.
// parseFlags() becomes a thin caller of this over flag.CommandLine and
// os.Args[1:], which is what keeps the two from drifting — the ONE thing a row
// through this function proves that a row through registerContractVersionFlag
// alone does not.
//
// Contract:
//
//  1. It registers exactly the flags parseFlags registers — config, worktree,
//     base, task, out, json, no-git — and -contract-version THROUGH
//     registerContractVersionFlag, never by calling flag.String here. The
//     indirection is the capability registry's, and duplicating it would let
//     the probe's answer and the flag's existence disagree.
//  2. It does not parse -contract-version. The raw string is threaded to
//     options.contractVersion and validated in run(), for the reason the field
//     already documents: parsing here would have to log.Fatalf, which exits 1,
//     and a mistyped contract owes the caller exit 3.
//  3. A flag error is RETURNED, not fatal. fs.Parse's error comes back so
//     RunWiring can map it to an exit code; the caller sets
//     fs.SetOutput/ContinueOnError. Today flag.CommandLine is ExitOnError and
//     an unknown flag exits 2 — a code in NO declared set. See hole H3.
//  4. It does NOT call log.SetFlags/log.SetPrefix. parseFlags does that today
//     as a side effect of parsing, which is why the log prefix depends on
//     whether flags were parsed. Process-wide logger configuration belongs to
//     main(), not to a function a test calls a hundred times.
//
// STUB. Returns ErrWiringNotImplemented. GO-1-3 owns the body.
func parseInvocationFlags(fs *flag.FlagSet, args []string) (options, error) {
	_, _ = fs, args
	return options{}, ErrWiringNotImplemented
}
