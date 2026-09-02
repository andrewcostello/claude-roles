package main

// Seals for wiring.go — GO-1-2.
//
// THE SUBJECT IS THE MAPPING, NOT THE LIBRARY:
//
//	(contract × -out × -json) → artifact set + exit code
//
// EmitV1, EmitV2, WriteV2Sidecar and ParseContractVersion each have green seals
// of their own. None of those can say which of them run() CHOSE, and at
// 0cfdb57 rewriting emit()'s ContractV2 arm to EmitV1 reddened nothing in this
// package — re-derived at b0313fa before this file was written: whole suite
// `ok`. Every row below judges what the spine ANSWERED for a cell of that
// mapping, beside a cell that must answer the other way.
//
// TWO DRIVERS, ONE TABLE. RunWiring is the GO-1-1 stub and returns
// ErrWiringNotImplemented, so a row that reaches production only through it is
// red whatever the mutation does and detects nothing. The mapping is therefore
// driven twice:
//
//   - driveSpine goes through the seam main() runs TODAY —
//     `os.Exit(run(parseFlags()))` — with the flag half bypassed: the row
//     supplies the options struct parseFlags would have produced. These rows
//     are GREEN today and RED under the measured mutation. They are the seal.
//   - driveRunWiring goes through RunWiring with the same expectations. These
//     rows are RED today, by the sentinel and by nothing else, and become the
//     seal of record when GO-1-3 lands the body. A body that answers the table
//     differently from the spine is a finding, not a stub.
//
// The table is one value, mappingCells, so the two drivers cannot be judged
// against different expectations.
//
// TWO MORE OBSERVERS, for what no in-process driver can see. driveChild runs
// RunWiring in a re-executed copy of this test binary whose file descriptors
// 1 and 2 are pipes, so a writer cached before any stream variable was swapped
// still lands in the parent's hands. And TestSeal_Wiring_MainForwardsRunWiring_LiveBinary
// builds the tree and runs the shipped binary beside RunWiring on the same
// argv — clause 8 is a claim about main(), and main() runs nowhere else.
//
// THE STRUCT IS NOT THE EVIDENCE. Both drivers take their own two snapshots of
// -out and its sidecar. driveSpine has to, because run() reports nothing;
// driveRunWiring does anyway, and holds the Artifacts RunWiring returned
// against the tree. A body that answers Written/Removed with the right Bytes
// and never touches a file is a finding, not a pass — and on the day
// driveSpine retires it would otherwise be the only thing left looking.
//
// None of these tests may call t.Parallel: they capture os.Stdout and
// os.Stderr, retarget the standard logger's writer, swap the process-wide
// digest recorder and certified-config slot, and watch the process's own
// working directory for files no row asked for.

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ─── the fixture ─────────────────────────────────────────────────────────────

// wiringFixture is one directory holding a rule table and a diff, with the
// exact bytes of each kept so the dual digest echo can be checked against what
// the run consumed rather than against whatever the source returned.
type wiringFixture struct {
	dir         string
	configPath  string // absolute
	diffPath    string // absolute
	configBytes []byte
	diffBytes   []byte
}

// Relative names, resolved against wiringFixture.dir. Rows through RunWiring
// pass these (clause 7 says relative paths resolve against Invocation.Dir);
// rows through the spine pass the absolute forms, because run() resolves
// nothing.
const (
	fixtureConfigName = "config.json"
	fixtureDiffName   = "wallet.diff"
	fixtureRunState   = "run.json"
)

func newWiringFixture(t *testing.T) wiringFixture {
	t.Helper()
	cfg, err := os.ReadFile(exampleConfigPath)
	if err != nil {
		t.Fatalf("read %s: %v", exampleConfigPath, err)
	}
	dir := t.TempDir()
	f := wiringFixture{
		dir:         dir,
		configPath:  filepath.Join(dir, fixtureConfigName),
		diffPath:    filepath.Join(dir, fixtureDiffName),
		configBytes: cfg,
		diffBytes:   []byte(diffFor(walletPath)),
	}
	if err := os.WriteFile(f.configPath, f.configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.diffPath, f.diffBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// wiringSeedRunState and wiringSeedSidecar are what a PREVIOUS run left at -out. The
// run-state is valid JSON because writeRunState merges into it and treats an
// unparsable file as fatal; the sidecar is deliberately not a real sidecar, so
// that "written" and "stale" cannot be confused by a body that rewrote the
// seed's own bytes.
const (
	wiringSeedRunState = "{\n  \"schema_version\": 1,\n  \"status\": \"seeded by the wiring seal\"\n}\n"
	wiringSeedSidecar  = "{\"seeded_by\": \"the wiring seal\", \"not\": \"a sidecar\"}\n"
)

// ─── the artifact oracle ─────────────────────────────────────────────────────

// fileSnap is one observation of one path.
type fileSnap struct {
	present bool
	bytes   []byte
}

func snapFile(t *testing.T, p string) fileSnap {
	t.Helper()
	data, err := os.ReadFile(p) // #nosec G304 -- a temp path this test created
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileSnap{}
		}
		t.Fatalf("snapshot %s: %v", p, err)
	}
	return fileSnap{present: true, bytes: data}
}

// stateOf is ArtifactState's oracle, exactly as wiring.go states it: two
// observations, byte-equality, no history.
func stateOf(before, after fileSnap) ArtifactState {
	switch {
	case !before.present && !after.present:
		return ArtifactAbsent
	case before.present && !after.present:
		return ArtifactRemoved
	case before.present && bytes.Equal(before.bytes, after.bytes):
		return ArtifactStale
	default:
		return ArtifactWritten
	}
}

// The oracle is test code, and every row below rests on it, so its four cells
// are pinned once. Stale versus Written is the distinction D3.6 turns on.
func TestSeal_Wiring_ArtifactOracle_FourStates(t *testing.T) {
	defer red(t)
	a, b := fileSnap{present: true, bytes: []byte("a")}, fileSnap{present: true, bytes: []byte("b")}
	none := fileSnap{}
	for _, row := range []struct {
		name          string
		before, after fileSnap
		want          ArtifactState
	}{
		{"absent", none, none, ArtifactAbsent},
		{"created", none, a, ArtifactWritten},
		{"rewritten", a, b, ArtifactWritten},
		{"stale", a, a, ArtifactStale},
		{"removed", a, none, ArtifactRemoved},
	} {
		if got := stateOf(row.before, row.after); got != row.want {
			t.Errorf("%s: stateOf = %s, want %s", row.name, got, row.want)
		}
	}
}

// ─── the mapping table ───────────────────────────────────────────────────────

type outMode int

const (
	outNone   outMode = iota // no -out
	outFresh                 // -out names a path nothing has written
	outSeeded                // -out and its v2 sidecar both exist from a "previous run"
)

type stdoutShape int

const (
	shapeReport  stdoutShape = iota // the human report; identical under both contracts
	shapeV1                         // the bare v1 Classification
	shapeWrapper                    // the ResponseWrapper carrying the v2 envelope
)

// What the run SAYS on stderr about each artifact it wrote or removed. These
// are persist()'s own lines; the mapping pins them because they are how a
// reader of stderr learns what the artifact set was, and because a body that
// leaves them on the process-global logger has an Artifacts.Stderr that
// carries none of them (RunWiring clause 3).
const (
	saysRunStateWritten = "run state written to"
	saysSidecarWritten  = "v2 sidecar written to"
	saysSidecarRemoved  = "stale v2 sidecar removed from"
)

// mappingCell is one point of (contract × -out × -json) and what it must
// answer. Expectations are STATED, not derived from the code under test.
type mappingCell struct {
	name     string
	contract string // the raw -contract-version value
	json     bool
	out      outMode

	exit     ExitCode
	stdout   stdoutShape
	runState ArtifactState
	sidecar  ArtifactState

	// stderrSays are substrings stderr must carry — one per artifact the cell
	// writes or removes; stderrSilentOn are the lines for artifacts it does
	// not touch, which must be absent.
	stderrSays     []string
	stderrSilentOn []string
}

// mappingCells is the whole table: 2 contracts × 3 -out modes × 2 -json
// settings. Every cell is judged in one test, so each contract's cells are the
// other's control: a body that always emits the wrapper fails every v1 -json
// cell, one that never emits it fails every v2 -json cell, and one that always
// (or never) writes the sidecar fails the seeded cells of one contract or the
// other.
//
// THE MEASURED MUTATION — emit()'s ContractV2 arm rewritten to EmitV1 —
// reddens exactly the three cells with contract "2" and json true, on the
// stdout shape; the mirror mutation (ContractV1 arm → EmitV2) reddens exactly
// the three v1 -json cells; dropping RemoveV2Sidecar from persist()'s v1 arm
// reddens exactly the two v1 seeded cells. Measured at b0313fa with this file
// in place; the other cells stayed green under each.
func mappingCells() []mappingCell {
	var cells []mappingCell
	for _, contract := range []string{"1", "2"} {
		for _, out := range []outMode{outNone, outFresh, outSeeded} {
			for _, asJSON := range []bool{false, true} {
				c := mappingCell{contract: contract, json: asJSON, out: out, exit: exitOK}
				switch {
				case !asJSON:
					c.stdout = shapeReport
				case contract == "1":
					c.stdout = shapeV1
				default:
					c.stdout = shapeWrapper
				}
				switch out {
				case outNone:
					c.runState, c.sidecar = ArtifactNotApplicable, ArtifactNotApplicable
				case outFresh:
					c.runState = ArtifactWritten
					if contract == "2" {
						c.sidecar = ArtifactWritten
					} else {
						c.sidecar = ArtifactAbsent
					}
				case outSeeded:
					c.runState = ArtifactWritten
					if contract == "2" {
						c.sidecar = ArtifactWritten
					} else {
						c.sidecar = ArtifactRemoved
					}
				}
				// stderr narrates the artifact set, and nothing else about it.
				for _, line := range []struct {
					says string
					when bool
				}{
					{saysRunStateWritten, c.runState == ArtifactWritten},
					{saysSidecarWritten, c.sidecar == ArtifactWritten},
					{saysSidecarRemoved, c.sidecar == ArtifactRemoved},
				} {
					if line.when {
						c.stderrSays = append(c.stderrSays, line.says)
					} else {
						c.stderrSilentOn = append(c.stderrSilentOn, line.says)
					}
				}
				c.name = "contract=" + contract + "/out=" + [...]string{"none", "fresh", "seeded"}[out] + "/json=" + map[bool]string{false: "off", true: "on"}[asJSON]
				cells = append(cells, c)
			}
		}
	}
	return cells
}

// prepareOut seeds -out in dir per the mode and returns the run-state path, or
// "" for outNone.
func prepareOut(t *testing.T, dir string, mode outMode) string {
	t.Helper()
	if mode == outNone {
		return ""
	}
	runState := filepath.Join(dir, fixtureRunState)
	if mode == outSeeded {
		if err := os.WriteFile(runState, []byte(wiringSeedRunState), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(V2SidecarPath(runState), []byte(wiringSeedSidecar), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return runState
}

// ─── observing one invocation ────────────────────────────────────────────────

// wiringRun is one observed invocation: what it answered, what escaped past
// the answer onto the process's own streams, and the two process-wide slots
// the input path writes into.
type wiringRun struct {
	art Artifacts
	err error

	// Bytes that reached the PROCESS's stdout, stderr and standard logger
	// during the call. Through RunWiring every one is a clause 3 leak. Through
	// the spine, run() reports on the standard logger by design today, so
	// driveSpine folds the logged bytes into art.Stderr — that IS the stderr
	// an operator sees — and leaves these empty.
	leakedStdout, leakedStderr, logged string

	// digests is the fresh recorder this run's input path wrote into, so a row
	// can ask whether the config and diff channels were CONSUMED — not whether
	// the files exist. certified is the fresh slot the config SEARCH certifies
	// a resolved table into, so a row can ask whether a search resolved
	// anything. Both are observations of what the run did, not of its output.
	digests   *unframedDigestSource
	certified *certifiedConfigRead
}

func (r wiringRun) consumed() (config, diff bool) {
	r.digests.mu.Lock()
	defer r.digests.mu.Unlock()
	return r.digests.sawConfig, r.digests.sawDiff
}

func (r wiringRun) searchCertified() bool {
	r.certified.mu.Lock()
	defer r.certified.mu.Unlock()
	return r.certified.held
}

// isolateProcessState installs a fresh recorder as BOTH the package recorder
// the input path writes into AND the installed DigestSource the wrapper reads
// from, installs a fresh certified-config slot, and restores all of it
// afterwards. It returns the two fresh slots so the row can read them.
//
// Both recorder slots, not one: init() stored the recorder's pointer in
// digestSource, so swapping unframedDigests alone leaves EmitV2 reading a
// recorder nothing wrote into. It would raise, run() would log.Fatalf, and the
// whole test binary would exit 1 mid-row.
func isolateProcessState(t *testing.T) (*unframedDigestSource, *certifiedConfigRead) {
	t.Helper()
	fresh, slot := &unframedDigestSource{}, &certifiedConfigRead{}
	savedRecorder, savedSlot := unframedDigests, certifiedConfig
	t.Cleanup(func() { unframedDigests, certifiedConfig = savedRecorder, savedSlot })
	unframedDigests, certifiedConfig = fresh, slot
	withHooks(t, contractFlagRegistrar, fresh, framedStdinReader)
	return fresh, slot
}

// processStreamsOf runs fn and returns what reached the process's stdout, its
// stderr, and its standard logger.
//
// The logger is observed on its own because swapping os.Stderr cannot see it:
// the log package bound its writer to the ORIGINAL os.Stderr at package init,
// so a body that leaves a log.Printf in place writes to the terminal, not to
// the swapped pipe, and only the logger's own writer can witness it. It is
// teed to its previous writer so that a log.Fatalf mid-row still reaches the
// terminal before it exits the process.
//
// WHAT THIS CANNOT SEE: a writer bound to the original descriptor before the
// swap — `var out = os.Stdout` at package level, a log.New(os.Stderr, ...)
// held in a global, an os.NewFile(1, ...) — keeps writing to the terminal and
// is invisible to all three channels here. driveChild observes those, from
// outside the process; the structural row rejects the bindings themselves.
func processStreamsOf(t *testing.T, fn func()) (stdout, stderr, logged string) {
	t.Helper()
	var logBuf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(io.MultiWriter(&logBuf, prev))
	defer log.SetOutput(prev)
	stderr = captureStream(t, &os.Stderr, func() {
		stdout = captureStream(t, &os.Stdout, fn)
	})
	return stdout, stderr, logBuf.String()
}

// judgeDisk holds one FileArtifact a run reported against the two snapshots
// the driver took of the same path. The state must be the one the oracle
// derives, and Bytes must be the file's content after the run — or nothing,
// when there is no file. A not-applicable artifact must have no file at its
// conventional path either: a run that says it produces no run-state and
// writes one anyway is not exempt for having said so.
func judgeDisk(t *testing.T, what string, got FileArtifact, before, after fileSnap) {
	t.Helper()
	disk := stateOf(before, after)
	if got.State == ArtifactNotApplicable {
		if disk != ArtifactAbsent {
			t.Errorf("%s: reported not-applicable, but the file at its conventional path is %s — the run touched an artifact it says it does not produce", what, disk)
		}
		return
	}
	if got.State != disk {
		t.Errorf("%s: reported %s, but the two snapshots say %s — the struct does not match the tree", what, got.State, disk)
	}
	if after.present && !bytes.Equal(got.Bytes, after.bytes) {
		t.Errorf("%s: reported %d bytes, but the file on disk holds %d different bytes — Bytes must be the content after the run", what, len(got.Bytes), len(after.bytes))
	}
	if !after.present && len(got.Bytes) != 0 {
		t.Errorf("%s: reported %d bytes for a file that is not there — Bytes is nil in every state but written and stale", what, len(got.Bytes))
	}
}

// pairSnap is one run-state path and its v2 sidecar, observed together.
type pairSnap struct {
	path   string
	rs, sc fileSnap
}

func snapPair(t *testing.T, p string) pairSnap {
	t.Helper()
	return pairSnap{path: p, rs: snapFile(t, p), sc: snapFile(t, V2SidecarPath(p))}
}

// cwdRunState is the conventional run-state path in the TEST PROCESS's working
// directory — a place no row names. A file appearing there is a path that
// resolved against the cwd instead of -worktree or Invocation.Dir, and the
// operator's tree would carry it. The row fails as a fixture problem if it is
// already there, so a stale file cannot be misread as this run's.
func cwdRunState(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(cwd, fixtureRunState)
	for _, q := range []string{p, V2SidecarPath(p)} {
		if snapFile(t, q).present {
			t.Fatalf("fixture: %s exists before the run — the test process's working directory is dirty; remove it and re-run", q)
		}
	}
	return p
}

// judgeUntouched holds a path THIS ROW DID NOT NAME to judgeDisk's
// not-applicable rule: the run must have left nothing there, whatever the
// struct said. Every driver applies it to the process cwd on every call, and
// to -worktree's conventional run-state path whenever the row names no -out —
// which is the case the panel measured: a persist() that writes a default
// run-state when there is no -out is silent on stderr, reports NotApplicable
// truthfully, and would otherwise pass.
//
// A file the run CREATED there — absent before, present after — is removed
// once reported, so that one defect reads as one failure per cell rather than
// one failure and then a dirty-cwd fixture complaint from every later cell.
// Only a file both snapshots prove the run made is touched.
func judgeUntouched(t *testing.T, name string, before pairSnap) {
	t.Helper()
	after := snapPair(t, before.path)
	notApplicable := FileArtifact{State: ArtifactNotApplicable}
	judgeDisk(t, name+": run-state at "+before.path+" (a path this row never named)", notApplicable, before.rs, after.rs)
	judgeDisk(t, name+": v2 sidecar beside "+before.path+" (a path this row never named)", notApplicable, before.sc, after.sc)
	for _, f := range []struct {
		p             string
		before, after fileSnap
	}{{before.path, before.rs, after.rs}, {V2SidecarPath(before.path), before.sc, after.sc}} {
		if !f.before.present && f.after.present {
			if err := os.Remove(f.p); err != nil {
				t.Errorf("%s: could not remove %s, which this run created: %v", name, f.p, err)
			}
		}
	}
}

// ─── driver 1: the spine main() runs today ───────────────────────────────────

// spineOptions is the options struct parseFlags would produce for the cell.
// The defaults it fills (-base, -task) are the ones parseFlags registers;
// -worktree is the fixture directory rather than "." so that nothing here
// depends on the test process's working directory.
func spineOptions(f wiringFixture, cell mappingCell, runState string) options {
	return options{
		configPath:      f.configPath,
		worktree:        f.dir,
		base:            "origin/main",
		out:             runState,
		json:            cell.json,
		noGit:           true,
		contractVersion: cell.contract,
		args:            []string{f.diffPath},
	}
}

// driveSpine runs one invocation through run() — the code main() runs today —
// and reports it in the contract's own vocabulary.
//
// run() reports through the standard logger, which is process-global. The
// driver captures that logger's output for the duration of the call and
// reports it as Stderr, beside anything written to os.Stderr itself, because
// that is what an operator's stderr carries today.
//
// THE DISK IS SNAPSHOTTED ON EVERY CALL, not only when -out is named. With
// -out the two snapshots ARE the reported state. Without it the driver
// pre-fills NotApplicable — run() reports nothing — and then holds that claim
// to the tree: -worktree's conventional run-state path and the process cwd
// must both be untouched, or the "not applicable" the driver synthesised was
// a lie the row would have judged as truth.
func driveSpine(t *testing.T, name string, opts options) wiringRun {
	t.Helper()
	var r wiringRun
	r.digests, r.certified = isolateProcessState(t)

	r.art = Artifacts{RunState: FileArtifact{State: ArtifactNotApplicable}, V2Sidecar: FileArtifact{State: ArtifactNotApplicable}}
	var rsBefore, scBefore fileSnap
	if opts.out != "" {
		rsBefore, scBefore = snapFile(t, opts.out), snapFile(t, V2SidecarPath(opts.out))
	}
	untouched := []pairSnap{snapPair(t, cwdRunState(t))}
	if opts.out == "" {
		untouched = append(untouched, snapPair(t, filepath.Join(opts.worktree, fixtureRunState)))
	}

	var code int
	stdout, stderr, logged := processStreamsOf(t, func() { code = run(opts) })
	r.art.ExitCode = ExitCode(code)
	r.art.Stdout = []byte(stdout)
	r.art.Stderr = []byte(stderr + logged)

	if opts.out != "" {
		rsAfter, scAfter := snapFile(t, opts.out), snapFile(t, V2SidecarPath(opts.out))
		r.art.RunState = FileArtifact{Path: opts.out, State: stateOf(rsBefore, rsAfter), Bytes: rsAfter.bytes}
		r.art.V2Sidecar = FileArtifact{Path: V2SidecarPath(opts.out), State: stateOf(scBefore, scAfter), Bytes: scAfter.bytes}
	}
	for _, u := range untouched {
		judgeUntouched(t, name, u)
	}
	return r
}

// ─── driver 2: RunWiring ─────────────────────────────────────────────────────

// invocationFor is the argv an operator would type for the cell, with every
// path RELATIVE so that the row also proves Dir resolution (clause 7).
func invocationFor(f wiringFixture, cell mappingCell) Invocation {
	args := []string{"-no-git", "-config", fixtureConfigName, "-contract-version", cell.contract}
	if cell.json {
		args = append(args, "-json")
	}
	if cell.out != outNone {
		args = append(args, "-out", fixtureRunState)
	}
	args = append(args, fixtureDiffName)
	return Invocation{Args: args, Dir: f.dir}
}

// driveRunWiring calls RunWiring and, beside its answer, reports every byte
// that reached the PROCESS's streams during the call — which clause 3 says
// must be none — and holds the returned Artifacts against the disk.
//
// Every row names -out as fixtureRunState relative to Dir, or not at all, so
// the driver snapshots that conventional path either way and judgeDisk decides
// what the reported state may be beside what the tree says. The process cwd is
// snapshotted too: a body that resolved "run.json" against the cwd instead of
// Dir would leave the Dir path absent — caught — and the cwd path written,
// which is the operator's tree polluted and is reported as such.
func driveRunWiring(t *testing.T, name string, inv Invocation) wiringRun {
	t.Helper()
	var r wiringRun
	r.digests, r.certified = isolateProcessState(t)

	runState := filepath.Join(inv.Dir, fixtureRunState)
	rsBefore, scBefore := snapFile(t, runState), snapFile(t, V2SidecarPath(runState))
	cwd := snapPair(t, cwdRunState(t))
	r.leakedStdout, r.leakedStderr, r.logged = processStreamsOf(t, func() { r.art, r.err = RunWiring(inv) })
	if r.err != nil {
		return r
	}
	rsAfter, scAfter := snapFile(t, runState), snapFile(t, V2SidecarPath(runState))
	judgeDisk(t, name+": run-state", r.art.RunState, rsBefore, rsAfter)
	judgeDisk(t, name+": v2 sidecar", r.art.V2Sidecar, scBefore, scAfter)
	judgeUntouched(t, name, cwd)
	return r
}

// ─── driver 3: RunWiring on the process's REAL descriptors ───────────────────

// processStreamsOf swaps the os.Stdout and os.Stderr VARIABLES and retargets
// the standard logger. A writer bound before the swap keeps the original
// descriptor and is invisible to all three. The only observer that sees every
// byte is the operating system, so this driver runs RunWiring in a CHILD
// PROCESS — this test binary re-executed — whose file descriptors 1 and 2 are
// pipes the parent holds, and the Artifacts come back through a file, never a
// stream. Anything on either pipe is a leak, whatever wrote it.
//
// TestWiringSealChild is the child half. It runs only when wiringChildEnv
// names a request file, and it calls os.Exit before the testing package can
// print anything of its own, so an empty pipe means the run wrote nothing.
//
// The CONTROL is a mode that writes through writers cached at THIS FILE's
// package init — the exact shape the in-process observer cannot see — and
// through the standard logger; the parent must receive every byte.

const wiringChildEnv = "CLASSIFY_WIRING_SEAL_CHILD"

const (
	wiringChildModeRun         = "run"
	wiringChildModeLeakControl = "leak-control"
)

// Bound at package init, before any test swaps a stream variable. They live in
// a _test.go file, which the structural row's scan skips on purpose.
var wiringCachedStdout, wiringCachedStderr = os.Stdout, os.Stderr

const (
	wiringControlStdout = "wiring-seal control: a writer cached at package init reached fd 1\n"
	wiringControlStderr = "wiring-seal control: a writer cached at package init reached fd 2\n"
	wiringControlLogged = "wiring-seal control: the standard logger reached fd 2"
)

type wiringChildRequest struct {
	Mode string
	Args []string
	Dir  string
}

type wiringChildReply struct {
	Art  Artifacts
	Stub bool   // the error was ErrWiringNotImplemented
	Err  string // any other error's text; "" for nil
}

// TestWiringSealChild is the child half of driveChild. Under a plain `go test`
// it skips; with wiringChildEnv set it does its work and never returns.
func TestWiringSealChild(t *testing.T) {
	reqPath := os.Getenv(wiringChildEnv)
	if reqPath == "" {
		t.Skip("harness: runs only as the child of driveChild")
	}
	die := func(format string, args ...any) {
		_, _ = fmt.Fprintf(wiringCachedStderr, "child: "+format+"\n", args...)
		os.Exit(3)
	}
	data, err := os.ReadFile(reqPath) // #nosec G304 -- the parent's own temp file
	if err != nil {
		die("read request: %v", err)
	}
	var req wiringChildRequest
	if err := json.Unmarshal(data, &req); err != nil {
		die("decode request: %v", err)
	}
	var reply wiringChildReply
	switch req.Mode {
	case wiringChildModeLeakControl:
		_, _ = fmt.Fprint(wiringCachedStdout, wiringControlStdout)
		_, _ = fmt.Fprint(wiringCachedStderr, wiringControlStderr)
		log.Print(wiringControlLogged)
	case wiringChildModeRun:
		art, err := RunWiring(Invocation{Args: req.Args, Dir: req.Dir})
		reply.Art = art
		switch {
		case errors.Is(err, ErrWiringNotImplemented):
			reply.Stub = true
		case err != nil:
			reply.Err = err.Error()
		}
	default:
		die("unknown mode %q", req.Mode)
	}
	out, err := json.Marshal(reply)
	if err != nil {
		die("encode reply: %v", err)
	}
	if err := os.WriteFile(reqPath+".reply", out, 0o600); err != nil {
		die("write reply: %v", err)
	}
	os.Exit(0)
}

// runWiringChild re-executes this test binary in an EMPTY working directory
// with fds 1 and 2 on pipes, and returns the bytes from each pipe and the
// reply the child wrote. The working directory must still be empty afterwards.
func runWiringChild(t *testing.T, req wiringChildRequest) (stdout, stderr string, reply wiringChildReply) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	reqPath := filepath.Join(scratch, "request.json")
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reqPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(scratch, "cwd")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(self, "-test.run=^TestWiringSealChild$") // #nosec G204 -- this test binary
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), wiringChildEnv+"="+reqPath)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	runErr := cmd.Run()
	stdout, stderr = outBuf.String(), errBuf.String()
	if runErr != nil {
		t.Fatalf("the child process failed (%v) — a crash, not a leak\nstdout:\n%s\nstderr:\n%s", runErr, stdout, stderr)
	}
	replyData, err := os.ReadFile(reqPath + ".reply") // #nosec G304 -- this test's own temp file
	if err != nil {
		t.Fatalf("the child wrote no reply: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if err := json.Unmarshal(replyData, &reply); err != nil {
		t.Fatalf("the child's reply does not decode: %v\n%s", err, replyData)
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("%s: the child left %q in its working directory — a path resolved against the cwd, not Invocation.Dir (clause 7)", req.Mode, e.Name())
	}
	return stdout, stderr, reply
}

// driveChild is driveRunWiring with the process's real descriptors as the
// leak observer. There is no recorder to ask — the child has its own — so
// rows through it do not call judgeConsumed; the disk oracle is the same.
func driveChild(t *testing.T, name string, inv Invocation) wiringRun {
	t.Helper()
	runState := filepath.Join(inv.Dir, fixtureRunState)
	rsBefore, scBefore := snapFile(t, runState), snapFile(t, V2SidecarPath(runState))
	stdout, stderr, reply := runWiringChild(t, wiringChildRequest{Mode: wiringChildModeRun, Args: inv.Args, Dir: inv.Dir})
	r := wiringRun{art: reply.Art, leakedStdout: stdout, leakedStderr: stderr}
	switch {
	case reply.Stub:
		r.err = ErrWiringNotImplemented
	case reply.Err != "":
		r.err = errors.New(reply.Err)
	}
	if r.err != nil {
		return r
	}
	rsAfter, scAfter := snapFile(t, runState), snapFile(t, V2SidecarPath(runState))
	judgeDisk(t, name+": run-state", r.art.RunState, rsBefore, rsAfter)
	judgeDisk(t, name+": v2 sidecar", r.art.V2Sidecar, scBefore, scAfter)
	return r
}

// requireRunWiringBody is the one place the stub is named. It FAILS — never
// skips — so the row stays red until GO-1-3 lands, and it distinguishes the
// sentinel from every other error so that a body that is present and broken
// reads as a finding.
func requireRunWiringBody(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, ErrWiringNotImplemented) {
		t.Fatalf("SEAL RED — RunWiring is still the GO-1-1 stub: %v\nThis row judges nothing until the body lands. The same table is judged today through driveSpine.", err)
	}
	if err != nil {
		t.Fatalf("RunWiring itself failed (clause 5 — not the run's exit code): %v", err)
	}
}

// judgeNoLeak is clause 3 on all three process-global channels: nothing may
// reach the process's stdout, its stderr, or the standard logger, because a
// byte that did was not written through a writer RunWiring supplied and is
// therefore missing from Artifacts.
func judgeNoLeak(t *testing.T, name string, r wiringRun) {
	t.Helper()
	for _, l := range []struct{ where, got string }{
		{"the process's stdout", r.leakedStdout},
		{"the process's stderr", r.leakedStderr},
		{"the process-global logger", r.logged},
	} {
		if l.got != "" {
			t.Errorf("%s: %d bytes reached %s instead of a writer RunWiring supplied (clause 3):\n%s", name, len(l.got), l.where, l.got)
		}
	}
}

// judgeConsumed asks the run's recorder which input channels it read.
func judgeConsumed(t *testing.T, name string, r wiringRun, wantConfig, wantDiff bool) {
	t.Helper()
	gotConfig, gotDiff := r.consumed()
	if gotConfig != wantConfig || gotDiff != wantDiff {
		t.Errorf("%s: the run consumed config=%v diff=%v, want config=%v diff=%v", name, gotConfig, gotDiff, wantConfig, wantDiff)
	}
}

// ─── the judge ───────────────────────────────────────────────────────────────

// judgeCell holds one Artifacts against one cell. It is shared by both drivers
// so that the expectations, and the reading of the observation, are one code
// path.
func judgeCell(t *testing.T, cell mappingCell, f wiringFixture, runState string, art Artifacts) {
	t.Helper()

	if art.ExitCode != cell.exit {
		t.Errorf("%s: exit %d, want %d\nstdout:\n%s", cell.name, art.ExitCode, cell.exit, art.Stdout)
	}
	if art.RunState.State != cell.runState {
		t.Errorf("%s: run-state is %s, want %s", cell.name, art.RunState.State, cell.runState)
	}
	if art.V2Sidecar.State != cell.sidecar {
		t.Errorf("%s: v2 sidecar is %s, want %s", cell.name, art.V2Sidecar.State, cell.sidecar)
	}

	// Paths: empty exactly when NotApplicable, else the resolved -out.
	wantRS, wantSC := "", ""
	if runState != "" {
		wantRS, wantSC = runState, V2SidecarPath(runState)
	}
	if art.RunState.Path != wantRS {
		t.Errorf("%s: run-state path %q, want %q", cell.name, art.RunState.Path, wantRS)
	}
	if art.V2Sidecar.Path != wantSC {
		t.Errorf("%s: sidecar path %q, want %q", cell.name, art.V2Sidecar.Path, wantSC)
	}

	judgeStdout(t, cell, f, art.Stdout)

	// What stderr SAYS about the artifact set. A body that returns Stdout
	// correctly and leaves persist()'s log lines on the process-global logger
	// has an Artifacts.Stderr carrying none of them, and reads as red here —
	// and a body that simply deletes the lines reads the same way.
	stderr := string(art.Stderr)
	for _, s := range cell.stderrSays {
		if !strings.Contains(stderr, s) {
			t.Errorf("%s: stderr does not say %q — the run's report of its own artifacts did not reach Artifacts.Stderr:\n%s", cell.name, s, stderr)
		}
	}
	for _, s := range cell.stderrSilentOn {
		if strings.Contains(stderr, s) {
			t.Errorf("%s: stderr says %q about an artifact this cell does not touch:\n%s", cell.name, s, stderr)
		}
	}

	// What the artifacts SAY, not merely that they changed. The wallet diff
	// classifies critical with the human PR gate on; a run-state or sidecar
	// carrying anything else is a previous run's verdict or nobody's.
	if art.RunState.State == ArtifactWritten {
		var st RunState
		if err := json.Unmarshal(art.RunState.Bytes, &st); err != nil {
			t.Errorf("%s: run-state is not valid JSON: %v", cell.name, err)
		} else if st.Classification == nil || !st.Classification.HumanPRGate || st.Classification.Risk != "critical" {
			t.Errorf("%s: run-state carries human_pr_gate=%v risk=%q, want true/critical — not this run's verdict", cell.name, st.Classification != nil && st.Classification.HumanPRGate, riskOf(st.Classification))
		}
	}
	if art.V2Sidecar.State == ArtifactWritten {
		var sc V2Sidecar
		if err := json.Unmarshal(art.V2Sidecar.Bytes, &sc); err != nil {
			t.Errorf("%s: sidecar is not valid JSON: %v", cell.name, err)
			return
		}
		if sc.SchemaVersion != v2SidecarSchemaVersion {
			t.Errorf("%s: sidecar schema_version %d, want %d", cell.name, sc.SchemaVersion, v2SidecarSchemaVersion)
		}
		judgeWrapper(t, cell.name+" (sidecar)", f, sc.Response)
	}
}

func riskOf(c *Classification) string {
	if c == nil {
		return "<no classification>"
	}
	return c.Risk
}

// judgeStdout reads stdout as the shape the cell promises AND as not the other
// machine shape. The wrapper and the bare v1 payload are both JSON objects, so
// "parses" proves nothing; the discriminators are the wrapper's own four keys
// and the envelope's contract_version, which v1 does not carry.
func judgeStdout(t *testing.T, cell mappingCell, f wiringFixture, stdout []byte) {
	t.Helper()
	switch cell.stdout {
	case shapeReport:
		if !bytes.HasPrefix(stdout, []byte("=== CLASSIFICATION ===")) {
			t.Errorf("%s: stdout is not the human report:\n%s", cell.name, stdout)
		}
	case shapeV1:
		if !json.Valid(stdout) {
			t.Errorf("%s: stdout is not JSON:\n%s", cell.name, stdout)
			return
		}
		if hasKey(t, stdout, "response_version") || hasKey(t, stdout, "contract_version") {
			t.Errorf("%s: a v1 run emitted v2 material on stdout (keys %v)", cell.name, topKeys(t, stdout))
		}
		var cls Classification
		if err := json.Unmarshal(stdout, &cls); err != nil {
			t.Errorf("%s: stdout does not decode as the v1 Classification: %v", cell.name, err)
		} else if !cls.HumanPRGate || cls.Risk != "critical" {
			t.Errorf("%s: v1 payload says human_pr_gate=%v risk=%q, want true/critical", cell.name, cls.HumanPRGate, cls.Risk)
		}
	case shapeWrapper:
		if !json.Valid(stdout) {
			t.Errorf("%s: stdout is not JSON:\n%s", cell.name, stdout)
			return
		}
		if got, want := topKeys(t, stdout), []string{"classification", "computed_config_sha256", "computed_diff_sha256", "response_version"}; !sameStrings(got, want) {
			t.Errorf("%s: stdout keys %v, want the ResponseWrapper's %v — a v2 run answered with something other than the wrapper", cell.name, got, want)
			return
		}
		var w ResponseWrapper
		if err := json.Unmarshal(stdout, &w); err != nil {
			t.Errorf("%s: stdout does not decode as the ResponseWrapper: %v", cell.name, err)
			return
		}
		judgeWrapper(t, cell.name, f, w)
	}
}

// judgeWrapper holds a wrapper to the bytes THIS run consumed and to the
// verdict this run reached. The digests are computed here from the fixture
// bytes, not read back from the source, so a wrapper carrying the right SHAPE
// over the wrong bytes is red.
func judgeWrapper(t *testing.T, name string, f wiringFixture, w ResponseWrapper) {
	t.Helper()
	if w.ResponseVersion != responseVersion {
		t.Errorf("%s: response_version %d, want %d", name, w.ResponseVersion, responseVersion)
	}
	if want := hexSHA256(f.configBytes); w.ComputedConfigSHA256 != want {
		t.Errorf("%s: computed_config_sha256 %q, want sha256 of the consumed table %q", name, w.ComputedConfigSHA256, want)
	}
	if want := hexSHA256(f.diffBytes); w.ComputedDiffSHA256 != want {
		t.Errorf("%s: computed_diff_sha256 %q, want sha256 of the consumed diff %q", name, w.ComputedDiffSHA256, want)
	}
	var env map[string]any
	if err := json.Unmarshal(w.Classification, &env); err != nil {
		t.Errorf("%s: wrapper.classification is not a JSON object: %v", name, err)
		return
	}
	if env["contract_version"] != float64(ContractV2) {
		t.Errorf("%s: wrapper carries contract_version=%v, want %d — the wrapper is not wrapping a v2 envelope", name, env["contract_version"], ContractV2)
	}
	if env["human_pr_gate"] != true || env["risk"] != "critical" {
		t.Errorf("%s: envelope says human_pr_gate=%v risk=%v, want true/critical", name, env["human_pr_gate"], env["risk"])
	}
}

// ─── the rows ────────────────────────────────────────────────────────────────

// THE MAPPING, through the spine main() runs today. GREEN at b0313fa; RED under
// the measured mutation in exactly the three v2 -json cells.
//
// Every cell also consumes both input channels — the positive control for the
// recorder the rejected-contract row asks the opposite question of.
func TestSeal_Wiring_Mapping_ContractByOutByJSON(t *testing.T) {
	defer red(t)
	for _, cell := range mappingCells() {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			defer red(t)
			f := newWiringFixture(t)
			runState := prepareOut(t, f.dir, cell.out)
			r := driveSpine(t, cell.name, spineOptions(f, cell, runState))
			judgeCell(t, cell, f, runState, r.art)
			judgeConsumed(t, cell.name, r, true, true)
		})
	}
}

// THE SAME MAPPING, through RunWiring, plus what only RunWiring can answer:
// relative paths resolve against Dir (clause 7), nothing reaches the process's
// own stdout, stderr or logger (clause 3), every state is set (clause 5's
// boundary), the exit code is a member of the declared set, and — inside
// driveRunWiring — the reported artifacts match the tree (clause 4).
//
// RED TODAY by the stub. requireRunWiringBody is the only tolerance, and it
// fails rather than skips.
func TestSeal_Wiring_RunWiring_AnswersTheMapping(t *testing.T) {
	defer red(t)
	for _, cell := range mappingCells() {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			defer red(t)
			f := newWiringFixture(t)
			runState := prepareOut(t, f.dir, cell.out)
			r := driveRunWiring(t, cell.name, invocationFor(f, cell))
			requireRunWiringBody(t, r.err)
			judgeNoLeak(t, cell.name, r)
			if !r.art.RunState.State.Valid() || !r.art.V2Sidecar.State.Valid() {
				t.Errorf("%s: an artifact state is unset beside a nil error (run-state %s, sidecar %s)", cell.name, r.art.RunState.State, r.art.V2Sidecar.State)
			}
			if !containsExit(DeclaredExitCodes, int(r.art.ExitCode)) {
				t.Errorf("%s: exit %d is outside DeclaredExitCodes %v", cell.name, r.art.ExitCode, DeclaredExitCodes)
			}
			judgeCell(t, cell, f, runState, r.art)
			judgeConsumed(t, cell.name, r, true, true)
		})
	}
}

func containsExit(set []int, code int) bool {
	for _, c := range set {
		if c == code {
			return true
		}
	}
	return false
}

// D3 — a rejected -contract-version exits 3, names the accepted set, does so
// BEFORE the config search and before any input is read (D3.5), and disturbs
// neither artifact (D3.6).
//
// "Before any input is read" is OBSERVED, not inferred from silence. Every
// read on the input path records into a recorder that is fresh per run, and
// the row asks it afterwards whether the config or diff channel was consumed;
// the config search certifies a resolved table into a slot that is fresh too,
// and the row asks it whether a search resolved anything. A body that reads
// the inputs, discards them and then reports the contract error prints no
// problem line and would pass a stdout check; it reads as red here.
//
// Two legs, each through both drivers:
//
//   - BARE — D3.5 as written: no rule table, no -config, no diff. The CONTROL
//     is contract "1" against the same inputs, which fails LATER, at the
//     config search, and its ✗ problem lines must be absent from every
//     rejected run's stdout.
//   - BAITED — a table at .agent/risk-paths.json, a wallet diff, no -config:
//     a run that got past the contract check would search, resolve, read both
//     channels and WRITE -out. The CONTROL is contract "1", which does exactly
//     that and exits 0 — the proof that the recorder sees reads on this path.
//     The search instrument is controlled by resolving the same table
//     directly and watching a fresh slot fill.
func TestSeal_Wiring_RejectedContractExits3BeforeAnyInputIsRead(t *testing.T) {
	defer red(t)

	rejected := []string{"0", "3", "v1", "02", " 2", ""}

	// A driver runs one invocation of the classify path in dir, -json, -out
	// fixtureRunState, optionally naming the fixture diff, with no -config.
	type driver struct {
		name string
		run  func(t *testing.T, name, dir, raw string, withDiff bool) wiringRun
	}
	drivers := []driver{
		{"spine", func(t *testing.T, name, dir, raw string, withDiff bool) wiringRun {
			opts := options{worktree: dir, base: "origin/main", out: filepath.Join(dir, fixtureRunState), json: true, noGit: true, contractVersion: raw}
			if withDiff {
				opts.args = []string{filepath.Join(dir, fixtureDiffName)}
			}
			return driveSpine(t, name, opts)
		}},
		{"RunWiring", func(t *testing.T, name, dir, raw string, withDiff bool) wiringRun {
			args := []string{"-no-git", "-json", "-out", fixtureRunState, "-contract-version", raw}
			if withDiff {
				args = append(args, fixtureDiffName)
			}
			r := driveRunWiring(t, name, Invocation{Args: args, Dir: dir})
			requireRunWiringBody(t, r.err)
			judgeNoLeak(t, name, r)
			return r
		}},
	}

	// judgeRejected is what every rejected value owes, whichever leg and
	// driver produced it.
	judgeRejected := func(t *testing.T, name, raw string, r wiringRun, controlProblems []string) {
		t.Helper()
		if r.art.ExitCode != exitInvalid {
			t.Errorf("%s: exit %d, want %d", name, r.art.ExitCode, exitInvalid)
		}
		out := string(r.art.Stdout)
		if !strings.HasPrefix(out, "=== CLASSIFY: INVALID_INPUT ===") {
			t.Errorf("%s: stdout is not the INVALID_INPUT block:\n%s", name, out)
		}
		// The message is the contract's own, not a paraphrase: ParseContractVersion
		// is asked what it says about this value and stdout must carry it.
		_, want := ParseContractVersion(raw)
		if want == nil {
			t.Fatalf("%s: ParseContractVersion accepted a value this row lists as rejected — re-derive the set", name)
		}
		if !strings.Contains(out, want.Error()) {
			t.Errorf("%s: stdout does not carry ParseContractVersion's message %q:\n%s", name, want.Error(), out)
		}
		if !strings.Contains(out, "accepted values are 1 and 2") {
			t.Errorf("%s: the message does not name the accepted set:\n%s", name, out)
		}
		for _, p := range controlProblems {
			if strings.Contains(out, p) {
				t.Errorf("%s: the config search ran — its complaint %q is on stdout, so the contract was NOT validated first (D3.5)", name, p)
			}
		}
		if r.art.RunState.State != ArtifactStale || r.art.V2Sidecar.State != ArtifactStale {
			t.Errorf("%s: a rejected contract touched -out (run-state %s, sidecar %s), want stale/stale (D3.6)", name, r.art.RunState.State, r.art.V2Sidecar.State)
		}
		// D3.5, observed: no channel consumed, no table resolved.
		judgeConsumed(t, name, r, false, false)
		if r.searchCertified() {
			t.Errorf("%s: the config search ran and resolved a table before the contract was validated (D3.5)", name)
		}
	}

	for _, d := range drivers {
		d := d
		t.Run(d.name+"/bare", func(t *testing.T) {
			defer red(t)
			dir := t.TempDir()
			prepareOut(t, dir, outSeeded)

			// CONTROL — an accepted contract against the same inputs fails LATER,
			// at the config search, and its complaint is about the table.
			ctl := d.run(t, "CONTROL", dir, defaultContractVersion.String(), false)
			if ctl.art.ExitCode != exitInvalid {
				t.Fatalf("CONTROL: contract %q with no rule table exited %d, want %d\n%s", defaultContractVersion, ctl.art.ExitCode, exitInvalid, ctl.art.Stdout)
			}
			controlProblems := problemLines(string(ctl.art.Stdout))
			if len(controlProblems) == 0 {
				t.Fatalf("CONTROL: the config-search failure reported no problem lines, so there is nothing to tell the two exit-3 paths apart:\n%s", ctl.art.Stdout)
			}
			if strings.Contains(string(ctl.art.Stdout), "accepted values are") {
				t.Fatalf("CONTROL: an accepted contract was reported as rejected:\n%s", ctl.art.Stdout)
			}
			if ctl.art.RunState.State != ArtifactStale || ctl.art.V2Sidecar.State != ArtifactStale {
				t.Errorf("CONTROL: a run that failed the config search touched -out (run-state %s, sidecar %s), want stale/stale", ctl.art.RunState.State, ctl.art.V2Sidecar.State)
			}

			for _, raw := range rejected {
				name := fmt.Sprintf("contract=%q", raw)
				judgeRejected(t, name, raw, d.run(t, name, dir, raw, false), controlProblems)
			}
		})

		t.Run(d.name+"/baited", func(t *testing.T) {
			defer red(t)
			dir := t.TempDir()
			prepareOut(t, dir, outSeeded)
			f := baitedWorktree(t, dir)

			// INSTRUMENT CONTROL — the search, run directly against this
			// worktree, resolves the bait and fills a fresh slot. Without this,
			// "the slot stayed empty" could mean the instrument sees nothing.
			_, slot := isolateProcessState(t)
			if got, err := ResolveConfigDual(dir); err != nil || got != f.configPath {
				t.Fatalf("INSTRUMENT: the search over the bait resolved %q, %v — want %q", got, err, f.configPath)
			}
			if !slot.holds() {
				t.Fatal("INSTRUMENT: the search resolved a table and certified nothing — the slot cannot witness a search that ran")
			}

			// CONTROL — an accepted contract against the bait runs to the end:
			// exit 0, the v1 payload on stdout, both channels consumed, -out
			// written. This is the run every rejected value must NOT have been.
			ctl := d.run(t, "CONTROL", dir, "1", true)
			if ctl.art.ExitCode != exitOK {
				t.Fatalf("CONTROL: contract \"1\" against the bait exited %d, want %d\n%s\n%s", ctl.art.ExitCode, exitOK, ctl.art.Stdout, ctl.art.Stderr)
			}
			judgeStdout(t, mappingCell{name: "CONTROL", stdout: shapeV1}, f, ctl.art.Stdout)
			judgeConsumed(t, "CONTROL", ctl, true, true)
			if ctl.art.RunState.State != ArtifactWritten {
				t.Errorf("CONTROL: a run that classified left the run-state %s, want written", ctl.art.RunState.State)
			}

			for _, raw := range rejected {
				name := fmt.Sprintf("contract=%q", raw)
				// Re-seed: the control rewrote -out and removed the sidecar.
				prepareOut(t, dir, outSeeded)
				judgeRejected(t, name, raw, d.run(t, name, dir, raw, true), nil)
			}
		})
	}
}

// holds reports whether the slot currently carries a certified read.
func (c *certifiedConfigRead) holds() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held
}

// baitedWorktree puts a searchable rule table and a wallet diff in dir, so
// that a run which gets past the contract check has everything it needs to
// resolve, read, classify and write. It returns the fixture view of them.
func baitedWorktree(t *testing.T, dir string) wiringFixture {
	t.Helper()
	cfg, err := os.ReadFile(exampleConfigPath)
	if err != nil {
		t.Fatalf("read %s: %v", exampleConfigPath, err)
	}
	f := wiringFixture{
		dir:         dir,
		configPath:  filepath.Join(dir, agentConfigDirs[0], "risk-paths.json"),
		diffPath:    filepath.Join(dir, fixtureDiffName),
		configBytes: cfg,
		diffBytes:   []byte(diffFor(walletPath)),
	}
	if err := os.MkdirAll(filepath.Dir(f.configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.configPath, f.configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.diffPath, f.diffBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// problemLines returns the "  ✗ ..." lines of an INVALID_INPUT block.
func problemLines(stdout string) []string {
	var out []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "  ✗ ") {
			out = append(out, line)
		}
	}
	return out
}

// parseInvocationFlags — D2.1 and its own four clauses. RED TODAY by the stub.
//
//  1. it registers exactly the classify flags, -contract-version among them;
//  2. it does not parse -contract-version: "3" comes back raw, with no error;
//  3. a flag error is returned, and its text went to the FlagSet's writer;
//  4. (D2, third state) with no registrar installed it registers no
//     -contract-version flag and yields the compiled-in default, and passing
//     the flag then fails inside the flag package, not at exit 3.
//
// The logger is not asserted on (clause 4): a test cannot observe that a
// function did NOT call log.SetFlags without swapping process state.
func TestSeal_Wiring_ParseInvocationFlags_RegistersAndThreadsWithoutParsing(t *testing.T) {
	defer red(t)

	newFS := func() (*flag.FlagSet, *bytes.Buffer) {
		fs := flag.NewFlagSet("wiring-seal", flag.ContinueOnError)
		var out bytes.Buffer
		fs.SetOutput(&out)
		return fs, &out
	}

	fs, _ := newFS()
	opts, err := parseInvocationFlags(fs, []string{
		"-json", "-no-git", "-contract-version", "3", "-out", "state.json",
		"-config", "table.json", "-worktree", "wt", "-base", "origin/dev", "-task", "SMG-1", "change.diff",
	})
	if errors.Is(err, ErrWiringNotImplemented) {
		t.Fatalf("SEAL RED — parseInvocationFlags is still the GO-1-1 stub: %v", err)
	}
	if err != nil {
		t.Fatalf("a well-formed argv was rejected: %v", err)
	}
	want := options{configPath: "table.json", worktree: "wt", base: "origin/dev", task: "SMG-1", out: "state.json", json: true, noGit: true, contractVersion: "3", args: []string{"change.diff"}}
	if opts.configPath != want.configPath || opts.worktree != want.worktree || opts.base != want.base || opts.task != want.task ||
		opts.out != want.out || opts.json != want.json || opts.noGit != want.noGit || opts.contractVersion != want.contractVersion ||
		!sameStrings(opts.args, want.args) {
		t.Errorf("threading: got %+v\n                want %+v", opts, want)
	}
	if opts.contractVersion != "3" {
		t.Errorf("clause 2: -contract-version was parsed here (got %q) — validation is run()'s, so that a mistyped contract and a mistyped flag can owe different exit codes", opts.contractVersion)
	}
	if got, want := flagNames(fs), []string{"base", "config", "contract-version", "json", "no-git", "out", "task", "worktree"}; !sameStrings(sortedCopy(got), want) {
		t.Errorf("clause 1: registered flags %v, want exactly %v", sortedCopy(got), want)
	}

	// Clause 3 — a flag error comes back, and its text went through the
	// caller's writer. The control is the successful parse above, which wrote
	// nothing.
	fs2, out2 := newFS()
	if _, err := parseInvocationFlags(fs2, []string{"-no-such-flag"}); err == nil {
		t.Error("clause 3: an unknown flag parsed without error")
	} else if out2.Len() == 0 {
		t.Errorf("clause 3: the flag error %q reached no writer the caller supplied — it went to a process-global stream", err)
	}

	// D2 third state — no registrar installed.
	withHooks(t, nil, digestSource, framedStdinReader)
	fs3, _ := newFS()
	opts3, err := parseInvocationFlags(fs3, nil)
	if err != nil {
		t.Fatalf("no registrar: an empty argv was rejected: %v", err)
	}
	if opts3.contractVersion != defaultContractVersion.String() {
		t.Errorf("no registrar: contractVersion %q, want the compiled-in default %q", opts3.contractVersion, defaultContractVersion.String())
	}
	if fs3.Lookup(flagContractVersion) != nil {
		t.Errorf("no registrar: -%s was registered anyway — the flag's existence and the probe's answer must be the same fact", flagContractVersion)
	}
	fs4, _ := newFS()
	if _, err := parseInvocationFlags(fs4, []string{"-" + flagContractVersion, "2"}); err == nil {
		t.Errorf("no registrar: -%s 2 parsed on a binary that has no such flag", flagContractVersion)
	}
}

// RunWiring clause 6 and H3 — the pre-flag-parse branch and the flag-error
// mapping. RED TODAY by the stub.
//
//   - "capabilities" answers with exactly one JSON object on Stdout, and it is
//     THIS object: the report B1's registry owes, byte for byte, with the exit
//     code its biconditional derives from Missing;
//   - "help", "-h", "--help" exit 0 with the usage on Stderr — naming every
//     subcommand it dispatches — and nothing on Stdout;
//   - an unknown flag exits exitFlagError, with the flag package's message
//     naming the flag on Stderr — the mapping parseInvocationFlags clause 3
//     assigns;
//   - "init" against a fresh worktree scaffolds .agent/risk-paths.json UNDER
//     Dir (clause 7: "-worktree ." resolves against Dir), exits 0, and the
//     file is the generator's output for an empty scan; a second init refuses
//     (exitInvalid) and leaves that file byte-identical.
//
// Artifacts has no field for init's config file, and its doc says a row about
// init confines its assertions ON ARTIFACTS to ExitCode and the streams. This
// row does: the scaffold is observed by the row's own snapshot of the tree,
// which is the same oracle both drivers apply to -out, and claims nothing
// about what Artifacts reports.
//
// Every leg also asserts nothing reached the process's stdout, stderr or
// logger (clause 3), that the working directory is unchanged (clause 7), and
// that both artifacts are NotApplicable with no file at the conventional path
// — no subcommand produces them.
func TestSeal_Wiring_RunWiring_SubcommandsAndFlagErrors(t *testing.T) {
	defer red(t)

	cwdBefore, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	drive := func(name string, args ...string) wiringRun {
		t.Helper()
		r := driveRunWiring(t, name, Invocation{Args: args, Dir: dir})
		requireRunWiringBody(t, r.err)
		judgeNoLeak(t, name, r)
		if r.art.RunState.State != ArtifactNotApplicable || r.art.V2Sidecar.State != ArtifactNotApplicable {
			t.Errorf("%s: a subcommand reported artifacts (run-state %s, sidecar %s), want not-applicable/not-applicable", name, r.art.RunState.State, r.art.V2Sidecar.State)
		}
		return r
	}
	expectExit := func(name string, r wiringRun, want ExitCode) {
		t.Helper()
		if r.art.ExitCode != want {
			t.Errorf("%s: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", name, r.art.ExitCode, want, r.art.Stdout, r.art.Stderr)
		}
	}
	stdoutSays := func(name string, r wiringRun, musts ...string) {
		t.Helper()
		for _, m := range musts {
			if !strings.Contains(string(r.art.Stdout), m) {
				t.Errorf("%s: stdout does not say %q:\n%s", name, m, r.art.Stdout)
			}
		}
	}
	stderrSays := func(name string, r wiringRun, musts ...string) {
		t.Helper()
		for _, m := range musts {
			if !strings.Contains(string(r.art.Stderr), m) {
				t.Errorf("%s: stderr does not say %q:\n%s", name, m, r.art.Stderr)
			}
		}
	}
	silent := func(name, stream string, b []byte) {
		t.Helper()
		if len(b) != 0 {
			t.Errorf("%s: %s is not empty:\n%s", name, stream, b)
		}
	}

	// capabilities. The registry under isolateProcessState is B1's world — a
	// flag registrar and a digest source installed, no framed stdin reader —
	// so the probe owes exactly this report and exitCapabilityIncomplete. B2
	// flips framed_authoritative_stdin, empties missing and moves the exit to
	// 0; this row is edited then, not loosened now.
	wantReport := CapabilityReport{
		ProbeVersion:     1,
		Producer:         "cmd/classify",
		Capabilities:     Capabilities{FramedAuthoritativeStdin: false, DualDigestEcho: true, ContractVersionFlag: true},
		ContractVersions: []int{1, 2},
		Missing:          []string{"framed_authoritative_stdin"},
	}
	var wantReportBytes bytes.Buffer
	if err := writeCapabilityReport(&wantReportBytes, wantReport); err != nil {
		t.Fatal(err)
	}
	r := drive("capabilities", probeSubcommand)
	expectExit("capabilities", r, exitCapabilityIncomplete)
	silent("capabilities", "stderr", r.art.Stderr)
	if !bytes.Equal(r.art.Stdout, wantReportBytes.Bytes()) {
		t.Errorf("capabilities: stdout is not the report B1's registry owes, byte for byte\ngot:\n%s\nwant:\n%s", r.art.Stdout, wantReportBytes.Bytes())
	}
	// The same fact in the struct's vocabulary, so a value change and a
	// formatting change fail on different lines. Unknown fields are rejected:
	// a stream that decodes because nothing in it was looked at is not one
	// JSON object of this shape.
	dec := json.NewDecoder(bytes.NewReader(r.art.Stdout))
	dec.DisallowUnknownFields()
	var rep CapabilityReport
	if err := dec.Decode(&rep); err != nil {
		t.Errorf("capabilities: stdout does not decode as one CapabilityReport: %v", err)
	} else if !reflect.DeepEqual(rep, wantReport) {
		t.Errorf("capabilities: report %+v, want %+v", rep, wantReport)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Errorf("capabilities: stdout carries more than one JSON value (second decode: %v)", err)
	}

	// help, -h, --help.
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		name := strings.Join(args, " ")
		r := drive(name, args...)
		expectExit(name, r, exitOK)
		silent(name, "stdout", r.art.Stdout)
		stderrSays(name, r, "classify init", "classify "+probeSubcommand)
	}

	// An unknown flag: the flag package's own message, which names the flag.
	r = drive("unknown flag", "-no-such-flag")
	expectExit("unknown flag", r, exitFlagError)
	silent("unknown flag", "stdout", r.art.Stdout)
	stderrSays("unknown flag", r, "no-such-flag")

	// init, fresh. The expected file is computed BEFORE the run, over the
	// empty worktree: nothing to detect, so the rules are the fixed ones.
	scaffold := filepath.Join(dir, agentConfigDirs[0], "risk-paths.json")
	if snapFile(t, scaffold).present {
		t.Fatalf("fixture: %s exists before init ran", scaffold)
	}
	wantScaffold, err := json.MarshalIndent(scaffoldConfig(scanRepo(dir)), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	wantScaffold = append(wantScaffold, '\n')
	r = drive("init fresh", "init", "-worktree", ".")
	expectExit("init fresh", r, exitOK)
	stdoutSays("init fresh", r, "=== CLASSIFY INIT ===", "Wrote ")
	written := snapFile(t, scaffold)
	if !written.present {
		t.Errorf("init fresh: %s was not written — \"-worktree .\" did not resolve against Dir (clause 7), or nothing was scaffolded", scaffold)
	} else {
		var cfg Config
		if err := json.Unmarshal(written.bytes, &cfg); err != nil {
			t.Errorf("init fresh: the scaffold is not a Config: %v", err)
		} else {
			if !cfg.Scaffold {
				t.Error("init fresh: scaffold is not marked \"scaffold\": true — a table nobody has reviewed would classify as if it had been")
			}
			if cfg.SchemaVersion != schemaVersion {
				t.Errorf("init fresh: schema_version %d, want %d", cfg.SchemaVersion, schemaVersion)
			}
			rules := map[string]Rule{}
			for _, rule := range cfg.Rules {
				rules[rule.ID] = rule
			}
			if money, ok := rules["TODO-money-paths"]; !ok || !money.Financial || money.Risk != "critical" {
				t.Errorf("init fresh: TODO-money-paths present=%v financial=%v risk=%q, want present/true/critical", ok, money.Financial, money.Risk)
			}
			if _, ok := rules["TODO-auth-paths"]; !ok {
				t.Error("init fresh: TODO-auth-paths is missing from the scaffold")
			}
		}
		if !bytes.Equal(written.bytes, wantScaffold) {
			t.Errorf("init fresh: the scaffold is not the generator's output for an empty worktree\ngot:\n%s\nwant:\n%s", written.bytes, wantScaffold)
		}
	}

	// init, again: refuses, and the table is byte-identical afterwards.
	before := snapFile(t, scaffold)
	r = drive("init refuses", "init", "-worktree", ".")
	expectExit("init refuses", r, exitInvalid)
	stdoutSays("init refuses", r, "REFUSING TO OVERWRITE")
	if got := stateOf(before, snapFile(t, scaffold)); got != ArtifactStale {
		t.Errorf("init refuses: the existing table is %s afterwards, want stale — a refusing init may not touch it", got)
	}

	if cwdAfter, err := os.Getwd(); err != nil || cwdAfter != cwdBefore {
		t.Errorf("clause 7: the working directory moved from %q to %q (err %v)", cwdBefore, cwdAfter, err)
	}
}

// RunWiring clause 3 ON THE DESCRIPTORS THEMSELVES. RED TODAY by the stub.
//
// TestSeal_Wiring_RunWiring_AnswersTheMapping observes the process's streams
// by swapping the os.Stdout/os.Stderr variables and the logger's writer. A
// writer bound BEFORE the swap — a package-level `var out = os.Stdout`, a
// logger built over os.Stderr at init — writes to the original descriptor and
// passes every one of those checks. This row runs the same invocations in a
// child process whose fds 1 and 2 are this test's pipes, where there is no
// "before the swap": the pipe IS the descriptor. Every byte on either pipe is
// a leak, whatever wrote it.
//
// The INSTRUMENT CONTROL is judged first, in the same call: a child that
// writes through writers cached at this file's package init, and through the
// standard logger, must deliver every byte to the parent. Without it "nothing
// arrived" could mean the pipes see nothing.
func TestSeal_Wiring_RunWiring_NoLeakOnTheRealDescriptors(t *testing.T) {
	defer red(t)

	stdout, stderr, _ := runWiringChild(t, wiringChildRequest{Mode: wiringChildModeLeakControl})
	if stdout != wiringControlStdout {
		t.Fatalf("INSTRUMENT: the child's fd 1 carried %q, want the cached-writer control %q — the pipe cannot witness a cached writer", stdout, wiringControlStdout)
	}
	if !strings.HasPrefix(stderr, wiringControlStderr) || !strings.Contains(stderr, wiringControlLogged) {
		t.Fatalf("INSTRUMENT: the child's fd 2 carried %q, want the cached-writer control followed by the logger's line", stderr)
	}

	// Three cells of the mapping, chosen so that each artifact outcome and
	// each stdout shape is exercised on the real descriptors at least once.
	for _, want := range []string{"contract=2/out=fresh/json=on", "contract=1/out=seeded/json=off", "contract=2/out=none/json=on"} {
		cell, ok := cellNamed(want)
		if !ok {
			t.Fatalf("mappingCells has no cell %q", want)
		}
		t.Run(cell.name, func(t *testing.T) {
			defer red(t)
			f := newWiringFixture(t)
			runState := prepareOut(t, f.dir, cell.out)
			r := driveChild(t, cell.name, invocationFor(f, cell))
			requireRunWiringBody(t, r.err)
			judgeNoLeak(t, cell.name, r)
			judgeCell(t, cell, f, runState, r.art)
		})
	}

	t.Run("rejected contract", func(t *testing.T) {
		defer red(t)
		f := newWiringFixture(t)
		prepareOut(t, f.dir, outSeeded)
		name := "contract=3"
		r := driveChild(t, name, Invocation{Args: []string{"-no-git", "-json", "-out", fixtureRunState, "-contract-version", "3", fixtureDiffName}, Dir: f.dir})
		requireRunWiringBody(t, r.err)
		judgeNoLeak(t, name, r)
		if r.art.ExitCode != exitInvalid {
			t.Errorf("%s: exit %d, want %d", name, r.art.ExitCode, exitInvalid)
		}
		if !strings.HasPrefix(string(r.art.Stdout), "=== CLASSIFY: INVALID_INPUT ===") {
			t.Errorf("%s: stdout is not the INVALID_INPUT block:\n%s", name, r.art.Stdout)
		}
	})

	t.Run("subcommands", func(t *testing.T) {
		defer red(t)
		dir := t.TempDir()
		for _, leg := range []struct {
			name string
			args []string
			exit ExitCode
		}{
			{"capabilities", []string{probeSubcommand}, exitCapabilityIncomplete},
			{"help", []string{"help"}, exitOK},
			{"unknown flag", []string{"-no-such-flag"}, exitFlagError},
		} {
			r := driveChild(t, leg.name, Invocation{Args: leg.args, Dir: dir})
			requireRunWiringBody(t, r.err)
			judgeNoLeak(t, leg.name, r)
			if r.art.ExitCode != leg.exit {
				t.Errorf("%s: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", leg.name, r.art.ExitCode, leg.exit, r.art.Stdout, r.art.Stderr)
			}
		}
	})
}

// cellNamed finds one cell of the table by its name.
func cellNamed(name string) (mappingCell, bool) {
	for _, c := range mappingCells() {
		if c.name == name {
			return c, true
		}
	}
	return mappingCell{}, false
}

// ─── RunWiring clause 8: the shipped binary ──────────────────────────────────

// Clause 8 — "main() FORWARDS THE RESULT AND ADDS NOTHING" — is the one
// clause no in-process row can reach, and the structural row exempts main()
// from every stream check on its authority. This row is what makes that
// exemption honest. It builds the current tree (liveClassify, with its
// freshness guards) and, for each invocation below, runs RunWiring in process
// AND the binary with the same argv, and requires the process's exit status,
// stdout and stderr to be the ExitCode, Stdout and Stderr RunWiring returned
// — byte for byte. A main() that forwards neither stream, exits 0 whatever
// RunWiring answered, or adds a line of its own is red here and nowhere else:
// `_, _ = RunWiring(inv); os.Exit(0)` satisfies every structural check and
// ships a silent classifier, which a consumer reads as "nothing to gate".
//
// RED TODAY by the stub, on the in-process side, like every other RunWiring
// row.
//
// Every path in the argv is ABSOLUTE, so the bytes cannot depend on which Dir
// main() passes (clause 7: absolute paths are used unchanged), and -worktree
// is named for the same reason — the report and the INVALID_INPUT block both
// print it. Legs that write -out are compared with timestamps masked (the
// payload carries classified_at, and the two runs are moments apart); every
// other leg is compared raw.
//
// Each leg also pins the exit code the mapping rows already owe, and the live
// stream must SAY something the leg names outright — an instrument that agreed
// on two empty streams would have agreed on nothing.
//
// The non-nil-error arm is provoked with an -out that names a DIRECTORY, which
// can neither be snapshotted (clause 4) nor written. Whether the body reports
// that as a returned error or as exitInternal beside a nil error is its call;
// either way the binary exits exitInternal, and on the error arm its stderr
// carries the error's text and its stdout nothing (clause 8).
func TestSeal_Wiring_MainForwardsRunWiring_LiveBinary(t *testing.T) {
	defer red(t)
	bin := liveClassify(t)
	f := newWiringFixture(t)
	runState := filepath.Join(f.dir, fixtureRunState)
	outDir := filepath.Join(f.dir, "out-is-a-directory")
	if err := os.Mkdir(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	classifyArgs := func(extra ...string) []string {
		args := []string{"-no-git", "-worktree", f.dir, "-config", f.configPath}
		args = append(args, extra...)
		return append(args, f.diffPath)
	}
	type says struct{ stream, substr string }
	legs := []struct {
		name     string
		args     []string
		exit     ExitCode
		mask     bool // -out is written: mask timestamps before comparing
		errArm   bool // RunWiring may answer with a non-nil error here
		liveSays says
	}{
		{"classify report, no -out", classifyArgs("-contract-version", "1"), exitOK, false, false, says{"stdout", "=== CLASSIFICATION ==="}},
		{"classify -json -out, contract 2", classifyArgs("-contract-version", "2", "-json", "-out", runState), exitOK, true, false, says{"stderr", saysSidecarWritten}},
		{"rejected -contract-version 3", classifyArgs("-contract-version", "3", "-json", "-out", runState), exitInvalid, false, false, says{"stdout", "accepted values are 1 and 2"}},
		{"capabilities", []string{probeSubcommand}, exitCapabilityIncomplete, false, false, says{"stdout", `"cmd/classify"`}},
		{"help", []string{"help"}, exitOK, false, false, says{"stderr", "classify init"}},
		{"unknown flag", []string{"-no-such-flag"}, exitFlagError, false, false, says{"stderr", "no-such-flag"}},
		{"-out names a directory", classifyArgs("-contract-version", "1", "-out", outDir), exitInternal, true, true, says{"stderr", ""}},
	}
	resetOut := func() {
		t.Helper()
		for _, p := range []string{runState, V2SidecarPath(runState)} {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
	for _, leg := range legs {
		leg := leg
		t.Run(leg.name, func(t *testing.T) {
			defer red(t)
			isolateProcessState(t)
			resetOut()
			var (
				art Artifacts
				err error
			)
			processStreamsOf(t, func() { art, err = RunWiring(Invocation{Args: leg.args, Dir: f.dir}) })
			if errors.Is(err, ErrWiringNotImplemented) {
				requireRunWiringBody(t, err)
			}
			resetOut()
			live := runLive(t, bin, f.dir, nil, leg.args...)

			if err != nil {
				if !leg.errArm {
					t.Fatalf("RunWiring itself failed (clause 5 — not the run's exit code): %v", err)
				}
				if live.exit != exitInternal {
					t.Errorf("%s: RunWiring returned an error and the binary exited %d, want %d (clause 8)", leg.name, live.exit, exitInternal)
				}
				if !strings.Contains(live.stderr, err.Error()) {
					t.Errorf("%s: RunWiring returned %q and the binary's stderr does not report it (clause 8):\n%s", leg.name, err, live.stderr)
				}
				if live.stdout != "" {
					t.Errorf("%s: RunWiring returned an error — Artifacts asserts nothing — and the binary still wrote to stdout:\n%s", leg.name, live.stdout)
				}
				return
			}

			if art.ExitCode != leg.exit {
				t.Errorf("%s: RunWiring answered exit %d, want %d", leg.name, art.ExitCode, leg.exit)
			}
			if live.exit != int(art.ExitCode) {
				t.Errorf("%s: the binary exited %d, RunWiring answered %d — main() does not exit with ExitCode (clause 8)\nbinary stderr:\n%s", leg.name, live.exit, art.ExitCode, live.stderr)
			}
			judgeForwarded(t, leg.name, "stdout", live.stdout, art.Stdout, leg.mask)
			judgeForwarded(t, leg.name, "stderr", live.stderr, art.Stderr, leg.mask)
			if leg.liveSays.substr != "" {
				got := map[string]string{"stdout": live.stdout, "stderr": live.stderr}[leg.liveSays.stream]
				if !strings.Contains(got, leg.liveSays.substr) {
					t.Errorf("%s: the binary's %s does not say %q:\n%s", leg.name, leg.liveSays.stream, leg.liveSays.substr, got)
				}
			}
		})
	}
}

// wiringTimestamps matches the two stamps a run can carry: RFC3339 in the
// payloads (classified_at, created_at, updated_at) and the log package's
// default prefix, should a body build a logger with the default flags.
var wiringTimestamps = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z|\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}`)

// judgeForwarded requires the binary's stream to be the Artifacts field byte
// for byte. With mask, timestamps are replaced on both sides first.
func judgeForwarded(t *testing.T, name, stream, live string, want []byte, mask bool) {
	t.Helper()
	got, exp := live, string(want)
	if mask {
		got = wiringTimestamps.ReplaceAllString(got, "<timestamp>")
		exp = wiringTimestamps.ReplaceAllString(exp, "<timestamp>")
	}
	if got != exp {
		t.Errorf("%s: the binary's %s is not Artifacts.%s byte for byte (clause 8)\nbinary (%d bytes):\n%s\nRunWiring (%d bytes):\n%s",
			name, stream, strings.ToUpper(stream[:1])+stream[1:], len(live), live, len(want), want)
	}
}

// D1's structural obligation, RunWiring clauses 1, 2 and 3. RED TODAY on five
// counts, each of which is one line of GO-1-3's checklist:
//
//   - main() calls RunWiring;
//   - parseFlags no longer exists (clause 1: "ceases to exist", not "becomes a
//     second caller");
//   - os.Exit is called inside main() and nowhere else, and log.Fatal* and
//     log.Panic* are called nowhere (clause 2);
//   - main() never exits with a literal: `os.Exit(0)` is an exit code nobody
//     returned. This is the syntactic half of clause 8; the whole of it is
//     TestSeal_Wiring_MainForwardsRunWiring_LiveBinary;
//   - outside main(), nothing writes to a process-global stream (clause 3):
//     no log.Print* or other use of the standard logger, no fmt.Print* naming
//     no writer, no os.NewFile, and no reference to os.Stdout or os.Stderr at
//     all, IN ANY DECLARATION — a fmt.Fprintf(os.Stderr, ...), an
//     EmitV1(os.Stdout, ...) and a package-level `var out = os.Stdout` are the
//     same defect, and the last one is invisible to every in-process stream
//     capture because it bound the descriptor before the swap. Sites are
//     reported per function (or per package-level declaration) with a count,
//     so the checklist reads as things to fix, not lines.
//
// main() is exempt from the stream rule because clause 8 makes it the one
// place that forwards to those streams — and that exemption rests on the live
// binary row, which holds main() to clause 8 byte for byte, not on the clause.
//
// Read from the AST, not by substring, so a comment that MENTIONS os.Exit —
// wiring.go's contract does — is not a hit. CONTROLS: the scan must find at
// least one os.Exit inside main(), which is how main exits and always will
// be; and at least one reference to os.Stdout or os.Stderr somewhere in the
// package, which main() will always carry (clause 8). A scan that finds
// neither is blind and says nothing about the rest.
func TestSeal_Wiring_MainDelegatesAndNothingElseExits(t *testing.T) {
	defer red(t)

	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	type site struct{ file, fn, call string }
	var (
		exits, fatals []site
		globals       = map[site]int{}
		streamRefs    int
		mainCallsRun  bool
		mainFound     bool
		mainExitLits  int
		parseFlagsIn  string
		parsed        int
	)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsed++
		// inspect applies the rules to one declaration's expressions: a
		// function body, or a package-level initialiser, which is where a
		// stream gets cached before anything can swap it.
		inspect := func(where string, isMain bool, node ast.Node) {
			ast.Inspect(node, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					switch f := x.Fun.(type) {
					case *ast.Ident:
						if isMain && f.Name == "RunWiring" {
							mainCallsRun = true
						}
					case *ast.SelectorExpr:
						pkg, ok := f.X.(*ast.Ident)
						if !ok {
							return true
						}
						s := site{name, where, pkg.Name + "." + f.Sel.Name}
						switch {
						case pkg.Name == "os" && f.Sel.Name == "Exit":
							exits = append(exits, s)
							if isMain && len(x.Args) == 1 {
								if _, lit := x.Args[0].(*ast.BasicLit); lit {
									mainExitLits++
								}
							}
						case pkg.Name == "log" && (strings.HasPrefix(f.Sel.Name, "Fatal") || strings.HasPrefix(f.Sel.Name, "Panic")):
							fatals = append(fatals, s)
						case pkg.Name == "log" && f.Sel.Name != "New" && !isMain:
							// Print*, SetOutput, SetFlags, Default, Writer ...:
							// the standard logger. log.New over a supplied
							// writer is fine; over os.Stderr it is caught below.
							globals[s]++
						case pkg.Name == "fmt" && strings.HasPrefix(f.Sel.Name, "Print") && !isMain:
							globals[s]++
						case pkg.Name == "os" && f.Sel.Name == "NewFile" && !isMain:
							// os.NewFile(1, ...) is os.Stdout under another name.
							globals[s]++
						}
					}
				case *ast.SelectorExpr:
					// Any mention of the stream, in any position: as a
					// fmt.Fprint* argument, as an argument to EmitV1, as a
					// receiver, as a var initialiser. The CallExpr case above
					// sees only call targets.
					if pkg, ok := x.X.(*ast.Ident); ok && pkg.Name == "os" && (x.Sel.Name == "Stdout" || x.Sel.Name == "Stderr") {
						streamRefs++
						if !isMain {
							globals[site{name, where, "os." + x.Sel.Name}]++
						}
					}
				}
				return true
			})
		}
		for _, d := range file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				if d.Name.Name == "parseFlags" && d.Recv == nil {
					parseFlagsIn = name
				}
				isMain := d.Name.Name == "main" && d.Recv == nil && name == "main.go"
				if isMain {
					mainFound = true
				}
				inspect(d.Name.Name, isMain, d.Body)
			case *ast.GenDecl:
				// Package-level var and const initialisers. A body that
				// caches `var out = os.Stdout` here has bound the descriptor
				// before any test could swap it; the binding is the defect.
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					names := make([]string, 0, len(vs.Names))
					for _, n := range vs.Names {
						names = append(names, n.Name)
					}
					for _, v := range vs.Values {
						inspect("var "+strings.Join(names, ","), false, v)
					}
				}
			}
		}
	}
	if parsed == 0 || !mainFound {
		t.Fatalf("scanned %d files and found main() = %v — the scan is broken, not the source", parsed, mainFound)
	}

	// CONTROLS.
	inMain := 0
	for _, s := range exits {
		if s.fn == "main" && s.file == "main.go" {
			inMain++
		}
	}
	if inMain == 0 {
		t.Fatal("CONTROL: the scan found no os.Exit inside main() — main exits, so the scan is not reading call sites")
	}
	if streamRefs == 0 {
		t.Fatal("CONTROL: the scan found no reference to os.Stdout or os.Stderr anywhere — main() forwards both (clause 8), so the scan is not reading selectors")
	}

	if !mainCallsRun {
		t.Error("clause 1: main() does not call RunWiring — the spine every row drives is not the spine the binary runs")
	}
	if mainExitLits > 0 {
		t.Errorf("clause 8: main() calls os.Exit with a literal ×%d — an exit code nobody returned; main exits with the ExitCode RunWiring answered, or exitInternal beside its error", mainExitLits)
	}
	if parseFlagsIn != "" {
		t.Errorf("clause 1: parseFlags still exists in %s — it is to cease to exist, not become a second caller of parseInvocationFlags", parseFlagsIn)
	}
	for _, s := range exits {
		if s.fn != "main" || s.file != "main.go" {
			t.Errorf("clause 2: %s called from %s.%s — os.Exit survives in main() and nowhere else", s.call, s.file, s.fn)
		}
	}
	for _, s := range fatals {
		t.Errorf("clause 2: %s called from %s.%s — every log.Fatalf on this path becomes a returned ExitCode", s.call, s.file, s.fn)
	}
	sites := make([]site, 0, len(globals))
	for s := range globals {
		sites = append(sites, s)
	}
	sort.Slice(sites, func(i, j int) bool {
		a, b := sites[i], sites[j]
		if a.file != b.file {
			return a.file < b.file
		}
		if a.fn != b.fn {
			return a.fn < b.fn
		}
		return a.call < b.call
	})
	for _, s := range sites {
		t.Errorf("clause 3: %s.%s uses %s ×%d — a process-global stream; every byte must pass through a writer RunWiring supplied", s.file, s.fn, s.call, globals[s])
	}
}
