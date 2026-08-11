package main

// Shared machinery for the unit B1 seals.
//
// THESE FILES ARE SEALS, NOT AN IMPLEMENTATION. Every scaffold function in
// contract.go, capability.go and readset.go panics today, so most rows here are
// RED on purpose. Red is the correct state until the body author implements to
// the scaffold's doc comments. A green row is either (a) a fact about code that
// already exists at baseline, recorded so a future edit is noticed, or (b) a
// mistake — investigate before relaxing anything.
//
// Two rules the seals hold themselves to:
//
//   - Every row is judged alongside a CONTROL in the same call: the benign twin
//     that must come out the other way. A row with no control cannot tell an
//     implementation from a constant.
//   - Every input is one PRODUCTION can emit. Where it is not — and there is
//     exactly one such case, v1 `config_scaffold: false` on the wire — the seal
//     says so in place and seals the non-producibility instead.

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

// red turns a scaffold panic into an ordinary test failure.
//
// Without it the first `panic("B1: not implemented")` takes the whole test
// binary down and every later seal reports nothing at all, which would hide the
// shape of the work from the body author. With it, each seal fails on its own
// line and the failure list IS the implementation checklist.
//
// It is deliberately NOT a tolerance: once the body lands, nothing panics and
// this defer is inert. If it ever fires again it is naming a real panic.
func red(t *testing.T) {
	t.Helper()
	if r := recover(); r != nil {
		t.Errorf("SEAL RED — panicked: %v", r)
	}
}

// ─── fixtures ────────────────────────────────────────────────────────────────

// exampleConfigPath and scaffoldConfigPath are the two rule tables the seals
// classify against.
//
// example-monorepo.json is the evenplay-mono table validated against 98 real PR
// merges (see realConfig in main_test.go). scaffold-config.json is the VERBATIM
// output of `classify init` — production's own scaffold generator, copied in so
// that the config_scaffold: true row is fed an input production really produces
// rather than one a seal author invented.
const (
	exampleConfigPath  = "testdata/example-monorepo.json"
	scaffoldConfigPath = "testdata/scaffold-config.json"
)

// sealFixture is one (config, diff) pair, and the reason it is in the set.
type sealFixture struct {
	// Name is the golden file stem.
	Name string
	// ConfigPath is passed verbatim as -config, and lands verbatim in the v1
	// payload's config_path, so the differential compares like with like.
	ConfigPath string
	// Files are the changed paths the diff carries.
	Files []string
	// Diff, when non-empty, overrides the diff built from Files. Used where the
	// changed LINES matter (gate signals scan content, not paths).
	Diff string
	// Why records what this fixture is the only fixture to exercise, so nobody
	// prunes the set without noticing what they are dropping.
	Why string
}

// sealFixtures is the T10 fixture set for both contracts.
//
// Between them they cover: both panel intensities the producer can emit, an
// empty and a populated components list, an empty and a populated gate_signals
// list (the one place GateHit's inherited `sample,omitempty` is observable), an
// empty and a populated unmatched_files, and config_scaffold both omitted (v1)
// and true.
func sealFixtures() []sealFixture {
	debug := "apps/skillstrike-mobile/src/components/DebugPanel.tsx"
	return []sealFixture{
		{
			Name:       "wallet-critical",
			ConfigPath: exampleConfigPath,
			Files:      []string{"apps/finance-domain/wallet/service/debit.go"},
			Why:        "critical money path: FULL panel, components non-empty, human_pr_gate, recheck floor medium, config_scaffold omitted",
		},
		{
			Name:       "docs-only",
			ConfigPath: exampleConfigPath,
			Files:      []string{"docs/plans/2026-07-29-graph-spine.md", "README.md"},
			Why:        "the only SINGLE-panel fixture, and the only one with an empty components list",
		},
		{
			Name:       "unmatched-fail-closed",
			ConfigPath: exampleConfigPath,
			Files:      []string{"apps/brand-new-service/handler.go"},
			Why:        "the only fixture with a non-empty unmatched_files",
		},
		{
			Name:       "gate-signal",
			ConfigPath: exampleConfigPath,
			Files:      []string{debug},
			Diff: "diff --git a/" + debug + " b/" + debug + "\n--- a/" + debug + "\n+++ b/" + debug +
				"\n@@ -1 +1 @@\n-const show = false\n+const show = __DEV__ || process.env.SHOW_DEBUG === '1'\n",
			Why: "the only fixture with a non-empty gate_signals, so GateHit's inherited sample,omitempty is observable",
		},
		{
			Name:       "scaffold-config",
			ConfigPath: scaffoldConfigPath,
			Files:      []string{"apps/finance-domain/wallet/service/debit.go"},
			Why:        "the only fixture with config_scaffold true — produced by `classify init`, not hand-written",
		},
	}
}

// diffText is the fixture's diff bytes.
func (f sealFixture) diffText() string {
	if f.Diff != "" {
		return f.Diff
	}
	return diffFor(f.Files...)
}

// classification builds the fixture's classification IN PROCESS, by the same
// call sequence run() uses, so that what the seals compare against the pinned
// binary is the real production path and not a reconstruction.
//
// The repo is the one run() derives under -no-git: validateInput returns
// Repo{Worktree: opts.worktree, BaseRef: opts.base} untouched, and the flag
// defaults are "." and "origin/main" (main.go:205-206). -no-git is mandatory
// here for the reason SemanticEquivalentV1's contract gives: a live worktree
// would make resolveRepo's git state an uncontrolled input.
func (f sealFixture) classification(t *testing.T) *Classification {
	t.Helper()
	data, err := os.ReadFile(f.ConfigPath)
	if err != nil {
		t.Fatalf("%s: read config %s: %v", f.Name, f.ConfigPath, err)
	}
	cfg, err := parseConfig(data)
	if err != nil {
		t.Fatalf("%s: config %s is invalid: %v", f.Name, f.ConfigPath, err)
	}
	d := f.diffText()
	return buildClassification(cfg, parseDiffFiles(d), d, Repo{Worktree: ".", BaseRef: "origin/main"}, f.ConfigPath)
}

// ─── the pinned binary ───────────────────────────────────────────────────────

// pinnedBinary is the tracked v1 producer at baseline. It is a FIXTURE, not a
// build artifact: it must not be rebuilt, because the whole point of the
// differential is that one side of the comparison predates this unit.
const pinnedBinary = "./classify"

// pinnedV1 runs the pinned binary over the fixture and returns its v1 stdout.
//
// It FAILS rather than skips when the binary is absent. A differential that
// quietly skips when it cannot find its baseline is the vacuous-pass failure in
// a different costume: it would go green on exactly the machine where it
// mattered least to run.
func pinnedV1(t *testing.T, f sealFixture) []byte {
	t.Helper()
	if _, err := os.Stat(pinnedBinary); err != nil {
		t.Fatalf("pinned v1 producer %s is missing: %v — it is a tracked fixture and the differential cannot run without it", pinnedBinary, err)
	}
	diffPath := filepath.Join(t.TempDir(), "fixture.diff")
	if err := os.WriteFile(diffPath, []byte(f.diffText()), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(pinnedBinary, "-json", "-no-git", "-config", f.ConfigPath, diffPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: pinned binary failed: %v\nstderr: %s", f.Name, err, stderr.String())
	}
	return stdout.Bytes()
}

// ─── JSON helpers ────────────────────────────────────────────────────────────

// topKeys returns the top-level key set of a JSON object, sorted.
func topKeys(t *testing.T, b []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, b)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// hasKey reports whether a JSON object carries a top-level key, present with
// ANY value including a zero one. Presence, not truthiness: the whole unit
// exists because those are different facts.
func hasKey(t *testing.T, b []byte, key string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, b)
	}
	_, ok := m[key]
	return ok
}

// sameStrings compares two string slices element-wise, in order.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sortedCopy is a sorted copy; the input is left alone because several seals
// assert on generator-produced order.
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// containsAll reports which of want is missing from got.
func containsAll(got, want []string) []string {
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	var missing []string
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	return missing
}

// ─── the capability registry ─────────────────────────────────────────────────

// withHooks installs the three registry hooks for the duration of the test and
// restores whatever was there before.
//
// Tests using it must NOT call t.Parallel: the registry is package state, and a
// seal that raced another seal's registry would be exactly the kind of
// non-deterministic evidence this unit is trying to eliminate.
func withHooks(t *testing.T, reg ContractFlagRegistrar, dig DigestSource, framed FramedStdinReader) {
	t.Helper()
	oldReg, oldDig, oldFramed := contractFlagRegistrar, digestSource, framedStdinReader
	t.Cleanup(func() {
		contractFlagRegistrar, digestSource, framedStdinReader = oldReg, oldDig, oldFramed
	})
	contractFlagRegistrar, digestSource, framedStdinReader = reg, dig, framed
}

// fakeRegistrar is a stand-in ContractFlagRegistrar. It exists only so a seal
// can install SOMETHING non-nil and observe the probe change its answer.
type fakeRegistrar struct{}

func (fakeRegistrar) RegisterContractVersionFlag(fs *flag.FlagSet) *string {
	return fs.String(flagContractVersion, defaultContractVersion.String(), "seal fake")
}

// fakeDigests returns fixed digests so a golden of the response wrapper is
// deterministic without depending on which bytes the body's real DigestSource
// happens to have consumed.
type fakeDigests struct {
	config, diff string
	err          error
}

func (f fakeDigests) ConsumedDigests() (string, string, error) { return f.config, f.diff, f.err }

// fakeFramed is a non-nil FramedStdinReader for the honesty seal only. B1 must
// never install one of these in production — that is B2's unit.
type fakeFramed struct{}

func (fakeFramed) ReadFramedRequest(*os.File) ([]byte, []byte, error) {
	return nil, nil, errNotB1
}

// errNotB1 is returned by fakeFramed to make it obvious in any output that this
// object is a seal fixture and not an implementation.
var errNotB1 = errors.New("seal fixture: B2 owns the frame format")

// stdoutOf captures os.Stdout for the duration of fn.
//
// cmdCapabilities' contract is about stdout specifically — "writes exactly one
// JSON object to stdout, and writes nothing to stdout on any other path" — so
// the seal has to look at the real descriptor rather than at an injected
// writer.
func stdoutOf(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		done <- b.String()
	}()
	func() {
		defer func() {
			os.Stdout = old
			_ = w.Close()
		}()
		fn()
	}()
	out := <-done
	_ = r.Close()
	return out
}
