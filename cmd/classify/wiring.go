// Wiring contract — GO-1-1 (scaffold). STUBS AND TYPE DEFINITIONS ONLY.
//
// This file defines the data types and function signatures for the classify
// wiring. Two functions, ArtifactState.String() and ArtifactState.Valid(), have
// full implementations required for type correctness. RunWiring and
// parseInvocationFlags are stubs that return ErrWiringNotImplemented; their
// bodies are GO-1-3's deliverable. A scaffold that implemented the mapping its
// own seals will judge would destroy the point of sealing first.
package main

import (
	"errors"
	"flag"
	"fmt"
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
// "4 CAPABILITY_INCOMPLETE (probe only)"; neither mentions 1, 2 and 1 is
// nonetheless reachable from seven log.Fatalf call sites inside run() and
// persist(). It is listed because it is REAL, not because it is intended; see
// hole H2 in docs/DECISIONS.md. Exit 2 comes from the flag package when an
// unknown flag is passed; it is an observable exit code but is NOT advertised
// in usage() — see hole H3 in docs/DECISIONS.md.
var DeclaredExitCodes = []int{
	exitOK,
	exitInvalid,
	exitInternal,
	exitFlagError,
	exitCapabilityIncomplete,
}

const (
	// exitOK is a completed classification. Named rather than written as a
	// bare 0 so that "the run succeeded" and "the zero value of an int nobody
	// set" stop being the same token in this package's signatures. exitInvalid (3)
	// is defined in main.go, exitInternal (1) is defined below, and
	// exitCapabilityIncomplete (4) is defined in capability.go.
	exitOK = 0
	// exitInternal is what log.Fatalf produces. It is UNDOCUMENTED in usage()
	// and it is the code an operator actually receives when the config is
	// unreadable, the diff cannot be read, emit fails, the run state cannot be
	// written, or persist's sidecar write or teardown fails. Naming it does not
	// bless it.
	exitInternal = 1
	// exitFlagError is what the flag package produces when an unknown flag is
	// passed or a flag has an invalid argument. It is UNDOCUMENTED in usage() and
	// not advertised as a possible exit code, but it is observable when passing
	// unknown flags like --unknown or -contract-version without a value.
	exitFlagError = 2
)

// ─── the artifact set ────────────────────────────────────────────────────────

// ExitCode is a named type for the process exit code returned by a classify run.
// Its zero value is exitOK (0), which means success. RunWiring always returns an
// ExitCode that is one of the DeclaredExitCodes.
type ExitCode int

// ArtifactState is what a single run did to a single file on disk.
//
// FOUR states, not a bool, and this is the design decision the unit turns on.
// "The sidecar is not there" is two different facts — it was never there, or
// this run tore down a previous run's — and "the sidecar is there" is two more:
// this run wrote it, or a previous run did and this run left it alone.
// A bool cannot express the difference between a correct write and a defect.
// RunWiring snapshots before and after to distinguish them.
//
// This is `skills/explicit-state.md` applied to a file: absence is a state and
// it must be nameable, and here absence is TWO states.
type ArtifactState int

const (
	// ArtifactStateUnset is the zero value and is ILLEGAL at every boundary.
	// It exists so that "nobody looked" raises instead of silently reading as
	// "absent" — the same discipline ContractVersionUnset enforces for the
	// contract.
	ArtifactStateUnset ArtifactState = iota
	// ArtifactAbsent: not present before the run, not present after. The run
	// neither wrote nor removed it.
	ArtifactAbsent
	// ArtifactWritten: present after and (did not exist before OR bytes differ
	// from before). Distinguishable from Stale ONLY through snapshots: files
	// that differ before and after are Written, identical ones are Stale. The
	// oracle is the snapshot, not byte-equality alone. A deterministic replay
	// that writes identical bytes is indistinguishable from leaving the file
	// alone; the outcome is Stale in that case, not Written. This is a policy
	// choice on how to interpret "unchanged by this run" when bytes are
	// byte-identical.
	ArtifactWritten
	// ArtifactRemoved: present before, absent after, because this run removed
	// it. The correct outcome for a v2 sidecar under ContractV1 with -out.
	ArtifactRemoved
	// ArtifactStale: present before AND after, byte-identical. Files that do not
	// change from before the run to after are reported as Stale. This includes
	// cases where a deterministic run writes identical bytes back. Stale is the
	// correct outcome when a run makes no changes to a file.
	ArtifactStale
)

// String renders the state for failure messages. Total: it must render
// ArtifactStateUnset as a visibly-wrong token rather than as "absent", and any
// out-of-set value in a form that keeps the raw integer visible, for the reason
// ContractVersion.String gives.
func (s ArtifactState) String() string {
	switch s {
	case ArtifactStateUnset:
		return "unset"
	case ArtifactAbsent:
		return "absent"
	case ArtifactWritten:
		return "written"
	case ArtifactRemoved:
		return "removed"
	case ArtifactStale:
		return "stale"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// Valid reports membership in the closed set {Absent, Written, Removed, Stale}.
// ArtifactStateUnset is NOT valid.
func (s ArtifactState) Valid() bool {
	switch s {
	case ArtifactAbsent, ArtifactWritten, ArtifactRemoved, ArtifactStale:
		return true
	default:
		return false
	}
}

// FileArtifact is one on-disk output of a run, with the state that produced it.
//
// Bytes carries the file's content AFTER the run, or nil when State is
// ArtifactAbsent or ArtifactRemoved. Path is the resolved file path on disk.
// When there is no -out flag, RunState and V2Sidecar are both ArtifactAbsent
// with empty Path fields; they were not checked. When -out is supplied, Path
// is populated whether the file exists or not.
type FileArtifact struct {
	Path  string
	State ArtifactState
	Bytes []byte
}

// Artifacts holds the results of one classify invocation: the exit code and
// captured output streams. RunState and V2Sidecar are populated only on the
// classify path; subcommands (init, capabilities, help) produce exit code,
// stdout, and stderr only, leaving RunState and V2Sidecar empty.
//
// ExitCode is a member of DeclaredExitCodes. Its zero value IS exitOK (0), so
// "nobody set it" and "the run succeeded" are the same value. RunWiring always
// populates ExitCode; a zero ExitCode paired with a non-nil err means
// RunWiring itself failed to run the invocation (see clause 5 of RunWiring).
//
// Stdout and Stderr carry output from functions given an io.Writer argument.
// The log package writes to process-global streams and cannot be captured
// without log.SetOutput, which is forbidden (see RunWiring clause 3).
type Artifacts struct {
	ExitCode  ExitCode
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
// CRITICAL: Dir MUST NOT cause os.Chdir. The process is shared and may be
// running in a shared CI environment. Paths must be resolved directory-scoped.
// For absolute paths or relative paths that should be relative to Dir, use
// filepath.Join(Dir, path). The process's working directory must never change.
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
//     reimplementation. main() delegates to it. If this is a separate
//     implementation, every row that calls it is vacuous by construction.
//  2. IT NEVER EXITS THE PROCESS. Every os.Exit and every log.Fatalf on the
//     classify path becomes a returned Artifacts.ExitCode. os.Exit survives in
//     main() and nowhere else.
//  3. IT CAPTURES OUTPUT written through io.Writer arguments. Stdout and Stderr
//     on the returned Artifacts capture output from emit, printReport,
//     printInvalidInput, and reportConfigSearch, which are given an io.Writer.
//     Output from the log package is process-global and is not captured without
//     log.SetOutput, which is forbidden because it is a process-wide mutation
//     (same rationale as clause 7, os.Chdir).
//  4. IT REPORTS ARTIFACT STATE, NOT EXISTENCE. It snapshots -out and
//     V2SidecarPath(-out) BEFORE running and compares after, so files that do
//     not exist before and after are ArtifactAbsent, files that differ are
//     ArtifactWritten or ArtifactRemoved, and files that are byte-identical are
//     ArtifactStale (unchanged by this run, even if identical bytes were written).
//     With no -out, both FileArtifacts are ArtifactAbsent with the Path field
//     empty.
//  5. THE ERROR RETURN IS NOT THE RUN'S FAILURE. A classify run that fails is
//     a non-zero ExitCode with err == nil. A non-nil err means RunWiring
//     ITSELF could not run the invocation — it could not snapshot files,
//     could not resolve paths, was handed an Invocation it cannot honour. Rows
//     must not conflate them: `if err != nil { t.Fatal }` then assert on
//     ExitCode.
//  6. SUBCOMMANDS ARE IN SCOPE. Args[0] of "init", "capabilities", "help",
//     "-h" or "--help" takes main()'s pre-flag-parse branch, and its exit code
//     is this function's answer too. The capabilities probe dispatches AHEAD
//     of flag parsing on purpose and that ordering is part of the mapping.
//  7. IT NEVER CALLS os.Chdir. Paths are resolved directory-scoped: relative
//     paths joined with inv.Dir using filepath.Join. The process's working
//     directory is never changed, because the process is shared and may be
//     running in a shared CI environment.
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
//     RunWiring can map it to an exit code. The caller sets
//     fs.SetErrorHandling(flag.ContinueOnError). Flag errors are returned to
//     RunWiring, not passed through os.Exit. RunWiring maps flag errors to
//     exit code 2 (exitFlagError, a member of DeclaredExitCodes).
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
