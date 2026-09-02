package main

// Seals for GO-1-1's WIRING CONTRACT: (contract x -out x -json) -> artifact set
// + exit code.
//
// WHY THESE ROWS DRIVE A BUILT BINARY AND NOT RunWiring. RunWiring (wiring.go)
// is a stub returning ErrWiringNotImplemented; GO-1-3 owns its body. A row that
// reached production only through that stub would be red today for a reason
// that has nothing to do with the mapping, would stay red whatever anyone did
// to emit(), and would therefore detect nothing at all. So the subject here is
// the shipped path — main() -> run() -> emit()/persist() — reached by execing
// a build of the current tree. RunWiring clause 8 is what makes that the same
// subject after GO-1-3 lands: main() forwards RunWiring's streams and exit code
// and adds nothing, so a body that changes any answer below changes these rows
// too. When the body lands, these rows keep judging the artifact that ships.
//
// WHAT WAS MISSING, stated as the defect rather than as a topic. Every existing
// emission seal calls EmitV1/EmitV2/WriteV2Sidecar as a LIBRARY
// (contract_seal_test.go, readset_seal_test.go's goldens): they judge what each
// emitter writes when CALLED, never which one the wiring CHOSE. run()'s own
// contract says so in as many words (main.go:301-304). The measured
// consequence: rewriting emit()'s ContractV2 arm to call EmitV1 — so that
// `-contract-version 2 -json` silently ships the legacy payload — leaves the
// whole suite green at 0cfdb57. TestSeal_Wiring_JSONEmitterIsChosenByTheContract
// is the row that reddens for it, and it reddens on the ANSWER (the wrapper's
// keys, its contract_version, and its two digests bound to the exact bytes the
// process consumed), not on the fact that an emitter ran.
//
// NON-VACUITY. Every row judges its twin invocation in the same call: the arm
// that must come out the other way. A binary that emitted one constant, or that
// ignored a flag, fails the twin rather than passing both legs. liveClassify
// adds the third guard — the artifact under test is a build of the CURRENT tree
// and is byte-different from the frozen baseline, so no row here can quietly be
// measuring a binary that cannot respond to a fix.
//
// NOT RE-SEALED HERE, deliberately:
//   - the stale-sidecar teardown a v1 re-run owes
//     (TestSeal_Repair_V1RerunMustNotLeaveAStaleV2Sidecar, repair_seal_test.go).
//   - the consumed-bytes/digest swap
//     (TestSeal_Repair_ResolveConfigDual_ConsumedBytesMustBeTheCertifiedBytes).
//     Re-derived rather than taken on trust: rewriting ConsumedDigests' return
//     to `hexSHA256(s.diff), hexSHA256(s.config)` reddens exactly that row. It
//     is sealed; a second row for it would be duplication, not coverage.
//
// ONE OVERLAP, DECLARED. Row C's v1 arm — a ContractV1 run creates no sidecar —
// is already asserted by that repair row's CONTROL_v1_run_creates_no_sidecar
// subtest, and it is measured: a persist() whose ContractV1 arm writes the
// sidecar instead of removing it reddens both. It is kept because Row C's three
// arms are one table and a table missing its negative arm is not a mapping. It
// is not what Row C adds; what Row C adds is the run-state's shape under both
// contracts and the whole-directory assertion for the no--out arm.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ─── fixtures for the mapping rows ───────────────────────────────────────────

// wiringScene is one scratch directory holding the inputs of a mapping row: an
// absolute config path and an absolute diff path.
//
// Absolute, both of them, and the run's working directory is the scratch dir:
// the config search has a worktree-relative tail and resolveRepo would shell
// out to git, so a row that let either resolve against the package directory
// would be reading a tree it does not control. Every invocation below also
// passes -no-git for the second half of that reason.
type wiringScene struct {
	dir    string
	config string
	diff   string
}

// newWiringScene stages the critical-money fixture — the one whose
// classification exercises a full panel, a non-empty components list and the
// human PR gate, so the payloads the rows compare are not near-empty objects.
func newWiringScene(t *testing.T) wiringScene {
	t.Helper()
	pkg, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	diff := filepath.Join(dir, "fixture.diff")
	if err := os.WriteFile(diff, []byte(diffFor("apps/finance-domain/wallet/service/debit.go")), 0o600); err != nil {
		t.Fatal(err)
	}
	return wiringScene{dir: dir, config: filepath.Join(pkg, exampleConfigPath), diff: diff}
}

// run invokes the live producer against this scene. The trailing argument is
// always the diff file, so every row's argv reads as the command line an
// operator would type.
func (s wiringScene) run(t *testing.T, bin string, args ...string) liveRun {
	t.Helper()
	full := append([]string{"-no-git", "-config", s.config}, args...)
	return runLive(t, bin, s.dir, nil, append(full, s.diff)...)
}

// sha256File is the digest of the bytes on disk at p, computed here rather than
// asked of the binary: the wrapper's echo is only worth anything if the value
// it carries is checked against an independently derived one.
func sha256File(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p) // #nosec G304 -- a fixture this test staged
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// dirEntries lists the scene directory, sorted, so a row can assert that an
// invocation created NOTHING rather than that one named file is absent.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// ─── ROW A — which emitter did the wiring choose? ────────────────────────────

// THE ROW THE MEASURED MUTATION REDDENS.
//
// The mapping's -json leg: with -json, the contract in force decides WHICH wire
// lands on stdout, and the choice is emit()'s switch (main.go:520). Three
// invocations differing only in -contract-version, judged together:
//
//	-contract-version 1  -> the bare v1 payload      (top-level "risk")
//	-contract-version 2  -> the response WRAPPER     (response_version + two
//	                        digests + a v2 classification)
//	(flag absent)        -> the v1 payload, byte-for-byte the same as the
//	                        explicit 1 — defaultContractVersion is ContractV1
//	                        and stays so for the whole coexistence period.
//
// WHAT MAKES IT NON-VACUOUS. Each leg is checked on what the run ANSWERED, not
// on the fact that it produced JSON:
//
//   - the v2 leg's top-level key set must be EXACTLY the wrapper's four, so a
//     v1 payload arriving here fails on both what is missing and what is
//     present;
//   - the wrapper's classification must carry contract_version 2, which no v1
//     payload has at any nesting level;
//   - the two digests must equal SHA-256 over the exact config and diff bytes
//     this test staged, computed here. That is the wrapper's whole claim — it
//     binds the response to the bytes the process consumed — and it is the leg
//     a wrapper emitted with plausible-looking constants fails.
//   - and the twin control: the v1 and v2 stdouts must DIFFER. A binary that
//     answered one constant for every contract fails there even if a future
//     edit weakened everything above it.
func TestSeal_Wiring_JSONEmitterIsChosenByTheContract(t *testing.T) {
	defer red(t)

	bin := liveClassify(t)
	s := newWiringScene(t)

	v1 := s.run(t, bin, "-json", "-contract-version", "1")
	v2 := s.run(t, bin, "-json", "-contract-version", "2")
	dflt := s.run(t, bin, "-json")

	for name, r := range map[string]liveRun{"v1": v1, "v2": v2, "default": dflt} {
		if r.exit != 0 {
			t.Fatalf("%s: -json run exited %d, want 0\n%s", name, r.exit, r.all())
		}
	}

	// ── the v1 leg: the bare legacy payload, and nothing of the wrapper.
	if !hasKey(t, []byte(v1.stdout), "risk") {
		t.Errorf("-contract-version 1 -json: stdout has no top-level \"risk\" — the v1 payload is the classification itself, unwrapped.\nkeys: %v", topKeys(t, []byte(v1.stdout)))
	}
	for _, k := range []string{"response_version", "computed_config_sha256", "computed_diff_sha256", "classification"} {
		if hasKey(t, []byte(v1.stdout), k) {
			t.Errorf("-contract-version 1 -json: stdout carries the wrapper key %q. A v1 run emits no v2 facts; emit()'s ContractV1 arm must reach EmitV1 and nothing else.", k)
		}
	}

	// ── the v2 leg: exactly the wrapper, and a v2 classification inside it.
	wantKeys := []string{"classification", "computed_config_sha256", "computed_diff_sha256", "response_version"}
	if got := topKeys(t, []byte(v2.stdout)); !sameStrings(got, wantKeys) {
		t.Fatalf("THE WIRING CHOSE THE WRONG EMITTER FOR -contract-version 2.\n"+
			"stdout top-level keys = %v\nwant the response wrapper's %v\n"+
			"A key set carrying \"risk\" and no \"response_version\" is the v1 payload: emit()'s ContractV2 arm (main.go:520) is reaching EmitV1. Every frozen consumer would keep parsing it, and the operator who asked for v2 would be shipped v1 with exit 0.\nstdout:\n%s", got, wantKeys, v2.stdout)
	}
	var wrapper struct {
		ResponseVersion int             `json:"response_version"`
		ConfigSHA       string          `json:"computed_config_sha256"`
		DiffSHA         string          `json:"computed_diff_sha256"`
		Classification  json.RawMessage `json:"classification"`
	}
	if err := json.Unmarshal([]byte(v2.stdout), &wrapper); err != nil {
		t.Fatalf("-contract-version 2 -json: stdout is not a response wrapper: %v\n%s", err, v2.stdout)
	}
	if wrapper.ResponseVersion != responseVersion {
		t.Errorf("response_version = %d, want %d", wrapper.ResponseVersion, responseVersion)
	}
	if !hasKey(t, wrapper.Classification, "contract_version") {
		t.Errorf("the wrapper's classification carries no contract_version — that field is ClassificationV2's and a v1 payload has none at any depth.\n%s", wrapper.Classification)
	} else {
		var env struct {
			ContractVersion int `json:"contract_version"`
		}
		if err := json.Unmarshal(wrapper.Classification, &env); err != nil {
			t.Fatalf("the wrapper's classification is not an object: %v", err)
		}
		if env.ContractVersion != int(ContractV2) {
			t.Errorf("the wrapper's classification declares contract_version %d, want %d", env.ContractVersion, int(ContractV2))
		}
	}

	// ── the digests: the wrapper's only claim, checked against bytes this test
	// hashed itself. An echo nobody re-derives is not an echo.
	if want := sha256File(t, s.config); wrapper.ConfigSHA != want {
		t.Errorf("computed_config_sha256 = %q, want SHA-256 of the config this run consumed = %q", wrapper.ConfigSHA, want)
	}
	if want := sha256File(t, s.diff); wrapper.DiffSHA != want {
		t.Errorf("computed_diff_sha256 = %q, want SHA-256 of the diff this run consumed = %q", wrapper.DiffSHA, want)
	}

	// ── the default: v1, byte-for-byte, timestamp aside.
	if !hasKey(t, []byte(dflt.stdout), "risk") || hasKey(t, []byte(dflt.stdout), "response_version") {
		t.Errorf("with no -contract-version the run did not emit v1. defaultContractVersion is ContractV1 and must stay so until both frozen consumers migrate (contract.go:70).\nkeys: %v", topKeys(t, []byte(dflt.stdout)))
	}
	if a, b := normaliseClassifiedAt(t, []byte(dflt.stdout)), normaliseClassifiedAt(t, []byte(v1.stdout)); string(a) != string(b) {
		t.Errorf("the default contract and an explicit -contract-version 1 emitted different bytes.\ndefault:\n%s\nexplicit 1:\n%s", a, b)
	}

	// ── CONTROL, judged in this same call: the two contracts must not agree.
	// This is the leg a binary that ignored -contract-version fails, and it
	// holds even if every assertion above were weakened to a presence check.
	if v1.stdout == v2.stdout {
		t.Errorf("-contract-version 1 and -contract-version 2 produced IDENTICAL stdout. Whichever emitter that is, the contract is not selecting it, and every row above is measuring one constant.")
	}

	// ── the stream split, which is what makes \"the wrapper is on stdout\" a
	// usable claim: with -out the run logs two lines, and they must not land in
	// the document a consumer parses.
	withOut := s.run(t, bin, "-json", "-contract-version", "2", "-out", filepath.Join(s.dir, "run.json"), "-task", "GO-1-1-SEAL")
	if withOut.exit != 0 {
		t.Fatalf("-json -out -contract-version 2 exited %d, want 0\n%s", withOut.exit, withOut.all())
	}
	dec := json.NewDecoder(strings.NewReader(withOut.stdout))
	var one json.RawMessage
	if err := dec.Decode(&one); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, withOut.stdout)
	}
	if dec.More() {
		t.Errorf("stdout carries MORE than one JSON document — a consumer decoding it gets the wrapper and then something else.\n%s", withOut.stdout)
	}
	if !strings.Contains(withOut.stderr, "run state written to") {
		t.Errorf("the run-state log line is not on stderr; stderr was %q. If it moved to stdout it would sit inside the payload.", withOut.stderr)
	}
	if strings.Contains(withOut.stdout, "run state written to") {
		t.Errorf("a log line landed on STDOUT beside the wrapper:\n%s", withOut.stdout)
	}
}

// ─── ROW B — the -json axis, and the report the contract does not reach ──────

// Without -json the run prints the human report, and it is THE SAME REPORT
// under both contracts: emit()'s first line returns before the contract switch
// is consulted (main.go:521-524), so the contract has no observable effect on
// this half of the mapping. Sealed as an equality rather than as a substring,
// because "the contract leaks nothing into the report" is the claim.
//
// CONTROL, same call: the same invocation WITH -json must produce JSON and not
// the report. Without it, a binary that printed the report on every path — or
// printed nothing at all — would pass the equality above.
//
// And the artifact leg: neither run was given -out, so neither may leave a file
// behind. Asserted over the whole directory listing, not over one expected name.
func TestSeal_Wiring_ReportIsTheSameUnderBothContractsAndWritesNothing(t *testing.T) {
	defer red(t)

	bin := liveClassify(t)
	s := newWiringScene(t)
	before := dirEntries(t, s.dir)

	r1 := s.run(t, bin, "-contract-version", "1")
	r2 := s.run(t, bin, "-contract-version", "2")
	if r1.exit != 0 || r2.exit != 0 {
		t.Fatalf("report runs exited %d and %d, want 0 and 0\n%s\n%s", r1.exit, r2.exit, r1.all(), r2.all())
	}
	if !strings.HasPrefix(r1.stdout, "=== CLASSIFICATION ===") {
		t.Fatalf("without -json the run did not print the report:\n%s", r1.stdout)
	}
	if r1.stdout != r2.stdout {
		t.Errorf("the human report DIFFERS between contracts. emit() returns the report before it reaches the contract switch, so the contract must be unobservable here.\nv1:\n%s\nv2:\n%s", r1.stdout, r2.stdout)
	}

	// CONTROL — the -json axis really is what selects the payload.
	j := s.run(t, bin, "-json", "-contract-version", "2")
	if j.exit != 0 {
		t.Fatalf("-json run exited %d\n%s", j.exit, j.all())
	}
	if strings.HasPrefix(j.stdout, "=== CLASSIFICATION ===") {
		t.Errorf("-json still printed the human report — the two rows above are then comparing one constant.\n%s", j.stdout)
	}
	if j.stdout == r1.stdout {
		t.Errorf("-json and no -json produced identical stdout; -json is not being read.")
	}

	// The artifact leg: no -out, so no artifact, whatever the contract.
	if got := dirEntries(t, s.dir); !sameStrings(got, before) {
		t.Errorf("a run with no -out changed the directory: %v -> %v. The run-state is written only for -out (main.go:441-444) and the sidecar only for ContractV2 AND -out.", before, got)
	}
}

// ─── ROW C — -out x contract -> the artifact set ─────────────────────────────

// The artifact half of the mapping, all three arms judged in one call:
//
//	-out, contract 2  -> run-state AND <out>.v2.json
//	-out, contract 1  -> run-state, and NO sidecar was ever created
//	no -out, either   -> neither file, and no other file
//
// The run-state stays v1 under BOTH contracts — cmd/gates and cmd/iterate read
// it and are frozen (main.go:446-449) — so the row also compares the two
// run-states' classifications and requires them EQUAL. That is the leg a body
// that "upgraded" the shared file to v2 fails, and it is why the sidecar exists
// at all.
//
// THE v1 ARM IS AN ACKNOWLEDGED OVERLAP, not a new fact:
// TestSeal_Repair_V1RerunMustNotLeaveAStaleV2Sidecar already carries
// "a v1 run creates no sidecar" as a control, and the two redden together. It
// stays because a three-arm table missing its negative arm is not a mapping —
// the row's own contribution is the two legs below it, which nothing else
// asserts: the shared run-state's classification is IDENTICAL under both
// contracts, and a run with no -out writes no file at all.
func TestSeal_Wiring_OutAndContractDecideTheArtifactSet(t *testing.T) {
	defer red(t)

	bin := liveClassify(t)

	// ── arm 1: -out with ContractV2.
	two := newWiringScene(t)
	twoOut := filepath.Join(two.dir, "run.json")
	a := two.run(t, bin, "-contract-version", "2", "-out", twoOut, "-task", "GO-1-1-SEAL")
	if a.exit != 0 {
		t.Fatalf("-out -contract-version 2 exited %d, want 0\n%s", a.exit, a.all())
	}
	twoState := readJSONFile(t, twoOut)
	sidecarPath := V2SidecarPath(twoOut)
	sidecar := readJSONFile(t, sidecarPath)

	// The sidecar is the wrapper inside a file envelope, not a bare payload.
	if got := topKeys(t, mustMarshal(t, sidecar)); !sameStrings(got, []string{"response", "schema_version"}) {
		t.Errorf("%s top-level keys = %v, want [response schema_version]", sidecarPath, got)
	}
	resp, ok := sidecar["response"].(map[string]any)
	if !ok {
		t.Fatalf("the sidecar's response is not an object: %T", sidecar["response"])
	}
	if got := topKeys(t, mustMarshal(t, resp)); !sameStrings(got, []string{"classification", "computed_config_sha256", "computed_diff_sha256", "response_version"}) {
		t.Errorf("the sidecar's response is not the wrapper; keys = %v", got)
	}
	if want := sha256File(t, two.config); resp["computed_config_sha256"] != want {
		t.Errorf("the sidecar's computed_config_sha256 = %v, want %q — the sidecar's whole value is that a reader of it alone can check the dual digest echo", resp["computed_config_sha256"], want)
	}
	if want := sha256File(t, two.diff); resp["computed_diff_sha256"] != want {
		t.Errorf("the sidecar's computed_diff_sha256 = %v, want %q", resp["computed_diff_sha256"], want)
	}

	// ── arm 2: -out with ContractV1, from a directory that never had a sidecar.
	one := newWiringScene(t)
	oneOut := filepath.Join(one.dir, "run.json")
	b := one.run(t, bin, "-contract-version", "1", "-out", oneOut, "-task", "GO-1-1-SEAL")
	if b.exit != 0 {
		t.Fatalf("-out -contract-version 1 exited %d, want 0\n%s", b.exit, b.all())
	}
	if _, err := os.Stat(V2SidecarPath(oneOut)); err == nil {
		t.Errorf("a ContractV1 run CREATED %s. A v1 run emits no v2 facts, so no v2 envelope may be readable beside its run-state.", V2SidecarPath(oneOut))
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", V2SidecarPath(oneOut), err)
	}
	oneState := readJSONFile(t, oneOut)

	// ── the shared run-state is v1 under both contracts.
	twoCls, okA := twoState["classification"].(map[string]any)
	oneCls, okB := oneState["classification"].(map[string]any)
	if !okA || !okB {
		t.Fatalf("a run-state carries no classification object (v2 %v, v1 %v)", okA, okB)
	}
	if _, present := twoCls["contract_version"]; present {
		t.Errorf("the ContractV2 run wrote a v2 classification into the SHARED run-state. cmd/gates and cmd/iterate unmarshal that file into closed structs and marshal it back; the v2 envelope lands in the sidecar and nowhere else (main.go:446-449).")
	}
	delete(twoCls, "classified_at")
	delete(oneCls, "classified_at")
	if x, y := string(mustMarshal(t, twoCls)), string(mustMarshal(t, oneCls)); x != y {
		t.Errorf("the run-state classification differs between contracts.\ncontract 2:\n%s\ncontract 1:\n%s", x, y)
	}

	// ── arm 3, and the control for arms 1 and 2: no -out writes no artifact,
	// under the contract that DOES write one when -out is given.
	none := newWiringScene(t)
	before := dirEntries(t, none.dir)
	c := none.run(t, bin, "-json", "-contract-version", "2")
	if c.exit != 0 {
		t.Fatalf("-contract-version 2 without -out exited %d, want 0\n%s", c.exit, c.all())
	}
	if got := dirEntries(t, none.dir); !sameStrings(got, before) {
		t.Errorf("a ContractV2 run with NO -out wrote into the directory: %v -> %v. The sidecar is written only when the contract is ContractV2 AND -out was given; with no run-state path there is nothing to derive one from.", before, got)
	}
	if !hasKey(t, []byte(c.stdout), "response_version") {
		t.Errorf("the no--out control did not emit the v2 wrapper on stdout, so it is not the same invocation as arm 1 minus -out:\n%s", c.stdout)
	}
}

// ─── ROW D — an unaccepted contract: exit 3, and nothing touched ─────────────

// `-contract-version 3` is the operator's argv being wrong, and run() validates
// the contract BEFORE resolveConfigPath and before any input is read
// (main.go:322-328). Two claims, and the second is the one prose alone cannot
// hold:
//
//  1. exit 3 (exitInvalid), with a message that names the rejected value AND
//     the accepted set — an operator told only "invalid" has to read the source
//     to learn that 3 is not a typo for something that exists.
//  2. the run writes and removes NOTHING. A run-state and a v2 sidecar left by
//     an earlier ContractV2 run survive BYTE-IDENTICAL. Ordering is the whole
//     content of that claim: a validation that happened after persist() would
//     still exit 3 and would already have rewritten both files.
//
// CONTROL, judged in the same call and the reason leg 2 is not vacuous: the
// SAME argv with -contract-version 2 exits 0 and DOES change the run-state. The
// files are therefore reachable and writable by this invocation; leaving them
// untouched is a decision, not an accident of the fixture.
func TestSeal_Wiring_UnacceptedContractExitsInvalidAndTouchesNothing(t *testing.T) {
	defer red(t)

	bin := liveClassify(t)
	s := newWiringScene(t)
	out := filepath.Join(s.dir, "run.json")
	sidecar := V2SidecarPath(out)

	// Seed both artifacts with a real ContractV2 run, so the bytes that must
	// survive are bytes production produced rather than bytes a seal invented.
	if seed := s.run(t, bin, "-contract-version", "2", "-out", out, "-task", "GO-1-1-SEAL"); seed.exit != 0 {
		t.Fatalf("seeding run exited %d, want 0\n%s", seed.exit, seed.all())
	}
	stateBefore := fileDigest(t, out)
	sidecarBefore := fileDigest(t, sidecar)

	bad := s.run(t, bin, "-json", "-contract-version", "3", "-out", out, "-task", "GO-1-1-SEAL")

	if bad.exit != exitInvalid {
		t.Errorf("-contract-version 3 exited %d, want exitInvalid (%d)\n%s", bad.exit, exitInvalid, bad.all())
	}
	report := bad.all()
	for _, want := range []string{flagContractVersion, `"3"`, "accepted values are 1 and 2"} {
		if !strings.Contains(report, want) {
			t.Errorf("the rejection does not contain %q. It must name the flag, the value received and the accepted set — %q alone sends the operator to the source.\n%s", want, "invalid", report)
		}
	}
	// -json was passed and no payload may follow an exit 3: the run never
	// classified anything, so there is nothing for a consumer to parse.
	var any0 json.RawMessage
	if err := json.Unmarshal([]byte(bad.stdout), &any0); err == nil {
		t.Errorf("an exit-3 run emitted a parseable JSON document on stdout. A consumer that trusts exit codes is fine; one that parses stdout would read a verdict this run never made.\n%s", bad.stdout)
	}

	if got := fileDigest(t, out); got != stateBefore {
		t.Errorf("the run-state was rewritten by a run that exited 3 (%s -> %s). The contract is validated before any input is read, so a rejected contract costs nothing on disk.", stateBefore[:12], got[:12])
	}
	if got := fileDigest(t, sidecar); got != sidecarBefore {
		t.Errorf("the v2 sidecar was rewritten or removed by a run that exited 3 (%s -> %s). It still asserts the seeding run's verdict, and that run is the only one that made it.", sidecarBefore[:12], got[:12])
	}

	// CONTROL — the same argv at an accepted contract reaches those files.
	ok := s.run(t, bin, "-json", "-contract-version", "2", "-out", out, "-task", "GO-1-1-CONTROL")
	if ok.exit != 0 {
		t.Fatalf("CONTROL -contract-version 2 exited %d, want 0\n%s", ok.exit, ok.all())
	}
	if fileDigest(t, out) == stateBefore {
		t.Errorf("CONTROL: an accepted contract did not change the run-state either, so \"unchanged\" above proves nothing about ordering — this invocation cannot write that file at all.")
	}
}

// ─── ROW E — a mistyped FLAG and a mistyped CONTRACT owe different codes ─────
//
// parseInvocationFlags clause 2 (wiring.go) turns on this and states it as the
// reason the flag half and the contract half are split: a flag error is
// exitFlagError and a contract error is exitInvalid, and a function that
// returned both on one channel would owe them different codes with no way to
// tell them apart. The numbers are the scaffold's choice — under
// ContinueOnError the flag package assigns none — so they are sealed here
// rather than inherited.
//
// Both legs are judged in one call, against the same binary and the same
// otherwise-valid argv, with a third leg proving that argv exits 0 untouched.
// Two error codes that had silently collapsed to one number would pass either
// leg alone.
func TestSeal_Wiring_FlagErrorAndContractErrorAreDifferentExits(t *testing.T) {
	defer red(t)

	bin := liveClassify(t)
	s := newWiringScene(t)

	badFlag := s.run(t, bin, "-json", "-no-such-flag")
	badContract := s.run(t, bin, "-json", "-contract-version", "3")
	good := s.run(t, bin, "-json")

	if good.exit != exitOK {
		t.Fatalf("CONTROL: the same argv without an error exited %d, want %d — the two error legs below would then be measuring a binary that fails on everything\n%s", good.exit, exitOK, good.all())
	}
	if badFlag.exit != exitFlagError {
		t.Errorf("an undefined flag exited %d, want exitFlagError (%d)\n%s", badFlag.exit, exitFlagError, badFlag.all())
	}
	if !strings.Contains(badFlag.stderr, "not defined") {
		t.Errorf("the flag error was not reported on stderr; stderr = %q", badFlag.stderr)
	}
	if badContract.exit != exitInvalid {
		t.Errorf("-contract-version 3 exited %d, want exitInvalid (%d)\n%s", badContract.exit, exitInvalid, badContract.all())
	}
	if badFlag.exit == badContract.exit {
		t.Errorf("A MISTYPED FLAG AND A MISTYPED CONTRACT BOTH EXITED %d. They are different failures owed different codes (wiring.go, parseInvocationFlags clause 2): a caller cannot tell \"you passed a flag I do not have\" from \"you asked for a contract I do not emit\".", badFlag.exit)
	}

	// Both codes must be inside the closed set the scaffold declares. Closed is
	// the operative word: a code outside it is a defect, not a new feature.
	declared := map[int]bool{}
	for _, c := range DeclaredExitCodes {
		declared[c] = true
	}
	for name, code := range map[string]int{"flag error": badFlag.exit, "unaccepted contract": badContract.exit, "success": good.exit} {
		if !declared[code] {
			t.Errorf("%s exited %d, which is not in DeclaredExitCodes %v", name, code, DeclaredExitCodes)
		}
	}
}

// ─── small helpers ───────────────────────────────────────────────────────────

func readJSONFile(t *testing.T, p string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(p) // #nosec G304 -- a path this test handed the binary as -out
	if err != nil {
		t.Fatalf("%s was not written: %v", p, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s is not a JSON object: %v\n%s", p, err, data)
	}
	return m
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
