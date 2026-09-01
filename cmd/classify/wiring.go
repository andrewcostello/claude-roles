// Wiring contract — GO-1-1 (scaffold). STUBS AND TYPE DEFINITIONS ONLY.
//
// Types and signatures for the classify wiring. ArtifactState.String and
// ArtifactState.Valid are implemented because type correctness needs them;
// RunWiring and parseInvocationFlags return ErrWiringNotImplemented and their
// bodies are GO-1-3's. A scaffold that implemented the mapping its own seals
// will judge would destroy the point of sealing first.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

// ErrWiringNotImplemented is what every stub in this file returns.
//
// A distinct sentinel, not a generic error, so GO-1-2's seals can tell "the
// body is not written yet" (expected RED) from "the body is written and wrong"
// (a finding). A seal that cannot tell those apart goes green the day the stub
// is replaced by a broken body.
var ErrWiringNotImplemented = errors.New(
	"classify wiring: stub from GO-1-1 (scaffold); the body is GO-1-3's and is not written yet")

const (
	// exitOK: the run classified successfully.
	exitOK = 0
	// exitInternal: the run could not complete for a reason that is not the
	// operator's input — an unreadable file, a failed write. It is also what
	// main returns when RunWiring itself fails (see RunWiring clause 8).
	exitInternal = 1
	// exitFlagError: argv did not parse. The flag package's code for this, and
	// therefore not ours to choose.
	exitFlagError = 2
)

// DeclaredExitCodes is the closed set of exit codes cmd/classify may produce.
//
// Closed is the operative word and the only claim here: an exit code outside
// this set is a defect, not a new feature. Enumerated once because three
// hand-written lists need three edits to stay honest, and the edit that only
// happened twice is this project's whole failure class.
//
// Membership is not endorsement — exitInternal is listed because it is
// reachable, and where it is reachable FROM is a fact about the tree that
// GO-1-3 changes. Nor is it a claim about usage(): the gap between what this
// binary can exit with and what it advertises is recorded in docs/DECISIONS.md
// (H2, H3), where a reviewer reads it once instead of every agent re-reading it
// forever.
var DeclaredExitCodes = []int{
	exitOK,
	exitInvalid,
	exitInternal,
	exitFlagError,
	exitCapabilityIncomplete,
}

// ─── the artifact set ────────────────────────────────────────────────────────

// ExitCode is the process exit code a classify run produced. Whenever RunWiring
// returns a nil error it is a member of DeclaredExitCodes.
//
// ITS ZERO VALUE IS exitOK, and this contract does not pretend otherwise. An
// exit code is an OS-defined integer in which 0 means success; a named type
// cannot give it a spare invalid state, and asserting one in prose would be a
// claim the type does not honour.
//
// "Nobody set it" is carried by the ERROR RETURN instead, which CAN express it:
// see RunWiring clause 5. When RunWiring returns a non-nil error the Artifacts
// value carries no claims at all, so a zero ExitCode beside an error is never
// success — it is nothing.
type ExitCode int

// ArtifactState is what one run did to one file on disk.
//
// A bool cannot express the difference between a correct write and a defect.
// "The file is not there" is two facts — never was, or this run removed it —
// and "the file is there" is two more: this run wrote it, or a previous run did
// and this one left it alone. Whether a run PRODUCES the artifact at all is a
// fifth fact, distinct from having looked and found nothing.
//
// This is skills/explicit-state.md applied to a file: absence is a state and it
// must be nameable.
type ArtifactState int

const (
	// ArtifactStateUnset is the zero value. It is ILLEGAL in an Artifacts
	// returned with a nil error — that is the boundary, and it is the whole
	// boundary. It exists so "nobody looked" raises instead of reading
	// silently as "absent", the discipline ContractVersionUnset enforces for
	// the contract version.
	//
	// It IS legal beside a non-nil error, because such an Artifacts asserts
	// nothing (clause 5). That is how "applicable, but the state could not be
	// determined" is expressed: RunWiring returns an error rather than
	// inventing an observation it never made.
	ArtifactStateUnset ArtifactState = iota
	// ArtifactAbsent: checked, and not present before or after. The run neither
	// wrote nor removed it.
	ArtifactAbsent
	// ArtifactWritten: checked, and the after-bytes differ from the
	// before-bytes (which includes not existing before).
	ArtifactWritten
	// ArtifactRemoved: checked, present before, absent after.
	ArtifactRemoved
	// ArtifactStale: checked, present before and after, byte-identical.
	//
	// The oracle is byte-equality across the snapshot, which cannot see a
	// deterministic rewrite of identical bytes — that reports Stale. The
	// contract states the oracle rather than claiming a distinction the oracle
	// cannot draw; "unchanged by this run" means "no bytes differ", nothing
	// more.
	ArtifactStale
	// ArtifactNotApplicable: this invocation does not produce this artifact, so
	// no snapshot was taken. Distinct from Absent, which is an observation.
	// FileArtifact.Path is empty in this state.
	ArtifactNotApplicable
)

// String renders the state for failure messages. Total: it renders
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
	case ArtifactNotApplicable:
		return "not-applicable"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// Valid reports membership in the closed set {Absent, Written, Removed, Stale,
// NotApplicable}. ArtifactStateUnset is NOT valid.
func (s ArtifactState) Valid() bool {
	switch s {
	case ArtifactAbsent, ArtifactWritten, ArtifactRemoved, ArtifactStale, ArtifactNotApplicable:
		return true
	default:
		return false
	}
}

// FileArtifact is one on-disk output of a run, with the state that produced it.
//
// Path is the resolved path when the invocation produces this artifact, and
// empty when State is ArtifactNotApplicable — a produced artifact has a path
// whether or not a file is there. Bytes carries the content after the run, and
// is nil in every state but ArtifactWritten and ArtifactStale.
type FileArtifact struct {
	Path  string
	State ArtifactState
	Bytes []byte
}

// Artifacts is everything one classify invocation produced.
//
// IT IS MEANINGFUL ONLY WHEN RunWiring RETURNED A NIL ERROR. Beside a non-nil
// error every field is unset and asserts nothing (clause 5).
//
// Stdout and Stderr are the bytes the run emitted, captured because RunWiring
// supplied the writers they were emitted through (clause 3).
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
// DIR IS THE RESOLUTION ROOT, NOT A WORKING DIRECTORY. Every path this
// invocation names — flag values and positional file arguments alike — resolves
// as: absolute paths are used unchanged, relative paths are taken relative to
// Dir. filepath.Join(Dir, p) is correct for the second case ONLY; applied to an
// absolute p it silently relocates the operator's target underneath Dir.
//
// Dir MUST NOT be applied with os.Chdir (clause 7). A row that leaves it empty
// runs against the test process's working directory, which is a live git
// repository — resolveRepo would then shell out to git against the repo under
// review. Rows pass t.TempDir() and -no-git unless git state is the subject.
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
//     reimplementation. If this is a separate implementation, every row that
//     calls it is vacuous by construction. main() retains exactly three
//     duties — process-wide logger configuration, forwarding the result
//     (clause 8), and the single os.Exit call. Subcommand dispatch, flag
//     parsing and the classify path all run inside RunWiring, so the global
//     parseFlags() entry point ceases to exist rather than becoming a second
//     caller of parseInvocationFlags.
//  2. IT NEVER EXITS THE PROCESS. Every os.Exit and every log.Fatalf on this
//     path becomes a returned ExitCode. os.Exit survives in main() and nowhere
//     else.
//  3. EVERY BYTE THE RUN EMITS PASSES THROUGH A WRITER RunWiring SUPPLIED, and
//     is therefore captured on Stdout/Stderr. This is a requirement on the
//     code, not a description of it: a path that writes to a process-global
//     stream — the log package, or a fmt call naming no writer — is a defect
//     for GO-1-3 to remove, not an exception to this clause. That includes the
//     FlagSet, whose output parseInvocationFlags's caller must retarget.
//     log.SetOutput and any other process-wide redirection are forbidden for
//     the reason clause 7 gives.
//  4. IT REPORTS ARTIFACT STATE, NOT EXISTENCE. It snapshots each artifact it
//     produces before running and compares after, per ArtifactState. An
//     artifact this invocation does not produce is ArtifactNotApplicable and is
//     not snapshotted.
//  5. THE ERROR RETURN IS NOT THE RUN'S FAILURE. A classify run that fails is a
//     non-zero ExitCode with a nil error. A non-nil error means RunWiring
//     ITSELF could not run the invocation — could not snapshot, could not
//     resolve a path, was handed an Invocation it cannot honour — and then the
//     Artifacts is empty and asserts nothing. Rows must not conflate them:
//     `if err != nil { t.Fatal }`, then assert on ExitCode.
//  6. SUBCOMMANDS ARE IN SCOPE. "init", "capabilities", "help", "-h" and
//     "--help" as Args[0] take the pre-flag-parse branch inside RunWiring, and
//     their exit codes are this function's answer too. The capabilities probe
//     dispatches ahead of flag parsing on purpose, and that ordering is part of
//     the mapping under test.
//  7. IT NEVER CALLS os.Chdir. Paths resolve against inv.Dir as Invocation
//     documents. The process is shared — by the test binary's own parallel
//     rows, and by whatever else runs in CI — so a process-global mutation is a
//     race, not a shortcut.
//  8. main() FORWARDS THE RESULT AND ADDS NOTHING. It writes Stdout to
//     os.Stdout and Stderr to os.Stderr, and exits with ExitCode. On a non-nil
//     error it reports that error on os.Stderr and exits exitInternal. Without
//     this clause every in-process seal could pass while the shipped binary is
//     silent, or exits 0 after failing to run at all.
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
//
// Contract:
//
//  1. It registers exactly the flags the classify path accepts, and
//     -contract-version THROUGH registerContractVersionFlag, never by calling
//     flag.String here. The indirection is the capability registry's, and
//     duplicating it would let the probe's answer and the flag's existence
//     disagree.
//  2. It does not parse -contract-version. The raw string is threaded to
//     options.contractVersion and validated in run(), because validating it
//     here could only report failure by exiting — exitInternal — and a mistyped
//     contract owes the caller exitInvalid.
//  3. A FLAG ERROR IS RETURNED, NOT FATAL. The caller supplies a FlagSet with
//     flag.ContinueOnError and its own output writer, and fs.Parse's error
//     comes back. RunWiring maps that error to exitFlagError — the mapping is
//     decided here, by the skeleton, and GO-1-2 seals it.
//  4. It does NOT configure the logger. Process-wide logger state belongs to
//     main() (RunWiring clause 1), not to a function a test calls a hundred
//     times.
//
// STUB. Returns ErrWiringNotImplemented. GO-1-3 owns the body.
func parseInvocationFlags(fs *flag.FlagSet, args []string) (options, error) {
	_, _ = fs, args
	return options{}, ErrWiringNotImplemented
}
