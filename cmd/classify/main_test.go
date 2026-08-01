package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realConfig loads the example-monorepo fixture: the evenplay-mono rule table
// as validated against 98 real PR merges. It is a fixture rather than the live
// config because the live one now lives in the project it describes — the rule
// table is project-specific and does not belong in this shared tooling repo.
//
// liveConfigStillMatches below guards the drift that split introduces.
func realConfig(t *testing.T) *Config {
	t.Helper()
	data, err := os.ReadFile("testdata/example-monorepo.json")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	cfg, err := parseConfig(data)
	if err != nil {
		t.Fatalf("fixture invalid: %v", err)
	}
	return cfg
}

// The live config lives in the project. If this machine has that checkout, it
// must still parse and still classify the money paths the fixture asserts —
// otherwise the fixture is quietly testing a config nobody runs.
func TestLiveProjectConfigStillClassifiesMoney(t *testing.T) {
	t.Parallel()
	live := filepath.Join(os.Getenv("HOME"), "Project/evenplay-mono/.claude/risk-paths.json")
	data, err := os.ReadFile(live)
	if err != nil {
		t.Skipf("live project config not on this machine: %v", err)
	}
	cfg, err := parseConfig(data)
	if err != nil {
		t.Fatalf("LIVE config is invalid: %v", err)
	}
	if cfg.Scaffold {
		t.Error("live config is still marked scaffold — its money paths were never reviewed")
	}
	for _, f := range []string{
		"apps/finance-domain/wallet/service/debit.go",
		"apps/platform-domain/bay-session/store/admin_bet_force_refund.go",
		"libs/go/wallet/balance.go",
	} {
		d := diffFor(f)
		cls := classify(cfg, parseDiffFiles(d), d)
		if !cls.FinancialPathsTouched {
			t.Errorf("live config: %s is not financial — the human PR gate would not fire", f)
		}
	}
}

func TestShippedConfigIsValid(t *testing.T) {
	t.Parallel()
	realConfig(t)
}

func TestMatchGlob(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"apps/finance-domain/wallet/**", "apps/finance-domain/wallet/service/debit.go", true},
		{"apps/finance-domain/wallet/**", "apps/finance-domain/wallet", true},
		{"apps/finance-domain/wallet/**", "apps/finance-domain/paygate/auth/Token.java", false},
		{"**/*.sql", "apps/platform-domain/core/dao/migrations/001_init.sql", true},
		{"**/*.sql", "main.go", false},
		{"**/migrations/**", "apps/game-domain/paylines/migrations/002_add.py", true},
		{"apps/*/e2e/**", "apps/burrito-golf-web/e2e/spec.ts", true},
		{"apps/*/e2e/**", "apps/burrito-golf-web/src/e2e/spec.ts", false},
		{"apps/platform-domain/bay-session/store/accept_bet*", "apps/platform-domain/bay-session/store/accept_bet_test.go", true},
		{"apps/platform-domain/bay-session/store/accept_bet*", "apps/platform-domain/bay-session/store/sqlc/accept_bet.go", false},
		{"apps/platform-domain/bay-session/cmd/*-recovery/**", "apps/platform-domain/bay-session/cmd/refund-recovery/main.go", true},
		{"**/pb/**", "apps/finance-domain/wallet/pb/wallet.pb.go", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.pattern+"__"+tt.name, func(t *testing.T) {
			t.Parallel()
			if got := matchGlob(tt.pattern, tt.name); got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
			}
		})
	}
}

func TestMaxRisk(t *testing.T) {
	t.Parallel()
	if got := maxRisk("low", "critical"); got != "critical" {
		t.Errorf("maxRisk(low, critical) = %q", got)
	}
	if got := maxRisk("high", "medium"); got != "high" {
		t.Errorf("maxRisk(high, medium) = %q", got)
	}
}

func diffFor(files ...string) string {
	var b strings.Builder
	for _, f := range files {
		b.WriteString("diff --git a/" + f + " b/" + f + "\n")
		b.WriteString("--- a/" + f + "\n+++ b/" + f + "\n@@ -1 +1 @@\n-old\n+new\n")
	}
	return b.String()
}

func TestParseDiffFiles(t *testing.T) {
	t.Parallel()
	d := diffFor("apps/finance-domain/wallet/service/debit.go", "README.md")
	got := parseDiffFiles(d)
	if len(got) != 2 || got[0] != "apps/finance-domain/wallet/service/debit.go" || got[1] != "README.md" {
		t.Fatalf("parseDiffFiles = %v", got)
	}
}

// evenplay-mono really contains
// "apps/skillstrike-mobile/src/components/ SupportGuideHeader.tsx". Splitting
// the `diff --git` header on whitespace yields "SupportGuideHeader.tsx", which
// matches no rule and lands in unmatched_files — fail-closed, but wrong.
func TestParseDiffFiles_PathWithSpace(t *testing.T) {
	t.Parallel()
	p := "apps/skillstrike-mobile/src/components/ SupportGuideHeader.tsx"
	d := "diff --git a/" + p + " b/" + p + "\n--- a/" + p + "\n+++ b/" + p + "\n@@ -1 +1 @@\n-a\n+b\n"

	got := parseDiffFiles(d)
	if len(got) != 1 || got[0] != p {
		t.Fatalf("parseDiffFiles = %#v, want [%q]", got, p)
	}
}

func TestParseDiffFiles_AdditionDeletionRenameBinary(t *testing.T) {
	t.Parallel()
	d := strings.Join([]string{
		// addition
		"diff --git a/apps/x/new.go b/apps/x/new.go",
		"new file mode 100644",
		"--- /dev/null",
		"+++ b/apps/x/new.go",
		"@@ -0,0 +1 @@",
		"+package x",
		// deletion — the b-side is /dev/null, so the a-side names the file
		"diff --git a/apps/x/old.go b/apps/x/old.go",
		"deleted file mode 100644",
		"--- a/apps/x/old.go",
		"+++ /dev/null",
		"@@ -1 +0,0 @@",
		"-package x",
		// rename — header sides differ; the +++ line is authoritative
		"diff --git a/apps/x/from.go b/apps/x/to.go",
		"similarity index 98%",
		"rename from apps/x/from.go",
		"rename to apps/x/to.go",
		"--- a/apps/x/from.go",
		"+++ b/apps/x/to.go",
		// binary — no ---/+++ at all, so the header is the only source
		"diff --git a/apps/x/logo.png b/apps/x/logo.png",
		"Binary files a/apps/x/logo.png and b/apps/x/logo.png differ",
		"",
	}, "\n")

	got := parseDiffFiles(d)
	want := []string{"apps/x/new.go", "apps/x/old.go", "apps/x/to.go", "apps/x/logo.png"}
	if len(got) != len(want) {
		t.Fatalf("parseDiffFiles = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestHeaderPath(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"a/x.go b/x.go":                   "x.go",
		"a/apps/w/s.go b/apps/w/s.go":     "apps/w/s.go",
		"a/dir/ sp.tsx b/dir/ sp.tsx":     "dir/ sp.tsx",
		"a/apps/x/from.go b/apps/x/to.go": "apps/x/to.go", // rename → last field
	}
	for rest, want := range tests {
		if got := headerPath(rest); got != want {
			t.Errorf("headerPath(%q) = %q, want %q", rest, got, want)
		}
	}
}

// Rules added after the 60-PR validation sweep on 2026-07-31. Each one closed a
// real gap the sweep exposed, so each gets a test.
func TestClassify_ValidationSweepGaps(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	tests := []struct {
		name       string
		file       string
		wantRisk   string
		wantFin    bool
		wantClient bool
	}{
		{"shared wallet lib", "libs/go/wallet/balance.go", "critical", true, false},
		{"shared authz lib", "libs/go/authz/check.go", "high", false, false},
		{"shared go lib", "libs/go/logging/log.go", "high", false, false},
		{"messaging lib", "libs/messaging/pkg/bus.go", "high", false, false},
		{"deploy manifest", "deploy/bay-session.yaml", "high", false, false},
		{"terraform vars", "terraform/environments/production/production.tfvars", "high", false, false},
		{"generated wire client", "apps/skillstrike-mobile/generated/smg_public_api_pb.ts", "high", false, false},
		{"analytics model", "analytics/backtesting/paylines_v2_backtest.py", "medium", false, false},
		{"root e2e harness", "tests/e2e/playwright/tests/cross-app/x.spec.ts", "low", false, true},
		{"mobile app root", "apps/skillstrike-mobile/App.tsx", "low", false, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := diffFor(tt.file)
			cls := classify(cfg, parseDiffFiles(d), d)
			if len(cls.UnmatchedFiles) != 0 {
				t.Errorf("%s is unclassified: %v", tt.file, cls.UnmatchedFiles)
			}
			if cls.Risk != tt.wantRisk {
				t.Errorf("risk = %q, want %q", cls.Risk, tt.wantRisk)
			}
			if cls.FinancialPathsTouched != tt.wantFin {
				t.Errorf("financial = %v, want %v", cls.FinancialPathsTouched, tt.wantFin)
			}
			if cls.ClientOnly != tt.wantClient {
				t.Errorf("client_only = %v, want %v", cls.ClientOnly, tt.wantClient)
			}
		})
	}
}

// PR 1262 (native card tokenization, 40 files) and PR 1350 (autoplay wager
// preference, 43 files) both took the single-reviewer carve-out because every
// changed path sat under apps/skillstrike-mobile/src/. Client withdrawal and
// card-entry UI displays balances and submits money movements — PR 1294's
// escaped regression was a display bug — so it is not presentation.
func TestClassify_ClientMoneyUIIsNotPresentation(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	for _, f := range []string{
		"apps/skillstrike-mobile/src/modals/withdraw-flow/AmountStep.tsx",
		"apps/skillstrike-mobile/src/screens/User/Account/Tabs/Wallet/AddCardInfoModal/index.tsx",
		"apps/skillstrike-mobile/src/components/WalletBalance.tsx",
		"apps/skillstrike-mobile/src/__tests__/modals/WithdrawFlow.test.tsx",
	} {
		f := f
		t.Run(f, func(t *testing.T) {
			t.Parallel()
			d := diffFor(f)
			cls := classify(cfg, parseDiffFiles(d), d)
			if cls.ClientOnly {
				t.Error("client_only = true — money UI must not qualify for the carve-out")
			}
			if cls.Panel.Reduced || cls.Panel.Seats != 5 {
				t.Errorf("panel = %+v, want the full 5-seat panel", cls.Panel)
			}
		})
	}

	// A genuinely presentational mobile screen still gets the carve-out.
	d := diffFor("apps/skillstrike-mobile/src/screens/Leaderboard/Row.tsx")
	cls := classify(cfg, parseDiffFiles(d), d)
	if !cls.Panel.Reduced {
		t.Errorf("non-money mobile UI should still qualify: %+v", cls.Panel)
	}
}

func TestClassify_WalletIsCriticalAndGated(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	d := diffFor("apps/finance-domain/wallet/service/debit.go")
	cls := classify(cfg, parseDiffFiles(d), d)

	if cls.Risk != "critical" {
		t.Errorf("risk = %q, want critical", cls.Risk)
	}
	if !contains(cls.Components, "wallet") {
		t.Errorf("components = %v, want wallet", cls.Components)
	}
	if !cls.FinancialPathsTouched {
		t.Error("financial_paths_touched = false, want true")
	}
	if !cls.HumanPRGate {
		t.Error("human_pr_gate = false, want true")
	}
	if cls.RecheckMinSeverity != "medium" {
		t.Errorf("recheck_min_severity = %q, want medium (component preset applies)", cls.RecheckMinSeverity)
	}
	if cls.Panel.Seats != 5 || cls.Panel.Reduced {
		t.Errorf("panel = %+v, want 5 seats not reduced", cls.Panel)
	}
}

// The regression this node exists to prevent: bet settlement and refund code
// lives under apps/platform-domain/bay-session/, which pr-raise.md's documented
// financial-paths list does not cover. Path-based classification must catch it.
func TestClassify_BaySessionMoneyPathsAreFinancial(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	for _, f := range []string{
		"apps/platform-domain/bay-session/store/bet_settlement_tiers.go",
		"apps/platform-domain/bay-session/store/admin_bet_force_refund.go",
		"apps/platform-domain/bay-session/store/admin_bet_dispute_reverse.go",
		"apps/platform-domain/bay-session/store/accept_bet.go",
		"apps/platform-domain/bay-session/store/wager.go",
		"apps/platform-domain/bay-session/cmd/settling-bet-recovery/reconcile.go",
	} {
		f := f
		t.Run(f, func(t *testing.T) {
			t.Parallel()
			d := diffFor(f)
			cls := classify(cfg, parseDiffFiles(d), d)
			if cls.Risk != "critical" {
				t.Errorf("risk = %q, want critical", cls.Risk)
			}
			if !cls.FinancialPathsTouched {
				t.Error("financial_paths_touched = false — the human PR gate would not fire")
			}
			if len(cls.Components) == 0 {
				t.Error("no component preset — reviewer would use the generic tier floor")
			}
		})
	}
}

func TestClassify_BaySessionNonMoneyStaysHigh(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	d := diffFor("apps/platform-domain/bay-session/store/bay_station_register.go")
	cls := classify(cfg, parseDiffFiles(d), d)
	if cls.Risk != "high" {
		t.Errorf("risk = %q, want high", cls.Risk)
	}
	if cls.FinancialPathsTouched {
		t.Error("financial_paths_touched = true on a non-money bay-session file — gate would over-fire")
	}
}

func TestClassify_ClientPresentationGetsReducedPanel(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	d := diffFor("apps/skillstrike-mobile/src/components/Scoreboard.tsx")
	cls := classify(cfg, parseDiffFiles(d), d)

	if !cls.ClientOnly {
		t.Error("client_only = false, want true")
	}
	if !cls.Panel.Reduced || cls.Panel.Seats != 1 {
		t.Errorf("panel = %+v, want reduced 1 seat", cls.Panel)
	}
	args := reviewerArgs(Repo{Worktree: "/wt", BaseRef: "origin/main"}, cls)
	if !contains(args, "-reviewers") {
		t.Errorf("reviewer_args = %v, want -reviewers for reduced panel", args)
	}
}

// PR 1298: a "client-only" debug-panel change was a fail-open gate. Content
// signals must revoke the carve-out even when every path is presentational.
func TestClassify_GateSignalRevokesCarveOut(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	f := "apps/skillstrike-mobile/src/components/DebugPanel.tsx"
	d := "diff --git a/" + f + " b/" + f + "\n--- a/" + f + "\n+++ b/" + f +
		"\n@@ -1 +1 @@\n-const show = false\n+const show = __DEV__ || process.env.SHOW_DEBUG === '1'\n"

	cls := classify(cfg, parseDiffFiles(d), d)
	if !cls.ClientOnly {
		t.Fatal("expected client_only true — the path is presentational")
	}
	if len(cls.GateSignals) == 0 {
		t.Fatal("no gate signals detected on a __DEV__/env gate")
	}
	if cls.Panel.Reduced || cls.Panel.Seats != 5 {
		t.Errorf("panel = %+v, want full 5-seat panel", cls.Panel)
	}
}

func TestClassify_ClientAuthIsNotPresentation(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	d := diffFor("apps/skillstrike-mobile/src/auth/token.ts")
	cls := classify(cfg, parseDiffFiles(d), d)
	if cls.ClientOnly {
		t.Error("client_only = true for client auth — token handling is a security surface")
	}
	if cls.Risk != "high" {
		t.Errorf("risk = %q, want high", cls.Risk)
	}
	if cls.Panel.Reduced {
		t.Error("reduced panel on client auth")
	}
}

func TestClassify_MixedDiffTakesMaxAndDisqualifiesCarveOut(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	d := diffFor(
		"apps/skillstrike-mobile/src/components/Scoreboard.tsx",
		"apps/finance-domain/wallet/service/debit.go",
	)
	cls := classify(cfg, parseDiffFiles(d), d)
	if cls.Risk != "critical" {
		t.Errorf("risk = %q, want critical (max wins)", cls.Risk)
	}
	if cls.ClientOnly {
		t.Error("client_only = true on a mixed diff")
	}
	if !cls.ServerSurface {
		t.Error("server_surface = false despite a .go file")
	}
}

func TestClassify_UnmatchedPathFailsClosed(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	d := diffFor("apps/brand-new-service/handler.go")
	cls := classify(cfg, parseDiffFiles(d), d)

	if len(cls.UnmatchedFiles) != 1 {
		t.Fatalf("unmatched_files = %v, want 1 entry", cls.UnmatchedFiles)
	}
	if cls.Risk != cfg.UnmatchedRisk {
		t.Errorf("risk = %q, want unmatched_risk %q", cls.Risk, cfg.UnmatchedRisk)
	}
	if cls.Panel.Reduced {
		t.Error("reduced panel on an unclassified path")
	}
}

func TestClassify_MigrationRoutesSkill(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	d := diffFor("apps/platform-domain/core/dao/migrations/017_add_col.sql")
	cls := classify(cfg, parseDiffFiles(d), d)
	if !cls.Migration {
		t.Error("migration = false on a migrations/*.sql change")
	}
	if !contains(cls.Skills, "migration-checklist") {
		t.Errorf("skills = %v, want migration-checklist", cls.Skills)
	}
	if !contains(cls.Skills, "critical-review-dispatch") {
		t.Errorf("skills = %v, want critical-review-dispatch at high risk", cls.Skills)
	}
}

func TestClassify_DocsOnlyStaysLow(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	d := diffFor("docs/plans/2026-07-29-graph-spine.md", "README.md")
	cls := classify(cfg, parseDiffFiles(d), d)
	if cls.Risk != "low" {
		t.Errorf("risk = %q, want low", cls.Risk)
	}
	if !cls.Panel.Reduced {
		t.Error("docs-only change should still get one reviewer, reduced")
	}
	if cls.HumanPRGate {
		t.Error("human_pr_gate on a docs change")
	}
}

func TestReviewerArgs_AlwaysCarriesRiskAndComponents(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	d := diffFor("apps/finance-domain/wallet/service/debit.go")
	cls := classify(cfg, parseDiffFiles(d), d)
	args := reviewerArgs(Repo{Worktree: "/wt", BaseRef: "origin/main"}, cls)
	joined := strings.Join(args, " ")

	for _, want := range []string{"-cwd /wt", "-base origin/main", "-risk critical", "-component wallet"} {
		if !strings.Contains(joined, want) {
			t.Errorf("reviewer_args %q missing %q", joined, want)
		}
	}
}

func TestParseConfig_RejectsUnknownComponent(t *testing.T) {
	t.Parallel()
	bad := `{"schema_version":1,"unmatched_risk":"high","rules":[
	  {"id":"r","paths":["a/**"],"risk":"critical","components":["wallett"]}]}`
	if _, err := parseConfig([]byte(bad)); err == nil {
		t.Fatal("expected error for unknown component")
	} else if !strings.Contains(err.Error(), "unknown component") {
		t.Errorf("error = %v", err)
	}
}

func TestParseConfig_RejectsPresentationWithMoney(t *testing.T) {
	t.Parallel()
	bad := `{"schema_version":1,"unmatched_risk":"high","rules":[
	  {"id":"r","paths":["a/**"],"risk":"low","presentation":true,"financial":true}]}`
	if _, err := parseConfig([]byte(bad)); err == nil {
		t.Fatal("expected error: a presentation rule cannot be financial")
	}
}

func TestParseConfig_RejectsDuplicateID(t *testing.T) {
	t.Parallel()
	bad := `{"schema_version":1,"unmatched_risk":"high","rules":[
	  {"id":"r","paths":["a/**"],"risk":"low"},
	  {"id":"r","paths":["b/**"],"risk":"high"}]}`
	if _, err := parseConfig([]byte(bad)); err == nil {
		t.Fatal("expected error for duplicate rule id")
	}
}

func TestScanGateSignals_IgnoresContextLines(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	f := "apps/skillstrike-mobile/src/App.tsx"
	// The gate token appears only on an unchanged context line.
	d := "diff --git a/" + f + " b/" + f + "\n--- a/" + f + "\n+++ b/" + f +
		"\n@@ -1,3 +1,3 @@\n if (__DEV__) {\n-  log('a')\n+  log('b')\n }\n"
	if hits := scanGateSignals(cfg, d); len(hits) != 0 {
		t.Errorf("gate signals on context lines: %+v", hits)
	}
}

func TestWriteRunState_PreservesOtherNodesFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "run.json")

	seed := `{"schema_version":1,"task_key":"SMG-1","created_at":"2026-07-01T00:00:00Z",
	  "repo":{"worktree":"/old","base_ref":"origin/main","base_sha":"abc1234"},
	  "gates":{"test":{"status":"pass"}},
	  "rounds":[{"round":1,"kind":"full","verdict":"iterate"}],
	  "status":"in_progress"}`
	if err := os.WriteFile(p, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	cls := &Classification{Risk: "critical", Panel: Panel{Required: true, Seats: 5}}
	repo := Repo{Worktree: "/new", BaseRef: "origin/main", BaseSHA: "def5678"}
	if err := writeRunState(p, "", repo, cls); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		`"task_key": "SMG-1"`,       // preserved
		`"created_at": "2026-07-01`, // preserved
		`"status": "pass"`,          // gates preserved
		`"verdict": "iterate"`,      // rounds preserved
		`"worktree": "/new"`,        // classify's field updated
		`"risk": "critical"`,        // classification written
	} {
		if !strings.Contains(got, want) {
			t.Errorf("run state missing %s\n%s", want, got)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// These tables are consumed by Go binaries, not by any one assistant:
// risk-paths.json says which paths are money, gates.json says which checks run.
// Naming the directory after an assistant implies they only matter to it.
func TestConfigCandidates_PrefersVendorNeutralDir(t *testing.T) {
	t.Parallel()
	got := configCandidates("/some/project")
	if len(got) < 2 {
		t.Fatalf("candidates = %v", got)
	}
	if got[0] != "/some/project/.agent/risk-paths.json" {
		t.Errorf("candidates[0] = %q, want .agent first", got[0])
	}
	if got[1] != "/some/project/.claude/risk-paths.json" {
		t.Errorf("candidates[1] = %q, want .claude as the compatibility fallback", got[1])
	}
	for _, c := range got {
		if strings.Contains(c, "claude-workflow") {
			t.Errorf("candidate %q reaches into the tooling repo — that is the cross-project bug", c)
		}
	}
}
