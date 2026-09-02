package main

// Seals for GO-1-1's wiring contract: (contract × -out × -json) → artifact set
// + exit code.
//
// THE SUBJECT IS A MAPPING, NOT A LIBRARY. A test that calls EmitV1/EmitV2 as
// a library never observes which of them emit() chose. The measured mutation
// — emit()'s ContractV2 arm rewritten to EmitV1 — leaves the existing suite
// green. Every mapping row below therefore drives a chooser: run() today,
// RunWiring when GO-1-3 lands its body.
//
// RunWiring is a stub. Rows that reach production ONLY through it are red
// whatever emit() does, and detect nothing. The live table therefore drives
// run(), which is the classify path the shipped binary runs today. RunWiring
// rows judge the same answers independently (exit, stdout SHAPE, artifact
// state) on the bed they own. The body's own Artifacts.RunState / V2Sidecar
// are the answers; a filesystem snapshot is a second check that a reported
// state matches the bed, not a stand-in for ArtifactStateUnset. They do not
// byte-compare stdout to a second invocation: classified_at and Worktree make
// that unsatisfiable.
//
// No row here execs a tracked binary. No row re-seals the digest swap; that
// already reddens TestSeal_Repair_ResolveConfigDual_ConsumedBytesMustBeTheCertifiedBytes.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ─── the bed ─────────────────────────────────────────────────────────────────

type classifyBed struct {
	dir      string
	cfgPath  string
	diffPath string
	outPath  string
}

func newClassifyBed(t *testing.T) classifyBed {
	t.Helper()
	dir := t.TempDir()
	cfg, err := filepath.Abs(exampleConfigPath)
	if err != nil {
		t.Fatalf("resolving %s: %v", exampleConfigPath, err)
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("fixture config %s is missing: %v", cfg, err)
	}
	diffPath := filepath.Join(dir, "change.diff")
	if err := os.WriteFile(diffPath, []byte(diffFor(walletPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	return classifyBed{
		dir:      dir,
		cfgPath:  cfg,
		diffPath: diffPath,
		outPath:  filepath.Join(dir, "run-state.json"),
	}
}

func (b classifyBed) seedRunState(t *testing.T) []byte {
	t.Helper()
	data := []byte(`{"schema_version": 1, "status": "seeded-by-the-seal"}` + "\n")
	if err := os.WriteFile(b.outPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func (b classifyBed) seedSidecar(t *testing.T) []byte {
	t.Helper()
	data := []byte(`{"schema_version": 1, "response": {"seeded": "by the seal"}}` + "\n")
	if err := os.WriteFile(V2SidecarPath(b.outPath), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

type fileSnap struct {
	exists bool
	bytes  []byte
}

func snap(t *testing.T, path string) fileSnap {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- path is inside t.TempDir()
	if err != nil {
		if os.IsNotExist(err) {
			return fileSnap{}
		}
		t.Fatalf("snapshotting %s: %v", path, err)
	}
	return fileSnap{exists: true, bytes: data}
}

func classifyArtifact(path string, produced bool, before, after fileSnap) FileArtifact {
	if !produced {
		return FileArtifact{State: ArtifactNotApplicable}
	}
	switch {
	case !before.exists && !after.exists:
		return FileArtifact{Path: path, State: ArtifactAbsent}
	case before.exists && !after.exists:
		return FileArtifact{Path: path, State: ArtifactRemoved}
	case !before.exists && after.exists:
		return FileArtifact{Path: path, State: ArtifactWritten, Bytes: after.bytes}
	case bytes.Equal(before.bytes, after.bytes):
		return FileArtifact{Path: path, State: ArtifactStale, Bytes: after.bytes}
	default:
		return FileArtifact{Path: path, State: ArtifactWritten, Bytes: after.bytes}
	}
}

func (b classifyBed) isolateRecorder(t *testing.T) {
	t.Helper()
	savedRec, savedSrc := unframedDigests, digestSource
	fresh := &unframedDigestSource{}
	unframedDigests, digestSource = fresh, fresh
	t.Cleanup(func() {
		unframedDigests, digestSource = savedRec, savedSrc
	})
}

// driveLive runs one invocation through run(), which is the classify path
// the shipped binary runs today and which RunWiring clause 1 requires GO-1-3
// to keep as its inside.
func (b classifyBed) driveLive(t *testing.T, contract string, asJSON, withOut bool) Artifacts {
	t.Helper()
	b.isolateRecorder(t)

	out := ""
	if withOut {
		out = b.outPath
	}
	sidecarPath := V2SidecarPath(b.outPath)
	beforeRun := snap(t, b.outPath)
	beforeSidecar := snap(t, sidecarPath)

	opts := options{
		configPath:      b.cfgPath,
		worktree:        b.dir,
		base:            "origin/main",
		out:             out,
		json:            asJSON,
		noGit:           true,
		contractVersion: contract,
		args:            []string{b.diffPath},
	}

	var code int
	stdout := stdoutOf(t, func() { code = run(opts) })

	return Artifacts{
		ExitCode:  ExitCode(code),
		Stdout:    []byte(stdout),
		RunState:  classifyArtifact(b.outPath, withOut, beforeRun, snap(t, b.outPath)),
		V2Sidecar: classifyArtifact(sidecarPath, withOut, beforeSidecar, snap(t, sidecarPath)),
	}
}

func (b classifyBed) dirNames(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(b.dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", b.dir, err)
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return sortedCopy(out)
}

func (b classifyBed) sha256Of(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- a fixture this test staged
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ─── stdout shapes ───────────────────────────────────────────────────────────

type stdoutShape int

const (
	shapeHumanReport stdoutShape = iota
	shapeV1Payload
	shapeV2Wrapper
	shapeInvalidInput
)

func (s stdoutShape) String() string {
	switch s {
	case shapeHumanReport:
		return "the human report"
	case shapeV1Payload:
		return "the bare v1 classification payload"
	case shapeV2Wrapper:
		return "the v2 response wrapper"
	case shapeInvalidInput:
		return "the INVALID_INPUT report"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

var v2WrapperKeys = []string{"classification", "computed_config_sha256", "computed_diff_sha256", "response_version"}

func assertStdoutShape(t *testing.T, label string, want stdoutShape, stdout []byte) {
	t.Helper()
	switch want {
	case shapeHumanReport:
		if !bytes.Contains(stdout, []byte("=== CLASSIFICATION ===")) {
			t.Errorf("%s: stdout is not %s:\n%s", label, want, stdout)
		}
		if json.Valid(bytes.TrimSpace(stdout)) {
			t.Errorf("%s: stdout parses as JSON, so this invocation emitted a machine wire where the human report was asked for:\n%s", label, stdout)
		}
	case shapeV1Payload:
		keys := topKeys(t, stdout)
		if !containsKey(keys, "risk") {
			t.Errorf("%s: stdout is not %s — no top-level \"risk\"; keys were %v", label, want, keys)
		}
		for _, k := range v2WrapperKeys {
			if containsKey(keys, k) {
				t.Errorf("%s: stdout carries the wrapper key %q, so contract 1 emitted a v2 response; keys were %v", label, k, keys)
			}
		}
	case shapeV2Wrapper:
		keys := topKeys(t, stdout)
		if !sameStrings(keys, v2WrapperKeys) {
			t.Errorf("%s: stdout is not %s — top-level keys are %v, want exactly %v. THIS IS THE ROW THAT CATCHES emit()'s ContractV2 arm calling EmitV1.", label, want, keys, v2WrapperKeys)
			return
		}
		var w ResponseWrapper
		if err := json.Unmarshal(stdout, &w); err != nil {
			t.Errorf("%s: stdout has the wrapper's keys but does not unmarshal as a ResponseWrapper: %v", label, err)
			return
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(w.Classification, &payload); err != nil {
			t.Errorf("%s: the wrapper's classification member is not a JSON object: %v", label, err)
			return
		}
		if got := string(payload["contract_version"]); got != "2" {
			t.Errorf("%s: the wrapped payload declares contract_version %s, want 2", label, got)
		}
	case shapeInvalidInput:
		if !bytes.Contains(stdout, []byte("=== CLASSIFY: INVALID_INPUT ===")) {
			t.Errorf("%s: stdout is not %s:\n%s", label, want, stdout)
		}
		if json.Valid(bytes.TrimSpace(stdout)) {
			t.Errorf("%s: a rejected invocation put a parseable JSON document on stdout:\n%s", label, stdout)
		}
	default:
		t.Fatalf("%s: the seal named shape %v, which is outside the closed set", label, want)
	}
}

func compactJSON(t *testing.T, label string, b []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		t.Fatalf("%s: not a JSON value (%v):\n%s", label, err, b)
	}
	return buf.String()
}

func containsKey(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

func inDeclaredExitCodes(code int) bool {
	for _, c := range DeclaredExitCodes {
		if c == code {
			return true
		}
	}
	return false
}

// ─── the mapping table ───────────────────────────────────────────────────────

type wiringRow struct {
	name         string
	contract     string
	asJSON       bool
	withOut      bool
	seedSidecar  bool
	seedRunState bool
	wantExit     ExitCode
	wantStdout   stdoutShape
	wantRunState ArtifactState
	wantSidecar  ArtifactState
	why          string
}

func wiringRows() []wiringRow {
	return []wiringRow{
		{
			name: "v1/report/no-out", contract: "1", asJSON: false, withOut: false,
			wantExit: exitOK, wantStdout: shapeHumanReport,
			wantRunState: ArtifactNotApplicable, wantSidecar: ArtifactNotApplicable,
			why: "no -json, no -out: no file is produced and NotApplicable is reachable",
		},
		{
			name: "v1/json/no-out", contract: "1", asJSON: true, withOut: false,
			wantExit: exitOK, wantStdout: shapeV1Payload,
			wantRunState: ArtifactNotApplicable, wantSidecar: ArtifactNotApplicable,
			why: "the v1 wire on stdout, and the CONTROL for the v2 wire row",
		},
		{
			name: "v2/report/no-out", contract: "2", asJSON: false, withOut: false,
			wantExit: exitOK, wantStdout: shapeHumanReport,
			wantRunState: ArtifactNotApplicable, wantSidecar: ArtifactNotApplicable,
			why: "the contract does not reach the human report — emit returns before the contract switch when asJSON is false",
		},
		{
			name: "v2/json/no-out", contract: "2", asJSON: true, withOut: false,
			wantExit: exitOK, wantStdout: shapeV2Wrapper,
			wantRunState: ArtifactNotApplicable, wantSidecar: ArtifactNotApplicable,
			why: "THE MEASURED MUTATION'S ROW: emit()'s ContractV2 arm rewritten to EmitV1 reddens here",
		},
		{
			name: "v1/report/out", contract: "1", asJSON: false, withOut: true,
			wantExit: exitOK, wantStdout: shapeHumanReport,
			wantRunState: ArtifactWritten, wantSidecar: ArtifactAbsent,
			why: "-out is independent of -json: persist still writes the run-state, and a v1 run still emits no sidecar",
		},
		{
			name: "v1/json/out", contract: "1", asJSON: true, withOut: true,
			wantExit: exitOK, wantStdout: shapeV1Payload,
			wantRunState: ArtifactWritten, wantSidecar: ArtifactAbsent,
			why: "json + -out under v1: Absent sidecar, which is a different fact from Removed",
		},
		{
			name: "v2/report/out", contract: "2", asJSON: false, withOut: true,
			wantExit: exitOK, wantStdout: shapeHumanReport,
			wantRunState: ArtifactWritten, wantSidecar: ArtifactWritten,
			why: "WriteV2Sidecar's guard is ContractV2 AND -out, not -json",
		},
		{
			name: "v2/json/out", contract: "2", asJSON: true, withOut: true,
			wantExit: exitOK, wantStdout: shapeV2Wrapper,
			wantRunState: ArtifactWritten, wantSidecar: ArtifactWritten,
			why: "both artifacts, and the sidecar bytes are cross-checked against this run's stdout",
		},
		{
			name: "v1/json/out/over-a-stale-sidecar", contract: "1", asJSON: true, withOut: true, seedSidecar: true,
			wantExit: exitOK, wantStdout: shapeV1Payload,
			wantRunState: ArtifactWritten, wantSidecar: ArtifactRemoved,
			why: "the only row reaching ArtifactRemoved; persist's ContractV1 arm must tear the sidecar down",
		},
		{
			name: "v3/json/no-out", contract: "3", asJSON: true, withOut: false,
			wantExit: exitInvalid, wantStdout: shapeInvalidInput,
			wantRunState: ArtifactNotApplicable, wantSidecar: ArtifactNotApplicable,
			why: "-contract-version 3 -> exit 3; the message must name the accepted set",
		},
		{
			name: "v3/json/out", contract: "3", asJSON: true, withOut: true, seedSidecar: true, seedRunState: true,
			wantExit: exitInvalid, wantStdout: shapeInvalidInput,
			wantRunState: ArtifactStale, wantSidecar: ArtifactStale,
			why: "rejected contract with -out: both seeded artifacts survive byte-identical. Sealed through run() AND RunWiring so a parallel body cannot clobber the shipped path",
		},
	}
}

func assertArtifact(t *testing.T, label string, want ArtifactState, got FileArtifact) {
	t.Helper()
	if got.State != want {
		t.Errorf("%s: state = %s, want %s (path %q)", label, got.State, want, got.Path)
	}
	if !got.State.Valid() {
		t.Errorf("%s: state %s is outside the closed set; ArtifactStateUnset beside a nil error is illegal", label, got.State)
	}
	if (got.State == ArtifactNotApplicable) != (got.Path == "") {
		t.Errorf("%s: state %s with path %q — Path is empty exactly when the state is not-applicable", label, got.State, got.Path)
	}
	if got.Bytes != nil && got.State != ArtifactWritten && got.State != ArtifactStale {
		t.Errorf("%s: state %s carries %d bytes — Bytes is nil in every state but written and stale", label, got.State, len(got.Bytes))
	}
}

func assertMapping(t *testing.T, row wiringRow, bed classifyBed, got Artifacts) {
	t.Helper()
	if got.ExitCode != row.wantExit {
		t.Errorf("exit = %d, want %d (%s)", got.ExitCode, row.wantExit, row.why)
	}
	if !inDeclaredExitCodes(int(got.ExitCode)) {
		t.Errorf("exit %d is not in DeclaredExitCodes %v", got.ExitCode, DeclaredExitCodes)
	}
	assertStdoutShape(t, row.name, row.wantStdout, got.Stdout)
	assertArtifact(t, row.name+" run-state", row.wantRunState, got.RunState)
	assertArtifact(t, row.name+" v2 sidecar", row.wantSidecar, got.V2Sidecar)

	if row.wantStdout == shapeV2Wrapper && sameStrings(topKeys(t, got.Stdout), v2WrapperKeys) {
		var w ResponseWrapper
		if err := json.Unmarshal(got.Stdout, &w); err != nil {
			t.Errorf("%s: wrapper keys present but unmarshal failed: %v", row.name, err)
		} else {
			if want := bed.sha256Of(t, bed.cfgPath); w.ComputedConfigSHA256 != want {
				t.Errorf("%s: computed_config_sha256 = %q, want SHA-256 of the config this run consumed = %q", row.name, w.ComputedConfigSHA256, want)
			}
			if want := bed.sha256Of(t, bed.diffPath); w.ComputedDiffSHA256 != want {
				t.Errorf("%s: computed_diff_sha256 = %q, want SHA-256 of the diff this run consumed = %q", row.name, w.ComputedDiffSHA256, want)
			}
		}
	}

	if row.contract == "3" {
		report := string(got.Stdout)
		if !strings.Contains(report, `"3"`) {
			t.Errorf("the report does not name the value it rejected:\n%s", report)
		}
		for _, v := range contractVersionSet {
			if !strings.Contains(report, v.String()) {
				t.Errorf("the report does not name accepted contract %s:\n%s", v, report)
			}
		}
		if !strings.Contains(report, "-"+flagContractVersion) {
			t.Errorf("the report never names -%s:\n%s", flagContractVersion, report)
		}
	}

	if !row.withOut {
		want := []string{filepath.Base(bed.diffPath)}
		if gotNames := bed.dirNames(t); !sameStrings(gotNames, want) {
			t.Errorf("%s: a run with no -out changed the directory: %v, want exactly %v", row.name, gotNames, want)
		}
	}
}

func (r wiringRow) argv(b classifyBed) []string {
	args := []string{"-no-git", "-worktree", b.dir, "-config", b.cfgPath}
	if r.asJSON {
		args = append(args, "-json")
	}
	if r.withOut {
		args = append(args, "-out", b.outPath)
	}
	args = append(args, "-"+flagContractVersion, r.contract)
	return append(args, b.diffPath)
}

func (b classifyBed) applySeeds(t *testing.T, row wiringRow) {
	t.Helper()
	if row.seedRunState {
		b.seedRunState(t)
	}
	if row.seedSidecar {
		b.seedSidecar(t)
	}
}

// TestSeal_Wiring_TheMappingIsWhatRunAnswers is the live table.
//
// GREEN TODAY, and that is the point: a seal that reaches production only
// through the unimplemented RunWiring stub detects nothing. These rows reach
// production now, so rewriting emit()'s ContractV2 arm to EmitV1 reddens
// v2/json/{no-out,out}.
func TestSeal_Wiring_TheMappingIsWhatRunAnswers(t *testing.T) {
	for _, row := range wiringRows() {
		row := row
		t.Run(row.name, func(t *testing.T) {
			defer red(t)
			bed := newClassifyBed(t)
			bed.applySeeds(t, row)
			assertMapping(t, row, bed, bed.driveLive(t, row.contract, row.asJSON, row.withOut))
		})
	}
}

// TestSeal_Wiring_TheContractSelectsTheWire is the row the measured mutation
// reddens, and it is where that mutation's control lives.
//
// ONE BED, FOUR ARMS, EVERY CLAIM JUDGED IN THIS CALL. Differences between
// outputs are attributable to (contract, -json) and to nothing else. Wrapper
// digests are checked against SHA-256 of the files this test staged.
func TestSeal_Wiring_TheContractSelectsTheWire(t *testing.T) {
	defer red(t)

	bed := newClassifyBed(t)
	v1Report := bed.driveLive(t, "1", false, false)
	v2Report := bed.driveLive(t, "2", false, false)
	v1JSON := bed.driveLive(t, "1", true, false)
	v2JSON := bed.driveLive(t, "2", true, false)

	for name, got := range map[string]Artifacts{
		"v1/report": v1Report, "v2/report": v2Report,
		"v1/json": v1JSON, "v2/json": v2JSON,
	} {
		if got.ExitCode != exitOK {
			t.Fatalf("%s exited %d, want %d:\n%s", name, got.ExitCode, exitOK, got.Stdout)
		}
	}

	assertStdoutShape(t, "v1/json", shapeV1Payload, v1JSON.Stdout)
	assertStdoutShape(t, "v2/json", shapeV2Wrapper, v2JSON.Stdout)
	assertStdoutShape(t, "v1/report", shapeHumanReport, v1Report.Stdout)
	assertStdoutShape(t, "v2/report", shapeHumanReport, v2Report.Stdout)
	if t.Failed() {
		t.Fatal("the wire shapes are the claim; the comparisons below are meaningless until they hold")
	}

	var v1Payload map[string]json.RawMessage
	if err := json.Unmarshal(v1JSON.Stdout, &v1Payload); err != nil {
		t.Fatalf("the v1 payload is not a JSON object: %v", err)
	}
	var wrapper ResponseWrapper
	if err := json.Unmarshal(v2JSON.Stdout, &wrapper); err != nil {
		t.Fatalf("the v2 stdout is not a ResponseWrapper: %v", err)
	}
	var v2Payload map[string]json.RawMessage
	if err := json.Unmarshal(wrapper.Classification, &v2Payload); err != nil {
		t.Fatalf("the wrapper's classification member is not a JSON object: %v", err)
	}
	for _, field := range []string{"risk", "financial_paths_touched", "human_pr_gate", "components"} {
		got1, ok1 := v1Payload[field]
		got2, ok2 := v2Payload[field]
		if !ok1 || !ok2 {
			t.Errorf("CONTROL: %q is present in the v1 payload=%v and in the v2 envelope=%v", field, ok1, ok2)
			continue
		}
		if compactJSON(t, "v1 "+field, got1) != compactJSON(t, "v2 "+field, got2) {
			t.Errorf("CONTROL: the two contracts disagree about %q (v1 %s, v2 %s). They classified the same config and the same diff, so the contract must change the WIRE and never the verdict.", field, got1, got2)
		}
	}

	if _, leaked := v1Payload["contract_version"]; leaked {
		t.Errorf("the v1 payload carries contract_version — that field is BuildV2's")
	}
	if got := string(v2Payload["contract_version"]); got != "2" {
		t.Errorf("the v2 envelope declares contract_version %s, want 2 — emit()'s ContractV2 arm calling EmitV1 produces a wrapper around the wrong envelope, or no wrapper at all", got)
	}
	if wrapper.ResponseVersion != responseVersion {
		t.Errorf("response_version = %d, want %d", wrapper.ResponseVersion, responseVersion)
	}
	if want := bed.sha256Of(t, bed.cfgPath); wrapper.ComputedConfigSHA256 != want {
		t.Errorf("computed_config_sha256 = %q, want %q", wrapper.ComputedConfigSHA256, want)
	}
	if want := bed.sha256Of(t, bed.diffPath); wrapper.ComputedDiffSHA256 != want {
		t.Errorf("computed_diff_sha256 = %q, want %q", wrapper.ComputedDiffSHA256, want)
	}
	if wrapper.ComputedConfigSHA256 == wrapper.ComputedDiffSHA256 {
		t.Errorf("the config and the diff digest are the same string %q", wrapper.ComputedConfigSHA256)
	}

	if !bytes.Equal(v1Report.Stdout, v2Report.Stdout) {
		t.Errorf("the human report differs between contracts, but emit returns before the contract switch when asJSON is false.\ncontract 1:\n%s\ncontract 2:\n%s", v1Report.Stdout, v2Report.Stdout)
	}
	if bytes.Equal(v1JSON.Stdout, v2JSON.Stdout) {
		t.Errorf("-contract-version 1 and -contract-version 2 produced IDENTICAL stdout. The contract is not selecting the emitter.")
	}
	if bytes.Equal(v1Report.Stdout, v1JSON.Stdout) {
		t.Errorf("CONTROL: -json changed nothing under contract 1, so the report/report equality proves nothing")
	}
	if len(v1Report.Stdout) == 0 {
		t.Error("CONTROL: the report arms captured no bytes, so their equality is the equality of two empty strings")
	}
}

// TestSeal_Wiring_TheSidecarIsThisRunsWrapper: with -out, the same wrapper that
// went to stdout is also on disk. ONE RUN, BOTH OBSERVATIONS — comparing two
// runs would compare two clocks.
func TestSeal_Wiring_TheSidecarIsThisRunsWrapper(t *testing.T) {
	defer red(t)

	bed := newClassifyBed(t)
	got := bed.driveLive(t, "2", true, true)

	if got.ExitCode != exitOK {
		t.Fatalf("exit = %d, want %d:\n%s", got.ExitCode, exitOK, got.Stdout)
	}
	assertStdoutShape(t, "v2/json/out", shapeV2Wrapper, got.Stdout)
	assertArtifact(t, "v2/json/out run-state", ArtifactWritten, got.RunState)
	assertArtifact(t, "v2/json/out v2 sidecar", ArtifactWritten, got.V2Sidecar)
	if t.Failed() {
		t.Fatal("the mapping answers are the claim; the same-run equalities below are meaningless until they hold")
	}

	var fromStdout ResponseWrapper
	if err := json.Unmarshal(got.Stdout, &fromStdout); err != nil {
		t.Fatalf("stdout is not a ResponseWrapper: %v", err)
	}
	var sidecar V2Sidecar
	if err := json.Unmarshal(got.V2Sidecar.Bytes, &sidecar); err != nil {
		t.Fatalf("the sidecar is not a V2Sidecar: %v\n%s", err, got.V2Sidecar.Bytes)
	}
	if sidecar.SchemaVersion != v2SidecarSchemaVersion {
		t.Errorf("sidecar schema_version = %d, want %d", sidecar.SchemaVersion, v2SidecarSchemaVersion)
	}
	if sidecar.Response.ComputedConfigSHA256 != fromStdout.ComputedConfigSHA256 {
		t.Errorf("config digest: sidecar %q, stdout %q", sidecar.Response.ComputedConfigSHA256, fromStdout.ComputedConfigSHA256)
	}
	if sidecar.Response.ComputedDiffSHA256 != fromStdout.ComputedDiffSHA256 {
		t.Errorf("diff digest: sidecar %q, stdout %q", sidecar.Response.ComputedDiffSHA256, fromStdout.ComputedDiffSHA256)
	}
	if compactJSON(t, "the sidecar payload", sidecar.Response.Classification) != compactJSON(t, "the stdout payload", fromStdout.Classification) {
		t.Errorf("the sidecar and stdout carry DIFFERENT classification payloads in the same run")
	}
	if fromStdout.ComputedConfigSHA256 == "" || fromStdout.ComputedDiffSHA256 == "" || len(fromStdout.Classification) == 0 {
		t.Errorf("CONTROL: the wrapper compared above is empty, so the equalities compared nothing")
	}

	var runState RunState
	if err := json.Unmarshal(got.RunState.Bytes, &runState); err != nil {
		t.Fatalf("the run-state is not a RunState: %v\n%s", err, got.RunState.Bytes)
	}
	if runState.SchemaVersion != schemaVersion {
		t.Errorf("run-state schema_version = %d, want %d — the shared run-state stays v1 under BOTH contracts", runState.SchemaVersion, schemaVersion)
	}
	if hasKey(t, got.RunState.Bytes, "response") {
		t.Errorf("the run-state carries a \"response\" key, so the v2 envelope landed in the shared file")
	}
	if got.V2Sidecar.Path == got.RunState.Path {
		t.Errorf("the sidecar and the run-state are the SAME path %q", got.RunState.Path)
	} else if got.V2Sidecar.Path != got.RunState.Path+v2SidecarSuffix {
		t.Errorf("the sidecar path %q is not the run-state path %q with %q appended", got.V2Sidecar.Path, got.RunState.Path, v2SidecarSuffix)
	}
}

// TestSeal_Wiring_RejectedContractIsValidatedFirst: the contract is validated
// BEFORE resolveConfigPath and before any input is read. A worktree with no
// config table and no -config still exits 3 naming the CONTRACT, not the
// missing table. Seeded -out artifacts survive byte-identical.
//
// CONTROL, judged in the same call: the same argv at contract 2 against a
// worktree that HAS a config writes those files, so "untouched" is a decision
// rather than an accident of an unwritable fixture.
func TestSeal_Wiring_RejectedContractIsValidatedFirst(t *testing.T) {
	defer red(t)

	rejectDir := t.TempDir()
	diffPath := filepath.Join(rejectDir, "change.diff")
	if err := os.WriteFile(diffPath, []byte(diffFor(walletPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(rejectDir, "run-state.json")
	seededState := []byte(`{"schema_version": 1, "status": "seeded-by-the-seal"}` + "\n")
	if err := os.WriteFile(outPath, seededState, 0o600); err != nil {
		t.Fatal(err)
	}
	sidecarPath := V2SidecarPath(outPath)
	seededSidecar := []byte(`{"schema_version": 1, "response": {"seeded": "by the seal"}}` + "\n")
	if err := os.WriteFile(sidecarPath, seededSidecar, 0o600); err != nil {
		t.Fatal(err)
	}

	var code int
	stdout := stdoutOf(t, func() {
		code = run(options{
			worktree:        rejectDir,
			base:            "origin/main",
			out:             outPath,
			json:            true,
			noGit:           true,
			contractVersion: "3",
			args:            []string{diffPath},
		})
	})

	if code != exitInvalid {
		t.Errorf("-contract-version 3 against a worktree with no config table exited %d, want %d", code, exitInvalid)
	}
	if !strings.Contains(stdout, "=== CLASSIFY: INVALID_INPUT ===") {
		t.Errorf("stdout is not the INVALID_INPUT report:\n%s", stdout)
	}
	if !strings.Contains(stdout, `"3"`) {
		t.Errorf("the report does not name the value it rejected:\n%s", stdout)
	}
	if !strings.Contains(stdout, "1") || !strings.Contains(stdout, "2") {
		t.Errorf("the report does not name the accepted set {1,2}:\n%s", stdout)
	}
	if !strings.Contains(stdout, "-"+flagContractVersion) {
		t.Errorf("the report never names -%s:\n%s", flagContractVersion, stdout)
	}
	for _, wrong := range []string{"no risk-paths config found", "no rule table", "diff is empty", "risk-paths.json"} {
		if strings.Contains(stdout, wrong) {
			t.Errorf("the report mentions %q — the contract is validated before resolveConfigPath, so a rejected contract cannot have an opinion about the config or the diff:\n%s", wrong, stdout)
		}
	}
	if json.Valid(bytes.TrimSpace([]byte(stdout))) {
		t.Errorf("an exit-3 run emitted a parseable JSON document on stdout:\n%s", stdout)
	}

	gotState, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("run-state disappeared: %v", err)
	}
	if !bytes.Equal(gotState, seededState) {
		t.Errorf("the run-state was rewritten by an invocation that exited %d", code)
	}
	gotSidecar, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("sidecar disappeared: %v", err)
	}
	if !bytes.Equal(gotSidecar, seededSidecar) {
		t.Errorf("the v2 sidecar did not survive byte-identical")
	}

	control := newClassifyBed(t)
	control.seedRunState(t)
	control.seedSidecar(t)
	accepted := control.driveLive(t, "2", true, true)
	if accepted.ExitCode != exitOK {
		t.Fatalf("CONTROL: -contract-version 2 over a worktree with a config exited %d, want %d:\n%s", accepted.ExitCode, exitOK, accepted.Stdout)
	}
	assertArtifact(t, "CONTROL run-state", ArtifactWritten, accepted.RunState)
	assertArtifact(t, "CONTROL v2 sidecar", ArtifactWritten, accepted.V2Sidecar)
}

// TestSeal_Wiring_TwoBedsAreNotByteIdentical is a self-measurement: two live
// v1 JSON runs on two beds produce different bytes (classified_at, Worktree
// in reviewer_args) but the same stdout SHAPE. A seal that required
// bytes.Equal between beds would be unsatisfiable; this row fails the suite
// if that ever stops being true, rather than blaming a body.
func TestSeal_Wiring_TwoBedsAreNotByteIdentical(t *testing.T) {
	defer red(t)

	a := newClassifyBed(t).driveLive(t, "1", true, false)
	b := newClassifyBed(t).driveLive(t, "1", true, false)
	if a.ExitCode != exitOK || b.ExitCode != exitOK {
		t.Fatalf("both arms must succeed (got %d and %d)", a.ExitCode, b.ExitCode)
	}
	assertStdoutShape(t, "bed-a", shapeV1Payload, a.Stdout)
	assertStdoutShape(t, "bed-b", shapeV1Payload, b.Stdout)
	if bytes.Equal(a.Stdout, b.Stdout) {
		t.Fatal("two live v1 JSON runs on two TempDirs produced identical stdout. classified_at and reviewer_args -cwd embed the directory and the clock; if they no longer do, the RunWiring comparison may start requiring bytes.Equal — do not, judge SHAPE")
	}
}

// ─── the declared spine ──────────────────────────────────────────────────────

// TestSeal_Wiring_RunWiringHonoursTheMapping drives every row through the
// function wiring.go declares to be the unit's subject.
//
// RED TODAY, BY NAME: RunWiring is GO-1-1's stub. When the body lands, each
// row is judged on the bed it owns — exit, stdout shape, artifact state —
// never by byte-comparing stdout to a second invocation. ArtifactStateUnset
// is illegal beside a nil error and is not rewritten from the filesystem:
// the body must return every applicable artifact's state, path, and bytes.
func TestSeal_Wiring_RunWiringHonoursTheMapping(t *testing.T) {
	for _, row := range wiringRows() {
		row := row
		t.Run(row.name, func(t *testing.T) {
			defer red(t)
			bed := newClassifyBed(t)
			bed.applySeeds(t, row)

			inv := Invocation{Args: row.argv(bed), Dir: bed.dir}
			sidecarPath := V2SidecarPath(bed.outPath)
			beforeRun := snap(t, bed.outPath)
			beforeSidecar := snap(t, sidecarPath)

			got, err := RunWiring(inv)
			if err != nil {
				if errors.Is(err, ErrWiringNotImplemented) {
					t.Errorf("RED, and this is the obligation: RunWiring is still GO-1-1's stub, so the mapping (%s) has no implementation to judge. GO-1-3 owes a body that answers exit %d, %s on stdout, run-state %s and sidecar %s for argv %q.",
						row.name, row.wantExit, row.wantStdout, row.wantRunState, row.wantSidecar, inv.Args)
					return
				}
				t.Fatalf("RunWiring could not run the invocation: %v. Clause 5 reserves the error return for RunWiring's own failure; a classify run that fails is a non-zero ExitCode with a NIL error.", err)
			}

			afterRun := snap(t, bed.outPath)
			afterSidecar := snap(t, sidecarPath)
			observedRun := classifyArtifact(bed.outPath, row.withOut, beforeRun, afterRun)
			observedSidecar := classifyArtifact(sidecarPath, row.withOut, beforeSidecar, afterSidecar)

			assertMapping(t, row, bed, got)
			assertReportedMatchesBed(t, row.name+" run-state", got.RunState, observedRun)
			assertReportedMatchesBed(t, row.name+" v2 sidecar", got.V2Sidecar, observedSidecar)

			if row.withOut && row.wantExit == exitOK {
				if len(got.Stderr) == 0 {
					t.Errorf("Stderr is empty for an invocation that reports where it wrote (%q). Clause 3 makes capture a requirement on the body.", bed.outPath)
				} else if !bytes.Contains(got.Stderr, []byte(bed.outPath)) {
					t.Errorf("Stderr does not name the run-state it wrote:\n%s", got.Stderr)
				}
			}
			if row.seedRunState && row.wantRunState == ArtifactStale {
				after := snap(t, bed.outPath)
				if !after.exists || !bytes.Equal(after.bytes, beforeRun.bytes) {
					t.Errorf("%s: a rejected contract rewrote the seeded run-state", row.name)
				}
			}
			if row.seedSidecar && row.wantSidecar == ArtifactStale {
				after := snap(t, sidecarPath)
				if !after.exists || !bytes.Equal(after.bytes, beforeSidecar.bytes) {
					t.Errorf("%s: a rejected contract rewrote the seeded sidecar", row.name)
				}
			}
		})
	}
}

// TestSeal_Wiring_RunWiringDispatchesSubcommands seals RunWiring clause 6:
// "init", "capabilities", "help", "-h" and "--help" as Args[0] take the
// pre-flag-parse branch. The capabilities probe's ordering is part of the
// mapping: `capabilities -not-a-real-flag` must reach the probe (exit 3,
// "takes no arguments") rather than flag-parse (exit 2).
//
// RED TODAY on the stub. Artifacts about init confine themselves to ExitCode
// and the streams; the scaffold file is observed directly.
func TestSeal_Wiring_RunWiringDispatchesSubcommands(t *testing.T) {
	dir := t.TempDir()

	t.Run("capabilities", func(t *testing.T) {
		defer red(t)
		got, err := RunWiring(Invocation{Args: []string{probeSubcommand}, Dir: dir})
		if stubRED(t, err, "RunWiring(capabilities) must write the probe JSON and exit 0 or %d, never fall through to the classify path", exitCapabilityIncomplete) {
			return
		}
		if got.ExitCode != exitOK && got.ExitCode != ExitCode(exitCapabilityIncomplete) {
			t.Errorf("capabilities exited %d, want 0 or %d", got.ExitCode, exitCapabilityIncomplete)
		}
		if !bytes.Contains(got.Stdout, []byte(`"cmd/classify"`)) || !bytes.Contains(got.Stdout, []byte(`"probe_version"`)) {
			t.Errorf("capabilities stdout is not the probe report:\n%s", got.Stdout)
		}
		if bytes.Contains(got.Stdout, []byte("=== CLASSIFY: INVALID_INPUT ===")) {
			t.Errorf("capabilities fell through to the classify path")
		}
	})

	t.Run("capabilities-ahead-of-flag-parse", func(t *testing.T) {
		defer red(t)
		got, err := RunWiring(Invocation{Args: []string{probeSubcommand, "-not-a-real-flag"}, Dir: dir})
		if stubRED(t, err, "capabilities with a stray flag must dispatch to the probe (exit %d, takes no arguments), not flag-parse (exit %d)", exitInvalid, exitFlagError) {
			return
		}
		if got.ExitCode == ExitCode(exitFlagError) {
			t.Errorf("capabilities -not-a-real-flag exited %d (flag error). Clause 6: the probe dispatches ahead of flag parsing, so this is extra argv to the probe (exit %d), not an unknown flag.", exitFlagError, exitInvalid)
		}
		if got.ExitCode != exitInvalid {
			t.Errorf("capabilities -not-a-real-flag exited %d, want %d", got.ExitCode, exitInvalid)
		}
		combined := string(got.Stdout) + string(got.Stderr)
		if !strings.Contains(combined, "takes no arguments") {
			t.Errorf("the probe's extra-argv refusal is missing; streams were stdout=%q stderr=%q", got.Stdout, got.Stderr)
		}
		if len(got.Stdout) != 0 && json.Valid(bytes.TrimSpace(got.Stdout)) {
			t.Errorf("stray argv still produced a probe report on stdout:\n%s", got.Stdout)
		}
	})

	for _, help := range []string{"help", "-h", "--help"} {
		help := help
		t.Run("help/"+help, func(t *testing.T) {
			defer red(t)
			got, err := RunWiring(Invocation{Args: []string{help}, Dir: dir})
			if stubRED(t, err, "RunWiring(%s) must print usage and exit 0", help) {
				return
			}
			if got.ExitCode != exitOK {
				t.Errorf("%s exited %d, want 0", help, got.ExitCode)
			}
			combined := string(got.Stdout) + string(got.Stderr)
			if !strings.Contains(combined, "classify") {
				t.Errorf("%s did not print usage:\n%s", help, combined)
			}
		})
	}

	t.Run("init", func(t *testing.T) {
		defer red(t)
		out := filepath.Join(dir, "risk-paths.json")
		got, err := RunWiring(Invocation{Args: []string{"init", "-worktree", dir, "-out", out}, Dir: dir})
		if stubRED(t, err, "RunWiring(init) must scaffold a rule table, exit 0, and confine Artifacts to ExitCode and the streams") {
			return
		}
		if got.ExitCode != exitOK {
			t.Errorf("init exited %d, want 0", got.ExitCode)
		}
		if got.RunState.State != ArtifactNotApplicable {
			t.Errorf("init asserted a run-state artifact (%s); Artifacts about init must confine themselves to ExitCode and the streams — ArtifactNotApplicable exactly, not Unset", got.RunState.State)
		}
		if got.V2Sidecar.State != ArtifactNotApplicable {
			t.Errorf("init asserted a v2 sidecar artifact (%s); Artifacts about init must confine themselves to ExitCode and the streams — ArtifactNotApplicable exactly, not Unset", got.V2Sidecar.State)
		}
		if _, err := os.Stat(out); err != nil {
			t.Errorf("init did not write the scaffold at %s: %v", out, err)
		}
		if !bytes.Contains(got.Stdout, []byte("=== CLASSIFY INIT ===")) && !bytes.Contains(got.Stderr, []byte("=== CLASSIFY INIT ===")) {
			t.Errorf("init did not print its report; stdout=%q stderr=%q", got.Stdout, got.Stderr)
		}
	})
}

func stubRED(t *testing.T, err error, format string, args ...any) bool {
	t.Helper()
	if err == nil {
		return false
	}
	if errors.Is(err, ErrWiringNotImplemented) {
		t.Errorf("RED, and this is the obligation: "+format, args...)
		return true
	}
	t.Fatalf("RunWiring could not run the invocation: %v", err)
	return true
}

// TestSeal_Wiring_RunWiringMapsFlagParseErrors seals parseInvocationFlags
// clause 3's assignment: "RunWiring maps that error to exitFlagError — the
// mapping is decided here, by the skeleton, and GO-1-2 seals it."
//
// The three argv shapes are the ones the flag table already measured against
// the reference FlagSet. RED today on the stub. A body that returns
// exitInternal (or any other DeclaredExitCodes member) for fs.Parse failure
// must redden here — membership in the closed set is not the mapping.
func TestSeal_Wiring_RunWiringMapsFlagParseErrors(t *testing.T) {
	bed := newClassifyBed(t)
	diff := bed.diffPath
	base := []string{"-no-git", "-worktree", bed.dir, "-config", bed.cfgPath}

	cases := []struct {
		name string
		args []string
	}{
		{
			name: "undefined-flag",
			args: append(append([]string{}, base...), "-not-a-real-flag", diff),
		},
		{
			name: "missing-flag-value",
			args: append(append([]string{}, base...), "-"+flagContractVersion),
		},
		{
			name: "malformed-bool",
			args: append(append([]string{}, base...), "-json=maybe", diff),
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			defer red(t)

			ref := referenceFlagSet()
			refErr := ref.Parse(c.args)
			if refErr == nil {
				t.Fatalf("CONTROL: the reference FlagSet parsed argv %q cleanly; this row is mis-measured", c.args)
			}

			got, err := RunWiring(Invocation{Args: c.args, Dir: bed.dir})
			if stubRED(t, err, "RunWiring must map argv %q to exit %d with the parse error on Stderr and no classification on Stdout. Reference FlagSet.Parse: %v", c.args, exitFlagError, refErr) {
				return
			}
			if got.ExitCode != ExitCode(exitFlagError) {
				t.Errorf("exit = %d, want %d (exitFlagError). argv-does-not-parse is this mapping, not exitInternal — a caller that distinguishes a bad argv from classify breaking would silently get the wrong code.", got.ExitCode, exitFlagError)
			}
			if !inDeclaredExitCodes(int(got.ExitCode)) {
				t.Errorf("exit %d is not in DeclaredExitCodes %v", got.ExitCode, DeclaredExitCodes)
			}
			if !bytes.Contains(got.Stderr, []byte(refErr.Error())) {
				t.Errorf("Stderr does not carry the FlagSet.Parse message %q:\n%s", refErr.Error(), got.Stderr)
			}
			if bytes.Contains(got.Stdout, []byte("=== CLASSIFICATION ===")) {
				t.Errorf("a flag-parse failure printed the human classification on stdout:\n%s", got.Stdout)
			}
			if bytes.Contains(got.Stdout, []byte("=== CLASSIFY: INVALID_INPUT ===")) {
				t.Errorf("a flag-parse failure reported INVALID_INPUT on stdout (that is exit %d, not exit %d):\n%s", exitInvalid, exitFlagError, got.Stdout)
			}
			if len(bytes.TrimSpace(got.Stdout)) > 0 && json.Valid(bytes.TrimSpace(got.Stdout)) {
				t.Errorf("a flag-parse failure emitted JSON classification on stdout:\n%s", got.Stdout)
			}
		})
	}
}

// TestSeal_Wiring_InitStillScaffolds is the live control for the init
// subcommand: cmdInit is what main dispatches to today, and GO-1-3 must MOVE
// that arm into RunWiring rather than delete it. A body that drops init
// leaves usage() advertising a command that no longer works.
func TestSeal_Wiring_InitStillScaffolds(t *testing.T) {
	defer red(t)

	dir := t.TempDir()
	out := filepath.Join(dir, "risk-paths.json")
	var code int
	stdout := stdoutOf(t, func() { code = cmdInit([]string{"-worktree", dir, "-out", out}) })
	if code != exitOK {
		t.Fatalf("cmdInit exited %d, want 0\n%s", code, stdout)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("init did not write %s: %v", out, err)
	}
	if !bytes.Contains(data, []byte(`"scaffold"`)) {
		t.Errorf("the scaffold config does not carry the scaffold marker:\n%s", data)
	}
	if !strings.Contains(stdout, "=== CLASSIFY INIT ===") {
		t.Errorf("init did not print its report:\n%s", stdout)
	}

	var again int
	stdoutOf(t, func() { again = cmdInit([]string{"-worktree", dir, "-out", out}) })
	if again != exitInvalid {
		t.Errorf("CONTROL: a second init without -force exited %d, want %d — the refusal to overwrite is the safety property", again, exitInvalid)
	}
}

// ─── the flag half ───────────────────────────────────────────────────────────

type sentinelRegistrar struct{}

const sentinelContractDefault = "sentinel-not-a-contract"

func (sentinelRegistrar) RegisterContractVersionFlag(fs *flag.FlagSet) *string {
	return fs.String(flagContractVersion, sentinelContractDefault, "seal sentinel")
}

func newContinueFS() *flag.FlagSet {
	fs := flag.NewFlagSet("classify", flag.ContinueOnError)
	fs.SetOutput(nopWriter{})
	return fs
}

func referenceFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("classify-reference", flag.ContinueOnError)
	fs.SetOutput(nopWriter{})
	fs.String("config", "", "")
	fs.String("worktree", ".", "")
	fs.String("base", "origin/main", "")
	fs.String("task", "", "")
	fs.String("out", "", "")
	fs.Bool("json", false, "")
	fs.Bool("no-git", false, "")
	registerContractVersionFlag(fs)
	return fs
}

func errPhrase(wantErr bool) string {
	if wantErr {
		return "returns a parse error"
	}
	return "parses cleanly"
}

// TestSeal_Wiring_ParseInvocationFlagsMapsArgvToOptions seals the flag half:
// argv in, options out, a parse failure RETURNED rather than fatal.
//
// RED TODAY: parseInvocationFlags is GO-1-1's stub. The well-formed row
// asserts every declared flag — worktree, base, task, out, config, json,
// no-git, contract-version — so a body that binds a name to a discarded
// destination cannot pass. The table measures itself against a reference
// FlagSet first, so a mis-measured argv is a defect in the seal.
func TestSeal_Wiring_ParseInvocationFlagsMapsArgvToOptions(t *testing.T) {
	const diff = "/tmp/seal.diff"

	cases := []struct {
		name        string
		args        []string
		wantErr     bool
		wantResidue []string
		measured    string
		check       func(t *testing.T, opts options)
	}{
		{
			name: "every-declared-flag",
			args: []string{
				"-no-git", "-json",
				"-worktree", "WT", "-base", "BASE", "-task", "TASK",
				"-out", "OUT", "-config", "C",
				"-" + flagContractVersion, "2",
				diff,
			},
			wantErr:     false,
			wantResidue: []string{diff},
			measured:    `err=<nil>, Args()=["/tmp/seal.diff"]`,
			check: func(t *testing.T, opts options) {
				if !opts.json {
					t.Error("-json did not reach options.json")
				}
				if !opts.noGit {
					t.Error("-no-git did not reach options.noGit")
				}
				if opts.worktree != "WT" {
					t.Errorf("options.worktree = %q, want %q", opts.worktree, "WT")
				}
				if opts.base != "BASE" {
					t.Errorf("options.base = %q, want %q", opts.base, "BASE")
				}
				if opts.task != "TASK" {
					t.Errorf("options.task = %q, want %q", opts.task, "TASK")
				}
				if opts.out != "OUT" {
					t.Errorf("options.out = %q, want %q", opts.out, "OUT")
				}
				if opts.configPath != "C" {
					t.Errorf("options.configPath = %q, want %q", opts.configPath, "C")
				}
				if opts.contractVersion != "2" {
					t.Errorf("options.contractVersion = %q, want %q — clause 2: the RAW string is threaded through and validated in run()", opts.contractVersion, "2")
				}
				if !sameStrings(opts.args, []string{diff}) {
					t.Errorf("options.args = %v, want %v", opts.args, []string{diff})
				}
			},
		},
		{
			name:        "absent-contract-flag-is-the-default",
			args:        []string{"-no-git", diff},
			wantErr:     false,
			wantResidue: []string{diff},
			measured:    `err=<nil>; the registrar defaults -contract-version to defaultContractVersion.String()`,
			check: func(t *testing.T, opts options) {
				if opts.contractVersion != defaultContractVersion.String() {
					t.Errorf("options.contractVersion = %q, want the registered default %q", opts.contractVersion, defaultContractVersion.String())
				}
			},
		},
		{
			name:        "an unaccepted contract is NOT a flag error",
			args:        []string{"-no-git", "-" + flagContractVersion, "3", diff},
			wantErr:     false,
			wantResidue: []string{diff},
			measured:    `err=<nil>: the flag package accepts any string for a string flag`,
			check: func(t *testing.T, opts options) {
				if opts.contractVersion != "3" {
					t.Errorf("options.contractVersion = %q, want %q: -%s is NOT parsed here. The rejection is run()'s, at exit %d, and rejecting it here would collapse it into exit %d.",
						opts.contractVersion, "3", flagContractVersion, exitInvalid, exitFlagError)
				}
			},
		},
		{
			name:        "a flag after the positional is not a flag",
			args:        []string{"-no-git", diff, "-json"},
			wantErr:     false,
			wantResidue: []string{diff, "-json"},
			measured:    `err=<nil>, Args()=["/tmp/seal.diff" "-json"]: Parse stops at the first non-flag argument`,
			check: func(t *testing.T, opts options) {
				if opts.json {
					t.Errorf("-json AFTER the positional set options.json")
				}
				if !sameStrings(opts.args, []string{diff, "-json"}) {
					t.Errorf("options.args = %v, want %v", opts.args, []string{diff, "-json"})
				}
			},
		},
		{
			name:     "a missing flag value IS a returned error",
			args:     []string{"-no-git", "-worktree", "D", "-config", "C", "-" + flagContractVersion},
			wantErr:  true,
			measured: `err="flag needs an argument: -contract-version"`,
		},
		{
			name:     "an undefined flag IS a returned error",
			args:     []string{"-no-git", "-not-a-real-flag", diff},
			wantErr:  true,
			measured: `err="flag provided but not defined: -not-a-real-flag"`,
		},
		{
			name:     "a malformed bool value IS a returned error",
			args:     []string{"-json=maybe", diff},
			wantErr:  true,
			measured: `err="invalid boolean value \"maybe\" for -json: parse error"`,
		},
	}

	t.Run("the measurements this table is built on", func(t *testing.T) {
		defer red(t)
		for _, c := range cases {
			c := c
			t.Run(c.name, func(t *testing.T) {
				defer red(t)
				ref := referenceFlagSet()
				err := ref.Parse(c.args)
				if c.wantErr && err == nil {
					t.Fatalf("this row demands a parse error from argv %q, but the reference FlagSet parsed it cleanly, leaving Args()=%q. Written measurement: %s", c.args, ref.Args(), c.measured)
				}
				if !c.wantErr && err != nil {
					t.Fatalf("this row demands a clean parse of argv %q, but the reference FlagSet returned %v. Written measurement: %s", c.args, err, c.measured)
				}
				if c.wantErr {
					return
				}
				if !sameStrings(ref.Args(), c.wantResidue) {
					t.Errorf("the stdlib leaves Args()=%q for argv %q, but the row was written against %q", ref.Args(), c.args, c.wantResidue)
				}
			})
		}
		ref := referenceFlagSet()
		for _, name := range []string{"config", "worktree", "base", "task", "out", "json", "no-git", flagContractVersion} {
			if ref.Lookup(name) == nil {
				t.Errorf("CONTROL: the reference FlagSet does not carry -%s; it has %v", name, flagNames(ref))
			}
		}
	})

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			defer red(t)
			fs := newContinueFS()
			opts, err := parseInvocationFlags(fs, c.args)
			if errors.Is(err, ErrWiringNotImplemented) {
				t.Errorf("RED, and this is the obligation: parseInvocationFlags is still GO-1-1's stub. GO-1-3 owes a body for which argv %q %s. Measured: %s.",
					c.args, errPhrase(c.wantErr), c.measured)
				return
			}
			if c.wantErr {
				if err == nil {
					t.Fatalf("argv %q parsed without error. MEASURED: %s. Clause 3 says the caller's fs.Parse error comes back and RunWiring maps it to exit %d.",
						c.args, c.measured, exitFlagError)
				}
				ref := referenceFlagSet()
				refErr := ref.Parse(c.args)
				if refErr == nil {
					t.Fatalf("CONTROL: the reference FlagSet parsed argv %q cleanly; this row is mis-measured", c.args)
				}
				if !flagParseErrorHonoursReference(err, refErr) {
					t.Errorf("argv %q returned %v; want the caller's fs.Parse error (reference: %v). An arbitrary internal error, or an unrelated wrapped error, is not a flag-parse failure.",
						c.args, err, refErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("argv %q failed with %v, but MEASURED: %s", c.args, err, c.measured)
			}
			c.check(t, opts)
		})
	}

	t.Run("clause 1/the flag is registered through the registry", func(t *testing.T) {
		defer red(t)
		withHooks(t, sentinelRegistrar{}, digestSource, framedStdinReader)
		fs := newContinueFS()
		if _, err := parseInvocationFlags(fs, []string{"-no-git"}); err != nil {
			if errors.Is(err, ErrWiringNotImplemented) {
				t.Errorf("RED: parseInvocationFlags is still a stub, so it registered nothing. GO-1-3 owes a body that registers -%s through registerContractVersionFlag; with a sentinel registrar installed the registered default must be %q.", flagContractVersion, sentinelContractDefault)
				return
			}
			t.Fatalf("parsing a well-formed argv failed: %v", err)
		}
		f := fs.Lookup(flagContractVersion)
		if f == nil {
			t.Fatalf("no -%s was registered; registered flags: %v", flagContractVersion, flagNames(fs))
		}
		if f.DefValue != sentinelContractDefault {
			t.Errorf("-%s registered with default %q, want the installed registrar's %q", flagContractVersion, f.DefValue, sentinelContractDefault)
		}
	})

	t.Run("clause 4/the logger is untouched", func(t *testing.T) {
		defer red(t)
		oldFlags, oldPrefix := log.Flags(), log.Prefix()
		t.Cleanup(func() { log.SetFlags(oldFlags); log.SetPrefix(oldPrefix) })
		const sentinelPrefix = "seal-sentinel: "
		log.SetFlags(log.Lshortfile)
		log.SetPrefix(sentinelPrefix)
		fs := newContinueFS()
		_, err := parseInvocationFlags(fs, []string{"-no-git", "-json", "/tmp/seal.diff"})
		if err != nil && !errors.Is(err, ErrWiringNotImplemented) {
			t.Fatalf("parsing a well-formed argv failed: %v", err)
		}
		if log.Flags() != log.Lshortfile || log.Prefix() != sentinelPrefix {
			t.Errorf("parseInvocationFlags changed the process-wide logger (flags %d, prefix %q). Clause 4: logger state belongs to main().", log.Flags(), log.Prefix())
		}
	})
}

// TestSeal_Wiring_NilRegistrarIsANamedState seals D2's third state, which is
// LIVE today. When the registrar is nil the function registers NO flag and
// returns defaultContractVersion ("1"). Passing -contract-version then fails
// inside the flag package, not at exit 3.
func TestSeal_Wiring_NilRegistrarIsANamedState(t *testing.T) {
	defer red(t)

	saved := contractFlagRegistrar
	contractFlagRegistrar = nil
	t.Cleanup(func() { contractFlagRegistrar = saved })

	fs := newContinueFS()
	got := registerContractVersionFlag(fs)
	if got == nil {
		t.Fatal("registerContractVersionFlag returned a nil pointer")
	}
	if *got != defaultContractVersion.String() {
		t.Errorf("with a nil registrar the fallback is %q, want %q", *got, defaultContractVersion.String())
	}
	if f := fs.Lookup(flagContractVersion); f != nil {
		t.Errorf("a nil registrar still registered -%s (default %q)", flagContractVersion, f.DefValue)
	}
	if err := fs.Parse([]string{"-" + flagContractVersion, "2"}); err == nil {
		t.Errorf("parsing -%s 2 succeeded against a FlagSet that must not carry the flag", flagContractVersion)
	} else if !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Errorf("parsing -%s 2 failed with %v, want the flag package's 'provided but not defined'", flagContractVersion, err)
	}

	contractFlagRegistrar = saved
	if contractFlagRegistrar == nil {
		t.Fatal("CONTROL: the installed registrar is nil, so the nil-registrar arm distinguished nothing")
	}
	live := newContinueFS()
	liveGot := registerContractVersionFlag(live)
	if liveGot == nil {
		t.Fatal("CONTROL: the real registrar returned a nil pointer")
	}
	f := live.Lookup(flagContractVersion)
	if f == nil {
		t.Fatalf("CONTROL: the real registrar registered no -%s", flagContractVersion)
	}
	if f.DefValue != defaultContractVersion.String() {
		t.Errorf("CONTROL: the real registrar's default is %q, want %q", f.DefValue, defaultContractVersion.String())
	}
	if err := live.Parse([]string{"-" + flagContractVersion, "2"}); err != nil {
		t.Fatalf("CONTROL: parsing -%s 2 against the real registrar failed: %v", flagContractVersion, err)
	}
	if *liveGot != "2" {
		t.Errorf("CONTROL: after parsing -%s 2 the destination holds %q", flagContractVersion, *liveGot)
	}
}

// ─── main(), sealed structurally ─────────────────────────────────────────────

// TestSeal_Wiring_MainForwardsTheResult is the row main.go's own comment
// asks for. main cannot be driven in process, so it is sealed STRUCTURALLY.
//
// Clause 8, not merely "and nothing else": main writes Artifacts.Stdout to
// os.Stdout and Artifacts.Stderr to os.Stderr, os.Exit takes ExitCode, and a
// non-nil error becomes exitInternal. Identifier presence is not enough —
// a body that mentions os.Stdout in a dead assignment, writes unrelated
// bytes, and exits through an unrelated nonliteral still fails the data-flow
// walk. A body of `art, _ := RunWiring(inv); os.Exit(0)` fails every one of
// those.
//
// Subcommand dispatch is required to leave main (clause 6), detected as the
// string cases "init" / "capabilities" / "help" / "-h" / "--help" — not as
// "any switch", which would keep a legal `switch err` red after GO-1-3.
func TestSeal_Wiring_MainForwardsTheResult(t *testing.T) {
	defer red(t)

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go") && !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing this package's sources: %v", err)
	}
	pkg, ok := pkgs["main"]
	if !ok {
		t.Fatalf("package main was not found among %v", keysOf(pkgs))
	}

	funcs := map[string]*ast.FuncDecl{}
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			if fn, isFn := d.(*ast.FuncDecl); isFn && fn.Recv == nil && fn.Name != nil {
				funcs[fn.Name.Name] = fn
			}
		}
	}
	for _, must := range []string{"main", "run", "emit", "persist", "RunWiring", "parseInvocationFlags", "usage"} {
		if funcs[must] == nil {
			t.Fatalf("CONTROL: the source scan did not find func %s. Every absence this row reports is unreliable until the scan is shown to find what is there.", must)
		}
	}
	if n := len(pkg.Files); n < 5 {
		t.Fatalf("CONTROL: the scan parsed %d files; this package has more", n)
	}

	if countNodes(funcs["emit"], isSelectorIdent("os", "Stdout")) == 0 {
		t.Fatal("CONTROL: the scan found no os.Stdout in emit, which writes through os.Stdout today. The selector detector is blind.")
	}
	if countNodes(funcs["usage"], isSelectorIdent("os", "Stderr")) == 0 {
		t.Fatal("CONTROL: the scan found no os.Stderr in usage, which Fprints to it. The selector detector is blind.")
	}
	if countCalls(funcs["main"], isSelectorCall("os", "Exit")) == 0 {
		t.Fatal("CONTROL: the scan found no os.Exit in main, which is where the only one lives today.")
	}
	if countNodes(funcs["main"], isSelectorIdent("os", "Args")) == 0 {
		t.Fatal("CONTROL: the scan found no os.Args in main, which reads it today.")
	}

	mainFn := funcs["main"]

	if countCalls(mainFn, isIdentCall("RunWiring")) != 1 {
		t.Errorf("main() does not call RunWiring exactly once (found %d). Clause 1: RunWiring IS the code main() runs.", countCalls(mainFn, isIdentCall("RunWiring")))
	}
	if countNodes(mainFn, isSelectorIdent("os", "Args")) == 0 {
		t.Errorf("main() does not mention os.Args. Clause 8's forwarding starts from os.Args[1:] into RunWiring.")
	}
	if countNodes(mainFn, isSelectorIdent("os", "Stdout")) == 0 {
		t.Errorf("main() does not mention os.Stdout. Clause 8: it writes Artifacts.Stdout to os.Stdout.")
	}
	if countNodes(mainFn, isSelectorIdent("os", "Stderr")) == 0 {
		t.Errorf("main() does not mention os.Stderr. Clause 8: it writes Artifacts.Stderr to os.Stderr, and reports a non-nil error there.")
	}
	if countNodes(mainFn, isIdent("exitInternal")) == 0 {
		t.Errorf("main() does not mention exitInternal. Clause 8: a non-nil error from RunWiring is reported on os.Stderr and exits exitInternal — discarding the error and os.Exit(0) is the escape this row exists to close.")
	}
	if countNodes(mainFn, isSelectorField("ExitCode")) == 0 {
		t.Errorf("main() does not mention ExitCode. Clause 8: os.Exit takes Artifacts.ExitCode, not a literal.")
	}
	if n := countCalls(mainFn, isSelectorCall("os", "Exit")); n != 1 {
		t.Errorf("main() makes %d os.Exit calls, want exactly 1. Extra ones are the subcommand arms clause 6 moves inside RunWiring.", n)
	}
	if n := countCalls(mainFn, osExitLiteral); n != 0 {
		t.Errorf("main() calls os.Exit with a literal (%d time(s)). Clause 8: the argument is Artifacts.ExitCode or exitInternal, never a constant that would let a silent binary exit 0.", n)
	}

	for _, p := range mainForwardingProblems(mainFn) {
		t.Errorf("clause 8 data flow: %s", p)
	}

	if cases := subcommandCases(mainFn); len(cases) != 0 {
		t.Errorf("main() still dispatches subcommands %v. Clause 6 moves that branch inside RunWiring, because the pre-flag-parse arms are part of the mapping under test.", cases)
	}

	for name, fn := range funcs {
		if name == "main" {
			continue
		}
		if n := countCalls(fn, isSelectorCall("os", "Exit")); n != 0 {
			t.Errorf("%s() calls os.Exit %d time(s). Clause 2: it never exits the process.", name, n)
		}
	}
	if funcs["parseFlags"] != nil {
		t.Errorf("parseFlags() still exists. Clause 1: GO-1-3 DELETES IT — keeping it as a thin wrapper over flag.CommandLine would leave the shipped binary parsing outside the seam every seal drives.")
	}
	for name, fn := range funcs {
		if n := countCalls(fn, isSelectorCall("os", "Chdir")); n != 0 {
			t.Errorf("%s() calls os.Chdir. Clause 7: paths resolve against inv.Dir.", name)
		}
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func isIdentCall(name string) func(ast.Node) bool {
	return func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return false
		}
		id, ok := call.Fun.(*ast.Ident)
		return ok && id.Name == name
	}
}

func isSelectorCall(pkg, fn string) func(ast.Node) bool {
	return func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != fn {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && id.Name == pkg
	}
}

func isSelectorIdent(pkg, name string) func(ast.Node) bool {
	return func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && id.Name == pkg
	}
}

func isIdent(name string) func(ast.Node) bool {
	return func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		return ok && id.Name == name
	}
}

func isSelectorField(name string) func(ast.Node) bool {
	return func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == name
	}
}

func osExitLiteral(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok || !isSelectorCall("os", "Exit")(n) {
		return false
	}
	if len(call.Args) != 1 {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	return ok && lit.Kind == token.INT
}

func subcommandCases(fn *ast.FuncDecl) []string {
	if fn == nil || fn.Body == nil {
		return nil
	}
	want := map[string]bool{"init": true, probeSubcommand: true, "help": true, "-h": true, "--help": true}
	var found []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range cc.List {
				switch e := expr.(type) {
				case *ast.BasicLit:
					if e.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(e.Value)
					if err != nil {
						continue
					}
					if want[v] {
						found = append(found, v)
					}
				case *ast.Ident:
					if e.Name == "probeSubcommand" {
						found = append(found, probeSubcommand)
					}
				}
			}
		}
		return true
	})
	return found
}

func countCalls(fn *ast.FuncDecl, match func(ast.Node) bool) int { return countNodes(fn, match) }

func countNodes(fn *ast.FuncDecl, match func(ast.Node) bool) int {
	if fn == nil || fn.Body == nil {
		return 0
	}
	n := 0
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		if node != nil && match(node) {
			n++
		}
		return true
	})
	return n
}

// ─── independent artifact observation (findings 1, 4, 6) ─────────────────────

func assertReportedMatchesBed(t *testing.T, label string, reported, observed FileArtifact) {
	t.Helper()
	for _, p := range reportedArtifactMismatches(reported, observed) {
		t.Errorf("%s: %s", label, p)
	}
}

// reportedArtifactMismatches is the second, independent assertion: the body's
// returned FileArtifact is compared against a filesystem snapshot. It never
// rewrites the return value. A zero-value report against a written bed is a
// mismatch, which is the defect the Unset backfill used to hide.
func reportedArtifactMismatches(reported, observed FileArtifact) []string {
	var out []string
	if reported.State == ArtifactStateUnset {
		out = append(out, "RunWiring left State at ArtifactStateUnset; Unset beside a nil error is illegal and this seal does not fill it in from the filesystem")
	}
	if reported.State != observed.State {
		out = append(out, fmt.Sprintf("RunWiring reported %s, but the bed observed %s (path %q). Clause 4: the snapshot checks a reported state against the bed; it is not a stand-in for the return value", reported.State, observed.State, observed.Path))
	}
	if reported.Path != observed.Path {
		out = append(out, fmt.Sprintf("reported path %q, bed observed %q", reported.Path, observed.Path))
	}
	if reported.State == ArtifactWritten || reported.State == ArtifactStale ||
		observed.State == ArtifactWritten || observed.State == ArtifactStale {
		if !bytes.Equal(reported.Bytes, observed.Bytes) {
			out = append(out, fmt.Sprintf("reported Bytes (len %d) do not equal the bytes on disk (len %d)", len(reported.Bytes), len(observed.Bytes)))
		}
	}
	return out
}

// TestSeal_Wiring_ReportedArtifactIsNotAFilesystemBackfill is GREEN today: it
// judges the matcher, not RunWiring. The defect the panel measured — a body
// that returns only ExitCode and Stdout, leaving RunState/V2Sidecar at Unset
// while the files on disk happen to match — must not satisfy the matcher.
func TestSeal_Wiring_ReportedArtifactIsNotAFilesystemBackfill(t *testing.T) {
	defer red(t)

	onDisk := []byte(`{"schema_version":1,"status":"written"}` + "\n")
	observed := FileArtifact{Path: "/tmp/run-state.json", State: ArtifactWritten, Bytes: onDisk}

	if problems := reportedArtifactMismatches(FileArtifact{}, observed); len(problems) == 0 {
		t.Fatal("CONTROL: a zero-value FileArtifact (ArtifactStateUnset) against a written bed must not match — that is the backfill this seal used to do")
	}
	if problems := reportedArtifactMismatches(observed, observed); len(problems) != 0 {
		t.Fatalf("CONTROL: an honest report of the bed must match, got %v", problems)
	}

	lyingWritten := FileArtifact{Path: observed.Path, State: ArtifactWritten, Bytes: []byte("nope")}
	if problems := reportedArtifactMismatches(lyingWritten, observed); len(problems) == 0 {
		t.Fatal("CONTROL: reporting written with bytes that are not on disk must not match")
	}

	wroteNothing := FileArtifact{Path: observed.Path, State: ArtifactWritten, Bytes: onDisk}
	absent := FileArtifact{Path: observed.Path, State: ArtifactAbsent}
	if problems := reportedArtifactMismatches(wroteNothing, absent); len(problems) == 0 {
		t.Fatal("CONTROL: reporting written while the bed observed absent must not match")
	}

	staleOnDisk := FileArtifact{Path: observed.Path, State: ArtifactStale, Bytes: onDisk}
	if problems := reportedArtifactMismatches(FileArtifact{Path: observed.Path, State: ArtifactStale, Bytes: onDisk}, staleOnDisk); len(problems) != 0 {
		t.Fatalf("CONTROL: an honest stale report must match, got %v", problems)
	}
	if problems := reportedArtifactMismatches(FileArtifact{Path: observed.Path, State: ArtifactStale, Bytes: []byte("{}")}, staleOnDisk); len(problems) == 0 {
		t.Fatal("CONTROL: stale with the wrong bytes must not match the disk")
	}

	na := FileArtifact{State: ArtifactNotApplicable}
	if problems := reportedArtifactMismatches(na, na); len(problems) != 0 {
		t.Fatalf("CONTROL: not-applicable against not-applicable must match, got %v", problems)
	}
	if problems := reportedArtifactMismatches(FileArtifact{}, na); len(problems) == 0 {
		t.Fatal("CONTROL: Unset is not NotApplicable")
	}
}

// ─── flag-parse error identity (finding 5) ───────────────────────────────────

// flagParseErrorHonoursReference requires the subject's error to be the
// caller's FlagSet.Parse error: same identity, or a wrap / copy that still
// carries the reference message. An arbitrary internal error does not qualify.
func flagParseErrorHonoursReference(got, want error) bool {
	if want == nil {
		return got == nil
	}
	if got == nil {
		return false
	}
	if errors.Is(got, ErrWiringNotImplemented) {
		return false
	}
	if errors.Is(got, want) {
		return true
	}
	if errors.Is(want, flag.ErrHelp) {
		return errors.Is(got, flag.ErrHelp)
	}
	msg := want.Error()
	if msg == "" {
		return false
	}
	return strings.Contains(got.Error(), msg)
}

// TestSeal_Wiring_FlagParseErrorMatcherRejectsUnrelatedErrors is GREEN today:
// it is the in-test control for the malformed-argv rows. Those rows used to
// accept every non-nil error except ErrWiringNotImplemented.
func TestSeal_Wiring_FlagParseErrorMatcherRejectsUnrelatedErrors(t *testing.T) {
	defer red(t)

	ref := referenceFlagSet()
	refErr := ref.Parse([]string{"-not-a-real-flag"})
	if refErr == nil {
		t.Fatal("CONTROL: the reference FlagSet accepted -not-a-real-flag")
	}

	if flagParseErrorHonoursReference(nil, refErr) {
		t.Error("a nil subject must not honour a parse error")
	}
	if flagParseErrorHonoursReference(errors.New("internal: classify broke"), refErr) {
		t.Error("an unrelated internal error must not honour the FlagSet.Parse error")
	}
	if flagParseErrorHonoursReference(fmt.Errorf("internal: %w", errors.New("boom")), refErr) {
		t.Error("an unrelated wrapped error must not honour the FlagSet.Parse error")
	}
	if flagParseErrorHonoursReference(ErrWiringNotImplemented, refErr) {
		t.Error("the stub sentinel must not honour a parse error")
	}
	if !flagParseErrorHonoursReference(refErr, refErr) {
		t.Error("the reference error must honour itself")
	}
	if !flagParseErrorHonoursReference(fmt.Errorf("parse: %w", refErr), refErr) {
		t.Error("wrapping the parse error must be accepted")
	}
	if !flagParseErrorHonoursReference(errors.New(refErr.Error()), refErr) {
		t.Error("a new error carrying the stable parse message must be accepted")
	}
}

// ─── main() data-flow (finding 3) ────────────────────────────────────────────

type identOrigin struct {
	fromExitCode bool
	fromInternal bool
}

func isIdentNamed(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func isOsStream(e ast.Expr, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "os"
}

func exprUsesField(e ast.Expr, obj, field string) bool {
	if e == nil {
		return false
	}
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != field {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == obj {
			found = true
		}
		return true
	})
	return found
}

func exprUsesIdent(e ast.Expr, name string) bool {
	if e == nil {
		return false
	}
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && id.Name == name {
			found = true
		}
		return true
	})
	return found
}

func peelConv(e ast.Expr) ast.Expr {
	for e != nil {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.CallExpr:
			id, ok := x.Fun.(*ast.Ident)
			if !ok || len(x.Args) != 1 {
				return e
			}
			switch id.Name {
			case "int", "ExitCode":
				e = x.Args[0]
			default:
				return e
			}
		default:
			return e
		}
	}
	return e
}

func isWriteMethod(name string) bool {
	switch name {
	case "Write", "WriteString", "WriteByte", "WriteRune", "ReadFrom":
		return true
	default:
		return false
	}
}

func streamWriteTarget(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if isWriteMethod(sel.Sel.Name) {
		if isOsStream(sel.X, "Stdout") {
			return "Stdout"
		}
		if isOsStream(sel.X, "Stderr") {
			return "Stderr"
		}
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	switch {
	case pkg.Name == "fmt" && (sel.Sel.Name == "Fprint" || sel.Sel.Name == "Fprintln" || sel.Sel.Name == "Fprintf"):
	case pkg.Name == "io" && (sel.Sel.Name == "Copy" || sel.Sel.Name == "CopyBuffer" || sel.Sel.Name == "WriteString"):
	default:
		return ""
	}
	if len(call.Args) == 0 {
		return ""
	}
	if isOsStream(call.Args[0], "Stdout") {
		return "Stdout"
	}
	if isOsStream(call.Args[0], "Stderr") {
		return "Stderr"
	}
	return ""
}

func callPayloadUsesField(call *ast.CallExpr, obj, field string) bool {
	for _, a := range call.Args {
		if exprUsesField(a, obj, field) {
			return true
		}
	}
	return false
}

func callPayloadUsesIdent(call *ast.CallExpr, name string) bool {
	for _, a := range call.Args {
		if exprUsesIdent(a, name) {
			return true
		}
	}
	return false
}

func runWiringResultBinding(fn *ast.FuncDecl) (artIdent, errIdent string, ok bool) {
	if fn == nil || fn.Body == nil {
		return "", "", false
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, isAs := n.(*ast.AssignStmt)
		if !isAs || len(as.Rhs) != 1 || !isIdentCall("RunWiring")(as.Rhs[0]) {
			return true
		}
		if len(as.Lhs) != 2 {
			return true
		}
		a, aOK := as.Lhs[0].(*ast.Ident)
		e, eOK := as.Lhs[1].(*ast.Ident)
		if !aOK || !eOK || a.Name == "_" || e.Name == "_" {
			return true
		}
		artIdent, errIdent = a.Name, e.Name
		return false
	})
	return artIdent, errIdent, artIdent != "" && errIdent != ""
}

func collectIdentOrigins(fn *ast.FuncDecl, artIdent string) map[string]identOrigin {
	origins := map[string]identOrigin{}
	add := func(name string, rhs ast.Expr) {
		if name == "" || name == "_" || rhs == nil {
			return
		}
		o := origins[name]
		if exprUsesField(rhs, artIdent, "ExitCode") {
			o.fromExitCode = true
		}
		if exprUsesIdent(rhs, "exitInternal") {
			o.fromInternal = true
		}
		if id, ok := peelConv(rhs).(*ast.Ident); ok {
			inner := origins[id.Name]
			o.fromExitCode = o.fromExitCode || inner.fromExitCode
			o.fromInternal = o.fromInternal || inner.fromInternal
		}
		origins[name] = o
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range s.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				var rhs ast.Expr
				switch {
				case len(s.Rhs) == len(s.Lhs):
					rhs = s.Rhs[i]
				case len(s.Rhs) == 1:
					rhs = s.Rhs[0]
				default:
					continue
				}
				add(id.Name, rhs)
			}
		case *ast.ValueSpec:
			for i, name := range s.Names {
				if i < len(s.Values) {
					add(name.Name, s.Values[i])
				}
			}
		}
		return true
	})
	return origins
}

func exitArgOrigins(arg ast.Expr, artIdent string, origins map[string]identOrigin) (fromCode, fromInternal bool) {
	if exprUsesField(arg, artIdent, "ExitCode") {
		fromCode = true
	}
	if exprUsesIdent(arg, "exitInternal") {
		fromInternal = true
	}
	if id, ok := peelConv(arg).(*ast.Ident); ok {
		o := origins[id.Name]
		fromCode = fromCode || o.fromExitCode
		fromInternal = fromInternal || o.fromInternal
	}
	return fromCode, fromInternal
}

func nilCheckErrorBody(ifs *ast.IfStmt, errIdent string) *ast.BlockStmt {
	bin, ok := ifs.Cond.(*ast.BinaryExpr)
	if !ok || (bin.Op != token.NEQ && bin.Op != token.EQL) {
		return nil
	}
	mentions := (isIdentNamed(bin.X, errIdent) && isIdentNamed(bin.Y, "nil")) ||
		(isIdentNamed(bin.Y, errIdent) && isIdentNamed(bin.X, "nil"))
	if !mentions {
		return nil
	}
	if bin.Op == token.NEQ {
		return ifs.Body
	}
	elseBlk, ok := ifs.Else.(*ast.BlockStmt)
	if !ok {
		return nil
	}
	return elseBlk
}

func errorBranchReportsErr(fn *ast.FuncDecl, errIdent string) bool {
	if fn == nil || fn.Body == nil || errIdent == "" {
		return false
	}
	reported := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		body := nilCheckErrorBody(ifs, errIdent)
		if body == nil {
			return true
		}
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if streamWriteTarget(call) == "Stderr" && callPayloadUsesIdent(call, errIdent) {
				reported = true
			}
			return true
		})
		return true
	})
	return reported
}

// mainForwardingProblems walks main's body for clause 8 data flow from the
// single RunWiring result. Identifier presence is not enough: Stdout/Stderr
// fields must be the values written to the corresponding streams, ExitCode
// must feed os.Exit, and the non-nil error branch must report that error on
// os.Stderr and select exitInternal.
func mainForwardingProblems(fn *ast.FuncDecl) []string {
	if fn == nil || fn.Body == nil {
		return []string{"main has no body"}
	}
	artIdent, errIdent, ok := runWiringResultBinding(fn)
	if !ok {
		return []string{"RunWiring's Artifacts and error results are not both bound to names. Clause 8 forwards that result; discarding either answer cannot forward it"}
	}
	origins := collectIdentOrigins(fn, artIdent)

	var (
		stdoutFromArt, stderrFromArt   bool
		exitFromCode, exitFromInternal bool
		problems                       []string
	)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch streamWriteTarget(call) {
		case "Stdout":
			if callPayloadUsesField(call, artIdent, "Stdout") {
				stdoutFromArt = true
			} else {
				problems = append(problems, "a write to os.Stdout does not take "+artIdent+".Stdout — unrelated bytes are not forwarding")
			}
		case "Stderr":
			if callPayloadUsesField(call, artIdent, "Stderr") {
				stderrFromArt = true
			} else if !callPayloadUsesIdent(call, errIdent) {
				problems = append(problems, "a write to os.Stderr is neither "+artIdent+".Stderr nor the RunWiring error")
			}
		}
		if isSelectorCall("os", "Exit")(call) && len(call.Args) == 1 {
			fromCode, fromInternal := exitArgOrigins(call.Args[0], artIdent, origins)
			exitFromCode = exitFromCode || fromCode
			exitFromInternal = exitFromInternal || fromInternal
		}
		return true
	})
	if !stdoutFromArt {
		problems = append(problems, artIdent+".Stdout is never written to os.Stdout")
	}
	if !stderrFromArt {
		problems = append(problems, artIdent+".Stderr is never written to os.Stderr")
	}
	if !exitFromCode {
		problems = append(problems, "os.Exit is not fed "+artIdent+".ExitCode")
	}
	if !errorBranchReportsErr(fn, errIdent) {
		problems = append(problems, "the non-nil error branch does not report the returned error on os.Stderr")
	}
	if !exitFromInternal {
		problems = append(problems, "exitInternal does not feed os.Exit (a dead mention is not selecting it)")
	}
	return problems
}

func parseMainSnippet(t *testing.T, src string) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		t.Fatalf("parse snippet: %v\n%s", err, src)
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if ok && fn.Name != nil && fn.Name.Name == "main" {
			return fn
		}
	}
	t.Fatal("snippet has no func main")
	return nil
}

// TestSeal_Wiring_MainForwardingAnalysisJudgesDataFlow is GREEN today: it is
// the in-test control for TestSeal_Wiring_MainForwardsTheResult. The panel's
// measured body — mention every identifier, write unrelated bytes, exit
// through an unrelated nonliteral — must redden the walk. A body that
// actually forwards the RunWiring result must pass it.
func TestSeal_Wiring_MainForwardingAnalysisJudgesDataFlow(t *testing.T) {
	defer red(t)

	honest := parseMainSnippet(t, `package main
func main() {
	art, err := RunWiring(Invocation{Args: os.Args[1:]})
	code := int(art.ExitCode)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = exitInternal
	} else {
		os.Stdout.Write(art.Stdout)
		os.Stderr.Write(art.Stderr)
	}
	os.Exit(code)
}`)
	if problems := mainForwardingProblems(honest); len(problems) != 0 {
		t.Fatalf("CONTROL: a body that forwards the RunWiring result must pass the walk, got %v", problems)
	}

	dead := parseMainSnippet(t, `package main
func main() {
	_ = os.Stdout
	_ = os.Stderr
	_ = exitInternal
	art, err := RunWiring(Invocation{Args: os.Args[1:]})
	_ = art.ExitCode
	_ = art.Stdout
	_ = art.Stderr
	_ = err
	code := 1
	os.Exit(code)
}`)
	if problems := mainForwardingProblems(dead); len(problems) == 0 {
		t.Fatal("the panel's measured body (dead assignments, unrelated nonliteral exit) passed the walk — identifier presence is not data flow")
	}

	unrelated := parseMainSnippet(t, `package main
func main() {
	art, err := RunWiring(Invocation{Args: os.Args[1:]})
	os.Stdout.Write([]byte("nope"))
	os.Stderr.Write([]byte("nope"))
	os.Exit(int(art.ExitCode))
	_ = err
	_ = exitInternal
}`)
	if problems := mainForwardingProblems(unrelated); len(problems) == 0 {
		t.Fatal("a body that writes unrelated bytes and dead-mentions exitInternal passed the walk")
	}

	discardedErr := parseMainSnippet(t, `package main
func main() {
	art, _ := RunWiring(Invocation{Args: os.Args[1:]})
	os.Stdout.Write(art.Stdout)
	os.Stderr.Write(art.Stderr)
	os.Exit(int(art.ExitCode))
}`)
	if problems := mainForwardingProblems(discardedErr); len(problems) == 0 {
		t.Fatal("discarding RunWiring's error must not pass the walk")
	}
}
