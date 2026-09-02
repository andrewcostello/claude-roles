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
// None of these tests may call t.Parallel: they capture os.Stdout and swap the
// process-wide digest recorder.

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
	"os"
	"path/filepath"
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
				c.name = "contract=" + contract + "/out=" + [...]string{"none", "fresh", "seeded"}[out] + "/json=" + map[bool]string{false: "off", true: "on"}[asJSON]
				cells = append(cells, c)
			}
		}
	}
	return cells
}

// prepareOut seeds -out per the cell and returns the run-state path, or "" for
// outNone.
func prepareOut(t *testing.T, f wiringFixture, mode outMode) string {
	t.Helper()
	if mode == outNone {
		return ""
	}
	runState := filepath.Join(f.dir, fixtureRunState)
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

// ─── driver 1: the spine main() runs today ───────────────────────────────────

// isolateDigests installs a fresh recorder as BOTH the package recorder the
// input path writes into AND the installed DigestSource the wrapper reads
// from, and restores both afterwards.
//
// Both, not one: init() stored the recorder's pointer in digestSource, so
// swapping unframedDigests alone leaves EmitV2 reading a recorder nothing
// wrote into. It would raise, run() would log.Fatalf, and the whole test
// binary would exit 1 mid-row.
func isolateDigests(t *testing.T) {
	t.Helper()
	fresh := &unframedDigestSource{}
	savedRecorder := unframedDigests
	t.Cleanup(func() { unframedDigests = savedRecorder })
	unframedDigests = fresh
	withHooks(t, contractFlagRegistrar, fresh, framedStdinReader)
}

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
// Stderr is NOT captured: run() reports through the log package, whose writer
// is process-global and which RunWiring clause 3 forbids retargeting. Rows
// through this driver assert on ExitCode, Stdout and the two artifacts only.
func driveSpine(t *testing.T, opts options) Artifacts {
	t.Helper()
	isolateDigests(t)

	art := Artifacts{RunState: FileArtifact{State: ArtifactNotApplicable}, V2Sidecar: FileArtifact{State: ArtifactNotApplicable}}
	var rsBefore, scBefore fileSnap
	if opts.out != "" {
		rsBefore, scBefore = snapFile(t, opts.out), snapFile(t, V2SidecarPath(opts.out))
	}

	var code int
	out := stdoutOf(t, func() { code = run(opts) })
	art.ExitCode = ExitCode(code)
	art.Stdout = []byte(out)

	if opts.out != "" {
		rsAfter, scAfter := snapFile(t, opts.out), snapFile(t, V2SidecarPath(opts.out))
		art.RunState = FileArtifact{Path: opts.out, State: stateOf(rsBefore, rsAfter), Bytes: rsAfter.bytes}
		art.V2Sidecar = FileArtifact{Path: V2SidecarPath(opts.out), State: stateOf(scBefore, scAfter), Bytes: scAfter.bytes}
	}
	return art
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
// that reached the PROCESS's stdout during the call — which clause 3 says must
// be none.
func driveRunWiring(t *testing.T, inv Invocation) (art Artifacts, leaked string, err error) {
	t.Helper()
	isolateDigests(t)
	leaked = stdoutOf(t, func() { art, err = RunWiring(inv) })
	return art, leaked, err
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
// the measured mutation in exactly the two v2 -json cells.
func TestSeal_Wiring_Mapping_ContractByOutByJSON(t *testing.T) {
	defer red(t)
	for _, cell := range mappingCells() {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			defer red(t)
			f := newWiringFixture(t)
			runState := prepareOut(t, f, cell.out)
			judgeCell(t, cell, f, runState, driveSpine(t, spineOptions(f, cell, runState)))
		})
	}
}

// THE SAME MAPPING, through RunWiring, plus what only RunWiring can answer:
// relative paths resolve against Dir (clause 7), nothing reaches the process's
// own stdout (clause 3), every state is set (clause 5's boundary), and the
// exit code is a member of the declared set.
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
			runState := prepareOut(t, f, cell.out)
			art, leaked, err := driveRunWiring(t, invocationFor(f, cell))
			requireRunWiringBody(t, err)
			if leaked != "" {
				t.Errorf("%s: %d bytes reached the process's stdout instead of Artifacts.Stdout (clause 3):\n%s", cell.name, len(leaked), leaked)
			}
			if !art.RunState.State.Valid() || !art.V2Sidecar.State.Valid() {
				t.Errorf("%s: an artifact state is unset beside a nil error (run-state %s, sidecar %s)", cell.name, art.RunState.State, art.V2Sidecar.State)
			}
			if !containsExit(DeclaredExitCodes, int(art.ExitCode)) {
				t.Errorf("%s: exit %d is outside DeclaredExitCodes %v", cell.name, art.ExitCode, DeclaredExitCodes)
			}
			judgeCell(t, cell, f, runState, art)
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

// D3 — a rejected -contract-version exits 3, names the accepted set, and does
// so BEFORE the config search and before any input is read.
//
// The worktree has no rule table and no -config, and no diff is supplied. A
// run that reached the config search would exit 3 for THAT reason; the
// CONTROL leg — contract "1", same inputs — does exactly that, and its
// problem lines are what every rejected value's output must NOT contain.
// Without the control, "exits 3 with INVALID_INPUT" is satisfied by a body
// that never looked at the contract at all.
//
// Artifacts (D3.6): -out and its sidecar both pre-exist, and both must be
// byte-identical afterwards — Stale, not Removed, not Written.
func TestSeal_Wiring_RejectedContractExits3BeforeAnyInputIsRead(t *testing.T) {
	defer red(t)

	dir := t.TempDir()
	runState := filepath.Join(dir, fixtureRunState)
	if err := os.WriteFile(runState, []byte(wiringSeedRunState), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(V2SidecarPath(runState), []byte(wiringSeedSidecar), 0o600); err != nil {
		t.Fatal(err)
	}
	base := options{worktree: dir, base: "origin/main", out: runState, json: true, noGit: true}

	// CONTROL — an accepted contract against the same inputs fails LATER, at
	// the config search, and its complaint is about the table.
	ctl := base
	ctl.contractVersion = defaultContractVersion.String()
	control := driveSpine(t, ctl)
	if control.ExitCode != exitInvalid {
		t.Fatalf("CONTROL: contract %q with no rule table exited %d, want %d\n%s", ctl.contractVersion, control.ExitCode, exitInvalid, control.Stdout)
	}
	controlProblems := problemLines(string(control.Stdout))
	if len(controlProblems) == 0 {
		t.Fatalf("CONTROL: the config-search failure reported no problem lines, so there is nothing to tell the two exit-3 paths apart:\n%s", control.Stdout)
	}
	if strings.Contains(string(control.Stdout), "accepted values are") {
		t.Fatalf("CONTROL: an accepted contract was reported as rejected:\n%s", control.Stdout)
	}
	if control.RunState.State != ArtifactStale || control.V2Sidecar.State != ArtifactStale {
		t.Errorf("CONTROL: a run that failed the config search touched -out (run-state %s, sidecar %s), want stale/stale", control.RunState.State, control.V2Sidecar.State)
	}

	for _, raw := range []string{"0", "3", "v1", "02", " 2", ""} {
		opts := base
		opts.contractVersion = raw
		art := driveSpine(t, opts)
		name := fmt.Sprintf("contract=%q", raw)

		if art.ExitCode != exitInvalid {
			t.Errorf("%s: exit %d, want %d", name, art.ExitCode, exitInvalid)
		}
		out := string(art.Stdout)
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
		if art.RunState.State != ArtifactStale || art.V2Sidecar.State != ArtifactStale {
			t.Errorf("%s: a rejected contract touched -out (run-state %s, sidecar %s), want stale/stale (D3.6)", name, art.RunState.State, art.V2Sidecar.State)
		}
	}
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
//   - "capabilities" answers with one JSON object on Stdout and the exit code
//     the probe's own contract derives from the registry;
//   - "help", "-h", "--help" exit 0 with usage on Stderr and nothing on Stdout;
//   - an unknown flag exits exitFlagError, with the flag package's message on
//     Stderr — the mapping parseInvocationFlags clause 3 assigns;
//   - "init" is judged on ExitCode and the streams only, as Artifacts requires:
//     a fresh worktree scaffolds (0), and a second init refuses (exitInvalid).
//
// Every leg also asserts nothing reached the process's stdout (clause 3), that
// the working directory is unchanged (clause 7), and that both artifacts are
// NotApplicable — no subcommand produces them.
func TestSeal_Wiring_RunWiring_SubcommandsAndFlagErrors(t *testing.T) {
	defer red(t)

	cwdBefore, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	type leg struct {
		name     string
		args     []string
		exit     ExitCode
		stdoutOK func([]byte) bool
		stderrOK func([]byte) bool
	}
	probeExit := ExitCode(exitOK)
	if len(buildCapabilityReport().Missing) > 0 {
		probeExit = exitCapabilityIncomplete
	}
	oneJSONObject := func(b []byte) bool {
		dec := json.NewDecoder(bytes.NewReader(b))
		var rep CapabilityReport
		if dec.Decode(&rep) != nil {
			return false
		}
		var extra any
		return errors.Is(dec.Decode(&extra), io.EOF)
	}
	empty := func(b []byte) bool { return len(b) == 0 }
	nonEmpty := func(b []byte) bool { return len(b) > 0 }

	legs := []leg{
		{"capabilities", []string{probeSubcommand}, probeExit, oneJSONObject, empty},
		{"help", []string{"help"}, exitOK, empty, nonEmpty},
		{"-h", []string{"-h"}, exitOK, empty, nonEmpty},
		{"--help", []string{"--help"}, exitOK, empty, nonEmpty},
		{"unknown flag", []string{"-no-such-flag"}, exitFlagError, empty, nonEmpty},
		{"init fresh", []string{"init", "-worktree", "."}, exitOK, func([]byte) bool { return true }, func([]byte) bool { return true }},
		{"init refuses", []string{"init", "-worktree", "."}, exitInvalid, nonEmpty, func([]byte) bool { return true }},
	}
	for _, l := range legs {
		art, leaked, err := driveRunWiring(t, Invocation{Args: l.args, Dir: dir})
		requireRunWiringBody(t, err)
		if art.ExitCode != l.exit {
			t.Errorf("%s: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", l.name, art.ExitCode, l.exit, art.Stdout, art.Stderr)
		}
		if !l.stdoutOK(art.Stdout) {
			t.Errorf("%s: stdout is not what the leg requires:\n%s", l.name, art.Stdout)
		}
		if !l.stderrOK(art.Stderr) {
			t.Errorf("%s: stderr is not what the leg requires:\n%s", l.name, art.Stderr)
		}
		if leaked != "" {
			t.Errorf("%s: %d bytes reached the process's stdout (clause 3):\n%s", l.name, len(leaked), leaked)
		}
		if art.RunState.State != ArtifactNotApplicable || art.V2Sidecar.State != ArtifactNotApplicable {
			t.Errorf("%s: a subcommand reported artifacts (run-state %s, sidecar %s), want not-applicable/not-applicable", l.name, art.RunState.State, art.V2Sidecar.State)
		}
	}
	if cwdAfter, err := os.Getwd(); err != nil || cwdAfter != cwdBefore {
		t.Errorf("clause 7: the working directory moved from %q to %q (err %v)", cwdBefore, cwdAfter, err)
	}
}

// D1's structural obligation, RunWiring clauses 1 and 2. RED TODAY on three
// counts, each of which is one line of GO-1-3's checklist:
//
//   - main() calls RunWiring;
//   - parseFlags no longer exists (clause 1: "ceases to exist", not "becomes a
//     second caller");
//   - os.Exit is called inside main() and nowhere else, and log.Fatal,
//     log.Fatalf and log.Fatalln are called nowhere (clause 2).
//
// Read from the AST, not by substring, so a comment that MENTIONS os.Exit —
// wiring.go's contract does — is not a hit. CONTROL: the scan must find at
// least one os.Exit inside main(), which is how main exits and always will be;
// a scan that finds none is blind and says nothing about the rest.
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
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := call.Fun.(type) {
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
					case pkg.Name == "log" && strings.HasPrefix(f.Sel.Name, "Fatal"):
						fatals = append(fatals, s)
					}
				}
				return true
			})
		}
	}
	if parsed == 0 || !mainFound {
		t.Fatalf("scanned %d files and found main() = %v — the scan is broken, not the source", parsed, mainFound)
	}

	// CONTROL.
	inMain := 0
	for _, s := range exits {
		if s.fn == "main" && s.file == "main.go" {
			inMain++
		}
	}
	if inMain == 0 {
		t.Fatal("CONTROL: the scan found no os.Exit inside main() — main exits, so the scan is not reading call sites")
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
}
