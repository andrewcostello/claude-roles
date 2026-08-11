package main

// B1 REPAIR SEALS — panel waves 1-3.
//
// These rows are RED on purpose. They are the failing seals for the root causes
// the B1 panel deduplicated (review-artifacts/b1/DEDUP.md): the dark
// ResolveConfigDual and its caller (wave 1), the sidecar lifecycle in persist()
// (wave 2), and the two "no verdict is not a verdict" call sites (wave 3).
//
// Nothing here edits an existing seal and nothing here touches production code.
//
// THE STANDARD EVERY ROW BELOW IS HELD TO. This unit has produced four vacuous
// seals — one where swapping the digests passed, one whose red-trigger could
// never fire because it measured a frozen artifact, one with a collapsed input
// space, one an absence-only assertion — plus one passable by executing
// nothing. So each row here carries:
//
//   - a CONTROL judged in the same call: the benign twin that must come out the
//     other way, so the row can tell an implementation from a constant. A body
//     that refuses everything fails the controls; a body that permits
//     everything fails the defect legs.
//   - a stated PRODUCTION ROUTE to the input, verified rather than asserted.
//   - a FIXTURE-VALIDITY check where the fixture could stop exhibiting the
//     defect (two config tables that stopped disagreeing, a file that stayed
//     readable), which raises rather than passing quietly.
//   - a PROOF OF EXECUTION where the pass condition could otherwise be met by
//     doing nothing (readset_seal_test.go:1077 is the pattern).
//
// WHY FOUR OF THESE ROWS BUILD A BINARY. The panel's headline finding is that
// ResolveConfigDual is dark: implemented, sealed green by six seals, and never
// called from main. A seventh seal that calls the function again would be the
// same non-evidence. So the wave-1 rows observe the LIVE config resolution
// end-to-end, through a binary built from the current tree.
//
// They must not use ./classify. That is the pinned v1 differential baseline —
// a tracked FIXTURE that predates this unit — and a seal measuring it can never
// go red when the source is fixed. That is exactly the vacuity the panel found
// in the recorded env-var seal (contract_seal_test.go:985). liveClassify builds
// to a scratch path and proves the artifact it returns is not the pinned one.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ─── the live producer ───────────────────────────────────────────────────────

// liveClassify builds the CURRENT tree to a scratch path and returns it.
//
// THE TRAP, which two authors have already sprung: `go build` with no -o writes
// cmd/classify/classify, which is the tracked pinned v1 producer the
// differential compares against. One rebuild makes both differential seals
// tautologies. This function always passes -o, and it hashes the pinned fixture
// before and after so that a future edit which reintroduces a default-path
// build fails here loudly instead of silently destroying the baseline.
//
// It also proves the returned artifact is NOT the pinned one. Without that, a
// mistake in the build could leave these rows measuring the frozen baseline —
// which cannot change when main.go is fixed, so the rows could never go green
// and, worse, could never have gone red for the right reason.
func liveClassify(t *testing.T) string {
	t.Helper()

	pinnedBefore := fileDigest(t, pinnedBinary)

	out := filepath.Join(t.TempDir(), "classify-live")
	build := exec.Command("go", "build", "-o", out, ".")
	var berr bytes.Buffer
	build.Stderr = &berr
	if err := build.Run(); err != nil {
		t.Fatalf("building the live producer failed: %v\n%s", err, berr.String())
	}

	if pinnedAfter := fileDigest(t, pinnedBinary); pinnedAfter != pinnedBefore {
		t.Fatalf("THE PINNED BASELINE WAS OVERWRITTEN by this build (%s: %s -> %s).\n"+
			"%s is a tracked FIXTURE — the v1 producer that predates this unit — and it is also the repo's default `go build` output path.\n"+
			"Restore it with `git checkout -- cmd/classify/classify` and build only to a scratch path.",
			pinnedBinary, pinnedBefore[:12], pinnedAfter[:12], pinnedBinary)
	}

	// FRESHNESS, the anti-vacuity guard: the artifact under test must be a build
	// of the current tree, not the frozen baseline. Two independent facts.
	if fileDigest(t, out) == pinnedBefore {
		t.Fatalf("the live build is byte-identical to the pinned baseline %s — these rows would be measuring a frozen artifact that cannot respond to a fix in main.go", pinnedBinary)
	}
	// The probe subcommand exists only in the current source; the pinned
	// baseline predates it and treats "capabilities" as a diff-file argument.
	// Exit 0 and exitCapabilityIncomplete are both truthful ANSWERS — B1
	// deliberately leaves framed_authoritative_stdin to unit B2, so 4 is the
	// steady state here (capability.go:276-280).
	probe := runLive(t, out, filepath.Dir(out), nil, probeSubcommand)
	if probe.exit != 0 && probe.exit != exitCapabilityIncomplete {
		t.Fatalf("the live producer does not answer %q (exit %d) — the capability probe is current-source-only, so this build is not what it should be\n%s", probeSubcommand, probe.exit, probe.all())
	}
	if !strings.Contains(probe.stdout, `"cmd/classify"`) {
		t.Fatalf("the live producer's %q output does not name cmd/classify:\n%s", probeSubcommand, probe.all())
	}
	return out
}

func fileDigest(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p) // #nosec G304 -- a repo fixture or a scratch build this test made
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// liveRun is one end-to-end invocation of the live producer.
type liveRun struct {
	exit   int
	stdout string
	stderr string
}

// all returns stdout and stderr together, which is where classify puts its
// INVALID_INPUT report and its log lines respectively.
func (r liveRun) all() string { return r.stdout + r.stderr }

// json parses the machine payload, failing if the run did not produce one.
func (r liveRun) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &m); err != nil {
		t.Fatalf("run exited %d and its stdout is not JSON: %v\nstdout: %s\nstderr: %s", r.exit, err, r.stdout, r.stderr)
	}
	return m
}

// runLive invokes the live producer. dir is the process's working directory,
// which matters: the config search path has a CWD-relative tail.
func runLive(t *testing.T, bin, dir string, env []string, args ...string) liveRun {
	t.Helper()
	cmd := exec.Command(bin, args...) // #nosec G204 -- bin is this test's own scratch build
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil && cmd.ProcessState == nil {
		t.Fatalf("could not run the live producer: %v", err)
	}
	return liveRun{exit: cmd.ProcessState.ExitCode(), stdout: stdout.String(), stderr: stderr.String()}
}

// ─── shared config fixtures ──────────────────────────────────────────────────

// realTable is the evenplay-mono table validated against 98 real PR merges. It
// names the money paths.
func realTable(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(exampleConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// driftedTable is realTable with every financial rule removed.
//
// It is DERIVED from the production fixture rather than hand-written, so it
// cannot drift into a table production could never hold, and it models the
// realistic failure: commit 2b18e02 moved agent config from .claude/ to
// .agent/, and a project mid-migration edits one copy and not the other. A
// wallet path under this table is unmatched, so it takes the fail-closed high
// tier with financial_paths_touched FALSE and no human PR gate.
//
// It is not a scaffold, so nothing forces the gate back on: this is a clean
// money-gate bypass, not the "luck, not design" case the recorded env-var seal
// notes at contract_seal_test.go:1055.
func driftedTable(t *testing.T) []byte {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(realTable(t), &cfg); err != nil {
		t.Fatal(err)
	}
	rules, ok := cfg["rules"].([]any)
	if !ok {
		t.Fatalf("fixture: %s has no rules array", exampleConfigPath)
	}
	kept := make([]any, 0, len(rules))
	dropped := 0
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("fixture: rule is not an object: %v", r)
		}
		if fin, _ := rule["financial"].(bool); fin {
			dropped++
			continue
		}
		kept = append(kept, r)
	}
	if dropped == 0 {
		t.Fatalf("fixture: %s carries no financial rules, so the drifted table would be identical and every row using it would be vacuous", exampleConfigPath)
	}
	cfg["rules"] = kept
	if sc, _ := cfg["scaffold"].(bool); sc {
		t.Fatal("fixture: the drifted table is a scaffold, which forces the human PR gate back on and would hide the bypass this row exists to show")
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseConfig(out); err != nil {
		t.Fatalf("fixture: the drifted table must still PARSE, or the row tests the wrong thing: %v", err)
	}
	return out
}

// assertTablesDisagreeOnMoney is the fixture-validity check for every row that
// contrasts two rule tables. If the two tables ever stop disagreeing about the
// wallet path, the row is measuring nothing and must say so rather than pass.
func assertTablesDisagreeOnMoney(t *testing.T, money, drifted []byte) {
	t.Helper()
	if bytes.Equal(money, drifted) {
		t.Fatal("fixture: the two tables are byte-identical, so no row contrasting them can observe anything")
	}
	verdict := func(data []byte) *Classification {
		cfg, err := parseConfig(data)
		if err != nil {
			t.Fatalf("fixture table does not parse: %v", err)
		}
		d := diffFor(walletPath)
		return classify(cfg, parseDiffFiles(d), d)
	}
	m, d := verdict(money), verdict(drifted)
	if !m.FinancialPathsTouched {
		t.Fatalf("fixture: %s does not classify %s as financial — the money fixture is wrong", exampleConfigPath, walletPath)
	}
	if d.FinancialPathsTouched {
		t.Fatal("fixture: the drifted table still classifies the wallet path as financial, so silently choosing it would be harmless and this row would prove nothing")
	}
	if !m.HumanPRGate || d.HumanPRGate {
		t.Fatalf("fixture: the human PR gate does not differ between the tables (money=%v drifted=%v) — the harm this row names is not exhibited", m.HumanPRGate, d.HumanPRGate)
	}
}

// walletPath is the money core: apps/finance-domain/wallet/** is rule
// "wallet-service", risk critical, component wallet, financial true.
const walletPath = "apps/finance-domain/wallet/service/debit.go"

// writeDual lays out a worktree with the two agent config directories.
func writeDual(t *testing.T, agent, claude []byte) string {
	t.Helper()
	wt := t.TempDir()
	for dir, data := range map[string][]byte{".agent": agent, ".claude": claude} {
		if data == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Join(wt, dir), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, dir, "risk-paths.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return wt
}

// writeDiff drops a diff file into dir and returns its path.
func writeDiff(t *testing.T, dir string, files ...string) string {
	t.Helper()
	p := filepath.Join(dir, "fixture.diff")
	if err := os.WriteFile(p, []byte(diffFor(files...)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// ─── WAVE 1 · A · the dark function ──────────────────────────────────────────

// ROW A (CRITICAL x2, HIGH x6) — ResolveConfigDual is never called, so two
// DIFFERING money-path tables still resolve silently to .agent/.
//
// WHAT IS ALREADY SEALED AND WHY IT IS NOT ENOUGH.
// TestSeal_ResolveConfigDual (contract_seal_test.go:852) proves the function
// returns INVALID_SCHEMA for a differing pair, in six green sub-rows. Every one
// of them calls ResolveConfigDual directly. Production calls findConfig
// (main.go:301 -> :439), which returns the FIRST candidate that exists and
// never compares the two. So six green seals certify a rule production never
// runs. A seventh direct-call row would be the same non-evidence.
//
// THIS ROW THEREFORE OBSERVES THE LIVE RESOLUTION END TO END. The subject is
// the binary's decision, not the function's return value, and the row can only
// go green when the production path actually consults the rule.
//
// MEASURED TODAY, live build, .agent = drifted / .claude = the real table:
//
//	exit 0, config_path = <wt>/.agent/risk-paths.json,
//	financial_paths_touched = false, human_pr_gate = false, risk = high
//
// while .claude/risk-paths.json on the same disk says the file is critical,
// financial, and gated. That is the CRITICAL: a money diff certified as
// touching no financial path, with a second table present that says otherwise.
//
// PRODUCTION ROUTE to "both present": commit 2b18e02 moved agent tooling config
// from .claude/ to .agent/. Every project mid-migration has both directories,
// and a table edited in one copy only is the ordinary way they come to differ.
func TestSeal_Repair_LiveResolution_DifferingDualTablesMustNotResolveSilently(t *testing.T) {
	defer red(t)

	money, drifted := realTable(t), driftedTable(t)
	assertTablesDisagreeOnMoney(t, money, drifted)

	bin := liveClassify(t)
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// CONTROL 1 — .agent alone. The ordinary case must keep working: a body
	// that fixed this row by rejecting any worktree with a config directory
	// fails here.
	agentOnly := writeDual(t, money, nil)
	c1 := runLive(t, bin, pkgDir, nil, "-json", "-no-git", "-worktree", agentOnly, writeDiff(t, agentOnly, walletPath))
	if c1.exit != 0 {
		t.Fatalf("CONTROL .agent alone exited %d, want 0 — a single table is not a dual conflict\n%s", c1.exit, c1.all())
	}
	if got := c1.json(t)["config_path"]; got != filepath.Join(agentOnly, ".agent", "risk-paths.json") {
		t.Errorf("CONTROL .agent alone resolved config_path %v, want the worktree's .agent table", got)
	}
	if c1.json(t)["financial_paths_touched"] != true {
		t.Errorf("CONTROL .agent alone did not report the wallet path as financial — the fixture is not exercising money")
	}

	// CONTROL 2 — both present and IDENTICAL. The rule says use .agent. This is
	// the benign twin of the defect leg: same two directories, same file names,
	// same call, and it must come out the other way. It passes today and must
	// still pass after the repair.
	same := writeDual(t, money, money)
	c2 := runLive(t, bin, pkgDir, nil, "-json", "-no-git", "-worktree", same, writeDiff(t, same, walletPath))
	if c2.exit != 0 {
		t.Fatalf("CONTROL both-identical exited %d, want 0 — identical tables agree, and the rule is \"use .agent\", not \"refuse\"\n%s", c2.exit, c2.all())
	}
	if got := c2.json(t)["config_path"]; got != filepath.Join(same, ".agent", "risk-paths.json") {
		t.Errorf("CONTROL both-identical resolved config_path %v, want the preferred .agent copy", got)
	}
	if c2.json(t)["financial_paths_touched"] != true {
		t.Error("CONTROL both-identical lost the financial verdict")
	}

	// THE DEFECT — both present and DIFFERING. §3.3 and ResolveConfigDual's own
	// doc make this INVALID_SCHEMA. The live path does not.
	wt := writeDual(t, drifted, money)
	got := runLive(t, bin, pkgDir, nil, "-json", "-no-git", "-worktree", wt, writeDiff(t, wt, walletPath))
	if got.exit == exitInvalid {
		if !strings.Contains(got.all(), ".agent") || !strings.Contains(got.all(), ".claude") {
			t.Errorf("the run refused, but its message does not name both tables the operator must reconcile:\n%s", got.all())
		}
		return
	}
	t.Errorf("TWO DIFFERING MONEY TABLES RESOLVED SILENTLY. exit %d, want %d (INVALID_SCHEMA).\n"+
		"  %s says the wallet path is NOT financial and needs no human PR gate.\n"+
		"  %s, on the same disk, says it is critical, financial and gated.\n"+
		"  The live run reported: config_path=%v financial_paths_touched=%v human_pr_gate=%v risk=%v\n"+
		"ResolveConfigDual implements this rule and six seals certify it, but nothing calls it: resolveConfigPath (main.go:297) goes to findConfig (main.go:439), which returns the first candidate that EXISTS and never compares the two.\n"+
		"Which table names this project's money paths is not something classify may guess.",
		got.exit, exitInvalid,
		filepath.Join(wt, ".agent", "risk-paths.json"),
		filepath.Join(wt, ".claude", "risk-paths.json"),
		got.json(t)["config_path"], got.json(t)["financial_paths_touched"],
		got.json(t)["human_pr_gate"], got.json(t)["risk"])
}

// ─── WAVE 1 · B · fail-open on read error ────────────────────────────────────

// ROW B (CRITICAL, HIGH x2) — ResolveConfigDual swallows every config read
// error, so UNREADABLE is indistinguishable from ABSENT and the differing-table
// gate fails OPEN.
//
// contract.go:760-763 is `data, readErr := os.ReadFile(c); if readErr != nil {
// continue }`. A .claude/risk-paths.json that cannot be read is therefore not
// "present and differing" and not "present and identical" — it is simply not
// counted, len(present) collapses to 1, and the function returns the .agent
// path with a nil error. The gate that exists to stop classify guessing between
// two money tables is silenced by the one condition under which it has least
// information.
//
// THE TWO STATES MUST BE NAMED AND MUST DIFFER. Absent is a legitimate,
// common state with a defined answer: use the other table. Unreadable is not an
// answer at all — the file is there, its contents are unknown, and whether it
// agrees with .agent is exactly the fact the gate needs.
//
// MEASURED TODAY, both states, same call:
//
//	ABSENT     .claude -> (<wt>/.agent/risk-paths.json, <nil>)
//	UNREADABLE .claude -> (<wt>/.agent/risk-paths.json, <nil>)   <- identical
//
// PRODUCTION ROUTE: a repo checked out or bind-mounted with a mode the running
// uid cannot read is ordinary in CI and in containers. The primary leg here
// does not even need that — it makes risk-paths.json a DIRECTORY, which
// os.ReadFile rejects with EISDIR for every uid including root, so this row can
// never go quietly vacuous on the machine it matters on.
//
// REACHABILITY NOTE: like the six existing rows, this one calls the function
// directly, because the defect is in the function. What makes that legitimate
// now is ROW A above, which seals the call site. Neither row substitutes for
// the other.
func TestSeal_Repair_ResolveConfigDual_UnreadableIsNotAbsent(t *testing.T) {
	defer red(t)

	money := realTable(t)
	agentPath := func(wt string) string { return filepath.Join(wt, ".agent", "risk-paths.json") }

	// CONTROL 1 — ABSENT. A named state with a defined answer.
	absent := writeDual(t, money, nil)
	absentPath, absentErr := ResolveConfigDual(absent)
	if absentErr != nil {
		t.Fatalf("CONTROL absent: errored %v — .claude/ missing is the ordinary case and must resolve to .agent", absentErr)
	}
	if absentPath != agentPath(absent) {
		t.Fatalf("CONTROL absent: resolved %q, want %q", absentPath, agentPath(absent))
	}

	// CONTROL 2 — PRESENT AND AGREEING. The other state that legitimately
	// resolves to .agent with no error. Together with CONTROL 1 this fixes what
	// "success" means, so the defect legs cannot be satisfied by a body that
	// simply errors more often.
	agreeing := writeDual(t, money, money)
	if p, err := ResolveConfigDual(agreeing); err != nil || p != agentPath(agreeing) {
		t.Fatalf("CONTROL agreeing: got (%q, %v), want (%q, <nil>)", p, err, agentPath(agreeing))
	}

	// DEFECT LEG 1 — UNREADABLE because the path is a DIRECTORY. uid-independent.
	dirCase := writeDual(t, money, nil)
	if err := os.MkdirAll(filepath.Join(dirCase, ".claude", "risk-paths.json"), 0o750); err != nil {
		t.Fatal(err)
	}
	assertUnreadable(t, filepath.Join(dirCase, ".claude", "risk-paths.json"))
	checkUnreadable(t, "a directory", dirCase, absentPath, absentErr, agentPath(dirCase))

	// DEFECT LEG 2 — UNREADABLE because of file mode. This is the form the
	// production route actually takes. It cannot be exhibited as root, so the
	// row proves the precondition and says which case it is in rather than
	// skipping into a quiet pass.
	modeCase := writeDual(t, money, money)
	victim := filepath.Join(modeCase, ".claude", "risk-paths.json")
	if err := os.Chmod(victim, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(victim); err == nil {
		if os.Geteuid() != 0 {
			t.Fatalf("fixture: %s is still readable at mode 000 as uid %d — this leg cannot exhibit the defect and must not pass quietly", victim, os.Geteuid())
		}
		t.Logf("leg 2 not applicable as root (uid 0 reads mode 000); leg 1, which uses a directory, covers every uid")
	} else {
		checkUnreadable(t, "mode 000", modeCase, absentPath, absentErr, agentPath(modeCase))
	}
}

// assertUnreadable is the fixture-validity check: if the victim can in fact be
// read, the leg is measuring nothing.
func assertUnreadable(t *testing.T, p string) {
	t.Helper()
	if _, err := os.ReadFile(p); err == nil { // #nosec G304 -- a temp path this test created
		t.Fatalf("fixture: %s is readable, so the unreadable state is not exhibited", p)
	}
}

// checkUnreadable asserts that the unreadable state is distinguishable from the
// absent state, whose observed outcome is passed in so the two are compared as
// one judgement rather than against a remembered constant.
func checkUnreadable(t *testing.T, why, wt, absentPath string, absentErr error, wantAgent string) {
	t.Helper()
	gotPath, gotErr := ResolveConfigDual(wt)
	if gotErr != nil {
		if !strings.Contains(gotErr.Error(), ".claude") {
			t.Errorf("%s: raised %v, but the message does not name the file it could not read", why, gotErr)
		}
		return
	}
	sameAsAbsent := (gotErr == nil) == (absentErr == nil) && gotPath == wantAgent && absentPath != ""
	t.Errorf("UNREADABLE IS INDISTINGUISHABLE FROM ABSENT (%s).\n"+
		"  unreadable .claude -> (%q, %v)\n"+
		"  absent     .claude -> (%q, %v)   [CONTROL, and it is correct]\n"+
		"  identical outcomes: %v\n"+
		"contract.go:760-763 does `if readErr != nil { continue }`, so a config it cannot read is not counted at all: len(present) collapses to 1 and the differing-table gate never runs. The gate is silenced by the one condition under which classify knows least about the second money table.\n"+
		"Unreadable is not an answer. Absent is. Make them two named states.",
		why, gotPath, gotErr, absentPath, absentErr, sameAsAbsent)
}

// ─── WAVE 1 · C · TOCTOU ─────────────────────────────────────────────────────

// ROW C (HIGH x2) — the agreement precondition is checked on bytes that are
// then DISCARDED, and the caller re-reads the path.
//
// ResolveConfigDual reads both candidates, digests them, compares, and returns
// a PATH (contract.go:757-790). The bytes go out of scope. loadConfig
// (main.go:488) then opens that path again, and it is the SECOND read whose
// bytes are parsed into the rule table, classified against, and recorded as the
// config channel's digest. Nothing binds the two reads together.
//
// This codebase has already written down why that matters, one file over.
// unframedDigestSource (capability.go:340-348) records the config bytes at the
// read rather than recomputing from the path at emission time, because
// re-reading "would digest whatever is on disk THEN, which is a different claim
// from 'these are the bytes I classified' and is exactly the gap an attacker
// who can rewrite a file between the two reads would use." That discipline
// closes the window between read and emit. It does nothing about the window
// between the dual check's read and loadConfig's read, which is the same gap
// one stage earlier — and it is the stage that decides whether the money gate
// runs at all.
//
// HOW THIS IS SEALED WITHOUT A RACE. The row does not try to win a timing
// window. It performs the write that a racing writer would perform, in the
// window that provably exists, and then asks the production digest channel
// which bytes the process actually consumed. If the answer is not the bytes the
// agreement was certified over, the certificate is worthless — no timing
// argument required.
//
// The measured sequence today: the check certifies that .agent and .claude
// agree on X, and the process then consumes Y, which the check never saw and
// which .claude does not agree with.
//
// PRODUCTION ROUTE: the config search reads the rule table out of the worktree
// under review, which is agent-writable — the same authority-channel premise
// that makes the env-var row (D) and the framed-stdin work of unit B2
// necessary. A run whose diff and config live in a tree another process is
// still writing is the normal case, not the adversarial one.
//
// ─── P4 RULING (adjudicate(B1-repair), dispute 2) ────────────────────────────
//
// The dispute: this row seals the property THROUGH loadConfig rather than
// directly, because a seal naming a not-yet-existing symbol fails compilation
// for the whole package and would take all 76 green rows down with it. That
// reasoning is correct and is affirmed. A body that prefers to thread the bytes
// through new signatures satisfies the same property but needs this row moved.
//
// RULED: THIS ROW STANDS AS WRITTEN AND IS THE DEFAULT. Relocation is permitted
// but CONDITIONAL, and the body does not perform it — body agents do not edit
// seals. P3 escalates with a concrete signature and the row is amended by that
// route. An amended row must keep all five of:
//
//  1. the CONTROL leg — nothing rewrites the file, consumed == certified;
//  2. the certified digest taken from what the CERTIFYING step actually read,
//     never from the fixture bytes the test wrote. This is the one that decides
//     whether the relocation is honest: assert on the bytes the resolver
//     RETURNED, or a body that hands back the .claude copy while the run
//     consumes .agent passes a row that was only ever checking its own input;
//  3. the interposed write into the window, still actually performed;
//  4. the consumed digest read from the production channel
//     (unframedDigests.ConsumedDigests()), never from the new function's return
//     value. Six green seals in this unit already certify a function production
//     never calls — asserting that a new resolver returns the bytes it read
//     would be that same non-evidence;
//  5. a demonstration that the amended row is RED against the unrepaired tree.
//     A relocated row that cannot be shown failing is a description, not a seal.
//
// WHY NOT RELOCATE IT END-TO-END, which would dissolve the dispute outright:
// the property IS observable from outside — `-contract-version 2` emits
// computed_config_sha256 in the response wrapper (contract.go:638-663), and P4
// confirmed it equals the SHA-256 of the table on disk. But the DEFECT leg
// cannot be reached that way without winning a real timing window inside a
// child process. This row's entire merit is that it needs no race. A racy seal
// is worse than a signature-coupled one, so the coupling is accepted
// deliberately rather than traded for flakiness.
func TestSeal_Repair_ResolveConfigDual_ConsumedBytesMustBeTheCertifiedBytes(t *testing.T) {
	defer red(t)

	money, drifted := realTable(t), driftedTable(t)
	assertTablesDisagreeOnMoney(t, money, drifted)

	// The process-wide recorder is package state. Swap in a fresh one so this
	// row reads only its own reads, and restore it afterwards. No existing test
	// touches it, and this row does not run in parallel.
	saved := unframedDigests
	t.Cleanup(func() { unframedDigests = saved })

	// CONTROL — nothing rewrites the file. The bytes consumed are the bytes
	// certified, and the row must come out green on this leg both today and
	// after the repair. Without it, a body that reported a constant digest, or
	// one that never resolved at all, could satisfy the defect leg.
	// The diff channel is recorded alongside the config channel because
	// ConsumedDigests raises unless BOTH were consumed, and because that is what
	// run() does: readDiff (main.go:568/584) records the diff, loadConfig
	// (main.go:496) records the config.
	wallet := []byte(diffFor(walletPath))

	unframedDigests = &unframedDigestSource{}
	control := writeDual(t, money, money)
	controlPath, err := ResolveConfigDual(control)
	if err != nil {
		t.Fatalf("CONTROL: agreeing tables must resolve: %v", err)
	}
	if _, err := loadConfig(controlPath); err != nil {
		t.Fatalf("CONTROL: loadConfig(%q): %v", controlPath, err)
	}
	unframedDigests.recordDiff(wallet)
	controlSHA, _, err := unframedDigests.ConsumedDigests()
	if err != nil {
		t.Fatalf("CONTROL: the config channel recorded nothing: %v", err)
	}
	if want := hexSHA256(money); controlSHA != want {
		t.Fatalf("CONTROL: the process consumed %s but the certified table digests to %s — this leg must agree before the defect leg means anything", controlSHA[:12], want[:12])
	}

	// THE DEFECT — the same call sequence, with the certified file rewritten in
	// the window the discarded bytes leave open.
	unframedDigests = &unframedDigestSource{}
	wt := writeDual(t, money, money)
	certifiedPath, err := ResolveConfigDual(wt)
	if err != nil {
		t.Fatalf("the agreeing pair must resolve before the window can be shown: %v", err)
	}
	// PROOF OF EXECUTION: the resolution really read this file and really
	// certified agreement over these bytes. A body that returned a path without
	// reading anything fails here, and so does one that returned the wrong copy.
	if certifiedPath != filepath.Join(wt, ".agent", "risk-paths.json") {
		t.Fatalf("resolved %q, want the preferred .agent copy — the certificate is over the wrong file", certifiedPath)
	}
	certified := hexSHA256(money)

	// The write a racing writer would perform: only .agent changes, so the two
	// tables no longer agree and the new table names no money paths.
	if err := os.WriteFile(certifiedPath, drifted, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(certifiedPath)
	if err != nil {
		t.Fatalf("loadConfig(%q): %v", certifiedPath, err)
	}
	unframedDigests.recordDiff(wallet)
	consumedSHA, _, err := unframedDigests.ConsumedDigests()
	if err != nil {
		t.Fatalf("the config channel recorded nothing: %v", err)
	}
	if consumedSHA == certified {
		return // the window is closed: the run consumed the bytes that were certified
	}

	d := diffFor(walletPath)
	cls := classify(cfg, parseDiffFiles(d), d)
	t.Errorf("THE AGREEMENT WAS CERTIFIED OVER BYTES THE RUN NEVER CONSUMED.\n"+
		"  certified by ResolveConfigDual: sha256 %s  (.agent and .claude agreed on it)\n"+
		"  consumed by the run:            sha256 %s  (.claude never agreed to this)\n"+
		"  and it changes the answer: financial_paths_touched=%v human_pr_gate=%v risk=%v for %s\n"+
		"ResolveConfigDual reads both candidates, digests them, compares, then DISCARDS the bytes and hands back a path (contract.go:757-790). loadConfig (main.go:488) opens that path a second time, and it is the second read that is parsed, classified and digested.\n"+
		"capability.go:340-348 already names this exact hazard as the reason digests are RECORDED at the read and never recomputed from a path: re-reading digests \"whatever is on disk THEN\", which is \"exactly the gap an attacker who can rewrite a file between the two reads would use\". Same gap, one stage earlier, on the read that decides whether the money gate runs.\n"+
		"The check and the use must be one read.",
		certified[:12], consumedSHA[:12],
		cls.FinancialPathsTouched, cls.HumanPRGate, cls.Risk, walletPath)
}

// ─── WAVE 1 · D · the environment variable outranks the money table ──────────

// ROW D (CRITICAL, HIGH) — $RISK_PATHS_CONFIG outranks BOTH config directories,
// so an agent that can set an environment variable silences the money gate, and
// ResolveConfigDual never runs at all.
//
// configCandidates (main.go:401-413) puts the environment variable at the head
// of the list and findConfig takes the first hit, so no table in the worktree is
// ever consulted.
//
// THE DIFFERENCE BETWEEN THIS ROW AND THE EXISTING ONE.
// TestSeal_Recorded_EnvVarOutranksBothConfigDirectories (contract_seal_test.go)
// RECORDS the behaviour as green. This row BLOCKS it.
//
// P4 RULING (adjudicate(B1-repair), dispute 1): THIS ROW GOVERNS THE SOURCE.
// The body must close the bypass; the recorded row is not a licence to leave it
// open. The recorded row is KEPT, with its doc corrected, because the two rows
// do not actually assert opposite things about the same subject:
//
//   - this row builds the current tree to a scratch path. Subject: SOURCE.
//   - the recorded row runs ./classify. Subject: THE SHIPPED ARTIFACT — every
//     documented invocation (roles/tasker.md:224, skills/pr-raise.md:36,
//     README.md:35) execs the committed cmd/classify/classify by absolute path.
//
// The recorded row's stated trigger — "it turns red when someone fixes the
// ordering" — is FALSE, and P4 verified it is false rather than reasoning about
// it: under a reference implementation that deleted the env-var candidate, this
// row went green and the recorded row STAYED GREEN. Its real trigger is the
// rebuild-and-commit of cmd/classify/classify, which B1 must never do because
// that file is the pinned v1 differential baseline.
//
// THE CONSEQUENCE THE BODY MUST NOT MISREAD, because it is bigger than this row:
// fixing main.go does not fix production. The Tasker runs the committed binary.
// Until cmd/classify/classify is rebuilt and committed — an act that also
// destroys the v1 differential baseline and therefore needs the operator, not
// the body — every repair in this unit is green in the tree and absent from the
// artifact that classifies real money diffs. Escalated to the operator; it is
// not P3's to resolve and must not be resolved by rebuilding the baseline.
//
// MEASURED TODAY, live build, worktree holding the real table, non-scaffold
// attacker table named by the variable:
//
//	exit 0, config_path = <attacker>, financial_paths_touched = false,
//	human_pr_gate = false, risk = high
//
// on apps/finance-domain/wallet/service/debit.go. A clean bypass — not the
// "luck, not design" scaffold case the recorded row notes at :1055.
//
// WHAT THE BODY MAY CHOOSE. Ignoring the variable and refusing outright both
// close the harm, so this row asserts the harm and not the mechanism: the money
// verdict of the worktree's own table must survive. CONTROL 2 fences the fix —
// an explicit -config must still be honoured, because naming the rule table is
// that flag's whole contract.
func TestSeal_Repair_EnvVarMustNotOutrankTheWorktreeMoneyTable(t *testing.T) {
	defer red(t)

	money, drifted := realTable(t), driftedTable(t)
	assertTablesDisagreeOnMoney(t, money, drifted)

	bin := liveClassify(t)
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	wt := writeDual(t, money, nil)
	attacker := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(attacker, drifted, 0o600); err != nil {
		t.Fatal(err)
	}
	diffPath := writeDiff(t, wt, walletPath)
	trusted := filepath.Join(wt, ".agent", "risk-paths.json")

	// CONTROL 1 — no variable. The worktree's table governs and the money
	// verdict is correct. This is the outcome the defect leg must preserve.
	c1 := runLive(t, bin, pkgDir, []string{"RISK_PATHS_CONFIG="}, "-json", "-no-git", "-worktree", wt, diffPath)
	if c1.exit != 0 {
		t.Fatalf("CONTROL no-env exited %d, want 0\n%s", c1.exit, c1.all())
	}
	if got := c1.json(t)["config_path"]; got != trusted {
		t.Fatalf("CONTROL no-env used config_path %v, want the worktree's .agent table %q", got, trusted)
	}
	if c1.json(t)["financial_paths_touched"] != true || c1.json(t)["human_pr_gate"] != true {
		t.Fatalf("CONTROL no-env: the wallet path is not financial+gated under the trusted table — the fixture is wrong")
	}

	// CONTROL 2 — an explicit -config naming the very same file the variable
	// tried to inject. This must STILL be honoured: naming the rule table is
	// that flag's whole contract, and a body that closed this row by refusing
	// every table outside the worktree would break the operator's flag. It
	// passes today and must still pass after the repair.
	c2 := runLive(t, bin, pkgDir, []string{"RISK_PATHS_CONFIG="}, "-json", "-no-git", "-worktree", wt, "-config", attacker, diffPath)
	if c2.exit != 0 {
		t.Fatalf("CONTROL explicit -config exited %d, want 0 — an operator naming the rule table is that flag's contract, and the fix must not take it away\n%s", c2.exit, c2.all())
	}
	if got := c2.json(t)["config_path"]; got != attacker {
		t.Errorf("CONTROL explicit -config used config_path %v, want the named file %q", got, attacker)
	}

	// THE DEFECT — the same file, injected through the environment instead.
	got := runLive(t, bin, pkgDir, []string{"RISK_PATHS_CONFIG=" + attacker}, "-json", "-no-git", "-worktree", wt, diffPath)
	if got.exit == exitInvalid {
		return // refusing is one of the two acceptable outcomes
	}
	if got.exit != 0 {
		t.Fatalf("the redirected run exited %d, which is neither success nor INVALID_INPUT\n%s", got.exit, got.all())
	}
	m := got.json(t)
	if m["financial_paths_touched"] == true && m["human_pr_gate"] == true && m["config_path"] != attacker {
		return // the variable did not govern; the worktree's table did
	}
	t.Errorf("AN ENVIRONMENT VARIABLE SILENCED THE MONEY GATE.\n"+
		"  $RISK_PATHS_CONFIG=%s\n"+
		"  worktree table (ignored): %s — says %s is critical, financial, gated\n"+
		"  the run reported: config_path=%v financial_paths_touched=%v human_pr_gate=%v risk=%v\n"+
		"configCandidates (main.go:401-413) puts the variable AHEAD of both config directories and findConfig takes the first hit, so no table in the worktree under review is ever consulted and ResolveConfigDual never runs.\n"+
		"An agent that can set an environment variable is not an operator naming a rule table. CONTROL 2 above shows the operator's -config still works, so closing this does not cost the flag.",
		attacker, trusted, walletPath,
		m["config_path"], m["financial_paths_touched"], m["human_pr_gate"], m["risk"])
}

// ─── WAVE 1 · the doc asserts a fail-closed rule the search path lacks ───────

// ROW E (HIGH) — the doc promises there is NO fallback to another checkout's
// config. The search path has one, and it is the tooling repo's own table.
//
// configCandidates' doc (main.go:394-400) is unambiguous: "There is deliberately
// NO fallback to another checkout's config. The rule table names one
// repository's money, auth and client paths; applied to a different repository
// it produces confidently wrong answers — a diff certified as touching no
// financial path because the paths it names do not exist here. A missing config
// is INVALID_INPUT, not a default."
//
// main.go:411 then appends `filepath.Join(agentConfigDirs[0], "risk-paths.json")`
// with no worktree prefix. That is CWD-relative. Run classify from a checkout
// that has .agent/risk-paths.json — this repo has one — against a worktree that
// has none, and the last candidate hits: the run silently classifies another
// repository's diff against THIS repository's money table. The exact outcome
// the doc says deliberately cannot happen.
//
// MEASURED TODAY, live build, cwd = repo root, -worktree = an empty directory:
//
//	exit 0, config_path = ".agent/risk-paths.json",
//	financial_paths_touched = false, human_pr_gate = false
//
// on apps/finance-domain/wallet/service/debit.go.
//
// WHY THE GREEN SEAL DID NOT CATCH IT. TestConfigCandidates_PrefersVendorNeutral-
// Dir (main_test.go:577) checks the head of the candidate list and then rejects
// any candidate containing "claude-workflow". The offending candidate is the
// relative string ".agent/risk-paths.json", which contains no such substring
// while resolving into precisely that repo. The seal certifies the doc by
// checking a spelling. Not a contradiction with this row — a coverage hole — so
// nothing there needs changing.
//
// P4 RULING (adjudicate(B1-repair)): CONFIRMED, and confirmed by running it
// rather than by reading it. Under a reference implementation that drops the
// CWD-relative candidate at main.go:411, this row goes GREEN and
// TestConfigCandidates_PrefersVendorNeutralDir stays GREEN — the surviving
// candidate list is exactly the two worktree-anchored paths it already asserts.
// No amendment anywhere. The coverage hole is real and stays open by design:
// that seal would still pass if a CWD-relative candidate came back under a
// different spelling, and THIS row is what closes it, behaviourally.
//
// PRODUCTION ROUTE: the Tasker runs classify from the tooling checkout with
// -worktree pointing at the project under review. That is the documented
// invocation, and this repo ships .agent/risk-paths.json at its root.
func TestSeal_Repair_NoFallbackToAnotherCheckoutsConfig(t *testing.T) {
	defer red(t)

	bin := liveClassify(t)
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// FIXTURE VALIDITY: without a table at the repo root there is no other
	// checkout's config to fall back to, and this row would pass by measuring
	// nothing.
	rootTable := filepath.Join(repoRoot, ".agent", "risk-paths.json")
	if _, err := os.Stat(rootTable); err != nil {
		t.Fatalf("fixture: %s does not exist, so the CWD-relative fallback cannot be exhibited and this row would pass vacuously: %v", rootTable, err)
	}

	// CONTROL — same cwd, same binary, but the worktree HAS its own table. It
	// must be used, and the money verdict must be right. A body that closed
	// this row by refusing every relative candidate, or by refusing whenever
	// cwd holds a table, fails here.
	withTable := writeDual(t, realTable(t), nil)
	c := runLive(t, bin, repoRoot, nil, "-json", "-no-git", "-worktree", withTable, writeDiff(t, withTable, walletPath))
	if c.exit != 0 {
		t.Fatalf("CONTROL worktree-with-table exited %d, want 0\n%s", c.exit, c.all())
	}
	if got := c.json(t)["config_path"]; got != filepath.Join(withTable, ".agent", "risk-paths.json") {
		t.Fatalf("CONTROL used config_path %v, want the worktree's own table", got)
	}
	if c.json(t)["financial_paths_touched"] != true {
		t.Fatal("CONTROL lost the financial verdict — the fixture is wrong")
	}

	// THE DEFECT — a worktree with no config at all.
	empty := t.TempDir()
	diffPath := writeDiff(t, t.TempDir(), walletPath)
	got := runLive(t, bin, repoRoot, nil, "-json", "-no-git", "-worktree", empty, diffPath)
	if got.exit == exitInvalid {
		return // a missing config is INVALID_INPUT, which is the documented rule
	}
	m := got.json(t)
	t.Errorf("A WORKTREE WITH NO CONFIG BORROWED ANOTHER CHECKOUT'S MONEY TABLE. exit %d, want %d.\n"+
		"  -worktree %s   (empty: no .agent/, no .claude/)\n"+
		"  cwd        %s   (this tooling repo, which ships %s)\n"+
		"  the run reported: config_path=%v financial_paths_touched=%v human_pr_gate=%v risk=%v for %s\n"+
		"main.go:411 appends agentConfigDirs[0]+\"/risk-paths.json\" with NO worktree prefix, so the last candidate is CWD-relative.\n"+
		"configCandidates' own doc (main.go:394-400) says: \"There is deliberately NO fallback to another checkout's config ... a diff certified as touching no financial path because the paths it names do not exist here. A missing config is INVALID_INPUT, not a default.\" The search path does not have that rule; the doc asserts it anyway.",
		got.exit, exitInvalid, empty, repoRoot, rootTable,
		m["config_path"], m["financial_paths_touched"], m["human_pr_gate"], m["risk"], walletPath)
}

// ─── WAVE 2 · E · the sidecar outlives the run that wrote it ─────────────────

// ROW F (CRITICAL) — a v1 re-run over the same -out leaves the PREVIOUS run's
// v2 sidecar in place, still asserting the superseded verdict.
//
// persist() (main.go:324-349) writes the sidecar under ContractV2 and does
// nothing about it otherwise. It is written but never torn down, and nothing
// marks it stale. WriteV2Sidecar's own contract — "it fully replaces any prior
// sidecar; it never merges" — holds only on the branch that runs.
//
// MEASURED TODAY, live build, one -out reused:
//
//	run A  -contract-version 2  docs-only diff  -> sidecar: risk=low,
//	                                               human_pr_gate=false
//	run B  (v1 default)         wallet diff     -> run-state: risk=critical,
//	                                               human_pr_gate=true
//	the sidecar is still there, still saying risk=low, human_pr_gate=false
//
// A consumer that reads the sidecar therefore reads "no human PR gate" for a
// critical money diff. That is the CRITICAL, and it is worse than a missing
// sidecar: WriteV2Sidecar makes a failed write a hard error precisely because
// "a silently missing sidecar is indistinguishable at the consumer from an old
// run". A silently PRESENT stale one is indistinguishable from a current one.
//
// PRODUCTION ROUTE: the same -out is reused across a task's rounds by design —
// writeRunState (main.go:1074-1076) exists to merge into an existing run state.
// Rolling a run back from -contract-version 2 to v1, which the rollback seal at
// readset_seal_test.go:954 treats as a supported mid-run move, is exactly this
// sequence.
//
// WHAT THE BODY MAY CHOOSE: removing the sidecar or rewriting it. The row
// asserts only that a reader can no longer obtain the superseded verdict.
func TestSeal_Repair_V1RerunMustNotLeaveAStaleV2Sidecar(t *testing.T) {
	defer red(t)

	bin := liveClassify(t)
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	docs := []string{"docs/plans/2026-07-29-graph-spine.md", "README.md"}

	// gateOf reads human_pr_gate out of a v2 sidecar, or reports its absence.
	gateOf := func(t *testing.T, sidecar string) (present bool, gate any, risk any) {
		t.Helper()
		data, err := os.ReadFile(sidecar) // #nosec G304 -- a temp path this test created
		if err != nil {
			return false, nil, nil
		}
		var sc V2Sidecar
		if err := json.Unmarshal(data, &sc); err != nil {
			return true, nil, nil // present but unreadable as a sidecar: no verdict obtainable
		}
		var cls map[string]any
		if err := json.Unmarshal(sc.Response.Classification, &cls); err != nil {
			return true, nil, nil
		}
		return true, cls["human_pr_gate"], cls["risk"]
	}

	// seed runs A (v2, docs-only) into a fresh -out and returns the paths.
	seed := func(t *testing.T) (runState, sidecar, walletDiff string) {
		t.Helper()
		dir := t.TempDir()
		runState = filepath.Join(dir, "run.json")
		docsDiff := filepath.Join(dir, "docs.diff")
		if err := os.WriteFile(docsDiff, []byte(diffFor(docs...)), 0o600); err != nil {
			t.Fatal(err)
		}
		walletDiff = filepath.Join(dir, "wallet.diff")
		if err := os.WriteFile(walletDiff, []byte(diffFor(walletPath)), 0o600); err != nil {
			t.Fatal(err)
		}
		a := runLive(t, bin, pkgDir, nil, "-no-git", "-contract-version", "2",
			"-config", exampleConfigPath, "-out", runState, docsDiff)
		if a.exit != 0 {
			t.Fatalf("run A (v2, docs-only) exited %d\n%s", a.exit, a.all())
		}
		sidecar = V2SidecarPath(runState)
		present, gate, risk := gateOf(t, sidecar)
		if !present {
			t.Fatalf("run A wrote no sidecar at %s — the row cannot show a stale one", sidecar)
		}
		if gate != false || risk != "low" {
			t.Fatalf("run A's sidecar says human_pr_gate=%v risk=%v, want false/low — the fixture is not set up to show a superseded verdict", gate, risk)
		}
		return runState, sidecar, walletDiff
	}

	// runStateGate reads the shared run-state's verdict.
	runStateGate := func(t *testing.T, runState string) (any, any) {
		t.Helper()
		data, err := os.ReadFile(runState) // #nosec G304 -- a temp path this test created
		if err != nil {
			t.Fatal(err)
		}
		var st struct {
			Classification map[string]any `json:"classification"`
		}
		if err := json.Unmarshal(data, &st); err != nil {
			t.Fatalf("run-state is not valid JSON: %v", err)
		}
		return st.Classification["human_pr_gate"], st.Classification["risk"]
	}

	// CONTROL 1 — run B also at v2. The sidecar is refreshed and agrees with
	// the run-state. A body that closed this row by always deleting the sidecar
	// still passes; one that stopped writing it at all does not.
	t.Run("CONTROL_v2_rerun_refreshes_the_sidecar", func(t *testing.T) {
		defer red(t)
		runState, sidecar, walletDiff := seed(t)
		b := runLive(t, bin, pkgDir, nil, "-no-git", "-contract-version", "2",
			"-config", exampleConfigPath, "-out", runState, walletDiff)
		if b.exit != 0 {
			t.Fatalf("run B (v2, wallet) exited %d\n%s", b.exit, b.all())
		}
		present, gate, risk := gateOf(t, sidecar)
		if !present {
			t.Fatal("a v2 run must WRITE its sidecar, not remove it")
		}
		if gate != true || risk != "critical" {
			t.Errorf("after a v2 re-run the sidecar says human_pr_gate=%v risk=%v, want true/critical", gate, risk)
		}
	})

	// CONTROL 2 — a v1 run into an -out that never had a sidecar must not
	// create one. This fences the other direction of the fix.
	t.Run("CONTROL_v1_run_creates_no_sidecar", func(t *testing.T) {
		defer red(t)
		dir := t.TempDir()
		runState := filepath.Join(dir, "run.json")
		walletDiff := filepath.Join(dir, "wallet.diff")
		if err := os.WriteFile(walletDiff, []byte(diffFor(walletPath)), 0o600); err != nil {
			t.Fatal(err)
		}
		r := runLive(t, bin, pkgDir, nil, "-no-git", "-config", exampleConfigPath, "-out", runState, walletDiff)
		if r.exit != 0 {
			t.Fatalf("v1 run exited %d\n%s", r.exit, r.all())
		}
		if _, err := os.Stat(V2SidecarPath(runState)); err == nil {
			t.Error("a v1 run created a v2 sidecar — the v2 envelope lands in the sidecar and nowhere else, and only under ContractV2")
		}
	})

	// THE DEFECT — run B at v1 over the same -out.
	t.Run("v1_rerun_leaves_the_superseded_verdict_readable", func(t *testing.T) {
		defer red(t)
		runState, sidecar, walletDiff := seed(t)

		b := runLive(t, bin, pkgDir, nil, "-no-git", "-config", exampleConfigPath, "-out", runState, walletDiff)
		if b.exit != 0 {
			t.Fatalf("run B (v1, wallet) exited %d\n%s", b.exit, b.all())
		}

		// PROOF OF EXECUTION: run B really happened and really changed the
		// verdict. Without this the row could pass on a run that never ran.
		stateGate, stateRisk := runStateGate(t, runState)
		if stateGate != true || stateRisk != "critical" {
			t.Fatalf("run B did not supersede the verdict: the run-state says human_pr_gate=%v risk=%v, want true/critical. Nothing was superseded, so there is no staleness to observe.", stateGate, stateRisk)
		}

		present, gate, risk := gateOf(t, sidecar)
		if !present {
			return // torn down: the superseded verdict is unreadable
		}
		if gate == nil {
			return // present but no verdict obtainable from it
		}
		if gate == stateGate && risk == stateRisk {
			return // rewritten to agree with the run that superseded it
		}
		t.Errorf("THE PREVIOUS RUN'S V2 SIDECAR SURVIVED A V1 RE-RUN, STILL ASSERTING THE SUPERSEDED VERDICT.\n"+
			"  %s\n"+
			"    run-state (run B, wallet): human_pr_gate=%v risk=%v\n"+
			"    sidecar   (run A, docs):   human_pr_gate=%v risk=%v   <- STALE, and still readable\n"+
			"A consumer reading the sidecar is told there is NO HUMAN PR GATE for a critical money diff.\n"+
			"persist() (main.go:324-349) writes the sidecar under ContractV2 and does nothing about it on any other branch: written, never torn down, nothing marks it stale.\n"+
			"WriteV2Sidecar makes a failed write a hard error because \"a silently missing sidecar is indistinguishable at the consumer from an old run\". A silently PRESENT one from an old run is indistinguishable from a current one, and it carries a verdict instead of nothing.",
			sidecar, stateGate, stateRisk, gate, risk)
	})
}

// ─── WAVE 2 · F · the sidecar write follows symlinks ─────────────────────────

// ROW G (HIGH) — WriteV2Sidecar writes through a symlink at a DERIVED path:
// arbitrary file overwrite, and it destroys the run-state.
//
// The path is not named by the operator. V2SidecarPath appends ".v2.json" to
// -out (contract.go:498-505), and contract.go:704 writes it with os.WriteFile,
// which opens O_WRONLY|O_CREATE|O_TRUNC and FOLLOWS a symlink. Whoever can
// create one file next to the run-state chooses which file classify truncates.
//
// MEASURED TODAY:
//
//	<out>.v2.json -> a decoy elsewhere : err=<nil>, decoy replaced by the
//	                                     1025-byte sidecar
//	<out>.v2.json -> the run-state     : err=<nil>, run-state DESTROYED
//
// The second leg breaks this function's own written contract: "It NEVER touches
// the run-state file. If this function is ever seen to open opts.out, that is
// the bug the separate-file rule exists to prevent" (contract.go:674-675). It
// does not open opts.out. It opens a symlink that is opts.out, which is the
// same bug arriving by the route the doc did not consider. TestSeal_WriteV2Side-
// car (contract_seal_test.go:723) asserts the run-state is untouched with a
// regular file at the sidecar path; this row extends that assertion to the
// symlink case. No existing assertion is weakened or contradicted.
//
// PRODUCTION ROUTE: -out is a shared run-state that other nodes and the human
// read, written 0644 into a worktree the agent under review can write. Creating
// "<out>.v2.json" there needs no privilege and no race — the name is derivable
// from -out by anyone who can see the argv.
func TestSeal_Repair_SidecarWriteMustNotFollowSymlinks(t *testing.T) {
	defer red(t)

	withHooks(t, nil, fakeDigests{config: strings.Repeat("c", 64), diff: strings.Repeat("d", 64)}, nil)
	cls := sealFixtures()[0].classification(t)
	runStateBytes := []byte(`{"schema_version":1,"task_key":"SMG-1","status":"in_progress"}` + "\n")

	// newRun lays out a run-state and returns it with its derived sidecar path.
	newRun := func(t *testing.T) (dir, runState, sidecar string) {
		t.Helper()
		dir = t.TempDir()
		runState = filepath.Join(dir, "run.json")
		if err := os.WriteFile(runState, runStateBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		return dir, runState, V2SidecarPath(runState)
	}

	// CONTROL — a REAL regular file at the sidecar path is replaced, and the
	// result is a usable sidecar. This is the benign twin: same function, same
	// derived path, and it must come out the other way. A body that closed the
	// defect legs by refusing to write whenever the sidecar path exists fails
	// here, and so does one that writes an empty file.
	t.Run("CONTROL_regular_file_is_replaced", func(t *testing.T) {
		defer red(t)
		_, runState, sidecar := newRun(t)
		if err := os.WriteFile(sidecar, []byte(`{"schema_version":1,"stale_marker":"earlier run"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := WriteV2Sidecar(runState, cls); err != nil {
			t.Fatalf("CONTROL: WriteV2Sidecar errored on a regular stale sidecar: %v", err)
		}
		got, err := os.ReadFile(sidecar) // #nosec G304 -- a temp path this test created
		if err != nil {
			t.Fatalf("CONTROL: no sidecar at %s: %v", sidecar, err)
		}
		if bytes.Contains(got, []byte("stale_marker")) {
			t.Errorf("CONTROL: the prior sidecar survived — the write must fully replace:\n%s", got)
		}
		// PROOF OF EXECUTION: a body that "closed" this row by never writing
		// anything would leave nothing to parse here.
		var sc V2Sidecar
		if err := json.Unmarshal(got, &sc); err != nil {
			t.Fatalf("CONTROL: the replacement does not parse as V2Sidecar: %v\n%s", err, got)
		}
		if sc.Response.ComputedConfigSHA256 == "" || len(sc.Response.Classification) == 0 {
			t.Error("CONTROL: the sidecar carries no wrapper — the write happened but produced nothing usable")
		}
		if after, err := os.ReadFile(runState); err != nil || !bytes.Equal(after, runStateBytes) {
			t.Errorf("CONTROL: the run-state changed (err=%v)", err)
		}
	})

	// DEFECT LEG 1 — the sidecar path is a symlink to an unrelated file.
	t.Run("symlink_to_an_unrelated_file_is_overwritten", func(t *testing.T) {
		defer red(t)
		dir, runState, sidecar := newRun(t)
		decoy := filepath.Join(dir, "decoy.txt")
		decoyBytes := []byte("PRECIOUS — not classify's file\n")
		if err := os.WriteFile(decoy, decoyBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(decoy, sidecar); err != nil {
			t.Fatal(err)
		}

		err := WriteV2Sidecar(runState, cls)

		after, readErr := os.ReadFile(decoy) // #nosec G304 -- a temp path this test created
		if readErr != nil {
			t.Fatalf("reading the decoy back: %v", readErr)
		}
		if !bytes.Equal(after, decoyBytes) {
			t.Errorf("WRITING THE SIDECAR OVERWROTE A FILE IT DOES NOT OWN. WriteV2Sidecar returned %v.\n"+
				"  symlink: %s -> %s\n"+
				"  the decoy now holds %d bytes of sidecar instead of its own %d\n"+
				"contract.go:704 uses os.WriteFile, which opens O_WRONLY|O_CREATE|O_TRUNC and follows the link. The sidecar path is DERIVED from -out by appending \".v2.json\" (contract.go:498-505), so it is not a path any operator named — anyone who can create one file beside the run-state chooses which file classify truncates.",
				err, sidecar, decoy, len(after), len(decoyBytes))
		}
		if err == nil {
			if fi, statErr := os.Lstat(sidecar); statErr == nil && fi.Mode()&os.ModeSymlink != 0 {
				t.Errorf("the write reported success but %s is still a symlink — the sidecar was not written where the consumer will look for it", sidecar)
			}
		}
	})

	// DEFECT LEG 2 — the sidecar path is a symlink to the run-state. The
	// function's own doc says it NEVER touches that file.
	t.Run("symlink_to_the_run_state_destroys_it", func(t *testing.T) {
		defer red(t)
		_, runState, sidecar := newRun(t)
		if err := os.Symlink(runState, sidecar); err != nil {
			t.Fatal(err)
		}

		err := WriteV2Sidecar(runState, cls)

		after, readErr := os.ReadFile(runState) // #nosec G304 -- a temp path this test created
		if readErr != nil {
			t.Fatalf("THE RUN-STATE IS GONE after writing the sidecar (WriteV2Sidecar returned %v): %v", err, readErr)
		}
		if !bytes.Equal(after, runStateBytes) {
			t.Errorf("WRITING THE SIDECAR DESTROYED THE RUN-STATE. WriteV2Sidecar returned %v.\n"+
				"  symlink: %s -> %s\n"+
				"  before: %s"+
				"  after:  %.120s...\n"+
				"This is the bug the separate-file rule exists to prevent, arriving by the route the doc did not consider. contract.go:674-675: \"It NEVER touches the run-state file. If this function is ever seen to open opts.out, that is the bug.\" It does not open opts.out — it opens a symlink that IS opts.out.\n"+
				"The run-state is the shared file cmd/gates and cmd/iterate read; losing it loses the run.",
				err, sidecar, runState, runStateBytes, after)
		}
	})
}

// ─── WAVE 3 · G · no verdict is not a verdict ────────────────────────────────

// ROW H (HIGH) — verdictExits accepts EVERY child exit code and discards the
// child's output, so a consumer leg that never ran reads as a successful
// rewrite.
//
// readset.go:1060 is `if inv.verdictExits && errors.As(err, &exitErr) { return
// nil }`. Any exit at all is treated as "the process ran to completion and
// returned a verdict". cmd/iterate's exit codes are a CLOSED SET, written down
// at iterate/main.go:497 and :42-45: 0 APPROVE, 1 ITERATE, 2 ESCALATE, 3
// INVALID_INPUT. Three of those are verdicts. INVALID_INPUT is not — it is
// iterate saying it could not do the job — and anything outside the set is not
// iterate speaking at all.
//
// MEASURED TODAY, consumerInvocation{verdictExits: true}.run():
//
//	exit 0 -> nil    exit 1 -> nil    exit 2 -> nil
//	exit 3 -> nil    exit 127 -> nil     <- both wrong, and both silent
//
// THE CONSEQUENCE, which is why this is not a tidiness row: SidecarSurvives
// (readset.go:933-937) treats a nil from run() as "the rewrite happened" and
// goes on to report survived=true. An iterate leg that exited 3 rewrote
// nothing, so the sidecar's bytes are trivially unchanged — the seal at
// readset_seal_test.go:1014 then certifies a survival it never observed. That
// is the same shape as the stub P4 had to build to catch the last vacuity here,
// and the same shape as seal_verify's `exit_code is None -> passed`, which
// certified every seal in a repo whose test command could not launch.
//
// PRODUCTION ROUTE, verified rather than assumed: ../iterate/iterate really
// does exit 3, printing "=== ITERATE: INVALID_INPUT ===", both for a run-state
// it cannot read and for one carrying no classification. The pipeline hands it
// a run-state that cmd/gates has just rewritten, and gates is the component
// that destroys classification keys — so the input that makes iterate exit 3 is
// produced by the stage immediately before it.
func TestSeal_Repair_VerdictExitsAcceptsOnlyIteratesVerdictCodes(t *testing.T) {
	defer red(t)

	dir := t.TempDir()
	marker := filepath.Join(dir, "child-ran")

	// stub is a child that touches a marker and exits with the given code, so
	// every leg carries its own proof that a process really ran.
	stub := func(t *testing.T, name string, code int, say string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		script := fmt.Sprintf("#!/bin/sh\necho ran >> %q\n", marker)
		if say != "" {
			script += fmt.Sprintf("echo %q >&2\n", say)
		}
		script += fmt.Sprintf("exit %d\n", code)
		if err := os.WriteFile(p, []byte(script), 0o700); err != nil { // #nosec G302 -- a child this test executes
			t.Fatal(err)
		}
		return p
	}
	ranCount := func(t *testing.T) int {
		t.Helper()
		data, err := os.ReadFile(marker) // #nosec G304 -- a temp path this test created
		if err != nil {
			return 0
		}
		return strings.Count(string(data), "ran\n")
	}

	// CONTROL — iterate's three real verdicts. Each must be accepted. Without
	// these a body could close this row by rejecting every non-zero exit, which
	// would abort the measurement on a healthy run: readset.go:961-962 says
	// exactly why verdictExits exists.
	for _, c := range []struct {
		name string
		code int
	}{{"approve", 0}, {"iterate", 1}, {"escalate", 2}} {
		before := ranCount(t)
		inv := consumerInvocation{name: "iterate", bin: stub(t, "v"+c.name, c.code, ""), verdictExits: true}
		if err := inv.run(); err != nil {
			t.Errorf("CONTROL %s (exit %d): run() returned %v, want nil — %d is one of iterate's verdicts (iterate/main.go:42-45) and treating it as a crash would abort the measurement on a healthy run", c.name, c.code, err, c.code)
		}
		// PROOF OF EXECUTION: a run() that short-circuits without spawning
		// anything would satisfy the control by doing nothing.
		if ranCount(t) != before+1 {
			t.Errorf("CONTROL %s: the child was not executed — run() returned a verdict for a process it never spawned", c.name)
		}
	}

	// DEFECT — exit codes that are not verdicts.
	for _, c := range []struct {
		name, why, say string
		code           int
	}{
		{"invalid_input", "iterate's own INVALID_INPUT — it is saying it could NOT do the job", "=== ITERATE: INVALID_INPUT ===", 3},
		{"outside_the_set", "outside iterate's closed exit set entirely — this is not iterate speaking", "", 127},
	} {
		before := ranCount(t)
		inv := consumerInvocation{name: "iterate", bin: stub(t, "d"+c.name, c.code, c.say), verdictExits: true}
		err := inv.run()
		if ranCount(t) != before+1 {
			t.Fatalf("%s: the child was not executed, so this leg is measuring nothing", c.name)
		}
		if err == nil {
			t.Errorf("A CHILD THAT EXITED %d WAS ACCEPTED AS A VERDICT (%s).\n"+
				"readset.go:1060 accepts EVERY exit code once verdictExits is set: `if inv.verdictExits && errors.As(err, &exitErr) { return nil }`. iterate's codes are a closed set — 0 APPROVE, 1 ITERATE, 2 ESCALATE, 3 INVALID_INPUT (iterate/main.go:497) — and only the first three are verdicts.\n"+
				"SidecarSurvives (readset.go:933-937) reads that nil as \"the rewrite happened\" and reports survived=true. A leg that exited %d rewrote nothing, so the sidecar's bytes are trivially unchanged and the survival seal false-passes. Same shape as seal_verify's `exit_code is None -> passed`, which silently certified every seal in a repo whose test command could not launch.",
				c.code, c.why, c.code)
			continue
		}
		// The child's output must survive into the error. run() captures stdout
		// and stderr into one buffer and then drops it on this branch, so an
		// operator is told a leg failed and never told what it said.
		if c.say != "" && !strings.Contains(err.Error(), c.say) {
			t.Errorf("%s: run() raised %v but discarded the child's output %q — the buffer is captured at readset.go:1053-1054 and never surfaces on this branch", c.name, err, c.say)
		}
		if !strings.Contains(err.Error(), fmt.Sprint(c.code)) {
			t.Errorf("%s: run() raised %v without naming exit code %d, so the operator cannot tell a verdict from a failure", c.name, err, c.code)
		}
	}

	// CONTROL — verdictExits=false is unaffected. cmd/gates has no verdict
	// exits, and its non-zero exit was already a failure. This must not change.
	before := ranCount(t)
	inv := consumerInvocation{name: "gates", bin: stub(t, "gatesfail", 1, "gates blew up"), verdictExits: false}
	if err := inv.run(); err == nil {
		t.Error("CONTROL verdictExits=false: a non-zero exit from gates must stay a failure")
	} else if !strings.Contains(err.Error(), "gates blew up") {
		t.Errorf("CONTROL verdictExits=false: the error %v drops the child's output, which this branch already carried", err)
	}
	if ranCount(t) != before+1 {
		t.Error("CONTROL verdictExits=false: the child was not executed")
	}
}

// ─── WAVE 3 · L · readAll cannot fail ────────────────────────────────────────

// ROW I (HIGH) — readAll returns a nil error on EVERY stdin read failure, so
// the digest attests to a silently truncated diff.
//
// main.go:588-604. All three of its return statements are `return out, nil`:
// the io.EOF branch, the n==0 branch, and the branch that is reached with a
// genuine error after a partial read. There is no path on which a read failure
// becomes an error. readDiff (main.go:578-585) then records the bytes it got —
// however few — as the diff channel's digest, and unframedDigestSource's whole
// premise is that the digest describes "the bytes I classified".
//
// So a diff truncated by a failing pipe is classified as if it were the whole
// change, and the v2 wrapper attests to the truncation with a valid-looking
// SHA-256. Files dropped off the end of a diff are files no rule matched, and
// the fail-closed tier never fires for a file that is not there.
//
// The EOF branch is the same defect wearing a different hat: it compares
// err.Error() to the string "EOF" rather than testing errors.Is(err, io.EOF),
// so clean termination is decided by a message.
//
// MEASURED TODAY:
//
//	CONTROL  pipe with bytes, closed cleanly -> ("hello diff", <nil>)   correct
//	directory fd  (EISDIR)                   -> ("",           <nil>)
//	write-only fd (EBADF)                    -> ("",           <nil>)
//	closed fd     (file already closed)      -> ("",           <nil>)
//
// PRODUCTION ROUTE, verified end to end: `classify < <a directory>` reaches
// readAll — the shell opens the directory, os.Stdin.Stat() succeeds and the
// mode is not ModeCharDevice, so readDiff takes the stdin branch. The live
// binary reports "diff is empty — pass a file argument or pipe a diff to
// stdin", which is the wrong diagnosis: stdin was not empty, it was unreadable.
// The same swallow on a pipe that breaks mid-transfer truncates instead of
// emptying, and nothing downstream can tell.
func TestSeal_Repair_ReadAllMustNotSwallowReadErrors(t *testing.T) {
	defer red(t)

	dir := t.TempDir()

	// CONTROL 1 — a clean pipe carrying real bytes. Exact content, no error.
	// This is what fixes "success" for the defect legs: a body that closed them
	// by returning an error unconditionally fails here.
	want := []byte("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = w.Write(want)
		_ = w.Close()
	}()
	got, err := readAll(r)
	if err != nil {
		t.Fatalf("CONTROL clean pipe: readAll errored: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("CONTROL clean pipe: readAll returned %q, want %q — a row that cannot read the ordinary case is measuring nothing", got, want)
	}
	_ = r.Close()

	// CONTROL 2 — clean EOF on an empty pipe. Empty is a legitimate outcome and
	// must NOT become an error; it is validateInput's job to reject an empty
	// diff (main.go:355), with a message that says so.
	r2, w2, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = w2.Close()
	if got, err := readAll(r2); err != nil || len(got) != 0 {
		t.Errorf("CONTROL empty pipe: got (%q, %v), want (\"\", <nil>) — clean EOF is not a read failure", got, err)
	}
	_ = r2.Close()

	// THE DEFECT — three descriptors on which a read genuinely fails.
	victim := filepath.Join(dir, "diff.txt")
	if err := os.WriteFile(victim, want, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, leg := range []struct {
		name, why string
		open      func(t *testing.T) *os.File
	}{
		{
			name: "directory",
			why:  "EISDIR — `classify < <a directory>` reaches readAll on every uid",
			open: func(t *testing.T) *os.File {
				t.Helper()
				f, err := os.Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				return f
			},
		},
		{
			name: "write_only",
			why:  "EBADF — stdin inherited on a descriptor opened for writing",
			open: func(t *testing.T) *os.File {
				t.Helper()
				f, err := os.OpenFile(victim, os.O_WRONLY, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				return f
			},
		},
		{
			name: "closed",
			why:  "the descriptor is gone — a supervisor that closed the pipe under the process",
			open: func(t *testing.T) *os.File {
				t.Helper()
				f, err := os.Open(victim)
				if err != nil {
					t.Fatal(err)
				}
				_ = f.Close()
				return f
			},
		},
	} {
		f := leg.open(t)
		// FIXTURE VALIDITY: the descriptor must really be unreadable, or the
		// leg proves nothing.
		probe := make([]byte, 1)
		if _, perr := f.Read(probe); perr == nil {
			t.Errorf("fixture %s: the descriptor is readable, so this leg cannot exhibit the defect", leg.name)
			_ = f.Close()
			continue
		}
		data, err := readAll(f)
		_ = f.Close()
		if err != nil {
			continue // the failure is reported, which is the contract
		}
		t.Errorf("A FAILED READ WAS REPORTED AS A SUCCESSFUL ONE (%s: %s).\n"+
			"  readAll returned (%d bytes, <nil>) on a descriptor whose Read fails.\n"+
			"main.go:588-604: every one of readAll's three return statements is `return out, nil` — the io.EOF branch, the n==0 branch, and the branch reached with a real error after a partial read. A read failure cannot become an error.\n"+
			"readDiff (main.go:578-585) then records whatever it got as the diff channel's digest, and unframedDigestSource exists on the premise that the digest describes \"the bytes I classified\". A diff truncated by a failing pipe is classified as the whole change and attested with a valid-looking SHA-256; the files that fell off the end match no rule, and the fail-closed tier never fires for a file that is not there.\n"+
			"(The EOF branch is the same defect in another costume: it compares err.Error() to \"EOF\" instead of testing errors.Is(err, io.EOF), so clean termination is decided by a message.)",
			leg.name, leg.why, len(data))
	}
}
