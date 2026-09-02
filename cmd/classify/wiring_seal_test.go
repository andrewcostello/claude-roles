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
	"sort"
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

// acceptedContractValuesPhrase is the rendered accepted-set clause derived
// from contractVersionSet, e.g. "accepted values are 1 and 2". Bare digits
// are not a seal: INVALID_INPUT reports print Worktree: /tmp/TestSeal_...,
// so Contains(report, "1") is decided by t.TempDir().
func acceptedContractValuesPhrase() string {
	parts := make([]string, len(contractVersionSet))
	for i, v := range contractVersionSet {
		parts[i] = v.String()
	}
	switch len(parts) {
	case 0:
		return "accepted values are"
	case 1:
		return "accepted values are " + parts[0]
	default:
		return "accepted values are " + strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

// rejectedContractReportProblems requires the INVALID_INPUT report to carry
// ParseContractVersion("3")'s own message (the resolver, not a re-worded
// copy) and the accepted-set phrase derived from contractVersionSet.
func rejectedContractReportProblems(report string) []string {
	var problems []string
	_, err := ParseContractVersion("3")
	if err == nil {
		return []string{`ParseContractVersion("3") succeeded — the accepted-set claim cannot be judged`}
	}
	if !strings.Contains(report, err.Error()) {
		problems = append(problems, fmt.Sprintf("does not carry ParseContractVersion's message %q", err.Error()))
	}
	phrase := acceptedContractValuesPhrase()
	if !strings.Contains(report, phrase) {
		problems = append(problems, fmt.Sprintf("does not name the accepted set as %q", phrase))
	}
	if !strings.Contains(report, `"3"`) {
		problems = append(problems, `does not name the rejected value "3"`)
	}
	if !strings.Contains(report, "-"+flagContractVersion) {
		problems = append(problems, "does not name -"+flagContractVersion)
	}
	return problems
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
		for _, p := range rejectedContractReportProblems(report) {
			t.Errorf("rejected-contract report %s:\n%s", p, report)
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
	for _, p := range rejectedContractReportProblems(stdout) {
		t.Errorf("rejected-contract report %s:\n%s", p, stdout)
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

// TestSeal_Wiring_RejectedContractReportNamesTheAcceptedSet is GREEN today:
// it is the in-test control for the accepted-set claim. A report that
// mentions "3", the flag, and the digits 1 and 2 only because Worktree
// contains them must redden; a report that carries ParseContractVersion's
// own message must pass.
func TestSeal_Wiring_RejectedContractReportNamesTheAcceptedSet(t *testing.T) {
	defer red(t)

	_, err := ParseContractVersion("3")
	if err == nil {
		t.Fatal(`CONTROL: ParseContractVersion("3") must error`)
	}
	honest := "=== CLASSIFY: INVALID_INPUT ===\nWorktree: /tmp/TestSeal_no-digits-here\n" + err.Error() + "\n"
	if problems := rejectedContractReportProblems(honest); len(problems) != 0 {
		t.Fatalf("CONTROL: a report carrying ParseContractVersion's message must pass, got %v", problems)
	}

	// The defect the panel measured: Contains(report, "1") && Contains(report, "2")
	// on the whole report, satisfied by Worktree: /tmp/...12... with no accepted set.
	vacuous := fmt.Sprintf("=== CLASSIFY: INVALID_INPUT ===\nWorktree: /tmp/TestSeal_12/001\n-%s %q is not a classification contract this binary emits\n", flagContractVersion, "3")
	if !strings.Contains(vacuous, "1") || !strings.Contains(vacuous, "2") || !strings.Contains(vacuous, `"3"`) || !strings.Contains(vacuous, "-"+flagContractVersion) {
		t.Fatal("CONTROL: the vacuous payload must contain the old substring matches so this row actually judges the defect")
	}
	if problems := rejectedContractReportProblems(vacuous); len(problems) == 0 {
		t.Fatal("a report that names 1 and 2 only because Worktree contains those digits passed the accepted-set check")
	}
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

// TestSeal_Wiring_RunWiringResolvesPathsAgainstInvocationDir seals clause 7
// behaviourally: relative -config, -worktree, -out and the diff argument
// resolve against Invocation.Dir, which is not the process working
// directory, and the process directory does not change.
//
// Judged on WHICH TREE WAS CONSUMED, not only where -out landed: the row
// runs under -contract-version 2 -json so computed_config_sha256 /
// computed_diff_sha256 must equal SHA-256 of the files staged in invDir,
// the reported worktree must resolve under invDir, and the cwd decoys
// (valid, different contents and digests) must stay byte-identical.
//
// RED today on the stub. A body that resolves only -out against inv.Dir
// and opens -config/-worktree/diff against cwd classifies the decoy bed
// and writes that answer into the invocation directory.
func TestSeal_Wiring_RunWiringResolvesPathsAgainstInvocationDir(t *testing.T) {
	defer red(t)

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	cwdDir := t.TempDir()
	invDir := t.TempDir()
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatalf("chdir to decoy cwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(origWD); err != nil {
			t.Errorf("restore wd: %v", err)
		}
	})

	cfgBytes, err := os.ReadFile(filepath.Join(origWD, exampleConfigPath))
	if err != nil {
		t.Fatalf("read fixture config: %v", err)
	}
	invDiff := []byte(diffFor(walletPath))

	const (
		relCfg      = "risk-paths.json"
		relDiff     = "change.diff"
		relOut      = "artifacts/run-state.json"
		relWorktree = "."
	)
	if err := os.WriteFile(filepath.Join(invDir, relCfg), cfgBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invDir, relDiff), invDiff, 0o600); err != nil {
		t.Fatal(err)
	}
	// writeRunState is a bare os.WriteFile with no MkdirAll. The row creates
	// the parent so a body that correctly resolves -out against inv.Dir is
	// not killed by log.Fatalf, which would abort the whole test binary.
	if err := os.MkdirAll(filepath.Join(invDir, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}

	decoyCfg := []byte(`{"schema_version":1,"unmatched_risk":"low","rules":[{"id":"decoy","paths":["never-matches-this"],"risk":"low"}]}` + "\n")
	decoyDiff := []byte(diffFor("docs/README.md"))
	decoyOut := []byte(`{"schema_version":1,"status":"decoy-cwd"}` + "\n")
	if bytes.Equal(decoyCfg, cfgBytes) {
		t.Fatal("CONTROL: decoy config must differ from the Invocation.Dir config")
	}
	if bytes.Equal(decoyDiff, invDiff) {
		t.Fatal("CONTROL: decoy diff must differ from the Invocation.Dir diff")
	}
	if sha256Bytes(decoyCfg) == sha256Bytes(cfgBytes) || sha256Bytes(decoyDiff) == sha256Bytes(invDiff) {
		t.Fatal("CONTROL: decoy digests must differ from the Invocation.Dir digests")
	}
	if err := os.WriteFile(filepath.Join(cwdDir, relCfg), decoyCfg, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwdDir, relDiff), decoyDiff, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cwdDir, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwdDir, relOut), decoyOut, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeDecoyCfg := snap(t, filepath.Join(cwdDir, relCfg))
	beforeDecoyDiff := snap(t, filepath.Join(cwdDir, relDiff))
	beforeDecoyOut := snap(t, filepath.Join(cwdDir, relOut))

	args := []string{
		"-no-git",
		"-worktree", relWorktree,
		"-config", relCfg,
		"-out", relOut,
		"-json",
		"-" + flagContractVersion, "2",
		relDiff,
	}
	got, runErr := RunWiring(Invocation{Args: args, Dir: invDir})

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd after RunWiring: %v", err)
	}
	if !sameResolvedPath(wd, cwdDir) {
		t.Errorf("RunWiring changed the process working directory to %s; clause 7 forbids os.Chdir and paths resolve against inv.Dir (%s)", wd, invDir)
	}

	if stubRED(t, runErr, "RunWiring must resolve relative -config/-worktree/-out/diff against Invocation.Dir without changing the process working directory") {
		return
	}
	if got.ExitCode != exitOK {
		t.Errorf("relative-path classify exited %d, want %d:\nstdout=%s\nstderr=%s", got.ExitCode, exitOK, got.Stdout, got.Stderr)
	}

	wantOut := filepath.Join(invDir, relOut)
	if !fileExists(wantOut) {
		t.Errorf("did not write the run-state under Invocation.Dir at %s", wantOut)
	}
	if got.RunState.Path != wantOut {
		t.Errorf("got.RunState.Path = %q, want exactly the resolved -out path %q", got.RunState.Path, wantOut)
	}
	if got.RunState.State != ArtifactWritten {
		t.Errorf("run-state state = %s, want written (the file under Invocation.Dir)", got.RunState.State)
	}

	afterDecoyCfg := snap(t, filepath.Join(cwdDir, relCfg))
	afterDecoyDiff := snap(t, filepath.Join(cwdDir, relDiff))
	afterDecoyOut := snap(t, filepath.Join(cwdDir, relOut))
	if !afterDecoyCfg.exists || !bytes.Equal(afterDecoyCfg.bytes, beforeDecoyCfg.bytes) {
		t.Errorf("cwd decoy config was rewritten — a body that opens -config against the process cwd mutates the decoy bed")
	}
	if !afterDecoyDiff.exists || !bytes.Equal(afterDecoyDiff.bytes, beforeDecoyDiff.bytes) {
		t.Errorf("cwd decoy diff was rewritten — a body that opens the positional diff against the process cwd mutates the decoy bed")
	}
	if !afterDecoyOut.exists || !bytes.Equal(afterDecoyOut.bytes, beforeDecoyOut.bytes) {
		t.Errorf("resolved -out against the process working directory (decoy run-state was rewritten)")
	}

	assertStdoutShape(t, "clause-7/v2/json", shapeV2Wrapper, got.Stdout)
	if sameStrings(topKeys(t, got.Stdout), v2WrapperKeys) {
		var w ResponseWrapper
		if err := json.Unmarshal(got.Stdout, &w); err != nil {
			t.Errorf("stdout has the wrapper's keys but does not unmarshal as a ResponseWrapper: %v", err)
		} else {
			if want := sha256Bytes(cfgBytes); w.ComputedConfigSHA256 != want {
				t.Errorf("computed_config_sha256 = %q, want SHA-256 of the config under Invocation.Dir = %q (cwd decoy is %q). A body that resolves only -out against inv.Dir still classifies the decoy table.",
					w.ComputedConfigSHA256, want, sha256Bytes(decoyCfg))
			}
			if want := sha256Bytes(invDiff); w.ComputedDiffSHA256 != want {
				t.Errorf("computed_diff_sha256 = %q, want SHA-256 of the diff under Invocation.Dir = %q (cwd decoy is %q). A body that resolves only -out against inv.Dir still classifies the decoy diff.",
					w.ComputedDiffSHA256, want, sha256Bytes(decoyDiff))
			}
			var payload ClassificationV2
			if err := json.Unmarshal(w.Classification, &payload); err != nil {
				t.Errorf("wrapped classification is not a v2 payload: %v", err)
			} else if !payload.FinancialPathsTouched {
				t.Errorf("financial_paths_touched = false; the Invocation.Dir bed is the wallet money path against the example table. The cwd decoy is a docs diff against a never-matches table — classifying that bed reports financial_paths_touched=false.")
			}
		}
	}

	if len(got.RunState.Bytes) > 0 {
		var rs RunState
		if err := json.Unmarshal(got.RunState.Bytes, &rs); err != nil {
			t.Errorf("run-state is not a RunState: %v\n%s", err, got.RunState.Bytes)
		} else if rs.Repo.Worktree == "" || (!pathUnderDir(rs.Repo.Worktree, invDir) && !sameResolvedPath(rs.Repo.Worktree, invDir)) {
			t.Errorf("reported worktree %q does not resolve under Invocation.Dir %q — relative -worktree must join against inv.Dir, not the process cwd", rs.Repo.Worktree, invDir)
		}
	}
}

func sha256Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sameResolvedPath(a, b string) bool {
	ea, err1 := filepath.EvalSymlinks(a)
	eb, err2 := filepath.EvalSymlinks(b)
	if err1 == nil && err2 == nil {
		return ea == eb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func pathUnderDir(p, dir string) bool {
	absP, err1 := filepath.Abs(p)
	absDir, err2 := filepath.Abs(dir)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absP)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// TestSeal_Wiring_RunWiringHonoursStdin seals Invocation.Stdin through
// RunWiring. Every other mapping row passes the diff as a file argument, so
// the shipped default (`git diff | classify`, advertised as "reads stdin
// when no file given") is otherwise unsealed. nil means no diff on stdin
// and is a different fact from an empty reader.
//
// RED today on the stub. A body that ignores inv.Stdin cannot classify the
// fixture bytes with no file argument, and cannot tell nil from empty.
func TestSeal_Wiring_RunWiringHonoursStdin(t *testing.T) {
	bed := newClassifyBed(t)
	diffBytes, err := os.ReadFile(bed.diffPath)
	if err != nil {
		t.Fatalf("read staged diff: %v", err)
	}
	base := []string{
		"-no-git",
		"-worktree", bed.dir,
		"-config", bed.cfgPath,
		"-json",
		"-" + flagContractVersion, "1",
	}

	live := bed.driveLive(t, "1", true, false)
	if live.ExitCode != exitOK {
		t.Fatalf("CONTROL: file-argument classify on this bed exited %d, want 0:\n%s", live.ExitCode, live.Stdout)
	}
	liveVerdict := v1Verdict(t, live.Stdout)
	if liveVerdict.Risk == "" {
		t.Fatal("CONTROL: file-argument v1 JSON on this bed has no risk — the stdin comparison would be vacuous")
	}

	t.Run("stdin-fixture-no-file", func(t *testing.T) {
		defer red(t)
		got, err := RunWiring(Invocation{Args: append([]string{}, base...), Stdin: bytes.NewReader(diffBytes), Dir: bed.dir})
		if stubRED(t, err, "RunWiring with the fixture on Stdin and no file argument must classify those bytes (same verdict as the file-argument row on this bed)") {
			return
		}
		if got.ExitCode != exitOK {
			t.Errorf("stdin-fixture exited %d, want 0:\nstdout=%s\nstderr=%s", got.ExitCode, got.Stdout, got.Stderr)
		}
		assertStdoutShape(t, "stdin-fixture", shapeV1Payload, got.Stdout)
		gotVerdict := v1Verdict(t, got.Stdout)
		if gotVerdict.Risk != liveVerdict.Risk || gotVerdict.Financial != liveVerdict.Financial || !sameStrings(gotVerdict.Files, liveVerdict.Files) {
			t.Errorf("stdin-fixture verdict (risk=%q financial=%v files=%v) does not match the file-argument row on this bed (risk=%q financial=%v files=%v). Stdin must carry the same bytes the file argument would have.",
				gotVerdict.Risk, gotVerdict.Financial, gotVerdict.Files, liveVerdict.Risk, liveVerdict.Financial, liveVerdict.Files)
		}
	})

	var nilArt Artifacts
	t.Run("stdin-nil-no-file", func(t *testing.T) {
		defer red(t)
		got, err := RunWiring(Invocation{Args: append([]string{}, base...), Stdin: nil, Dir: bed.dir})
		if stubRED(t, err, "RunWiring with Stdin nil and no file argument must exit %d with the %q problem", exitInvalid, "diff is empty") {
			return
		}
		nilArt = got
		if got.ExitCode != ExitCode(exitInvalid) {
			t.Errorf("stdin-nil exited %d, want %d", got.ExitCode, exitInvalid)
		}
		report := string(got.Stdout) + string(got.Stderr)
		if !strings.Contains(report, "diff is empty") {
			t.Errorf("stdin-nil did not report the %q problem:\nstdout=%s\nstderr=%s", "diff is empty", got.Stdout, got.Stderr)
		}
		if got.ExitCode == exitOK {
			t.Errorf("stdin-nil classified successfully; nil means no diff on stdin")
		}
	})

	t.Run("stdin-empty-reader-no-file", func(t *testing.T) {
		defer red(t)
		got, err := RunWiring(Invocation{Args: append([]string{}, base...), Stdin: bytes.NewReader(nil), Dir: bed.dir})
		if stubRED(t, err, "RunWiring with an empty Stdin reader and no file argument must refuse (piped nothing) and must be a different fact from Stdin nil") {
			return
		}
		if got.ExitCode != ExitCode(exitInvalid) {
			t.Errorf("empty stdin reader exited %d, want %d (piped nothing is an empty diff):\nstdout=%s\nstderr=%s", got.ExitCode, exitInvalid, got.Stdout, got.Stderr)
		}
		emptyReport := string(got.Stdout) + string(got.Stderr)
		if !strings.Contains(emptyReport, "diff is empty") {
			t.Errorf("empty stdin reader did not report the %q problem:\nstdout=%s\nstderr=%s", "diff is empty", got.Stdout, got.Stderr)
		}
		gotVerdict := v1WireVerdict{}
		if json.Valid(bytes.TrimSpace(got.Stdout)) {
			var cls Classification
			if json.Unmarshal(got.Stdout, &cls) == nil {
				gotVerdict = v1WireVerdict{Risk: cls.Risk, Financial: cls.FinancialPathsTouched}
			}
		}
		if got.ExitCode == exitOK && gotVerdict.Risk == liveVerdict.Risk && gotVerdict.Financial == liveVerdict.Financial {
			t.Errorf("empty stdin reader reproduced the file-argument verdict; an empty reader is piped nothing, not the fixture")
		}
		nilReport := string(nilArt.Stdout) + string(nilArt.Stderr)
		if got.ExitCode == nilArt.ExitCode && emptyReport == nilReport && got.ExitCode == exitOK {
			t.Errorf("empty reader and nil stdin both classified successfully — Stdin was not observed, so the two facts collapsed")
		}
	})
}

type v1WireVerdict struct {
	Risk      string
	Financial bool
	Files     []string
}

func v1Verdict(t *testing.T, stdout []byte) v1WireVerdict {
	t.Helper()
	var cls Classification
	if err := json.Unmarshal(stdout, &cls); err != nil {
		t.Fatalf("v1 stdout is not a Classification: %v\n%s", err, stdout)
		return v1WireVerdict{}
	}
	files := make([]string, 0, len(cls.ChangedFiles))
	for _, f := range cls.ChangedFiles {
		files = append(files, f.Path)
	}
	return v1WireVerdict{Risk: cls.Risk, Financial: cls.FinancialPathsTouched, Files: files}
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
		for _, p := range capabilityProbeProblems(got.Stdout, got.ExitCode) {
			t.Errorf("capabilities probe %s:\n%s", p, got.Stdout)
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
		assertInitArtifactsNotApplicable(t, "init", got)
		first, err := os.ReadFile(out)
		if err != nil {
			t.Errorf("init did not write the scaffold at %s: %v", out, err)
			return
		}
		for _, p := range scaffoldConfigProblems(first) {
			t.Errorf("ordinary init did not write a complete scaffold: %s\n%s", p, first)
		}
		if !bytes.Contains(got.Stdout, []byte("=== CLASSIFY INIT ===")) && !bytes.Contains(got.Stderr, []byte("=== CLASSIFY INIT ===")) {
			t.Errorf("init did not print its report; stdout=%q stderr=%q", got.Stdout, got.Stderr)
		}

		again, err := RunWiring(Invocation{Args: []string{"init", "-worktree", dir, "-out", out}, Dir: dir})
		if err != nil {
			t.Fatalf("second init without -force: %v", err)
		}
		if again.ExitCode != ExitCode(exitInvalid) {
			t.Errorf("second init without -force exited %d, want %d — the refusal to overwrite is the safety property", again.ExitCode, exitInvalid)
		}
		assertInitArtifactsNotApplicable(t, "second init without -force", again)
		after, err := os.ReadFile(out)
		if err != nil {
			t.Errorf("second init removed the scaffold: %v", err)
		} else if !bytes.Equal(after, first) {
			t.Errorf("second init without -force rewrote the scaffold")
		}
	})

	t.Run("init-refuses-existing-without-force", func(t *testing.T) {
		defer red(t)
		dest := t.TempDir()
		out := filepath.Join(dest, "risk-paths.json")
		seed := []byte(`{"schema_version":1,"scaffold":true,"seeded":"by-the-seal"}` + "\n")
		if err := os.WriteFile(out, seed, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := RunWiring(Invocation{Args: []string{"init", "-worktree", dest, "-out", out}, Dir: dest})
		if stubRED(t, err, "RunWiring(init) against an existing table without -force must exit %d and leave the file byte-identical", exitInvalid) {
			return
		}
		if got.ExitCode != ExitCode(exitInvalid) {
			t.Errorf("init without -force over a seeded table exited %d, want %d", got.ExitCode, exitInvalid)
		}
		assertInitArtifactsNotApplicable(t, "init-refuses-existing-without-force", got)
		after, err := os.ReadFile(out)
		if err != nil {
			t.Errorf("init without -force removed the seeded table: %v", err)
		} else if !bytes.Equal(after, seed) {
			t.Errorf("init without -force rewrote an existing rule table")
		}
	})

	t.Run("init-force-overwrites", func(t *testing.T) {
		defer red(t)
		dest := t.TempDir()
		out := filepath.Join(dest, "risk-paths.json")
		seed := []byte(`{"schema_version":1,"seeded":"by-the-seal"}` + "\n")
		if err := os.WriteFile(out, seed, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := RunWiring(Invocation{Args: []string{"init", "-worktree", dest, "-out", out, "-force"}, Dir: dest})
		if stubRED(t, err, "RunWiring(init -force) must overwrite an existing table, exit 0, and write a scaffold") {
			return
		}
		if got.ExitCode != exitOK {
			t.Errorf("init -force exited %d, want 0", got.ExitCode)
		}
		assertInitArtifactsNotApplicable(t, "init-force-overwrites", got)
		after, err := os.ReadFile(out)
		if err != nil {
			t.Errorf("init -force did not leave a file at %s: %v", out, err)
			return
		}
		if bytes.Equal(after, seed) {
			t.Errorf("init -force left the seeded table unchanged — -force is part of the init contract")
		}
		for _, p := range scaffoldConfigProblems(after) {
			t.Errorf("init -force did not write a complete scaffold: %s\n%s", p, after)
		}
	})
}

func assertInitArtifactsNotApplicable(t *testing.T, label string, got Artifacts) {
	t.Helper()
	if got.RunState.State != ArtifactNotApplicable {
		t.Errorf("%s asserted a run-state artifact (%s); Artifacts about init must confine themselves to ExitCode and the streams — ArtifactNotApplicable exactly, not Unset", label, got.RunState.State)
	}
	if got.V2Sidecar.State != ArtifactNotApplicable {
		t.Errorf("%s asserted a v2 sidecar artifact (%s); Artifacts about init must confine themselves to ExitCode and the streams — ArtifactNotApplicable exactly, not Unset", label, got.V2Sidecar.State)
	}
}

// scaffoldConfigProblems requires a complete, usable scaffold: valid JSON
// that parseConfig accepts (schema, unmatched_risk, rule table), with the
// scaffold marker set and the two TODO rules a generated table always
// carries. A byte substring `"scaffold"` in malformed JSON does not qualify.
func scaffoldConfigProblems(data []byte) []string {
	if !json.Valid(bytes.TrimSpace(data)) {
		return []string{"not valid JSON"}
	}
	cfg, err := parseConfig(data)
	if err != nil {
		return []string{fmt.Sprintf("not a usable rule table: %v", err)}
	}
	var problems []string
	if !cfg.Scaffold {
		problems = append(problems, "scaffold marker is not true")
	}
	if cfg.SchemaVersion != schemaVersion {
		problems = append(problems, fmt.Sprintf("schema_version = %d, want %d", cfg.SchemaVersion, schemaVersion))
	}
	ids := map[string]bool{}
	for _, r := range cfg.Rules {
		ids[r.ID] = true
	}
	for _, must := range []string{"TODO-money-paths", "TODO-auth-paths"} {
		if !ids[must] {
			problems = append(problems, fmt.Sprintf("missing required scaffold rule %q", must))
		}
	}
	return problems
}

// capabilityProbeProblems requires stdout to be a CapabilityReport, not two
// quoted substrings. The two-substring check accepted `"cmd/classify"
// "probe_version"` and any other malformed payload that happened to mention
// those tokens.
func capabilityProbeProblems(stdout []byte, code ExitCode) []string {
	var problems []string
	trim := bytes.TrimSpace(stdout)
	if !json.Valid(trim) {
		return []string{"stdout is not valid JSON"}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(trim, &raw); err != nil {
		return []string{fmt.Sprintf("stdout is not a JSON object: %v", err)}
	}
	for _, k := range []string{"probe_version", "producer", "capabilities", "contract_versions", "missing"} {
		if _, ok := raw[k]; !ok {
			problems = append(problems, fmt.Sprintf("missing schema key %q", k))
		}
	}
	var rep CapabilityReport
	if err := json.Unmarshal(trim, &rep); err != nil {
		problems = append(problems, fmt.Sprintf("does not unmarshal as CapabilityReport: %v", err))
		return problems
	}
	if rep.ProbeVersion != probeVersion {
		problems = append(problems, fmt.Sprintf("probe_version = %d, want %d", rep.ProbeVersion, probeVersion))
	}
	if rep.Producer != "cmd/classify" {
		problems = append(problems, fmt.Sprintf("producer = %q, want %q", rep.Producer, "cmd/classify"))
	}
	var caps map[string]json.RawMessage
	if err := json.Unmarshal(raw["capabilities"], &caps); err != nil {
		problems = append(problems, fmt.Sprintf("capabilities is not an object: %v", err))
	} else {
		for _, name := range requiredCapabilities {
			if _, ok := caps[name]; !ok {
				problems = append(problems, fmt.Sprintf("capability %q is absent from the report", name))
			}
		}
	}
	if bytes.Equal(bytes.TrimSpace(raw["missing"]), []byte("null")) {
		problems = append(problems, "missing marshaled as null; the contract is an empty slice, never null")
	}
	wantVersions := make([]int, 0, len(contractVersionSet))
	for _, v := range contractVersionSet {
		wantVersions = append(wantVersions, int(v))
	}
	if len(rep.ContractVersions) != len(wantVersions) {
		problems = append(problems, fmt.Sprintf("contract_versions = %v, want %v", rep.ContractVersions, wantVersions))
	} else {
		for i, v := range wantVersions {
			if rep.ContractVersions[i] != v {
				problems = append(problems, fmt.Sprintf("contract_versions = %v, want %v", rep.ContractVersions, wantVersions))
				break
			}
		}
	}
	switch code {
	case exitOK:
		if len(rep.Missing) != 0 {
			problems = append(problems, fmt.Sprintf("exit 0 with missing %v — the biconditional is empty missing iff exit 0", rep.Missing))
		}
	case ExitCode(exitCapabilityIncomplete):
		if len(rep.Missing) == 0 {
			problems = append(problems, fmt.Sprintf("exit %d with empty missing — the biconditional is empty missing iff exit 0", exitCapabilityIncomplete))
		}
	default:
		problems = append(problems, fmt.Sprintf("exit %d is not 0 or %d", code, exitCapabilityIncomplete))
	}
	return problems
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
	for _, p := range scaffoldConfigProblems(data) {
		t.Errorf("CONTROL: cmdInit did not write a complete scaffold: %s\n%s", p, data)
	}
	if !strings.Contains(stdout, "=== CLASSIFY INIT ===") {
		t.Errorf("init did not print its report:\n%s", stdout)
	}

	var again int
	stdoutOf(t, func() { again = cmdInit([]string{"-worktree", dir, "-out", out}) })
	if again != exitInvalid {
		t.Errorf("CONTROL: a second init without -force exited %d, want %d — the refusal to overwrite is the safety property", again, exitInvalid)
	}
	after, err := os.ReadFile(out)
	if err != nil {
		t.Errorf("CONTROL: the refusing init removed the scaffold: %v", err)
	} else if !bytes.Equal(after, data) {
		t.Errorf("CONTROL: the refusing init rewrote the scaffold — overwrite-refusal is the file still being byte-identical, not just the exit code")
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
			name:        "no-flags-are-the-declared-defaults",
			args:        []string{diff},
			wantErr:     false,
			wantResidue: []string{diff},
			measured:    `err=<nil>; worktree=".", base="origin/main", task="", out="", config="", json=false, no-git=false, contract-version=defaultContractVersion.String(), Args()=["/tmp/seal.diff"]`,
			check: func(t *testing.T, opts options) {
				ref := referenceFlagSet()
				if err := ref.Parse([]string{diff}); err != nil {
					t.Fatalf("CONTROL: the reference FlagSet failed to parse the no-flags argv: %v", err)
				}
				want := map[string]string{
					"worktree":          ".",
					"base":              "origin/main",
					"task":              "",
					"out":               "",
					"config":            "",
					"json":              "false",
					"no-git":            "false",
					flagContractVersion: defaultContractVersion.String(),
				}
				for name, v := range want {
					f := ref.Lookup(name)
					if f == nil {
						t.Fatalf("CONTROL: reference FlagSet has no -%s", name)
					}
					if f.DefValue != v {
						t.Errorf("CONTROL: reference -%s DefValue = %q, want declared default %q", name, f.DefValue, v)
					}
					if f.Value.String() != v {
						t.Errorf("CONTROL: reference -%s parsed to %q, want declared default %q", name, f.Value.String(), v)
					}
				}
				if !sameStrings(ref.Args(), []string{diff}) {
					t.Errorf("CONTROL: reference residue = %v, want [%q]", ref.Args(), diff)
				}
				if opts.worktree != "." {
					t.Errorf("options.worktree = %q, want the declared default %q", opts.worktree, ".")
				}
				if opts.base != "origin/main" {
					t.Errorf("options.base = %q, want the declared default %q", opts.base, "origin/main")
				}
				if opts.task != "" {
					t.Errorf("options.task = %q, want the declared default %q", opts.task, "")
				}
				if opts.out != "" {
					t.Errorf("options.out = %q, want the declared default %q", opts.out, "")
				}
				if opts.configPath != "" {
					t.Errorf("options.configPath = %q, want the declared default %q", opts.configPath, "")
				}
				if opts.json {
					t.Error("options.json = true, want the declared default false")
				}
				if opts.noGit {
					t.Error("options.noGit = true, want the declared default false")
				}
				if opts.contractVersion != defaultContractVersion.String() {
					t.Errorf("options.contractVersion = %q, want the registered default %q", opts.contractVersion, defaultContractVersion.String())
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
		if err := ref.Parse([]string{diff}); err != nil {
			t.Fatalf("CONTROL: reference FlagSet failed to parse the no-flags argv: %v", err)
		}
		for name, want := range map[string]string{
			"worktree":          ".",
			"base":              "origin/main",
			"task":              "",
			"out":               "",
			"config":            "",
			"json":              "false",
			"no-git":            "false",
			flagContractVersion: defaultContractVersion.String(),
		} {
			f := ref.Lookup(name)
			if f == nil {
				continue
			}
			if f.DefValue != want {
				t.Errorf("CONTROL: reference -%s DefValue = %q, want declared default %q", name, f.DefValue, want)
			}
			if f.Value.String() != want {
				t.Errorf("CONTROL: reference -%s after a no-flags parse = %q, want declared default %q", name, f.Value.String(), want)
			}
		}
		if !sameStrings(ref.Args(), []string{diff}) {
			t.Errorf("CONTROL: reference no-flags residue = %v, want [%q]", ref.Args(), diff)
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
		oldFlags, oldPrefix, oldWriter := log.Flags(), log.Prefix(), log.Writer()
		t.Cleanup(func() {
			log.SetFlags(oldFlags)
			log.SetPrefix(oldPrefix)
			log.SetOutput(oldWriter)
		})
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
		if log.Writer() != oldWriter {
			t.Errorf("parseInvocationFlags redirected log.Writer(). Clause 4: logger state belongs to main(); clause 3 forbids log.SetOutput for the reason clause 7 gives.")
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
	} else {
		for _, p := range runWiringInvocationProblems(mainFn) {
			t.Errorf("clause 8 argv: %s", p)
		}
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
	for name, fn := range funcs {
		if name == "main" {
			continue
		}
		if n := countCalls(fn, isSelectorCall("log", "SetOutput")); n != 0 {
			t.Errorf("%s() calls log.SetOutput. Clause 3 forbids process-wide redirection (and clause 4 puts logger configuration in main only).", name)
		}
		if n := countCalls(fn, isSelectorCall("log", "SetFlags")); n != 0 {
			t.Errorf("%s() calls log.SetFlags. Clause 4: process-wide logger state belongs to main(), not to a function a test calls a hundred times.", name)
		}
		if n := countCalls(fn, isSelectorCall("log", "SetPrefix")); n != 0 {
			t.Errorf("%s() calls log.SetPrefix. Clause 4: process-wide logger state belongs to main(), not to a function a test calls a hundred times.", name)
		}
	}
	for name, fn := range funcs {
		if assignsOsStream(fn, "Stdout") {
			t.Errorf("%s() assigns os.Stdout. Clause 3 forbids process-wide stream redirection for the reason clause 7 gives.", name)
		}
		if assignsOsStream(fn, "Stderr") {
			t.Errorf("%s() assigns os.Stderr. Clause 3 forbids process-wide stream redirection for the reason clause 7 gives.", name)
		}
	}
}

func assignsOsStream(fn *ast.FuncDecl, name string) bool {
	if fn == nil || fn.Body == nil {
		return false
	}
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			if isOsStream(lhs, name) {
				found = true
				return false
			}
		}
		return true
	})
	return found
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
			case "int", "ExitCode", "string":
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

// exprIsField reports whether e is obj.field after peeling parentheses and
// value-preserving conversions (int/ExitCode/string). Slices, index
// expressions, arithmetic and any other transformation do not qualify:
// os.Stdout.Write(art.Stdout[:0]) mentions the field and emits nothing.
func exprIsField(e ast.Expr, obj, field string) bool {
	e = peelConv(e)
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != field {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == obj
}

func exprIsIdentExact(e ast.Expr, name string) bool {
	e = peelConv(e)
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
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
		if exprIsField(a, obj, field) {
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

// originFromRHS is last-write, not a monotonic OR. An assignment whose RHS
// is neither art.ExitCode nor exitInternal (nor an alias of one) CLEARS
// both origins. `code := int(art.ExitCode); code = 0` therefore does not
// still count as fromExitCode. Arithmetic and slicing are not aliases:
// `int(art.ExitCode) * 0` and `exitInternal * 0` clear both origins.
func originFromRHS(rhs ast.Expr, artIdent string, origins map[string]identOrigin) identOrigin {
	if rhs == nil {
		return identOrigin{}
	}
	if exprIsField(rhs, artIdent, "ExitCode") {
		return identOrigin{fromExitCode: true}
	}
	if exprIsIdentExact(rhs, "exitInternal") {
		return identOrigin{fromInternal: true}
	}
	if id, ok := peelConv(rhs).(*ast.Ident); ok {
		return origins[id.Name]
	}
	return identOrigin{}
}

func assignLastWrite(origins map[string]identOrigin, lhs []ast.Expr, rhs []ast.Expr, artIdent string) {
	for i, dest := range lhs {
		id, ok := dest.(*ast.Ident)
		if !ok || id.Name == "" || id.Name == "_" {
			continue
		}
		var src ast.Expr
		switch {
		case len(rhs) == len(lhs):
			src = rhs[i]
		case len(rhs) == 1:
			src = rhs[0]
		default:
			continue
		}
		origins[id.Name] = originFromRHS(src, artIdent, origins)
	}
}

func collectIdentOrigins(fn *ast.FuncDecl, artIdent string) map[string]identOrigin {
	origins := map[string]identOrigin{}
	if fn == nil || fn.Body == nil {
		return origins
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			assignLastWrite(origins, s.Lhs, s.Rhs, artIdent)
		case *ast.ValueSpec:
			lhs := make([]ast.Expr, len(s.Names))
			for i, name := range s.Names {
				lhs[i] = name
			}
			assignLastWrite(origins, lhs, s.Values, artIdent)
		}
		return true
	})
	return origins
}

func exitArgOrigins(arg ast.Expr, artIdent string, origins map[string]identOrigin) (fromCode, fromInternal bool) {
	if exprIsField(arg, artIdent, "ExitCode") {
		return true, false
	}
	if exprIsIdentExact(arg, "exitInternal") {
		return false, true
	}
	if id, ok := peelConv(arg).(*ast.Ident); ok {
		o := origins[id.Name]
		return o.fromExitCode, o.fromInternal
	}
	return false, false
}

type pathState struct {
	origins          map[string]identOrigin
	stdoutFromArt    bool
	stderrFromArt    bool
	errReported      bool
	unrelatedStdout  bool
	unrelatedStderr  bool
	tookErrorBranch  bool
	sawExit          bool
	exitFromCode     bool
	exitFromInternal bool
}

func clonePathState(st pathState) pathState {
	cp := st
	cp.origins = make(map[string]identOrigin, len(st.origins))
	for k, v := range st.origins {
		cp.origins[k] = v
	}
	return cp
}

func elseAsBlock(e ast.Stmt) *ast.BlockStmt {
	if e == nil {
		return nil
	}
	if b, ok := e.(*ast.BlockStmt); ok {
		return b
	}
	return &ast.BlockStmt{List: []ast.Stmt{e}}
}

func errNilBranches(ifs *ast.IfStmt, errIdent string) (errBody, okBody *ast.BlockStmt, ok bool) {
	bin, binOK := ifs.Cond.(*ast.BinaryExpr)
	if !binOK || (bin.Op != token.NEQ && bin.Op != token.EQL) {
		return nil, nil, false
	}
	mentions := (isIdentNamed(bin.X, errIdent) && isIdentNamed(bin.Y, "nil")) ||
		(isIdentNamed(bin.Y, errIdent) && isIdentNamed(bin.X, "nil"))
	if !mentions {
		return nil, nil, false
	}
	if bin.Op == token.NEQ {
		return ifs.Body, elseAsBlock(ifs.Else), true
	}
	return elseAsBlock(ifs.Else), ifs.Body, true
}

func applyCall(st *pathState, call *ast.CallExpr, artIdent, errIdent string) {
	switch streamWriteTarget(call) {
	case "Stdout":
		if callPayloadUsesField(call, artIdent, "Stdout") {
			st.stdoutFromArt = true
		} else {
			st.unrelatedStdout = true
		}
	case "Stderr":
		if callPayloadUsesField(call, artIdent, "Stderr") {
			st.stderrFromArt = true
		} else if callPayloadUsesIdent(call, errIdent) {
			st.errReported = true
		} else {
			st.unrelatedStderr = true
		}
	}
	if isSelectorCall("os", "Exit")(call) && len(call.Args) == 1 {
		st.sawExit = true
		st.exitFromCode, st.exitFromInternal = exitArgOrigins(call.Args[0], artIdent, st.origins)
	}
}

func applyExpr(st *pathState, e ast.Expr, artIdent, errIdent string) {
	if e == nil {
		return
	}
	ast.Inspect(e, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if ok {
			applyCall(st, call, artIdent, errIdent)
		}
		return true
	})
}

func applyStmt(st *pathState, s ast.Stmt, artIdent, errIdent string) {
	switch x := s.(type) {
	case *ast.AssignStmt:
		assignLastWrite(st.origins, x.Lhs, x.Rhs, artIdent)
		for _, rhs := range x.Rhs {
			applyExpr(st, rhs, artIdent, errIdent)
		}
	case *ast.DeclStmt:
		gen, ok := x.Decl.(*ast.GenDecl)
		if !ok {
			return
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			lhs := make([]ast.Expr, len(vs.Names))
			for i, name := range vs.Names {
				lhs[i] = name
			}
			assignLastWrite(st.origins, lhs, vs.Values, artIdent)
			for _, v := range vs.Values {
				applyExpr(st, v, artIdent, errIdent)
			}
		}
	case *ast.ExprStmt:
		applyExpr(st, x.X, artIdent, errIdent)
	case *ast.IncDecStmt:
		if id, ok := x.X.(*ast.Ident); ok {
			st.origins[id.Name] = identOrigin{}
		}
	default:
		ast.Inspect(s, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				applyCall(st, call, artIdent, errIdent)
			}
			return true
		})
	}
}

func walkSeq(stmts []ast.Stmt, st pathState, artIdent, errIdent string, out *[]pathState) {
	if st.sawExit || len(stmts) == 0 {
		*out = append(*out, st)
		return
	}
	s, rest := stmts[0], stmts[1:]
	switch x := s.(type) {
	case *ast.BlockStmt:
		walkSeq(append(append([]ast.Stmt{}, x.List...), rest...), st, artIdent, errIdent, out)
	case *ast.IfStmt:
		if x.Init != nil {
			applyStmt(&st, x.Init, artIdent, errIdent)
			if st.sawExit {
				*out = append(*out, st)
				return
			}
		}
		errBody, okBody, isErr := errNilBranches(x, errIdent)
		if isErr {
			errSt := clonePathState(st)
			errSt.tookErrorBranch = true
			var errDone []pathState
			walkSeq(blockList(errBody), errSt, artIdent, errIdent, &errDone)
			for _, p := range errDone {
				walkSeq(rest, p, artIdent, errIdent, out)
			}
			okSt := clonePathState(st)
			var okDone []pathState
			walkSeq(blockList(okBody), okSt, artIdent, errIdent, &okDone)
			for _, p := range okDone {
				walkSeq(rest, p, artIdent, errIdent, out)
			}
			return
		}
		thenSt := clonePathState(st)
		var thenDone []pathState
		walkSeq(blockList(x.Body), thenSt, artIdent, errIdent, &thenDone)
		for _, p := range thenDone {
			walkSeq(rest, p, artIdent, errIdent, out)
		}
		elseSt := clonePathState(st)
		var elseDone []pathState
		walkSeq(blockList(elseAsBlock(x.Else)), elseSt, artIdent, errIdent, &elseDone)
		for _, p := range elseDone {
			walkSeq(rest, p, artIdent, errIdent, out)
		}
	case *ast.ReturnStmt:
		*out = append(*out, st)
	default:
		applyStmt(&st, s, artIdent, errIdent)
		walkSeq(rest, st, artIdent, errIdent, out)
	}
}

func blockList(b *ast.BlockStmt) []ast.Stmt {
	if b == nil {
		return nil
	}
	return b.List
}

func analyzeMainPaths(fn *ast.FuncDecl, artIdent, errIdent string) []pathState {
	st := pathState{origins: map[string]identOrigin{}}
	var out []pathState
	walkSeq(fn.Body.List, st, artIdent, errIdent, &out)
	return out
}

func isOsArgsSlicedFromOne(e ast.Expr) bool {
	sl, ok := e.(*ast.SliceExpr)
	if !ok || sl.Low == nil || sl.High != nil || sl.Max != nil || sl.Slice3 {
		return false
	}
	if !isSelectorIdent("os", "Args")(sl.X) {
		return false
	}
	lit, ok := sl.Low.(*ast.BasicLit)
	return ok && lit.Kind == token.INT && lit.Value == "1"
}

func compositeHasKeyedElts(lit *ast.CompositeLit) bool {
	for _, elt := range lit.Elts {
		if _, ok := elt.(*ast.KeyValueExpr); ok {
			return true
		}
	}
	return false
}

func invocationLitArgsFromOsArgsSlice(lit *ast.CompositeLit, aliases map[string]bool) bool {
	if lit == nil {
		return false
	}
	if !compositeHasKeyedElts(lit) {
		if len(lit.Elts) == 0 {
			return false
		}
		return exprIsOsArgsSlice(lit.Elts[0], aliases)
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Args" {
			continue
		}
		return exprIsOsArgsSlice(kv.Value, aliases)
	}
	return false
}

func exprIsOsArgsSlice(e ast.Expr, aliases map[string]bool) bool {
	if isOsArgsSlicedFromOne(e) {
		return true
	}
	id, ok := e.(*ast.Ident)
	return ok && aliases[id.Name]
}

type identBinding struct {
	pos    token.Pos
	name   string
	osArgs bool
	lit    *ast.CompositeLit
}

func collectIdentBindings(fn *ast.FuncDecl) []identBinding {
	if fn == nil || fn.Body == nil {
		return nil
	}
	var out []identBinding
	record := func(pos token.Pos, name string, rhs ast.Expr) {
		b := identBinding{pos: pos, name: name}
		if isOsArgsSlicedFromOne(rhs) {
			b.osArgs = true
		} else if lit, ok := rhs.(*ast.CompositeLit); ok {
			b.lit = lit
		}
		out = append(out, b)
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
				record(s.Pos(), id.Name, rhs)
			}
		case *ast.ValueSpec:
			for i, name := range s.Names {
				if name == nil {
					continue
				}
				var rhs ast.Expr
				if i < len(s.Values) {
					rhs = s.Values[i]
				} else if len(s.Values) == 1 {
					rhs = s.Values[0]
				}
				record(s.Pos(), name.Name, rhs)
			}
		}
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].pos < out[j].pos })
	return out
}

func aliasesAt(bindings []identBinding, at token.Pos) (map[string]bool, map[string]*ast.CompositeLit) {
	aliases := map[string]bool{}
	lits := map[string]*ast.CompositeLit{}
	for _, b := range bindings {
		if b.pos >= at {
			break
		}
		if b.osArgs {
			aliases[b.name] = true
			delete(lits, b.name)
			continue
		}
		aliases[b.name] = false
		if b.lit != nil {
			lits[b.name] = b.lit
		} else {
			delete(lits, b.name)
		}
	}
	return aliases, lits
}

func exprIsInvocationFromOsArgs(e ast.Expr, aliases map[string]bool, lits map[string]*ast.CompositeLit) bool {
	switch x := e.(type) {
	case *ast.CompositeLit:
		return invocationLitArgsFromOsArgsSlice(x, aliases)
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			return exprIsInvocationFromOsArgs(x.X, aliases, lits)
		}
	case *ast.Ident:
		return invocationLitArgsFromOsArgsSlice(lits[x.Name], aliases)
	}
	return false
}

func runWiringInvocationProblems(fn *ast.FuncDecl) []string {
	if fn == nil || fn.Body == nil {
		return []string{"main has no body"}
	}
	var call *ast.CallExpr
	count := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || !isIdentCall("RunWiring")(c) {
			return true
		}
		count++
		call = c
		return true
	})
	if count != 1 {
		return []string{fmt.Sprintf("RunWiring is called %d time(s), want 1", count)}
	}
	if len(call.Args) != 1 {
		return []string{"RunWiring is not passed a single Invocation"}
	}
	aliases, lits := aliasesAt(collectIdentBindings(fn), call.Pos())
	if !exprIsInvocationFromOsArgs(call.Args[0], aliases, lits) {
		return []string{"RunWiring's Invocation.Args does not derive from os.Args[1:]. An empty Invocation, unsliced os.Args, an unrelated slice, or an alias assigned AFTER the call drops the operator's argv"}
	}
	return nil
}

// mainForwardingProblems walks main's body for clause 8 data flow from the
// single RunWiring result. The walk is control-flow and last-assignment
// aware: artifact streams must be written on the successful path, exitInternal
// must be the value reaching os.Exit on the non-nil-error path, and a later
// write of 0 (or any unrelated RHS) clears both origins.
func mainForwardingProblems(fn *ast.FuncDecl) []string {
	if fn == nil || fn.Body == nil {
		return []string{"main has no body"}
	}
	artIdent, errIdent, ok := runWiringResultBinding(fn)
	if !ok {
		return []string{"RunWiring's Artifacts and error results are not both bound to names. Clause 8 forwards that result; discarding either answer cannot forward it"}
	}
	var problems []string
	problems = append(problems, runWiringInvocationProblems(fn)...)

	paths := analyzeMainPaths(fn, artIdent, errIdent)
	if len(paths) == 0 {
		return append(problems, "main has no executable path")
	}

	var (
		anyError, anySuccess     bool
		missingSuccessStreams    bool
		missingSuccessExitCode   bool
		missingErrorReport       bool
		missingErrorExitInternal bool
		unrelatedWrite           bool
	)
	for _, p := range paths {
		if p.unrelatedStdout || p.unrelatedStderr {
			unrelatedWrite = true
		}
		if p.tookErrorBranch {
			anyError = true
			if !p.errReported {
				missingErrorReport = true
			}
			if !p.sawExit || !p.exitFromInternal {
				missingErrorExitInternal = true
			}
			continue
		}
		anySuccess = true
		if !p.stdoutFromArt || !p.stderrFromArt {
			missingSuccessStreams = true
		}
		if !p.sawExit || !p.exitFromCode {
			missingSuccessExitCode = true
		}
	}
	if unrelatedWrite {
		problems = append(problems, "a write to os.Stdout/os.Stderr is neither the RunWiring artifact stream nor the RunWiring error — unrelated bytes are not forwarding")
	}
	if !anySuccess {
		problems = append(problems, "there is no successful path on which to forward "+artIdent+".Stdout/"+artIdent+".Stderr and "+artIdent+".ExitCode")
	} else {
		if missingSuccessStreams {
			problems = append(problems, artIdent+".Stdout/.Stderr are not written to the process streams on the successful path")
		}
		if missingSuccessExitCode {
			problems = append(problems, "os.Exit on the successful path is not fed "+artIdent+".ExitCode (a later overwrite of the exit identifier is not forwarding)")
		}
	}
	if !anyError {
		problems = append(problems, "there is no non-nil error path that reports the returned error on os.Stderr and selects exitInternal")
	} else {
		if missingErrorReport {
			problems = append(problems, "the non-nil error path does not report the returned error on os.Stderr")
		}
		if missingErrorExitInternal {
			problems = append(problems, "os.Exit on the non-nil error path is not fed exitInternal (a later overwrite of the exit identifier is not selecting it)")
		}
	}
	return problems
}

func parseMainSnippet(t *testing.T, src string) *ast.FuncDecl {
	t.Helper()
	return snippetFunc(t, src, "main")
}

func snippetFunc(t *testing.T, src, name string) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		t.Fatalf("parse snippet: %v\n%s", err, src)
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if ok && fn.Name != nil && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("snippet has no func %s", name)
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

	overwriteWrongBranch := parseMainSnippet(t, `package main
func main() {
	art, err := RunWiring(Invocation{Args: os.Args[1:]})
	code := int(art.ExitCode)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = exitInternal
		os.Stdout.Write(art.Stdout)
		os.Stderr.Write(art.Stderr)
	}
	code = 0
	os.Exit(code)
}`)
	if problems := mainForwardingProblems(overwriteWrongBranch); len(problems) == 0 {
		t.Fatal("CONTROL: streams only on the error path plus overwriting code with 0 before os.Exit must redden — that body emits nothing on success and always exits 0")
	}

	lastWriteClears := parseMainSnippet(t, `package main
func main() {
	art, err := RunWiring(Invocation{Args: os.Args[1:]})
	code := int(art.ExitCode)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = exitInternal
	}
	code = 0
	os.Stdout.Write(art.Stdout)
	os.Stderr.Write(art.Stderr)
	os.Exit(code)
}`)
	if problems := mainForwardingProblems(lastWriteClears); len(problems) == 0 {
		t.Fatal("CONTROL: code := int(art.ExitCode); code = 0; os.Exit(code) must redden — last-write of 0 is not ExitCode or exitInternal")
	}
	if o := collectIdentOrigins(lastWriteClears, "art")["code"]; o.fromExitCode || o.fromInternal {
		t.Fatal("collectIdentOrigins still ORs origins across assignments: last write of 0 must clear both")
	}

	emptyInv := parseMainSnippet(t, `package main
func main() {
	art, err := RunWiring(Invocation{})
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
	if problems := runWiringInvocationProblems(emptyInv); len(problems) == 0 {
		t.Fatal("CONTROL: RunWiring(Invocation{}) must fail the argv check — it drops os.Args[1:]")
	}

	unsliced := parseMainSnippet(t, `package main
func main() {
	art, err := RunWiring(Invocation{Args: os.Args})
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
	if problems := runWiringInvocationProblems(unsliced); len(problems) == 0 {
		t.Fatal("CONTROL: RunWiring(Invocation{Args: os.Args}) must fail the argv check — the slice is not os.Args[1:]")
	}

	unrelatedArgs := parseMainSnippet(t, `package main
func main() {
	art, err := RunWiring(Invocation{Args: []string{"classify"}})
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
	if problems := runWiringInvocationProblems(unrelatedArgs); len(problems) == 0 {
		t.Fatal("CONTROL: RunWiring with an unrelated Args slice must fail the argv check")
	}

	slicedAway := parseMainSnippet(t, `package main
func main() {
	art, err := RunWiring(Invocation{Args: os.Args[1:]})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitInternal)
	}
	os.Stdout.Write(art.Stdout[:0])
	os.Stderr.Write(art.Stderr[:0])
	os.Exit(int(art.ExitCode))
}`)
	if problems := mainForwardingProblems(slicedAway); len(problems) == 0 {
		t.Fatal("CONTROL: os.Stdout.Write(art.Stdout[:0]) must redden — slicing the field emits nothing and is not forwarding")
	}

	timesZero := parseMainSnippet(t, `package main
func main() {
	art, err := RunWiring(Invocation{Args: os.Args[1:]})
	code := int(art.ExitCode) * 0
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = exitInternal * 0
	} else {
		os.Stdout.Write(art.Stdout)
		os.Stderr.Write(art.Stderr)
	}
	os.Exit(code)
}`)
	if problems := mainForwardingProblems(timesZero); len(problems) == 0 {
		t.Fatal("CONTROL: int(art.ExitCode)*0 and exitInternal*0 must redden — arithmetic is not a value-preserving alias")
	}

	lateAlias := parseMainSnippet(t, `package main
func main() {
	argv := []string{"classify"}
	art, err := RunWiring(Invocation{Args: argv})
	argv = os.Args[1:]
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
	if problems := runWiringInvocationProblems(lateAlias); len(problems) == 0 {
		t.Fatal("CONTROL: assigning argv = os.Args[1:] AFTER RunWiring(Invocation{Args: argv}) must fail — aliases are judged at the call site")
	}

	aliasBefore := parseMainSnippet(t, `package main
func main() {
	argv := os.Args[1:]
	art, err := RunWiring(Invocation{Args: argv})
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
	if problems := mainForwardingProblems(aliasBefore); len(problems) != 0 {
		t.Fatalf("CONTROL: argv := os.Args[1:] before the call must still pass, got %v", problems)
	}

	helper := snippetFunc(t, `package main
func helper() {
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	log.SetPrefix("x")
	os.Stdout = os.Stderr
	os.Stderr = os.Stdout
}
func main() {}
`, "helper")
	if countCalls(helper, isSelectorCall("log", "SetOutput")) == 0 {
		t.Fatal("CONTROL: the log.SetOutput detector is blind")
	}
	if countCalls(helper, isSelectorCall("log", "SetFlags")) == 0 {
		t.Fatal("CONTROL: the log.SetFlags detector is blind")
	}
	if countCalls(helper, isSelectorCall("log", "SetPrefix")) == 0 {
		t.Fatal("CONTROL: the log.SetPrefix detector is blind")
	}
	if !assignsOsStream(helper, "Stdout") {
		t.Fatal("CONTROL: helper assignment of os.Stdout must be flagged")
	}
	if !assignsOsStream(helper, "Stderr") {
		t.Fatal("CONTROL: helper assignment of os.Stderr must be flagged")
	}
	honestMain := parseMainSnippet(t, `package main
func main() {
	art, err := RunWiring(Invocation{Args: os.Args[1:]})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitInternal)
	}
	os.Stdout.Write(art.Stdout)
	os.Stderr.Write(art.Stderr)
	os.Exit(int(art.ExitCode))
}`)
	if assignsOsStream(honestMain, "Stdout") || assignsOsStream(honestMain, "Stderr") {
		t.Fatal("CONTROL: an honest forwarding body must not be flagged as assigning os.Stdout/os.Stderr")
	}
}

// TestSeal_Wiring_CapabilityProbeMatcherRejectsTwoSubstrings is GREEN today:
// it is the in-test control for the capabilities dispatch row. The two
// quoted-substring check accepted `"cmd/classify" "probe_version"`.
func TestSeal_Wiring_CapabilityProbeMatcherRejectsTwoSubstrings(t *testing.T) {
	defer red(t)

	malformed := []byte(`"cmd/classify" "probe_version"`)
	if bytes.Contains(malformed, []byte(`"cmd/classify"`)) && bytes.Contains(malformed, []byte(`"probe_version"`)) {
		// the old check would have accepted this payload
	} else {
		t.Fatal("CONTROL: the malformed payload must contain the two substrings the old check looked for")
	}
	if problems := capabilityProbeProblems(malformed, exitOK); len(problems) == 0 {
		t.Fatal("the two-substring malformed payload passed the probe matcher")
	}

	honest, err := json.Marshal(CapabilityReport{
		ProbeVersion:     probeVersion,
		Producer:         "cmd/classify",
		Capabilities:     Capabilities{FramedAuthoritativeStdin: true, DualDigestEcho: true, ContractVersionFlag: true},
		ContractVersions: []int{int(ContractV1), int(ContractV2)},
		Missing:          []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if problems := capabilityProbeProblems(honest, exitOK); len(problems) != 0 {
		t.Fatalf("CONTROL: a complete CapabilityReport at exit 0 must pass, got %v", problems)
	}

	incomplete, err := json.Marshal(CapabilityReport{
		ProbeVersion:     probeVersion,
		Producer:         "cmd/classify",
		Capabilities:     Capabilities{ContractVersionFlag: true, DualDigestEcho: true},
		ContractVersions: []int{int(ContractV1), int(ContractV2)},
		Missing:          []string{"framed_authoritative_stdin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if problems := capabilityProbeProblems(incomplete, ExitCode(exitCapabilityIncomplete)); len(problems) != 0 {
		t.Fatalf("CONTROL: a CapabilityReport at exit %d with missing must pass, got %v", exitCapabilityIncomplete, problems)
	}
	if problems := capabilityProbeProblems(incomplete, exitOK); len(problems) == 0 {
		t.Fatal("CONTROL: exit 0 with a non-empty missing list must redden")
	}
}

// TestSeal_Wiring_ScaffoldConfigMatcherRejectsSubstring is GREEN today: it
// is the in-test control for the RunWiring init rows. Those rows used to
// accept any file containing the byte substring `"scaffold"`.
func TestSeal_Wiring_ScaffoldConfigMatcherRejectsSubstring(t *testing.T) {
	defer red(t)

	if problems := scaffoldConfigProblems([]byte(`{"scaffold": true}`)); len(problems) == 0 {
		t.Fatal(`{"scaffold": true} without a schema or rule table passed the scaffold matcher`)
	}
	if problems := scaffoldConfigProblems([]byte(`not json but "scaffold"`)); len(problems) == 0 {
		t.Fatal("a non-JSON document containing the substring \"scaffold\" passed the matcher")
	}
	malformed := []byte(`{"schema_version":1,"scaffold":true,"unmatched_risk":"high","rules":[]}` + "\n")
	if problems := scaffoldConfigProblems(malformed); len(problems) == 0 {
		t.Fatal("a document with scaffold:true and an empty rule table passed — parseConfig requires a usable table")
	}

	dir := t.TempDir()
	out := filepath.Join(dir, "risk-paths.json")
	var code int
	stdoutOf(t, func() { code = cmdInit([]string{"-worktree", dir, "-out", out}) })
	if code != exitOK {
		t.Fatalf("CONTROL: cmdInit exited %d, want 0", code)
	}
	honest, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("CONTROL: cmdInit wrote nothing: %v", err)
	}
	if problems := scaffoldConfigProblems(honest); len(problems) != 0 {
		t.Fatalf("CONTROL: cmdInit's own scaffold must pass the matcher, got %v\n%s", problems, honest)
	}
	if !bytes.Contains(malformed, []byte(`"scaffold"`)) {
		t.Fatal("CONTROL: the rejected payload must contain the old substring so this row judges the defect")
	}
}
