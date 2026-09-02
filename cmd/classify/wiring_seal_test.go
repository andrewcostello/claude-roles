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
// THE STRUCT IS NOT THE EVIDENCE. Both drivers take their own two snapshots of
// -out and its sidecar. driveSpine has to, because run() reports nothing;
// driveRunWiring does anyway, and holds the Artifacts RunWiring returned
// against the tree. A body that answers Written/Removed with the right Bytes
// and never touches a file is a finding, not a pass — and on the day
// driveSpine retires it would otherwise be the only thing left looking.
//
// None of these tests may call t.Parallel: they capture os.Stdout and
// os.Stderr, retarget the standard logger's writer, and swap the process-wide
// digest recorder and certified-config slot.

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
	"path/filepath"
	"reflect"
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
func driveSpine(t *testing.T, opts options) wiringRun {
	t.Helper()
	var r wiringRun
	r.digests, r.certified = isolateProcessState(t)

	r.art = Artifacts{RunState: FileArtifact{State: ArtifactNotApplicable}, V2Sidecar: FileArtifact{State: ArtifactNotApplicable}}
	var rsBefore, scBefore fileSnap
	if opts.out != "" {
		rsBefore, scBefore = snapFile(t, opts.out), snapFile(t, V2SidecarPath(opts.out))
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
// what the reported state may be beside what the tree says.
func driveRunWiring(t *testing.T, name string, inv Invocation) wiringRun {
	t.Helper()
	var r wiringRun
	r.digests, r.certified = isolateProcessState(t)

	runState := filepath.Join(inv.Dir, fixtureRunState)
	rsBefore, scBefore := snapFile(t, runState), snapFile(t, V2SidecarPath(runState))
	r.leakedStdout, r.leakedStderr, r.logged = processStreamsOf(t, func() { r.art, r.err = RunWiring(inv) })
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
			r := driveSpine(t, spineOptions(f, cell, runState))
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
		{"spine", func(t *testing.T, _, dir, raw string, withDiff bool) wiringRun {
			opts := options{worktree: dir, base: "origin/main", out: filepath.Join(dir, fixtureRunState), json: true, noGit: true, contractVersion: raw}
			if withDiff {
				opts.args = []string{filepath.Join(dir, fixtureDiffName)}
			}
			return driveSpine(t, opts)
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

// D1's structural obligation, RunWiring clauses 1, 2 and 3. RED TODAY on four
// counts, each of which is one line of GO-1-3's checklist:
//
//   - main() calls RunWiring;
//   - parseFlags no longer exists (clause 1: "ceases to exist", not "becomes a
//     second caller");
//   - os.Exit is called inside main() and nowhere else, and log.Fatal* and
//     log.Panic* are called nowhere (clause 2);
//   - outside main(), nothing writes to a process-global stream (clause 3):
//     no log.Print* or other use of the standard logger, no fmt.Print* naming
//     no writer, and no reference to os.Stdout or os.Stderr at all — a
//     fmt.Fprintf(os.Stderr, ...) and an EmitV1(os.Stdout, ...) are the same
//     defect. main() is exempt because clause 8 makes it the one place that
//     forwards to those streams. Sites are reported per function with a
//     count, so the checklist reads as functions to fix, not lines.
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
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if fn.Name.Name == "parseFlags" && fn.Recv == nil {
				parseFlagsIn = name
			}
			isMain := fn.Name.Name == "main" && fn.Recv == nil && name == "main.go"
			if isMain {
				mainFound = true
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
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
						s := site{name, fn.Name.Name, pkg.Name + "." + f.Sel.Name}
						switch {
						case pkg.Name == "os" && f.Sel.Name == "Exit":
							exits = append(exits, s)
						case pkg.Name == "log" && (strings.HasPrefix(f.Sel.Name, "Fatal") || strings.HasPrefix(f.Sel.Name, "Panic")):
							fatals = append(fatals, s)
						case pkg.Name == "log" && f.Sel.Name != "New" && !isMain:
							// Print*, SetOutput, SetFlags, ...: the standard
							// logger. log.New over a supplied writer is fine.
							globals[s]++
						case pkg.Name == "fmt" && strings.HasPrefix(f.Sel.Name, "Print") && !isMain:
							globals[s]++
						}
					}
				case *ast.SelectorExpr:
					// Any mention of the stream, in any position: as a
					// fmt.Fprint* argument, as an argument to EmitV1, as a
					// receiver. The CallExpr case above sees only call targets.
					if pkg, ok := x.X.(*ast.Ident); ok && pkg.Name == "os" && (x.Sel.Name == "Stdout" || x.Sel.Name == "Stderr") {
						streamRefs++
						if !isMain {
							globals[site{name, fn.Name.Name, "os." + x.Sel.Name}]++
						}
					}
				}
				return true
			})
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
