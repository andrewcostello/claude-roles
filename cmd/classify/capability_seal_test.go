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
	"encoding/json"
	"flag"
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
func TestSeal_InstalledDigestSource_YieldsHexOrErrors(t *testing.T) {
	defer red(t)

	src := digestSource
	if src == nil {
		t.Fatal("digestSource is nil — B1's body must install it")
	}
	cfgSHA, diffSHA, err := src.ConsumedDigests()
	if err != nil {
		// Legal: this process consumed no config and no diff. The contract is
		// that it errors rather than returning empty strings.
		if cfgSHA != "" || diffSHA != "" {
			t.Errorf("ConsumedDigests returned an error AND values %q/%q — pick one", cfgSHA, diffSHA)
		}
		return
	}
	for name, sha := range map[string]string{"config": cfgSHA, "diff": diffSHA} {
		if len(sha) != 64 {
			t.Errorf("%s digest %q is not 64 hex characters", name, sha)
		}
		if sha != strings.ToLower(sha) {
			t.Errorf("%s digest %q is not lowercase hex", name, sha)
		}
		for _, c := range sha {
			if !strings.ContainsRune("0123456789abcdef", c) {
				t.Errorf("%s digest %q is not hex", name, sha)
				break
			}
		}
	}
}

// ─── small helpers ───────────────────────────────────────────────────────────

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func flagNames(fs *flag.FlagSet) []string {
	var out []string
	fs.VisitAll(func(f *flag.Flag) { out = append(out, f.Name) })
	return out
}
