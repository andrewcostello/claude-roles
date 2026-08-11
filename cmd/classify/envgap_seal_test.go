package main

// B1 ENV-GAP SEALS — the environment is not an authority channel for the
// money-path table.
//
// WHY THIS FILE EXISTS, AND WHY IT IS A NEW FILE. The B1 repair closed the
// $RISK_PATHS_CONFIG bypass by DELETING the candidate from configCandidates
// (main.go). TestSeal_Repair_EnvVarMustNotOutrankTheWorktreeMoneyTable is green
// as a result. The body author then measured, against its own interest, that
// the green is not load-bearing:
//
//	"My first mutation restored $RISK_PATHS_CONFIG as a plain CANDIDATE rather
//	 than as an outranking short-circuit, and no row went red. With the dual
//	 resolver now reachable, env-table + worktree-table = two differing tables =
//	 exitInvalid, which the row accepts as a pass. The case the row does not
//	 reach is an empty worktree plus an env-var-named table — one candidate
//	 present, resolved silently to the attacker's file."
//
// Verified here, by running it rather than by reasoning about it, in throwaway
// clones that restore the variable in each of the three shapes a reintroduction
// could plausibly take — as a plain candidate at the head of configCandidates,
// as a plain candidate at its tail, and as a short-circuit in resolveConfigPath
// ahead of the search (the historical shape):
//
//	                                    tree today   at HEAD          at TAIL          short-circuit
//	empty worktree     + env=attacker   exit 3       exit 0,attacker  exit 0,attacker  exit 0,attacker
//	populated worktree + env=differing  exit 0, ok   exit 3           exit 3           exit 0,attacker
//	populated worktree + env=identical  exit 0, own  exit 0,ENV path  exit 0, own      exit 0,ENV path
//
// Every one of the eighty-five green rows survives the two plain-candidate
// shapes, the existing repair row included. The money gate is closed today by
// an absence, and nothing notices when the absence ends.
//
// ─── THE PROPERTY ────────────────────────────────────────────────────────────
//
// THE ENVIRONMENT MAY NOT NAME, REDIRECT, OR JOIN THE SET OF MONEY-PATH TABLES.
// Operationally, and this is what the rows below assert:
//
//	For every worktree state W, the run's DECISION under any environment must be
//	identical to its decision with the config-naming variables cleared.
//	Identical, not merely "no worse" — the exit code, the resolved config_path,
//	financial_paths_touched, human_pr_gate and risk must all match, and the
//	INVALID_INPUT report must never name a path only the environment supplied.
//
// EQUALITY, NOT FAIL-CLOSED, is the deliberate strengthening over the existing
// repair row. That row treats exitInvalid as an acceptable outcome and returns
// early on it, which is precisely the door the plain-candidate mutation walks
// through: with a populated worktree the reintroduced variable produces a
// differing-tables refusal, and a refusal is "safe" only if you are looking at
// this diff. It is not safe as a property. An actor who can set an environment
// variable can then refuse classification of any diff at will, and — the point
// that matters more — the refusal is proof the variable IS being consulted as an
// authority. A rule that permits the environment to change the answer, in any
// direction, has already conceded the channel.
//
// ─── $RISK_PATHS_CONFIG, OR ENVIRONMENT-NAMED CONFIG GENERALLY? ──────────────
//
// GENERALLY. The defect is an authority channel, not a spelling. A body that
// satisfied a name-specific seal by reintroducing the identical mechanism under
// $CLASSIFY_CONFIG would have fixed nothing, and the seal would stay green —
// that is exactly the "green on an incidental substring" vacuity this unit has
// already produced once.
//
// But a purely general behavioural property is not testable by enumeration:
// there are infinitely many spellings, and a row that guesses six of them has
// collapsed an unbounded input space to whatever its author happened to type —
// the other vacuity shape this unit has produced. So the property is split, and
// the split is stated rather than hidden:
//
//   - Rows 1-3 are BEHAVIOURAL and use the ONE name the tree historically read,
//     $RISK_PATHS_CONFIG. It is the only spelling for which "production can
//     reach this input" is a verifiable claim rather than an invented one, and
//     it is the exact name the reintroduction mutation restores.
//   - Row 4 carries the GENERALISATION, structurally: the package's non-test
//     sources must perform no environment read at all. That is total over
//     spellings, it is the only leg that can see an env read whose result the
//     CLI cannot distinguish, and it is the only leg that fires on a rename.
//     Its second leg is a behavioural tripwire over rename candidates, labelled
//     as a tripwire — it is not evidence of anything on its own.
//
// SCOPE. The rows are about the CONFIG TABLE specifically. classify reads no
// environment variable for any purpose today, so row 4 freezes that at zero;
// if a legitimate environment input is ever added, row 4 is where the reason
// gets written down.
//
// ─── HOW THESE ROWS ARE HELD TO THE UNIT'S STANDARD ──────────────────────────
//
// Five vacuous-seal shapes have been measured on this unit: green on an
// unproducible input; a pass condition satisfiable by executing nothing; green
// on an incidental substring; a collapsed input space; and a recording that
// measures a frozen artifact so its trigger can never fire. Against each:
//
//   - FROZEN ARTIFACT. Every behavioural row goes through liveClassify
//     (repair_seal_test.go), which builds the CURRENT tree to a scratch path,
//     hashes the pinned ./classify before and after to prove the baseline was
//     not overwritten, proves the fresh artifact is not byte-identical to the
//     baseline, and requires it to answer a probe the pinned binary predates.
//     A row that exec'd ./classify would measure the v1 fixture and could never
//     see a source fix — see the amendment on
//     TestSeal_Recorded_EnvVarOutranksBothConfigDirectories, where P4 verified
//     that a source fix left that recorded row green.
//   - EXECUTING NOTHING / A CONSTANT. Every row carries a positive CONTROL
//     judged in the same call: a run that must come out the OTHER way. A body
//     that refuses everything fails the controls; a body that permits
//     everything fails the defect legs; row 3, whose legs are all "these two
//     must be equal", additionally proves in-call that the binary's decision is
//     a FUNCTION of its inputs and not a constant.
//   - UNPRODUCIBLE INPUT. Each row states its production route and the route is
//     verified against the tree, not asserted.
//   - COLLAPSED INPUT SPACE / SUBSTRING. Covered by the split above.
//
// DEPENDENCIES ON repair_seal_test.go. This file uses liveClassify, runLive,
// liveRun, realTable, driftedTable, assertTablesDisagreeOnMoney, writeDual,
// writeDiff and walletPath from that file, and red from seal_helpers_test.go.
// That coupling is deliberate and was directed: building
// the live producer any other way reintroduces the frozen-artifact hazard, and
// re-deriving the money/drifted fixtures locally would let two copies of "the
// table that names money" drift apart. Nothing in this file edits those files.

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ─── the environment under test ──────────────────────────────────────────────

// envGapHistoricalName is the variable configCandidates used to read, at the
// head of the candidate list, ahead of both worktree directories. It is the
// name the reintroduction mutation restores, and the only name for which the
// production route below is a verified fact rather than a guess.
const envGapHistoricalName = "RISK_PATHS_CONFIG"

// envGapRenameCandidates are spellings a future author might reasonably pick
// for the same mechanism, derived from this tree's own vocabulary: the binary
// is "classify", the table is "risk-paths.json", and the directories are
// ".agent" and ".claude".
//
// THEY ARE A TRIPWIRE, NOT EVIDENCE. No implementation reads any of them today,
// so these legs pass under every body that could plausibly be written, and a
// green here proves nothing. They exist for one case: someone closes rows 1-3
// by renaming the variable. Row 4's source scan is the real generalisation.
var envGapRenameCandidates = []string{
	"CLASSIFY_CONFIG",
	"CLASSIFY_RISK_PATHS",
	"RISK_PATHS",
	"RISK_PATHS_CONFIG_PATH",
	"AGENT_RISK_PATHS_CONFIG",
	"CLAUDE_RISK_PATHS_CONFIG",
}

// envGapAllNames is every spelling these rows control for.
func envGapAllNames() []string {
	return append([]string{envGapHistoricalName}, envGapRenameCandidates...)
}

// envGapCleared is the baseline environment: every candidate spelling present
// and empty.
//
// Set-to-empty rather than unset, because runLive can only APPEND to the
// parent's environment. Emptiness is the right baseline anyway: it is what the
// historical code treated as "not supplied", and it also neutralises a stray
// export in the operator's own shell, which would otherwise make the control
// run and the defect run share a hidden input.
func envGapCleared() []string {
	names := envGapAllNames()
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, n+"=")
	}
	return out
}

// envGapNaming is envGapCleared with one spelling pointed at path.
func envGapNaming(name, path string) []string {
	out := envGapCleared()
	return append(out, name+"="+path)
}

// envGapNamingAll points EVERY candidate spelling at path in one run, so the
// tripwire costs one exec instead of seven.
func envGapNamingAll(path string) []string {
	out := envGapCleared()
	for _, n := range envGapAllNames() {
		out = append(out, n+"="+path)
	}
	return out
}

// ─── the decision, and equality over it ──────────────────────────────────────

// envGapDecision is everything a caller acts on: the exit code, and — when the
// run produced a verdict at all — the four fields that decide whether money
// code gets a human.
//
// The fields are captured as "%T=%v" strings rather than as `any`, so equality
// is total and cannot panic on a non-comparable dynamic type, and so that a
// JSON true can never compare equal to the string "true".
type envGapDecision struct {
	exit   int
	fields map[string]string
}

// envGapDecisionFields is the closed set. config_path is in it deliberately:
// it is the provenance claim the run state carries and a human reads as "the
// table this verdict came from", so a right verdict attributed to the wrong
// table is still a false statement — see row 3.
var envGapDecisionFields = []string{"config_path", "financial_paths_touched", "human_pr_gate", "risk"}

func envGapDecisionOf(t *testing.T, r liveRun) envGapDecision {
	t.Helper()
	d := envGapDecision{exit: r.exit, fields: map[string]string{}}
	if r.exit != 0 {
		// A refusal produces the INVALID_INPUT report, not JSON. There is no
		// verdict to compare; the exit code and the report carry the whole
		// decision.
		return d
	}
	m := r.json(t)
	for _, k := range envGapDecisionFields {
		d.fields[k] = fmt.Sprintf("%T=%v", m[k], m[k])
	}
	return d
}

func (d envGapDecision) String() string {
	if len(d.fields) == 0 {
		return fmt.Sprintf("exit=%d (no verdict)", d.exit)
	}
	parts := make([]string, 0, len(d.fields))
	for _, k := range envGapDecisionFields {
		parts = append(parts, k+"="+d.fields[k])
	}
	return fmt.Sprintf("exit=%d %s", d.exit, strings.Join(parts, " "))
}

// envGapDiff names every way two decisions differ, or nil when they are the
// same decision.
func envGapDiff(a, b envGapDecision) []string {
	var out []string
	if a.exit != b.exit {
		out = append(out, fmt.Sprintf("exit: %d vs %d", a.exit, b.exit))
	}
	for _, k := range envGapDecisionFields {
		if a.fields[k] != b.fields[k] {
			out = append(out, fmt.Sprintf("%s: %s vs %s", k, envGapOrAbsent(a.fields[k]), envGapOrAbsent(b.fields[k])))
		}
	}
	return out
}

func envGapOrAbsent(s string) string {
	if s == "" {
		return "(no verdict)"
	}
	return s
}

// envGapAssertSilentAbout fails if the run's output names a path that only the
// environment supplied.
//
// This is the leg that catches the half-fix: a body that reintroduces the
// environment as a candidate and then REFUSES the resulting conflict has still
// let the environment into the search, and the missing-config report says so
// out loud — reportConfigSearch prints configCandidates verbatim under "Looked
// in:". Exit-code equality alone would not see it in every arrangement; this
// does.
func envGapAssertSilentAbout(t *testing.T, r liveRun, label, secret string) {
	t.Helper()
	if strings.Contains(r.all(), secret) {
		t.Errorf("%s: the run's own output names %s, a path supplied ONLY by the environment.\n"+
			"Whatever the exit code, the environment has been admitted to the config search — configCandidates is what reportConfigSearch prints under \"Looked in:\".\n%s",
			label, secret, r.all())
	}
}

// ─── shared fixtures ─────────────────────────────────────────────────────────

// envGapOutside returns a path OUTSIDE any worktree, holding data. The
// attacker's table has to live somewhere the worktree search would never reach
// on its own, or the row is measuring the ordinary candidate list.
func envGapOutside(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// envGapNeutralCwd is a working directory holding no config of any kind.
//
// Row E (TestSeal_Repair_NoFallbackToAnotherCheckoutsConfig) owns the
// CWD-relative fallback. These rows must not accidentally re-measure it or,
// worse, be rescued by it: running from cmd/classify or the repo root would put
// a real table within reach of a relative candidate and could turn an
// environment redirect into a same-answer coincidence.
func envGapNeutralCwd(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range agentConfigDirs {
		if _, err := os.Stat(filepath.Join(dir, d)); err == nil {
			t.Fatalf("fixture: the supposedly neutral cwd %s contains %s", dir, d)
		}
	}
	return dir
}

// envGapProductionRoute verifies, rather than asserts, that both halves of the
// input these rows use are ones production reaches.
//
// (1) THE ENVIRONMENT IS AGENT-CONTROLLED. classify is invoked from a shell the
// agent already drives — roles/tasker.md:224, skills/pr-raise.md:36 and
// README.md:35 all run it from a bash block, inheriting that shell's
// environment. An `export` in any earlier step of the same block, a repo
// dotfile, or a wrapper script reaches it. Nothing sanitises the environment on
// the way in; there is no code in this package that reads or clears it (row 4
// proves the "reads" half).
//
// (2) A WORKTREE WITH NO TABLE IS A NAMED, EXPECTED STATE, not a broken one.
// missingConfigMessage's own remedy is "classify init -worktree ...", which
// exists precisely because a project reaches classify before it has a table.
// The check below confirms that remedy text is still in the binary's mouth.
func envGapProductionRoute(t *testing.T, r liveRun) {
	t.Helper()
	if !strings.Contains(r.all(), "classify init -worktree") {
		t.Fatalf("production route: an empty worktree no longer produces the `classify init` remedy, so \"a worktree with no table is an expected state\" is no longer evidenced by the tool itself:\n%s", r.all())
	}
}

// ─── ROW 1 · the gap · an empty worktree makes the variable decisive ─────────
//
// ROW 1 (CRITICAL) — with no table in the worktree, an environment-named table
// is the ONLY candidate, so it is resolved silently and unanimously. No dual
// comparison fires, because there is nothing to compare against. This is the
// state the existing repair row cannot reach: its defect leg populates the
// worktree, which is what turns the reintroduced candidate into a two-table
// conflict and hands the row an exitInvalid it accepts as a pass.
//
// MEASURED, live builds of a clone that restores the variable as a plain
// candidate, cwd neutral, -worktree an empty directory, the variable naming a
// non-scaffold table with every financial rule removed:
//
//	restored at the HEAD of the candidate list -> exit 0,
//	  config_path = <attacker>, financial_paths_touched = false,
//	  human_pr_gate = false, risk = high
//	restored at the TAIL of the candidate list -> byte-identical outcome
//
// on apps/finance-domain/wallet/service/debit.go, which the worktree's own
// table — if it had one — calls critical, financial and gated. Today the same
// input is exit 3, ConfigSearchAbsent.
//
// THIS ROW IS THE LOAD-BEARING ONE. Ordering does not save it: with an empty
// worktree the environment's candidate is the only present candidate wherever
// it sits in the list, so the row reddens under EVERY placement of the
// reintroduction. Rows 2 and 3 are placement-sensitive; this one is not.
//
// PRODUCTION ROUTE: see envGapProductionRoute, checked in-call.
func TestSeal_EnvGap_EmptyWorktreePlusEnvNamedTableMustNotClassify(t *testing.T) {
	defer red(t)

	money, drifted := realTable(t), driftedTable(t)
	// FIXTURE VALIDITY. If the attacker's table ever stopped disagreeing with
	// the real one about the wallet path, resolving it would be harmless and
	// this row would pass by measuring nothing. It raises instead.
	assertTablesDisagreeOnMoney(t, money, drifted)

	bin := liveClassify(t)
	cwd := envGapNeutralCwd(t)
	attacker := envGapOutside(t, "attacker-risk-paths.json", drifted)
	diffPath := writeDiff(t, t.TempDir(), walletPath)
	empty := t.TempDir()

	// CONTROL — a populated worktree, no environment. It must SUCCEED, with the
	// worktree's own table and the correct money verdict. Judged in the same
	// call because every leg below is satisfied by a binary that refuses
	// everything, and this is what tells an implementation from that constant.
	pop := writeDual(t, money, nil)
	popDiff := writeDiff(t, pop, walletPath)
	ctrl := runLive(t, bin, cwd, envGapCleared(), "-json", "-no-git", "-worktree", pop, popDiff)
	if ctrl.exit != 0 {
		t.Fatalf("CONTROL populated+no-env exited %d, want 0 — the ordinary case must keep working\n%s", ctrl.exit, ctrl.all())
	}
	cd := envGapDecisionOf(t, ctrl)
	if want := fmt.Sprintf("%T=%v", "", filepath.Join(pop, ".agent", "risk-paths.json")); cd.fields["config_path"] != want {
		t.Fatalf("CONTROL populated+no-env resolved %s, want the worktree's own .agent table", cd.fields["config_path"])
	}
	if cd.fields["financial_paths_touched"] != "bool=true" || cd.fields["human_pr_gate"] != "bool=true" {
		t.Fatalf("CONTROL populated+no-env: %s is not financial+gated under the real table — the fixture is wrong (%s)", walletPath, cd)
	}

	// BASELINE — the same empty worktree with the environment cleared. This is
	// the decision the defect leg must reproduce exactly.
	base := runLive(t, bin, cwd, envGapCleared(), "-json", "-no-git", "-worktree", empty, diffPath)
	if base.exit != exitInvalid {
		t.Fatalf("BASELINE empty+no-env exited %d, want %d (ConfigSearchAbsent) — a missing config is INVALID_INPUT, not a default\n%s", base.exit, exitInvalid, base.all())
	}
	envGapProductionRoute(t, base)

	// THE DEFECT — the same empty worktree, with the variable naming the
	// attacker's table.
	got := runLive(t, bin, cwd, envGapNaming(envGapHistoricalName, attacker), "-json", "-no-git", "-worktree", empty, diffPath)

	// The half-fix leg: even a run that refuses must not have LOOKED there.
	envGapAssertSilentAbout(t, got, "empty worktree + $"+envGapHistoricalName, attacker)

	if diffs := envGapDiff(envGapDecisionOf(t, got), envGapDecisionOf(t, base)); len(diffs) > 0 {
		t.Errorf("AN ENVIRONMENT VARIABLE NAMED THE MONEY TABLE FOR A WORKTREE THAT HAS NONE.\n"+
			"  -worktree %s   (empty: no .agent/, no .claude/)\n"+
			"  cwd       %s   (neutral: no table within reach of a relative candidate)\n"+
			"  $%s=%s   (a non-scaffold table with every financial rule removed)\n"+
			"  with the variable:  %s\n"+
			"  with it cleared:    %s\n"+
			"  differences:        %s\n"+
			"With no table in the worktree the environment's candidate is the ONLY candidate, so the dual-config comparison has nothing to compare and resolves it unanimously and silently. %s is critical, financial and gated under the project's real table; under the attacker's it is neither financial nor gated.\n"+
			"An agent that can set an environment variable is not an operator naming a rule table. The operator's -config still names one file explicitly and is honoured ahead of the search (resolveConfigPath), so closing this costs the flag nothing — row 2 fences that.",
			empty, cwd, envGapHistoricalName, attacker,
			envGapDecisionOf(t, got), envGapDecisionOf(t, base), strings.Join(diffs, "; "), walletPath)
	}
}

// ─── ROW 2 · the differing case, judged by equality rather than by safety ────
//
// ROW 2 (HIGH) — a populated worktree plus an environment-named table that
// DISAGREES. The input is the one the existing repair row already uses. The
// judgment is not.
//
// IS THIS A CONTROL, OR A DUPLICATE? NEITHER, and it was worth checking rather
// than assuming. Measured against the restored-candidate clone:
//
//	TestSeal_Repair_EnvVarMustNotOutrankTheWorktreeMoneyTable  -> GREEN
//	this row                                                   -> RED
//
// The two rows put the same bytes on disk and disagree about the answer. The
// repair row opens its defect leg with `if got.exit == exitInvalid { return }`
// — "refusing is one of the two acceptable outcomes" — and the reintroduced
// candidate produces exactly that refusal, because now there are two present
// tables whose bytes differ and ResolveConfigDual is reachable. So the input is
// covered and the CONCLUSION is not: the row concludes "safe", and the true
// state is "the environment is back in the search and is deciding outcomes".
//
// It is therefore not a control either — a control is a leg that must stay
// green under the mutation, and this one must not. It is a strictly stronger
// judgment on a shared input, which is why it is stated as its own row rather
// than smuggled into row 1.
//
// WHY REFUSAL IS NOT SAFETY. Two reasons, in order of weight. First, it is
// evidence: a refusal that only appears when the variable is set is a refusal
// caused by the variable, which is the whole finding. Second, it is a harm in
// its own right — anyone who can set the variable can make classify refuse
// every diff, and roles/tasker.md:224 says an exit 3 means "fix the input and
// re-run", which sends a human hunting a wrong worktree or a stale base while
// the real cause is an exported string.
//
// MEASURED: tree today, exit 0 with the worktree's table and the correct money
// verdict, variable set or cleared. Restored as a plain candidate at either end
// of the list: exit 3.
func TestSeal_EnvGap_DifferingEnvTableMustNotChangeTheDecision(t *testing.T) {
	defer red(t)

	money, drifted := realTable(t), driftedTable(t)
	assertTablesDisagreeOnMoney(t, money, drifted)

	bin := liveClassify(t)
	cwd := envGapNeutralCwd(t)
	wt := writeDual(t, money, nil)
	diffPath := writeDiff(t, wt, walletPath)
	attacker := envGapOutside(t, "attacker-risk-paths.json", drifted)
	trusted := filepath.Join(wt, ".agent", "risk-paths.json")

	// CONTROL 1 — the environment cleared. This is the decision that must
	// survive, and it is also the "not a constant refuser" control.
	base := runLive(t, bin, cwd, envGapCleared(), "-json", "-no-git", "-worktree", wt, diffPath)
	if base.exit != 0 {
		t.Fatalf("CONTROL no-env exited %d, want 0\n%s", base.exit, base.all())
	}
	bd := envGapDecisionOf(t, base)
	if want := fmt.Sprintf("%T=%v", "", trusted); bd.fields["config_path"] != want {
		t.Fatalf("CONTROL no-env resolved %s, want the worktree's .agent table %q", bd.fields["config_path"], trusted)
	}
	if bd.fields["financial_paths_touched"] != "bool=true" || bd.fields["human_pr_gate"] != "bool=true" {
		t.Fatalf("CONTROL no-env: %s is not financial+gated under the trusted table — the fixture is wrong (%s)", walletPath, bd)
	}

	// CONTROL 2 — the operator's flag, naming the very file the environment
	// tried to inject. It must STILL be honoured. This fences the fix: a body
	// that closed row 1 by refusing every table outside the worktree, or by
	// refusing whenever a config path came from anywhere unusual, breaks the
	// -config contract and fails here. It is green today and must stay green.
	flagged := runLive(t, bin, cwd, envGapCleared(), "-json", "-no-git", "-worktree", wt, "-config", attacker, diffPath)
	if flagged.exit != 0 {
		t.Fatalf("CONTROL explicit -config exited %d, want 0 — naming the rule table is that flag's whole contract and the fix must not take it away\n%s", flagged.exit, flagged.all())
	}
	fd := envGapDecisionOf(t, flagged)
	if want := fmt.Sprintf("%T=%v", "", attacker); fd.fields["config_path"] != want {
		t.Errorf("CONTROL explicit -config resolved %s, want the named file %q", fd.fields["config_path"], attacker)
	}
	// CONTROL 2 doubles as the PROOF OF EXECUTION for this row and row 3: the
	// two controls above differ in their verdict, on the same binary and the
	// same diff, so the decision demonstrably varies with its inputs. Every
	// remaining leg is an equality, and an equality between two constants is
	// satisfiable by a binary that does nothing.
	if len(envGapDiff(bd, fd)) == 0 {
		t.Fatalf("fixture: the trusted table and the attacker table produce the SAME decision (%s), so every equality leg below is satisfiable by a constant and this row would prove nothing", bd)
	}

	// THE DEFECT — the same file, offered through the environment instead of
	// through the flag.
	got := runLive(t, bin, cwd, envGapNaming(envGapHistoricalName, attacker), "-json", "-no-git", "-worktree", wt, diffPath)
	envGapAssertSilentAbout(t, got, "populated worktree + differing $"+envGapHistoricalName, attacker)

	if diffs := envGapDiff(envGapDecisionOf(t, got), bd); len(diffs) > 0 {
		t.Errorf("AN ENVIRONMENT VARIABLE CHANGED THE DECISION.\n"+
			"  -worktree %s   (holds the project's real table at %s)\n"+
			"  $%s=%s\n"+
			"  with the variable:  %s\n"+
			"  with it cleared:    %s\n"+
			"  differences:        %s\n"+
			"The environment must be INERT, not merely fail-closed. A refusal that appears only when the variable is set is caused by the variable, which is the finding; and it is a harm of its own — roles/tasker.md:224 reads exit 3 as \"fix the input (wrong worktree, stale base, empty diff) and re-run\", so an exported string sends a human hunting the wrong cause.\n"+
			"CONTROL 2 above shows the operator's -config naming this same file is still honoured, so nothing here asks the fix to cost that flag.",
			wt, trusted, envGapHistoricalName, attacker,
			envGapDecisionOf(t, got), bd, strings.Join(diffs, "; "))
	}
}

// ─── ROW 3 · agreement is not a licence ──────────────────────────────────────
//
// ROW 3 (MEDIUM) — a populated worktree plus an environment-named table whose
// bytes are IDENTICAL to the worktree's own. The verdict is right either way.
// The question is whether that makes the variable harmless.
//
// THE RULING: NO. An agreeing environment-named table must be as inert as a
// disagreeing one. It must not become the resolved config_path, it must not
// join the agreement set, and it must not change any part of the decision.
// Three reasons, and the first is the one that decides it:
//
//  1. AGREEMENT IS THE ATTACKER'S TO ARRANGE. A rule of the form "the
//     environment may name the table when its bytes agree" grants authority as
//     a function of content, and the content is chosen by whoever set the
//     variable. Pre-positioning an agreeing copy is one `cp`. And agreement is
//     decided at ONE INSTANT: ResolveConfigDual digests the candidates and then
//     certifies present[0]'s BYTES — so where the environment's candidate heads
//     the list, the bytes that get parsed, classified and digested are the
//     attacker's file's, not the worktree's. Measured: under the
//     restored-at-HEAD clone this row's input yields config_path = the
//     environment's path.
//  2. PROVENANCE IS PART OF THE ANSWER. config_path is written into the run
//     state and read downstream and by a human as "the table this verdict came
//     from". A correct verdict attributed to a file outside the worktree is a
//     false statement about where the project's money rules live, and it is
//     false at exactly the moment someone is auditing why a gate did or did not
//     fire.
//  3. THIS UNIT HAS ALREADY REFUSED "RIGHT BY COINCIDENCE" ONCE.
//     contract_seal_test.go:1055 records that human_pr_gate survived a redirect
//     only because the attacker's table happened to be a scaffold, and names it
//     "luck, not design". Agreement is the same luck wearing a better suit.
//
// KNOWN LIMIT, STATED RATHER THAN PAPERED OVER. Measured: restored at the HEAD
// of the candidate list, this row goes RED (config_path moves to the
// environment's path). Restored at the TAIL, the decision is byte-identical to
// the cleared run and this row stays GREEN — with agreeing bytes and the
// worktree's own candidate winning the ordering, the environment's
// participation is genuinely unobservable at the CLI. That corner is covered by
// row 4's source scan, which sees the read itself rather than its consequences,
// and by row 1, which reddens under every placement. This row does not claim
// what it cannot see.
func TestSeal_EnvGap_IdenticalEnvTableIsNotALicence(t *testing.T) {
	defer red(t)

	money := realTable(t)

	bin := liveClassify(t)
	cwd := envGapNeutralCwd(t)
	wt := writeDual(t, money, nil)
	diffPath := writeDiff(t, wt, walletPath)
	trusted := filepath.Join(wt, ".agent", "risk-paths.json")

	// The twin: the same bytes, at a path outside the worktree.
	twin := envGapOutside(t, "identical-risk-paths.json", money)

	// FIXTURE VALIDITY — the whole row depends on these two facts, and each
	// could quietly stop holding.
	onDisk, err := os.ReadFile(trusted) // #nosec G304 -- a temp path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	twinBytes, err := os.ReadFile(twin) // #nosec G304 -- a temp path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, twinBytes) {
		t.Fatalf("fixture: %s and %s are not byte-identical, so this row is measuring the DIFFERING case that row 2 already owns", trusted, twin)
	}
	if strings.HasPrefix(twin, wt+string(os.PathSeparator)) {
		t.Fatalf("fixture: the twin %s is inside the worktree %s, so the ordinary candidate search could reach it and the row would not be about the environment at all", twin, wt)
	}

	// CONTROL — the environment cleared. Positive, and the decision every leg
	// below must reproduce. A body that refuses everything fails here.
	base := runLive(t, bin, cwd, envGapCleared(), "-json", "-no-git", "-worktree", wt, diffPath)
	if base.exit != 0 {
		t.Fatalf("CONTROL no-env exited %d, want 0\n%s", base.exit, base.all())
	}
	bd := envGapDecisionOf(t, base)
	if want := fmt.Sprintf("%T=%v", "", trusted); bd.fields["config_path"] != want {
		t.Fatalf("CONTROL no-env resolved %s, want the worktree's own table %q", bd.fields["config_path"], trusted)
	}
	if bd.fields["financial_paths_touched"] != "bool=true" || bd.fields["human_pr_gate"] != "bool=true" {
		t.Fatalf("CONTROL no-env: %s is not financial+gated — the fixture is wrong (%s)", walletPath, bd)
	}

	// PROOF THE DECISION IS NOT A CONSTANT. Every other leg of this row is an
	// equality between two runs that SHOULD agree, which a binary that emits a
	// fixed payload satisfies perfectly. So the same binary is asked, in the
	// same call, a question with a different right answer: classify the same
	// diff against a table with the financial rules removed. If that comes back
	// identical, the equalities below are worthless and the row says so.
	drifted := driftedTable(t)
	assertTablesDisagreeOnMoney(t, money, drifted)
	other := envGapOutside(t, "drifted-risk-paths.json", drifted)
	varied := runLive(t, bin, cwd, envGapCleared(), "-json", "-no-git", "-worktree", wt, "-config", other, diffPath)
	if varied.exit != 0 {
		t.Fatalf("PROOF-OF-EXECUTION run exited %d, want 0\n%s", varied.exit, varied.all())
	}
	if len(envGapDiff(bd, envGapDecisionOf(t, varied))) == 0 {
		t.Fatalf("the live producer returned the SAME decision (%s) for two tables that classify %s differently — its output is not a function of its config input, so every equality in this row is vacuous", bd, walletPath)
	}

	// THE DEFECT — the identical twin, offered through the environment.
	got := runLive(t, bin, cwd, envGapNaming(envGapHistoricalName, twin), "-json", "-no-git", "-worktree", wt, diffPath)
	envGapAssertSilentAbout(t, got, "populated worktree + identical $"+envGapHistoricalName, twin)

	if diffs := envGapDiff(envGapDecisionOf(t, got), bd); len(diffs) > 0 {
		t.Errorf("AN AGREEING ENVIRONMENT-NAMED TABLE STILL CHANGED THE ANSWER.\n"+
			"  -worktree %s   (its own table: %s)\n"+
			"  $%s=%s   (byte-identical to the worktree's table, outside the worktree)\n"+
			"  with the variable:  %s\n"+
			"  with it cleared:    %s\n"+
			"  differences:        %s\n"+
			"Agreement is not a licence. The bytes agree because whoever set the variable arranged for them to agree — it is one `cp` — and ResolveConfigDual certifies present[0]'s bytes, so a candidate that heads the list is the one parsed, classified and digested even when a second table agrees with it.\n"+
			"config_path is the provenance the run state carries and a human reads as \"the table this verdict came from\". A right verdict attributed to a file outside the worktree is a false statement about where this project's money rules live, and it is false exactly when someone is auditing why a gate fired.\n"+
			"This unit has refused right-by-coincidence before: contract_seal_test.go:1055 calls a verdict that survived a redirect only because the attacker's table was a scaffold \"luck, not design\".",
			wt, trusted, envGapHistoricalName, twin,
			envGapDecisionOf(t, got), bd, strings.Join(diffs, "; "))
	}
}

// ─── ROW 4 · the generalisation: no environment read at all ──────────────────
//
// ROW 4 (HIGH) — the property is about environment-named config GENERALLY, and
// rows 1-3 can only speak about one spelling. This row carries the rest.
//
// LEG A, THE REAL ONE — the package's non-test sources must perform no
// environment read whatsoever. Total over spellings, and it is the only leg in
// this file that can see a read whose result the CLI cannot distinguish: the
// tail-placement corner of row 3, where an agreeing environment table is
// consulted and changes nothing observable. It reads the AST rather than
// grepping, so a comment or a string literal naming os.Getenv cannot satisfy or
// trip it — this unit has already shipped one seal that passed on a spelling
// while the thing it named was happening (TestConfigCandidates_PrefersVendor-
// NeutralDir rejected candidates containing "claude-workflow" while a relative
// candidate resolved into exactly that repo).
//
// The frozen count is ZERO. If a legitimate environment input is ever needed,
// this is the row that has to be amended, and the amendment is where the reason
// gets written down and reviewed — which is the point. It is deliberately
// broader than "config": a variable that selected the contract version, the
// panel shape or the risk floor would be the same authority channel wearing a
// different label.
//
// LEG B, THE TRIPWIRE — rename candidates, all pointed at the attacker's table
// in a single run, on the empty worktree where any consulted candidate is
// decisive. No implementation reads these names, so a green proves nothing and
// the comment on envGapRenameCandidates says so. It fires on exactly one event:
// someone closes rows 1-3 by renaming the variable.
//
// MEASURED: leg A finds zero reads in the tree today and finds the restored
// os.Getenv("RISK_PATHS_CONFIG") under both placements of the reintroduction
// mutation. Its control finds the one real read in main_test.go, so the scanner
// is demonstrably able to see a positive.
func TestSeal_EnvGap_TheConfigSearchReadsNoEnvironment(t *testing.T) {
	defer red(t)

	// ── LEG A · the source scan ──────────────────────────────────────────────

	// CONTROL — the scanner must be able to SEE an environment read. main_test.go
	// contains exactly one (os.Getenv("HOME"), main_test.go:34) and it is not
	// this file's, so the control cannot be satisfied by the literals in this
	// file's own tripwire list. A scanner that matched nothing would report a
	// clean tree forever.
	if hits := envGapEnvReads(t, "main_test.go"); len(hits) == 0 {
		t.Fatalf("CONTROL: the scanner found no environment read in main_test.go, which contains one — it cannot see a positive, so its clean report over the production sources means nothing")
	}

	// THE FROZEN SET — zero.
	var all []string
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("fixture: no non-test .go sources found in the package directory, so this scan is measuring nothing")
	}
	for _, n := range names {
		all = append(all, envGapEnvReads(t, n)...)
	}
	if len(all) > 0 {
		t.Errorf("THE PACKAGE READS THE ENVIRONMENT. Found %d read(s) in %d production source(s) (%s):\n  %s\n"+
			"The frozen count is ZERO, and the reason is the whole of this file: an environment variable that reaches the config search is an authority channel over the money-path table that an agent can set and an operator cannot see. Rows 1-3 can only name one spelling; this leg is what makes the property hold for all of them, and it is the only leg that can see a read whose effect the CLI cannot distinguish.\n"+
			"If this read is legitimate, it does not get added quietly: amend this row with the variable's name and the reason, so the next reviewer sees a decision instead of a diff.",
			len(all), len(names), strings.Join(names, ", "), strings.Join(all, "\n  "))
	}

	// ── LEG B · the rename tripwire ──────────────────────────────────────────

	money, drifted := realTable(t), driftedTable(t)
	assertTablesDisagreeOnMoney(t, money, drifted)

	bin := liveClassify(t)
	cwd := envGapNeutralCwd(t)
	attacker := envGapOutside(t, "attacker-risk-paths.json", drifted)
	diffPath := writeDiff(t, t.TempDir(), walletPath)
	empty := t.TempDir()

	// CONTROL — a populated worktree with the whole candidate set cleared must
	// still succeed with the right verdict, so leg B cannot be satisfied by a
	// binary that refuses everything.
	pop := writeDual(t, money, nil)
	ctrl := runLive(t, bin, cwd, envGapCleared(), "-json", "-no-git", "-worktree", pop, writeDiff(t, pop, walletPath))
	if ctrl.exit != 0 {
		t.Fatalf("CONTROL populated+cleared exited %d, want 0\n%s", ctrl.exit, ctrl.all())
	}
	if cd := envGapDecisionOf(t, ctrl); cd.fields["financial_paths_touched"] != "bool=true" || cd.fields["human_pr_gate"] != "bool=true" {
		t.Fatalf("CONTROL populated+cleared: %s is not financial+gated — the fixture is wrong (%s)", walletPath, cd)
	}

	base := runLive(t, bin, cwd, envGapCleared(), "-json", "-no-git", "-worktree", empty, diffPath)
	if base.exit != exitInvalid {
		t.Fatalf("BASELINE empty+cleared exited %d, want %d\n%s", base.exit, exitInvalid, base.all())
	}

	got := runLive(t, bin, cwd, envGapNamingAll(attacker), "-json", "-no-git", "-worktree", empty, diffPath)
	envGapAssertSilentAbout(t, got, "empty worktree + every candidate spelling", attacker)
	if diffs := envGapDiff(envGapDecisionOf(t, got), envGapDecisionOf(t, base)); len(diffs) > 0 {
		t.Errorf("A RENAMED ENVIRONMENT VARIABLE NAMES THE MONEY TABLE.\n"+
			"  set simultaneously, all pointing at %s: %s\n"+
			"  with them set:      %s\n"+
			"  with them cleared:  %s\n"+
			"  differences:        %s\n"+
			"Rows 1-3 seal $%s, the spelling this tree historically read. This leg exists so that closing them by renaming the mechanism does not read as a fix. Re-run with one name at a time to find which.",
			attacker, strings.Join(envGapAllNames(), " "),
			envGapDecisionOf(t, got), envGapDecisionOf(t, base), strings.Join(diffs, "; "), envGapHistoricalName)
	}
}

// envGapEnvReads returns "file:line: expr" for every environment read in a Go
// source file.
//
// AST, not grep. The read is identified as a call to Getenv, LookupEnv, Environ
// or ExpandEnv selected from the identifier `os` or `syscall`, which cannot be
// satisfied by a comment or by a string literal that merely spells it — see the
// note on row 4 about a seal in this package that checked a spelling instead of
// a fact and passed while the fact was false.
func envGapEnvReads(t *testing.T, file string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	readers := map[string]bool{"Getenv": true, "LookupEnv": true, "Environ": true, "ExpandEnv": true}
	pkgs := map[string]bool{"os": true, "syscall": true}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !readers[sel.Sel.Name] {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || !pkgs[ident.Name] {
			return true
		}
		out = append(out, fmt.Sprintf("%s: %s.%s(...)", fset.Position(call.Pos()), ident.Name, sel.Sel.Name))
		return true
	})
	return out
}
