package main

// Seals for capability.go — the probe that cannot lie.
//
// The honesty mechanism is the point of this file. A capability is reported
// present IFF its implementation object is installed in the registry; there is
// no boolean literal anywhere in the probe. That property is directly
// assertable, and the assertion is the one below that installs a hook and
// demands the answer change. A probe that ignored the registry and returned
// constants would pass a golden test and fail this one.
//
// None of these tests may call t.Parallel: they mutate package-level registry
// state and capture os.Stdout.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ─── the honesty property ────────────────────────────────────────────────────

// Every capability is true iff its registry variable is non-nil, over all eight
// installation states.
//
// Eight rows, not three: with one hook at a time you cannot tell "reads the
// registry" from "returns the first hook's nil-ness three times". The full
// lattice also seals the Missing/exit biconditional in the same pass, which the
// scaffold calls out as contractual — "a report and an exit code that disagree
// is a broken probe".
func TestSeal_ProbeHonesty_EveryCapabilityTracksItsRegistryVar(t *testing.T) {
	defer red(t)

	for _, mask := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
		wantFramed := mask&1 != 0
		wantDigest := mask&2 != 0
		wantFlag := mask&4 != 0

		var framed FramedStdinReader
		var dig DigestSource
		var reg ContractFlagRegistrar
		if wantFramed {
			framed = fakeFramed{}
		}
		if wantDigest {
			dig = fakeDigests{config: strings.Repeat("a", 64), diff: strings.Repeat("b", 64)}
		}
		if wantFlag {
			reg = fakeRegistrar{}
		}
		withHooks(t, reg, dig, framed)

		caps := probeCapabilities()
		if caps.FramedAuthoritativeStdin != wantFramed {
			t.Errorf("mask %d: framed_authoritative_stdin = %v, want %v (framedStdinReader installed: %v)", mask, caps.FramedAuthoritativeStdin, wantFramed, wantFramed)
		}
		if caps.DualDigestEcho != wantDigest {
			t.Errorf("mask %d: dual_digest_echo = %v, want %v (digestSource installed: %v)", mask, caps.DualDigestEcho, wantDigest, wantDigest)
		}
		if caps.ContractVersionFlag != wantFlag {
			t.Errorf("mask %d: contract_version_flag = %v, want %v (contractFlagRegistrar installed: %v)", mask, caps.ContractVersionFlag, wantFlag, wantFlag)
		}

		// Missing is requiredCapabilities minus the installed ones, in
		// requiredCapabilities order, so the output is deterministic and
		// goldenable.
		var wantMissing []string
		for _, name := range requiredCapabilities {
			switch name {
			case "framed_authoritative_stdin":
				if !wantFramed {
					wantMissing = append(wantMissing, name)
				}
			case "dual_digest_echo":
				if !wantDigest {
					wantMissing = append(wantMissing, name)
				}
			case "contract_version_flag":
				if !wantFlag {
					wantMissing = append(wantMissing, name)
				}
			default:
				t.Fatalf("requiredCapabilities gained %q — this seal enumerates the tuple and must be extended with a probe_version bump", name)
			}
		}
		rep := buildCapabilityReport()
		if !sameStrings(rep.Missing, wantMissing) {
			t.Errorf("mask %d: Missing = %v, want %v (in requiredCapabilities order)", mask, rep.Missing, wantMissing)
		}
		if rep.Missing == nil {
			t.Errorf("mask %d: Missing is nil — it must be an empty slice, never null", mask)
		}

		// The biconditional.
		code := cmdCapabilitiesExit(t, nil)
		if len(wantMissing) == 0 && code != 0 {
			t.Errorf("mask %d: exit %d with an EMPTY Missing — must be 0", mask, code)
		}
		if len(wantMissing) > 0 && code != exitCapabilityIncomplete {
			t.Errorf("mask %d: exit %d with Missing %v — must be %d", mask, code, wantMissing, exitCapabilityIncomplete)
		}
	}
}

// cmdCapabilitiesExit runs the probe subcommand and returns its exit code,
// discarding stdout. Split out so the lattice above stays readable.
func cmdCapabilitiesExit(t *testing.T, args []string) int {
	t.Helper()
	var code int
	stdoutOf(t, func() { code = cmdCapabilities(args) })
	return code
}

// The drift-catcher, stated as its own row because it is the specific failure
// the scaffold names: "a seal that sets a hook and observes no change in the
// probe output has found the exact drift this design forbids."
//
// B2 will assign framedStdinReader with NO edit to the probe. If that
// assignment does not move the probe's answer, the probe has grown a boolean
// literal somewhere and every preflight decision made from it is a guess.
func TestSeal_ProbeHonesty_InstallingAHookMovesTheOutput(t *testing.T) {
	defer red(t)

	withHooks(t, nil, nil, nil)
	before := stdoutOf(t, func() { _ = cmdCapabilities(nil) })

	withHooks(t, nil, nil, fakeFramed{}) // exactly what B2's body does
	after := stdoutOf(t, func() { _ = cmdCapabilities(nil) })

	if before == after {
		t.Fatalf("installing framedStdinReader changed nothing in the probe output. The probe is not reading the registry.\n%s", before)
	}
	if !strings.Contains(before, `"framed_authoritative_stdin": false`) {
		t.Errorf("with no reader installed the probe does not report framed_authoritative_stdin: false\n%s", before)
	}
	if !strings.Contains(after, `"framed_authoritative_stdin": true`) {
		t.Errorf("with a reader installed the probe does not report framed_authoritative_stdin: true\n%s", after)
	}
}

// ─── the probe's wire ────────────────────────────────────────────────────────

// The exact bytes, encoded like the classification output: two-space indent,
// trailing newline.
//
// The golden is written inline rather than in a file because it is short and
// because the point is that a reader can check it against CapabilityReport's
// field order by eye.
func TestSeal_WriteCapabilityReport_ExactBytes(t *testing.T) {
	defer red(t)

	rep := CapabilityReport{
		ProbeVersion:     probeVersion,
		Producer:         "cmd/classify",
		Capabilities:     Capabilities{FramedAuthoritativeStdin: false, DualDigestEcho: true, ContractVersionFlag: true},
		ContractVersions: []int{1, 2},
		Missing:          []string{"framed_authoritative_stdin"},
	}
	var b strings.Builder
	if err := writeCapabilityReport(&b, rep); err != nil {
		t.Fatalf("writeCapabilityReport errored: %v", err)
	}
	want := `{
  "probe_version": 1,
  "producer": "cmd/classify",
  "capabilities": {
    "framed_authoritative_stdin": false,
    "dual_digest_echo": true,
    "contract_version_flag": true
  },
  "contract_versions": [
    1,
    2
  ],
  "missing": [
    "framed_authoritative_stdin"
  ]
}
`
	if b.String() != want {
		t.Errorf("writeCapabilityReport bytes differ.\ngot:\n%s\nwant:\n%s", b.String(), want)
	}

	// Every capability key is present even when false — an absent key must
	// never mean false here, because a consumer has to tell "this producer says
	// no" from "this producer never heard of the question".
	var back map[string]any
	if err := json.Unmarshal([]byte(b.String()), &back); err != nil {
		t.Fatal(err)
	}
	caps, ok := back["capabilities"].(map[string]any)
	if !ok {
		t.Fatal("capabilities is not an object")
	}
	for _, name := range requiredCapabilities {
		if _, present := caps[name]; !present {
			t.Errorf("capability %q is absent from the report", name)
		}
	}

	// An empty Missing marshals as [], never null.
	rep.Missing = []string{}
	rep.Capabilities.FramedAuthoritativeStdin = true
	var b2 strings.Builder
	if err := writeCapabilityReport(&b2, rep); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b2.String(), `"missing": []`) {
		t.Errorf("an empty Missing must marshal as [], never null:\n%s", b2.String())
	}
}

// ContractVersions is derived from the closed set ParseContractVersion accepts,
// ascending — not written out by hand. The seal cannot see the derivation, so
// it seals the consequence: every listed version must parse, and every version
// that parses must be listed.
func TestSeal_CapabilityReport_ContractVersionsMatchTheClosedSet(t *testing.T) {
	defer red(t)

	withHooks(t, fakeRegistrar{}, fakeDigests{}, fakeFramed{})
	rep := buildCapabilityReport()

	if rep.ProbeVersion != probeVersion {
		t.Errorf("probe_version = %d, want %d", rep.ProbeVersion, probeVersion)
	}
	if len(rep.ContractVersions) == 0 {
		t.Fatal("contract_versions is empty")
	}
	for i, v := range rep.ContractVersions {
		if i > 0 && rep.ContractVersions[i-1] >= v {
			t.Errorf("contract_versions %v is not ascending", rep.ContractVersions)
			break
		}
	}
	for _, v := range rep.ContractVersions {
		if _, err := ParseContractVersion(strconv.Itoa(v)); err != nil {
			t.Errorf("contract_versions lists %d, which ParseContractVersion rejects: %v", v, err)
		}
	}
	for _, want := range []ContractVersion{ContractV1, ContractV2} {
		found := false
		for _, v := range rep.ContractVersions {
			if ContractVersion(v) == want {
				found = true
			}
		}
		if !found {
			t.Errorf("contract_versions %v omits %d, which this binary can emit", rep.ContractVersions, int(want))
		}
	}
	if rep.Producer == "" {
		t.Error("producer is empty")
	}
}

// The subcommand's argv contract: any argument is INVALID_INPUT, exit 3, and
// NOTHING on stdout. A probe that tolerated stray argv would let a caller
// believe it had asked something it had not.
//
// CONTROL: the empty-args call in the same test writes a report.
func TestSeal_CmdCapabilities_RejectsStrayArgv(t *testing.T) {
	defer red(t)

	withHooks(t, fakeRegistrar{}, fakeDigests{}, fakeFramed{})

	control := stdoutOf(t, func() {
		if code := cmdCapabilities(nil); code != 0 {
			t.Errorf("CONTROL: cmdCapabilities(nil) with everything installed exited %d, want 0", code)
		}
	})
	if !strings.Contains(control, `"probe_version"`) {
		t.Errorf("CONTROL: no report on stdout:\n%s", control)
	}

	for _, args := range [][]string{{"-json"}, {"extra"}, {"", ""}, {"capabilities"}} {
		var code int
		out := stdoutOf(t, func() { code = cmdCapabilities(args) })
		if code != exitInvalid {
			t.Errorf("cmdCapabilities(%q) exited %d, want %d (INVALID_INPUT)", args, code, exitInvalid)
		}
		if strings.TrimSpace(out) != "" {
			t.Errorf("cmdCapabilities(%q) wrote to stdout on the reject path:\n%s", args, out)
		}
	}
}

// When a REQUIRED capability is absent the report is STILL written — naming
// which one is absent is the point of the exit code.
func TestSeal_CmdCapabilities_ReportsOnTheIncompletePath(t *testing.T) {
	defer red(t)

	withHooks(t, fakeRegistrar{}, fakeDigests{}, nil) // B1 complete, B2 not yet
	var code int
	out := stdoutOf(t, func() { code = cmdCapabilities(nil) })

	if code != exitCapabilityIncomplete {
		t.Errorf("exit %d, want %d", code, exitCapabilityIncomplete)
	}
	if !strings.Contains(out, `"missing"`) || !strings.Contains(out, "framed_authoritative_stdin") {
		t.Errorf("the report was not written on the incomplete path, or does not name what is missing:\n%s", out)
	}
	var rep CapabilityReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("stdout is not exactly one JSON object: %v\n%s", err, out)
	}
	if !sameStrings(rep.Missing, []string{"framed_authoritative_stdin"}) {
		t.Errorf("missing = %v, want exactly [framed_authoritative_stdin]", rep.Missing)
	}
}

// The exit-code CHOICE, pinned. 4 is the first value unambiguous across the
// family: classify uses 0 and 3, and cmd/iterate uses 1 (ITERATE) and 2
// (ESCALATE). Reusing 3 would conflate "your argv is wrong" with "this binary
// is too old" — and the second is the operator's signal to rebuild.
func TestSeal_ExitCodesAreDistinct(t *testing.T) {
	t.Parallel()
	if exitCapabilityIncomplete != 4 {
		t.Errorf("exitCapabilityIncomplete = %d, want 4", exitCapabilityIncomplete)
	}
	for _, taken := range []int{0, 1, 2, exitInvalid} {
		if exitCapabilityIncomplete == taken {
			t.Errorf("exitCapabilityIncomplete collides with %d, which already carries a meaning in this tool family", taken)
		}
	}
	if probeSubcommand != "capabilities" {
		t.Errorf("probeSubcommand = %q — the full contractual invocation is `classify capabilities`", probeSubcommand)
	}
	if flagContractVersion != "contract-version" {
		t.Errorf("flagContractVersion = %q, want %q", flagContractVersion, "contract-version")
	}
}

// ─── what B1's body must install, and what it must not ───────────────────────

// At B1 completion the registry holds exactly two of the three hooks.
//
// RED TODAY ON TWO COUNTS, and both are the point:
//   - contractFlagRegistrar and digestSource are nil; B1's body installs them.
//   - framedStdinReader must STAY nil. It is B2's, and a B1 body that installed
//     a placeholder to make the probe exit 0 would be making the probe lie in
//     the one direction that matters: REQUIRED preflight would pass against a
//     producer with no framed channel, and the policy bytes would come from an
//     agent-writable file.
//
// This test reads the LIVE registry, so it must run before any test that
// installs fakes leaves them behind. withHooks restores on cleanup, so ordering
// is not load-bearing; the assertion is about the package's own initialisation.
func TestSeal_B1InstallsItsOwnHooksAndNotB2s(t *testing.T) {
	defer red(t)

	if contractFlagRegistrar == nil {
		t.Error("contractFlagRegistrar is nil — B1's body installs it; installing the implementation IS the capability flip, and no edit to the probe is permitted")
	}
	if digestSource == nil {
		t.Error("digestSource is nil — B1's body installs the unframed source (config bytes read at main.go:417, diff bytes at main.go:486)")
	}
	if framedStdinReader != nil {
		t.Errorf("framedStdinReader is installed (%T) — that is unit B2's, and installing it here would make the probe report a framed authoritative channel this binary does not have. REQUIRED preflight would then pass on a producer whose policy bytes still come from an agent-writable file.", framedStdinReader)
	}
}

// The installed registrar's own contract: it must register under exactly
// flagContractVersion, default to defaultContractVersion.String(), and the
// pointer it returns must receive the parsed value.
//
// Registering through the interface rather than calling flag.String in
// parseFlags is what makes the capability observable BEFORE flag.Parse runs,
// which the probe subcommand requires — it dispatches ahead of flag parsing
// (main.go:176-187).
func TestSeal_InstalledRegistrar_RegistersTheRealFlag(t *testing.T) {
	defer red(t)

	reg := contractFlagRegistrar
	if reg == nil {
		t.Fatal("contractFlagRegistrar is nil — B1's body must install it (see TestSeal_B1InstallsItsOwnHooksAndNotB2s)")
	}

	fs := flag.NewFlagSet("seal", flag.ContinueOnError)
	fs.SetOutput(nopWriter{})
	got := reg.RegisterContractVersionFlag(fs)
	if got == nil {
		t.Fatal("RegisterContractVersionFlag returned a nil pointer")
	}

	f := fs.Lookup(flagContractVersion)
	if f == nil {
		t.Fatalf("no flag named %q was registered; registered flags: %v", flagContractVersion, flagNames(fs))
	}
	if f.DefValue != defaultContractVersion.String() {
		t.Errorf("default = %q, want defaultContractVersion.String() = %q", f.DefValue, defaultContractVersion.String())
	}
	// The default must itself be a value the parser accepts. A default the
	// binary rejects is a binary that cannot be run without the flag.
	if _, err := ParseContractVersion(f.DefValue); err != nil {
		t.Errorf("the registered default %q is rejected by ParseContractVersion: %v", f.DefValue, err)
	}
	if err := fs.Parse([]string{"-" + flagContractVersion, "2"}); err != nil {
		t.Fatalf("parsing -%s 2 failed: %v", flagContractVersion, err)
	}
	if *got != "2" {
		t.Errorf("the returned pointer holds %q after parsing -%s 2 — it is not the flag's destination", *got, flagContractVersion)
	}
}

// The installed digest source yields two lowercase-hex SHA-256 strings, or an
// error. It must never return an empty string for a channel it did not consume.
//
// ─── AMENDED (CV-OPUS), NOT STRUCK ───────────────────────────────────────────
//
// IT WAS VACUOUS, AND PROVED SO RATHER THAN ARGUED. As written, the row asked
// the installed source for its digests and returned early on error, having
// asserted nothing about any value. The error is the ONLY answer it could ever
// get: ConsumedDigests raises unless BOTH sawConfig and sawDiff are set
// (capability.go:373-388), the test process consumed neither, and no test in
// this package called readDiff. Running the row ALONE under coverage measured
// ConsumedDigests at 90.0% and hexSHA256 at 0.0% — the hex, length and
// lowercase assertions below the early return never executed once. A green row
// over an unexecuted body is the vacuous shape this module has now produced
// five times.
//
// AMENDED RATHER THAN STRUCK because the property is real and unsealed: the
// wrapper's dual digest echo is the only thing binding a v2 response to the
// bytes that produced it, and nothing else checks the INSTALLED source's
// success path. Striking the row would delete the obligation along with the
// hole. The name is kept so the record is traceable; what changed is that both
// halves of "hex OR errors" are now reached and judged, in one call:
//
//	leg 1  the installed source IS the recorder production writes into
//	leg 2  a recorder that consumed NOTHING errors, blanks both values, and
//	       names both channels
//	leg 3  a recorder that consumed only the config errors naming the DIFF —
//	       the control that leg 2 is not passing on a constant error
//	leg 4  a recorder driven through PRODUCTION'S OWN READERS answers the
//	       SHA-256 of the exact bytes those readers consumed, computed here
//	leg 5  one byte changed in the config moves the config digest and leaves
//	       the diff digest alone
//
// Leg 4 is what makes this seal what the source ANSWERED rather than that it
// was called: the expected digests are derived in this test from the staged
// bytes, so a source returning a plausible 64-character constant fails. It goes
// through loadConfig and readDiff — the two call sites that record
// (main.go:671, main.go:743) — rather than calling recordConfig/recordDiff
// directly, so the row also covers the wiring from the readers to the recorder.
//
// The recorder is swapped for a fresh one for the duration, following
// repair_seal_test.go:593: driving the process-wide unframedDigests would leave
// consumed state behind for whatever test ran next.
func TestSeal_InstalledDigestSource_YieldsHexOrErrors(t *testing.T) {
	defer red(t)

	src := digestSource
	if src == nil {
		t.Fatal("digestSource is nil — B1's body must install it")
	}

	// ── LEG 1 — the installed source is the process recorder, not some other
	// object that happens to satisfy the interface. Everything below drives the
	// recorder; without this the rest would be about a type production may not
	// use.
	installed, ok := src.(*unframedDigestSource)
	if !ok {
		t.Fatalf("digestSource is %T, want *unframedDigestSource — B1 installs unframedDigests (capability.go:400-403), and the digests the wrapper echoes are the ones THAT object recorded", src)
	}
	if installed != unframedDigests {
		t.Fatal("digestSource holds an *unframedDigestSource that is NOT unframedDigests. loadConfig and readDiff record into the package variable (main.go:671, main.go:743); a second instance would echo digests over bytes nothing consumed.")
	}

	// From here the row drives a FRESH recorder installed in both slots, and
	// restores whatever was there.
	savedRecorder, savedSource := unframedDigests, digestSource
	t.Cleanup(func() { unframedDigests, digestSource = savedRecorder, savedSource })

	// ── LEG 2 — consumed nothing: error, both values empty, both channels named.
	unframedDigests = &unframedDigestSource{}
	digestSource = unframedDigests
	cfgSHA, diffSHA, err := digestSource.ConsumedDigests()
	if err == nil {
		t.Fatalf("a source that consumed nothing returned %q/%q and no error — an empty digest in the wrapper is indistinguishable from a digest over empty bytes", cfgSHA, diffSHA)
	}
	if cfgSHA != "" || diffSHA != "" {
		t.Errorf("ConsumedDigests returned an error AND values %q/%q — pick one", cfgSHA, diffSHA)
	}
	for _, channel := range []string{"config", "diff"} {
		if !strings.Contains(err.Error(), channel) {
			t.Errorf("the unread-channel error does not name %q: %v", channel, err)
		}
	}

	// Stage the bytes. A private copy of the real table, so the path is one no
	// other test has certified a read for and consumeCertifiedConfigRead does
	// the ordinary read.
	dir := t.TempDir()
	cfgBytes, err := os.ReadFile(exampleConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "risk-paths.json")
	if err := os.WriteFile(cfgPath, cfgBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	diffBytes := []byte(diffFor("apps/finance-domain/wallet/service/debit.go"))
	diffPath := filepath.Join(dir, "fixture.diff")
	if err := os.WriteFile(diffPath, diffBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	wantCfg := hex.EncodeToString(sha256Of(cfgBytes))
	wantDiff := hex.EncodeToString(sha256Of(diffBytes))

	// ── LEG 3 — only the config consumed: still an error, and it names the DIFF
	// and not the config. The control for leg 2: a source that errored on a
	// constant would name both channels here too.
	unframedDigests = &unframedDigestSource{}
	digestSource = unframedDigests
	if _, err := loadConfig(cfgPath); err != nil {
		t.Fatalf("loadConfig(%s): %v", cfgPath, err)
	}
	cfgSHA, diffSHA, err = digestSource.ConsumedDigests()
	if err == nil {
		t.Fatalf("with the diff channel unconsumed ConsumedDigests answered %q/%q — a half-consumed process has nothing to echo", cfgSHA, diffSHA)
	}
	if strings.Contains(err.Error(), "config") || !strings.Contains(err.Error(), "diff") {
		t.Errorf("the error names the wrong channel: %v. The config WAS consumed; only the diff was not.", err)
	}

	// ── LEG 4 — both consumed, through production's readers: the answers are
	// the digests of exactly those bytes.
	unframedDigests = &unframedDigestSource{}
	digestSource = unframedDigests
	if _, err := loadConfig(cfgPath); err != nil {
		t.Fatalf("loadConfig(%s): %v", cfgPath, err)
	}
	if _, err := readDiff([]string{diffPath}); err != nil {
		t.Fatalf("readDiff(%s): %v", diffPath, err)
	}
	cfgSHA, diffSHA, err = digestSource.ConsumedDigests()
	if err != nil {
		t.Fatalf("both channels were consumed through loadConfig and readDiff, and ConsumedDigests still errored: %v", err)
	}
	for name, got := range map[string]string{"config": cfgSHA, "diff": diffSHA} {
		if len(got) != 64 {
			t.Errorf("%s digest %q is not 64 hex characters", name, got)
		}
		if got != strings.ToLower(got) {
			t.Errorf("%s digest %q is not lowercase hex", name, got)
		}
		if strings.Trim(got, "0123456789abcdef") != "" {
			t.Errorf("%s digest %q is not hex", name, got)
		}
	}
	if cfgSHA != wantCfg {
		t.Errorf("the config digest is %q, want SHA-256 of the bytes loadConfig consumed = %q. The wrapper's echo binds the response to what was read; a digest over anything else binds it to nothing.", cfgSHA, wantCfg)
	}
	if diffSHA != wantDiff {
		t.Errorf("the diff digest is %q, want SHA-256 of the bytes readDiff consumed = %q", diffSHA, wantDiff)
	}

	// ── LEG 5 — the digests track CONTENT, per channel. One byte added to the
	// config must move the config digest and leave the diff digest where it was.
	// This is the leg a source digesting the PATH, or digesting one buffer for
	// both channels, fails.
	unframedDigests = &unframedDigestSource{}
	digestSource = unframedDigests
	if err := os.WriteFile(cfgPath, append(append([]byte(nil), cfgBytes...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(cfgPath); err != nil {
		t.Fatalf("loadConfig after the one-byte edit: %v", err)
	}
	if _, err := readDiff([]string{diffPath}); err != nil {
		t.Fatalf("readDiff: %v", err)
	}
	movedCfg, sameDiff, err := digestSource.ConsumedDigests()
	if err != nil {
		t.Fatalf("ConsumedDigests after the one-byte edit: %v", err)
	}
	if movedCfg == wantCfg {
		t.Errorf("the config digest did not change when the config bytes did (%q both times) — it is not a digest over the consumed bytes", movedCfg)
	}
	if sameDiff != wantDiff {
		t.Errorf("the DIFF digest changed when only the CONFIG bytes changed: %q -> %q. The two channels are not separate.", wantDiff, sameDiff)
	}
}

// sha256Of is this file's own digest, deliberately not hexSHA256: the expected
// value in leg 4 must be derived independently of the function under seal, or
// the comparison is the implementation agreeing with itself.
func sha256Of(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// ─── small helpers ───────────────────────────────────────────────────────────

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func flagNames(fs *flag.FlagSet) []string {
	var out []string
	fs.VisitAll(func(f *flag.Flag) { out = append(out, f.Name) })
	return out
}
