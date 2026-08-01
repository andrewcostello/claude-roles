package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// realConfig loads the example-gates fixture. The LIVE table lives in the
// project it describes — its commands and coverage floors are project-specific.
func realConfig(t *testing.T) *Config {
	t.Helper()
	data, err := os.ReadFile("testdata/example-gates.json")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	cfg, err := parseConfig(data)
	if err != nil {
		t.Fatalf("fixture invalid: %v", err)
	}
	return cfg
}

func TestShippedConfigIsValid(t *testing.T) {
	t.Parallel()
	realConfig(t)
}

// The mutation gate's whole history is invocation errors. Lock the fixed form in.
func TestShippedConfig_MutationCommandIsCorrect(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	cmd := cfg.Gates["mutation"].Command

	if strings.Contains(cmd, "gremlins-go") {
		t.Error("command uses gremlins-go — the binary is `gremlins`")
	}
	if !strings.HasPrefix(cmd, "gremlins unleash") {
		t.Errorf("command = %q, want it to start with `gremlins unleash`", cmd)
	}
	if !strings.Contains(cmd, "--threshold-efficacy") {
		t.Error("no --threshold-efficacy — the tool's own exit code is the gate")
	}
	if !cfg.Gates["mutation"].ZeroUnitsIsFailure {
		t.Error("zero_units_is_failure must be set: a vacuous mutation run reads as a pass")
	}
}

func TestParseConfig_RejectsUnknownTrigger(t *testing.T) {
	t.Parallel()
	bad := `{"schema_version":1,"module_marker":"go.mod","gates":{
	  "x":{"command":"true","trigger":"whenever","scope":"module"}}}`
	if _, err := parseConfig([]byte(bad)); err == nil {
		t.Fatal("expected error for unknown trigger")
	}
}

func TestParseConfig_RejectsDanglingDerivedFrom(t *testing.T) {
	t.Parallel()
	bad := `{"schema_version":1,"module_marker":"go.mod","gates":{
	  "cov":{"derived_from":"nope","trigger":"always","scope":"module"}}}`
	if _, err := parseConfig([]byte(bad)); err == nil {
		t.Fatal("expected error for dangling derived_from")
	}
}

// ─── module discovery: the multi-module monorepo case ────────────────────────

func TestDiscoverModules_MultiModuleRepo(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	for _, m := range []string{
		"apps/finance-domain/wallet",
		"apps/platform-domain/bay-session",
	} {
		if err := os.MkdirAll(filepath.Join(wt, m, "service"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, m, "go.mod"), []byte("module x\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	mods, err := discoverModules(wt, "go.mod", []string{
		"apps/finance-domain/wallet/service/debit.go",
		"apps/finance-domain/wallet/service/credit.go",
		"apps/platform-domain/bay-session/service/accept.go",
		"README.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 2 {
		t.Fatalf("got %d modules, want 2: %+v", len(mods), mods)
	}
	if mods[0].Rel != "apps/finance-domain/wallet" {
		t.Errorf("mods[0].Rel = %q", mods[0].Rel)
	}
	if len(mods[0].Packages) != 1 {
		t.Errorf("wallet packages = %v, want 1 deduped entry", mods[0].Packages)
	}
}

func TestDiscoverModules_NoModuleForChangedFile(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, "apps/thing"), 0755); err != nil {
		t.Fatal(err)
	}
	mods, err := discoverModules(wt, "go.mod", []string{"apps/thing/main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 0 {
		t.Errorf("got %d modules, want 0 — no go.mod anywhere above the file", len(mods))
	}
}

func TestDiscoverModules_RootModule(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "go.mod"), []byte("module x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wt, "internal/svc"), 0755); err != nil {
		t.Fatal(err)
	}
	mods, err := discoverModules(wt, "go.mod", []string{"internal/svc/a.go", "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 1 || mods[0].Rel != "." {
		t.Fatalf("mods = %+v, want one module at .", mods)
	}
	if len(mods[0].Packages) != 2 {
		t.Errorf("packages = %v, want internal/svc and .", mods[0].Packages)
	}
}

// ─── plan derivation ─────────────────────────────────────────────────────────

func planNames(plans []plan) []string {
	var out []string
	for _, p := range plans {
		out = append(out, p.Gate)
	}
	return out
}

func TestDerivePlan_LowRiskSkipsStaticAnalysis(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	mods := []Module{{Rel: "m", Root: t.TempDir(), Packages: []string{"m/pkg"}}}
	cls := &Classification{Risk: "low"}

	got := planNames(derivePlan(cfg, cls, mods, "implementation", nil))
	for _, gate := range []string{"gosec", "staticcheck", "semgrep", "mutation"} {
		if contains(got, gate) {
			t.Errorf("%s planned at low risk: %v", gate, got)
		}
	}
	for _, gate := range []string{"build", "test", "lint", "complexity", "coverage"} {
		if !contains(got, gate) {
			t.Errorf("%s not planned: %v", gate, got)
		}
	}
}

func TestDerivePlan_HighRiskAddsStaticAnalysis(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	mods := []Module{{Rel: "m", Root: t.TempDir(), Packages: []string{"m/pkg"}}}
	cls := &Classification{Risk: "high"}

	got := planNames(derivePlan(cfg, cls, mods, "implementation", nil))
	for _, gate := range []string{"gosec", "staticcheck", "semgrep"} {
		if !contains(got, gate) {
			t.Errorf("%s not planned at high risk: %v", gate, got)
		}
	}
	if contains(got, "mutation") {
		t.Error("mutation planned without a money component")
	}
}

func TestDerivePlan_MutationOnlyForMoneyModules(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	mods := []Module{
		{Rel: "apps/finance-domain/wallet", Root: t.TempDir(), Packages: []string{"apps/finance-domain/wallet/service"}},
		{Rel: "apps/platform-domain/notifications", Root: t.TempDir(), Packages: []string{"apps/platform-domain/notifications/send"}},
	}
	cls := &Classification{Risk: "critical", Components: []string{"wallet"}}

	plans := derivePlan(cfg, cls, mods, "implementation", nil)
	var mutationModules []string
	for _, p := range plans {
		if p.Gate == "mutation" && p.Module != nil {
			mutationModules = append(mutationModules, p.Module.Rel)
		}
	}
	if len(mutationModules) != 1 || mutationModules[0] != "apps/finance-domain/wallet" {
		t.Errorf("mutation modules = %v, want only the wallet module", mutationModules)
	}
}

func TestDerivePlan_DeclaredGatesAreOptIn(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	mods := []Module{{Rel: "m", Root: t.TempDir(), Packages: []string{"m/pkg"}}}
	cls := &Classification{Risk: "critical", Components: []string{"wallet"}}

	if got := planNames(derivePlan(cfg, cls, mods, "implementation", nil)); contains(got, "differential") {
		t.Error("differential planned without being declared")
	}
	got := planNames(derivePlan(cfg, cls, mods, "implementation", map[string]bool{"differential": true}))
	if !contains(got, "differential") {
		t.Errorf("differential not planned when declared: %v", got)
	}
}

func TestDerivePlan_DomainSuiteOnlyOnIteration(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	mods := []Module{{Rel: "m", Root: t.TempDir(), Packages: []string{"m/pkg"}}}
	cls := &Classification{Risk: "high"}

	if got := planNames(derivePlan(cfg, cls, mods, "implementation", nil)); contains(got, "domain_suite") {
		t.Error("domain_suite planned outside an iteration")
	}
	if got := planNames(derivePlan(cfg, cls, mods, "iteration", nil)); !contains(got, "domain_suite") {
		t.Errorf("domain_suite not planned on iteration: %v", got)
	}
}

func TestDerivePlan_BuildOrderedFirst(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	mods := []Module{{Rel: "m", Root: t.TempDir(), Packages: []string{"m/pkg"}}}
	got := planNames(derivePlan(cfg, &Classification{Risk: "critical", Components: []string{"wallet"}}, mods, "implementation", nil))
	if len(got) == 0 || got[0] != "build" {
		t.Errorf("first gate = %v, want build", got)
	}
}

// ─── coverage parsing and evaluation ─────────────────────────────────────────

const goTestOutput = `
ok  	example.com/m/service	1.204s	coverage: 96.4% of statements
ok  	example.com/m/dao	0.812s	coverage: 64.0% of statements
?   	example.com/m/pb	[no test files]
ok  	example.com/m/handler	0.400s	coverage: 41.2% of statements
`

func TestParseCoverage(t *testing.T) {
	t.Parallel()
	cov := parseCoverage(goTestOutput)
	if len(cov) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(cov), cov)
	}
	byPkg := map[string]pkgCoverage{}
	for _, c := range cov {
		byPkg[c.Pkg] = c
	}
	if got := byPkg["example.com/m/service"].Pct; got != 96.4 {
		t.Errorf("service coverage = %v, want 96.4", got)
	}
	if !byPkg["example.com/m/pb"].NoTests {
		t.Error("pb should be flagged NoTests")
	}
}

// Regression: an earlier regex captured the DURATION column as the package name,
// so every changed-package lookup missed and the gate passed having evaluated
// nothing. Assert the package name is the package name.
func TestParseCoverage_CapturesPackageNotDuration(t *testing.T) {
	t.Parallel()
	real := "ok  \tgithub.com/yourorg/claude-workflow/classify\t0.011s\tcoverage: 53.8% of statements\n"
	cov := parseCoverage(real)
	if len(cov) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(cov), cov)
	}
	if cov[0].Pkg != "github.com/yourorg/claude-workflow/classify" {
		t.Errorf("Pkg = %q — a duration or timing field leaked into the package slot", cov[0].Pkg)
	}
	if cov[0].Pct != 53.8 {
		t.Errorf("Pct = %v, want 53.8", cov[0].Pct)
	}
}

func TestParseCoverage_CachedAndNoStatements(t *testing.T) {
	t.Parallel()
	out := "ok  \texample.com/m/a\t(cached)\tcoverage: 71.0% of statements\n" +
		"ok  \texample.com/m/types\t0.002s\tcoverage: [no statements]\n"
	cov := parseCoverage(out)
	if len(cov) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(cov), cov)
	}
	byPkg := map[string]pkgCoverage{}
	for _, c := range cov {
		byPkg[c.Pkg] = c
	}
	if byPkg["example.com/m/a"].Pct != 71.0 {
		t.Errorf("cached line not parsed: %+v", byPkg["example.com/m/a"])
	}
	if !byPkg["example.com/m/types"].NoStatements {
		t.Error("[no statements] not flagged")
	}
}

// The core anti-vacuity rule: if the changed-package filter matches nothing, the
// gate FAILS rather than reporting a pristine 100%.
func TestEvaluateCoverage_NoMatchedPackageIsFailure(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	p := plan{Gate: "coverage", Module: &Module{Rel: "m", Packages: []string{"m/service"}}}
	out := "ok  \texample.com/other/unrelated\t0.011s\tcoverage: 12.0% of statements\n"

	g := evaluateCoverage(cfg, p, out)
	if g.Status != "fail" {
		t.Errorf("status = %q, want fail — nothing was evaluated", g.Status)
	}
	if got := g.Metrics["packages_evaluated"]; got != 0 {
		t.Errorf("packages_evaluated = %v, want 0", got)
	}
}

func TestChangedPathFor_ExactViaModulePath(t *testing.T) {
	t.Parallel()
	m := &Module{
		Rel:        "apps/finance-domain/wallet",
		ImportPath: "github.com/EvenPlay/evenplay-mono/apps/finance-domain/wallet",
		Packages:   []string{"apps/finance-domain/wallet/service", "apps/finance-domain/wallet"},
	}
	tests := []struct {
		importPath string
		want       string
		ok         bool
	}{
		{"github.com/EvenPlay/evenplay-mono/apps/finance-domain/wallet/service", "apps/finance-domain/wallet/service", true},
		{"github.com/EvenPlay/evenplay-mono/apps/finance-domain/wallet", "apps/finance-domain/wallet", true},
		{"github.com/EvenPlay/evenplay-mono/apps/finance-domain/wallet/db", "", false},
		// A same-named package in a DIFFERENT module must not match.
		{"github.com/EvenPlay/evenplay-mono/apps/platform-domain/core/service", "", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.importPath, func(t *testing.T) {
			t.Parallel()
			got, ok := changedPathFor(m, tt.importPath)
			if ok != tt.ok || (ok && got != tt.want) {
				t.Errorf("changedPathFor(%q) = (%q, %v), want (%q, %v)", tt.importPath, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestReadModulePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(p, []byte("// comment\n\nmodule github.com/x/y/z\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := readModulePath(p); got != "github.com/x/y/z" {
		t.Errorf("readModulePath = %q", got)
	}
	if got := readModulePath(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("missing go.mod = %q, want empty", got)
	}
}

func TestDiscoverModules_ReadsImportPath(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, "apps/w/service"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "apps/w/go.mod"), []byte("module example.com/apps/w\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mods, err := discoverModules(wt, "go.mod", []string{"apps/w/service/a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 1 || mods[0].ImportPath != "example.com/apps/w" {
		t.Fatalf("mods = %+v, want ImportPath example.com/apps/w", mods)
	}
}

// Without go.mod readable, suffix matching still works for sub-packages but
// cannot resolve the module root — and must report that as no-work, not a pass.
func TestChangedPathFor_SuffixFallback(t *testing.T) {
	t.Parallel()
	m := &Module{Rel: "apps/finance-domain/wallet", Packages: []string{
		"apps/finance-domain/wallet/service",
		"apps/finance-domain/wallet",
	}}
	tests := []struct {
		importPath string
		want       string
		ok         bool
	}{
		{"github.com/EvenPlay/mono/apps/finance-domain/wallet/service", "apps/finance-domain/wallet/service", true},
		{"github.com/EvenPlay/mono/apps/finance-domain/wallet/db", "", false},
		{"example.com/other/service", "apps/finance-domain/wallet/service", true}, // suffix match is intentional within a module
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.importPath, func(t *testing.T) {
			t.Parallel()
			got, ok := changedPathFor(m, tt.importPath)
			if ok != tt.ok || (ok && got != tt.want) {
				t.Errorf("changedPathFor(%q) = (%q, %v), want (%q, %v)", tt.importPath, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// The real cmd/classify module: one changed package at the module root. Its
// worktree path lands in the **/cmd/** wiring tier (floor 50), so 53.8% passes —
// but it must be genuinely EVALUATED, which is what the earlier regex bug broke.
// Resolving a module-root package needs the import path from go.mod.
func TestEvaluateCoverage_RootPackageIsActuallyEvaluated(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	p := plan{Gate: "coverage", Module: &Module{
		Rel:        "cmd/classify",
		ImportPath: "github.com/yourorg/claude-workflow/classify",
		Packages:   []string{"cmd/classify"},
	}}
	out := "ok  \tgithub.com/yourorg/claude-workflow/classify\t0.011s\tcoverage: 53.8% of statements\n"

	g := evaluateCoverage(cfg, p, out)
	if got := g.Metrics["packages_evaluated"]; got != 1 {
		t.Errorf("packages_evaluated = %v, want 1 — the gate must actually look at the package", got)
	}
	if got := g.Metrics["worst_coverage_pct"]; got != 53.8 {
		t.Errorf("worst_coverage_pct = %v, want 53.8 (was 100 while the regex was broken)", got)
	}
	if g.Status != "pass" {
		t.Errorf("status = %q, want pass (53.8%% ≥ 50%% wiring floor): %+v", g.Status, g.Metrics)
	}
}

// Same shape, but under the 95% financial floor: must fail.
func TestEvaluateCoverage_FinancialFloorFails(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	p := plan{Gate: "coverage", Module: &Module{
		Rel:        "apps/finance-domain/wallet",
		ImportPath: "github.com/EvenPlay/evenplay-mono/apps/finance-domain/wallet",
		Packages:   []string{"apps/finance-domain/wallet/service"},
	}}
	out := "ok  \tgithub.com/EvenPlay/evenplay-mono/apps/finance-domain/wallet/service\t1.2s\tcoverage: 88.0% of statements\n"

	g := evaluateCoverage(cfg, p, out)
	if g.Status != "fail" {
		t.Fatalf("status = %q, want fail (88%% < 95%% financial floor): %+v", g.Status, g.Metrics)
	}
	if v := toStrings(g.Metrics["violations"]); len(v) != 1 || !strings.Contains(v[0], "95%") {
		t.Errorf("violations = %v, want one naming the 95%% floor", v)
	}
}

func TestEvaluateCoverage_NoTestFilesFailsUnlessExempt(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)

	// A changed package with no tests fails.
	p := plan{Gate: "coverage", Module: &Module{Rel: "m", Packages: []string{"m/handler"}}}
	out := "?   	example.com/m/handler	[no test files]\n"
	if g := evaluateCoverage(cfg, p, out); g.Status != "fail" {
		t.Errorf("status = %q, want fail: %+v", g.Status, g.Metrics)
	}

	// Generated code is exempt.
	pExempt := plan{Gate: "coverage", Module: &Module{Rel: "m", Packages: []string{"m/pb"}}}
	outExempt := "?   	example.com/m/pb	[no test files]\n"
	if g := evaluateCoverage(cfg, pExempt, outExempt); g.Status != "pass" {
		t.Errorf("exempt package status = %q, want pass: %+v", g.Status, g.Metrics)
	}
}

func TestEvaluateCoverage_EmptyTestOutputIsFailure(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	p := plan{Gate: "coverage", Module: &Module{Rel: "m", Packages: []string{"m/service"}}}
	g := evaluateCoverage(cfg, p, "")
	if g.Status != "fail" {
		t.Errorf("status = %q, want fail — no output means nothing was verified", g.Status)
	}
}

func TestEvaluateCoverage_OnlyGatesChangedPackages(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	// handler is at 41.2% (floor 50) but was NOT changed, so it must not fail.
	p := plan{Gate: "coverage", Module: &Module{Rel: "m", Packages: []string{"m/service"}}}
	g := evaluateCoverage(cfg, p, goTestOutput)
	if g.Status != "pass" {
		t.Errorf("status = %q, want pass (only m/service changed, at 96.4%%): %+v", g.Status, g.Metrics)
	}
}

func TestFloorFor_FinancialTierIsStrictest(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	if got := floorFor(cfg, "apps/finance-domain/wallet/service/debit.go"); got != 95 {
		t.Errorf("wallet floor = %v, want 95", got)
	}
	if got := floorFor(cfg, "apps/platform-domain/bay-session/store/accept_bet.go"); got != 95 {
		t.Errorf("bay-session store floor = %v, want 95", got)
	}
	if got := floorFor(cfg, "apps/platform-domain/notifications/cmd/main.go"); got != 50 {
		t.Errorf("cmd floor = %v, want 50", got)
	}
}

// ─── no-op detection: the vacuous-pass class ─────────────────────────────────

func TestDetectZeroUnits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{"gremlins no mutants", "Mutation testing completed: 0 mutants generated", true},
		{"go no packages", "go: warning: \"./...\" matched no packages", true},
		{"no test files", "?   pkg   [no test files]", true},
		{"real run", "Mutation testing completed: 214 mutants generated\nTest efficacy: 84.10%", false},
		// Verbatim from `gremlins unleash ./...` on 2026-07-31 — it exits 0.
		{"gremlins matched no path", "Starting...\nGathering coverage... done in 120ms\n\nNo results to report.\n", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, why := detectZeroUnits(tt.out)
			if got != tt.want {
				t.Errorf("detectZeroUnits = %v (%s), want %v", got, why, tt.want)
			}
		})
	}
}

func TestParseMutationScore(t *testing.T) {
	t.Parallel()
	score, ok := parseMutationScore("Test efficacy: 84.10%\nMutant coverage: 91.00%")
	if !ok || score != 84.10 {
		t.Errorf("score = %v (ok=%v), want 84.10", score, ok)
	}
}

// ─── benchstat parsing ───────────────────────────────────────────────────────

func TestParseBenchstatRegressions(t *testing.T) {
	t.Parallel()
	out := `
goos: linux
                     │  base.txt   │            head.txt             │
                     │   sec/op    │   sec/op     vs base            │
BenchmarkDebit-8        1.204µ ± 2%   1.310µ ± 1%   +8.80% (p=0.000)
BenchmarkSettle-8       2.100µ ± 1%   2.900µ ± 2%  +38.10% (p=0.000)
BenchmarkRead-8         5.000µ ± 1%   4.500µ ± 1%  -10.00% (p=0.000)
`
	bad, worst := parseBenchstatRegressions(out, 10)
	if len(bad) != 1 || !strings.Contains(bad[0], "BenchmarkSettle-8") {
		t.Errorf("regressions = %v, want only BenchmarkSettle-8 over 10%%", bad)
	}
	if worst != 38.10 {
		t.Errorf("worst = %v, want 38.10", worst)
	}
}

// ─── waivers: no silent pass ─────────────────────────────────────────────────

func TestWaiveOrFail(t *testing.T) {
	t.Parallel()
	g := waiveOrFail(nil, "semgrep", "rules file missing")
	if g.Status != "fail" {
		t.Errorf("unwaived status = %q, want fail", g.Status)
	}
	if !strings.Contains(g.SkipReason, "-waive semgrep=") {
		t.Errorf("reason should name the escape hatch: %q", g.SkipReason)
	}

	g = waiveOrFail(map[string]string{"semgrep": "rules land in SMG-4001"}, "semgrep", "rules file missing")
	if g.Status != "skipped" {
		t.Errorf("waived status = %q, want skipped", g.Status)
	}
	if !strings.Contains(g.SkipReason, "SMG-4001") {
		t.Errorf("waiver reason not recorded: %q", g.SkipReason)
	}
}

func TestParseWaivers_RejectsReasonlessWaiver(t *testing.T) {
	t.Parallel()
	if _, err := parseWaivers([]string{"semgrep"}); err == nil {
		t.Fatal("expected error: a waiver without a reason is not a waiver")
	}
	if _, err := parseWaivers([]string{"semgrep="}); err == nil {
		t.Fatal("expected error for empty reason")
	}
}

// A reason is prose. Commas in it must survive, which is why -waive repeats
// rather than comma-splitting.
func TestParseWaivers_ReasonMayContainCommas(t *testing.T) {
	t.Parallel()
	w, err := parseWaivers([]string{
		"semgrep=rules live in evenplay-mono, not this repo",
		"mutation=gremlins install pending, tracked in SMG-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if w["semgrep"] != "rules live in evenplay-mono, not this repo" {
		t.Errorf("semgrep reason truncated: %q", w["semgrep"])
	}
	if len(w) != 2 {
		t.Errorf("got %d waivers, want 2", len(w))
	}
}

func TestStringList_Repeatable(t *testing.T) {
	t.Parallel()
	var l stringList
	if err := l.Set("a=1"); err != nil {
		t.Fatal(err)
	}
	if err := l.Set("b=2"); err != nil {
		t.Fatal(err)
	}
	if len(l) != 2 || l.String() != "a=1; b=2" {
		t.Errorf("stringList = %v (%q)", l, l.String())
	}
}

// ─── run state merge ─────────────────────────────────────────────────────────

func TestMergeGates_PreservesClassificationAndRounds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "run.json")
	seed := `{"schema_version":1,"task_key":"SMG-9","created_at":"2026-07-01T00:00:00Z",
	  "repo":{"worktree":"/wt","base_ref":"origin/main","base_sha":"abc1234"},
	  "classification":{"risk":"critical","components":["wallet"]},
	  "rounds":[{"round":1,"kind":"full","verdict":"iterate"}]}`
	if err := os.WriteFile(p, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	err := mergeGates(p, []result{
		{Key: "test:apps/finance-domain/wallet", Gate: "test", Outcome: Gate{Status: "pass", ExitCode: 0}},
		{Key: "semgrep", Gate: "semgrep", Outcome: Gate{Status: "fail", SkipReason: "rules missing"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(p)
	var state RunState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.TaskKey != "SMG-9" || state.CreatedAt != "2026-07-01T00:00:00Z" {
		t.Error("task metadata not preserved")
	}
	if state.Classification == nil || state.Classification.Risk != "critical" {
		t.Error("classification not preserved")
	}
	if len(state.Rounds) != 1 {
		t.Error("rounds not preserved")
	}
	if state.Gates["test:apps/finance-domain/wallet"].Status != "pass" {
		t.Error("test gate not recorded")
	}
	if state.Gates["semgrep"].Status != "fail" {
		t.Error("semgrep gate not recorded")
	}
}

func TestExpandTemplate(t *testing.T) {
	t.Parallel()
	got := expand("gremlins unleash --threshold-efficacy {{min_score}} -o {{output_json}} ./...",
		map[string]string{"min_score": "80", "output_json": "/tmp/x.json"})
	want := "gremlins unleash --threshold-efficacy 80 -o /tmp/x.json ./..."
	if got != want {
		t.Errorf("expand = %q, want %q", got, want)
	}
}

func TestMatchGlob(t *testing.T) {
	t.Parallel()
	if !matchGlob("**/pb/**", "apps/finance-domain/wallet/pb/x.pb.go") {
		t.Error("pb glob should match")
	}
	if matchGlob("**/pb/**", "apps/finance-domain/wallet/service/x.go") {
		t.Error("pb glob should not match service")
	}
}

// ─── execution paths ─────────────────────────────────────────────────────────

func TestRunOne_PassAndFail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	pass := plan{Gate: "build", Spec: GateSpec{Command: "echo built", TimeoutSeconds: 30}}
	g := runOne(pass, "echo built", dir, filepath.Join(dir, "pass.log"))
	if g.Status != "pass" || g.ExitCode != 0 {
		t.Errorf("pass gate = %+v", g)
	}
	if data, err := os.ReadFile(g.OutputPath); err != nil || !strings.Contains(string(data), "built") {
		t.Errorf("raw output not captured to disk: %v", err)
	}

	fail := plan{Gate: "lint", Spec: GateSpec{Command: "exit 3", TimeoutSeconds: 30}}
	g = runOne(fail, "exit 3", dir, filepath.Join(dir, "fail.log"))
	if g.Status != "fail" || g.ExitCode != 3 {
		t.Errorf("fail gate = %+v, want status fail exit 3", g)
	}
}

func TestRunOne_TimeoutIsFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := plan{Gate: "test", Spec: GateSpec{Command: "sleep 5", TimeoutSeconds: 1}}
	g := runOne(p, "sleep 5", dir, filepath.Join(dir, "t.log"))
	if g.Status != "fail" {
		t.Errorf("status = %q, want fail on timeout", g.Status)
	}
	if _, ok := g.Metrics["timed_out_after_seconds"]; !ok {
		t.Errorf("timeout not recorded: %+v", g.Metrics)
	}
}

// A command that exits 0 having done nothing must not pass.
func TestRunOne_EmptyOutputIsFailureWhenDeclared(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := plan{Gate: "test", Spec: GateSpec{Command: "true", TimeoutSeconds: 30, EmptyOutputIsFailure: true}}
	g := runOne(p, "true", dir, filepath.Join(dir, "e.log"))
	if g.Status != "fail" {
		t.Errorf("status = %q, want fail — no output means nothing was tested", g.Status)
	}
	if _, ok := g.Metrics["no_op"]; !ok {
		t.Error("no_op metric not set")
	}
}

func TestRunOne_ZeroUnitsIsFailureDespiteExitZero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := plan{Gate: "mutation", Spec: GateSpec{TimeoutSeconds: 30, ZeroUnitsIsFailure: true}}
	g := runOne(p, "echo '0 mutants generated'", dir, filepath.Join(dir, "m.log"))
	if g.Status != "fail" {
		t.Errorf("status = %q, want fail — a vacuous mutation run is not a pass", g.Status)
	}
}

func TestResolveCommand_MissingToolWaivesOrFails(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	state := &RunState{Repo: Repo{Worktree: t.TempDir()}}
	p := plan{Gate: "mutation", Spec: GateSpec{Command: "definitely-not-a-real-binary-xyz run"}}

	_, g, ok := resolveCommand(cfg, state, p, runOpts{}, "mutation")
	if ok || g.Status != "fail" {
		t.Errorf("missing tool: ok=%v status=%q, want false/fail", ok, g.Status)
	}

	ro := runOpts{Waivers: map[string]string{"mutation": "gremlins install pending SMG-1"}}
	_, g, ok = resolveCommand(cfg, state, p, ro, "mutation")
	if ok || g.Status != "skipped" || !strings.Contains(g.SkipReason, "SMG-1") {
		t.Errorf("waived: ok=%v %+v", ok, g)
	}
}

func TestResolveCommand_ExpandsPlaceholders(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	state := &RunState{Repo: Repo{Worktree: t.TempDir()}}
	p := plan{Gate: "test", Spec: GateSpec{Command: "sh -c 'echo {{coverprofile}}'"}}

	cmdStr, _, ok := resolveCommand(cfg, state, p, runOpts{OutDir: "/out"}, "test:m")
	if !ok {
		t.Fatal("expected ok")
	}
	if strings.Contains(cmdStr, "{{") {
		t.Errorf("unexpanded placeholder in %q", cmdStr)
	}
	if !strings.Contains(cmdStr, "/out/test-m.coverprofile") {
		t.Errorf("coverprofile path = %q", cmdStr)
	}
}

func TestResolveCommand_BenchAbsoluteNeedsCommand(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	state := &RunState{Repo: Repo{Worktree: t.TempDir()}}
	p := plan{Gate: "bench_absolute", Spec: GateSpec{}}

	_, g, ok := resolveCommand(cfg, state, p, runOpts{}, "bench_absolute")
	if ok || g.Status != "fail" {
		t.Errorf("ok=%v status=%q, want false/fail — SLO targets come from the Task Assignment", ok, g.Status)
	}
}

func TestExecuteOne_OnlyFilterIsNotAPass(t *testing.T) {
	t.Parallel()
	cfg := realConfig(t)
	state := &RunState{Repo: Repo{Worktree: t.TempDir()}}
	p := plan{Gate: "gosec", Spec: GateSpec{Command: "true"}}
	ro := runOpts{Only: map[string]bool{"build": true}, OutDir: t.TempDir()}

	g := executeOne(cfg, state, p, ro, gateID{Key: "gosec"}, map[string]string{})
	if g.Status != "skipped" {
		t.Errorf("status = %q, want skipped", g.Status)
	}
	if !strings.Contains(g.SkipReason, "NOT a pass") {
		t.Errorf("skip reason must say it is not a pass: %q", g.SkipReason)
	}
}

func TestGateKeyAndCwd(t *testing.T) {
	t.Parallel()
	m := &Module{Rel: "apps/w", Root: "/wt/apps/w"}
	if id := gateKey(plan{Gate: "test", Module: m}); id.Key != "test:apps/w" || id.ModRel != "apps/w" {
		t.Errorf("gateKey = %+v", id)
	}
	if id := gateKey(plan{Gate: "semgrep"}); id.Key != "semgrep" || id.ModRel != "" {
		t.Errorf("repo-scoped gateKey = %+v", id)
	}

	state := &RunState{Repo: Repo{Worktree: "/wt"}}
	if got := gateCwd(state, plan{Gate: "test", Module: m}); got != "/wt/apps/w" {
		t.Errorf("module cwd = %q", got)
	}
	if got := gateCwd(state, plan{Gate: "semgrep"}); got != "/wt" {
		t.Errorf("repo cwd = %q", got)
	}
}

// ─── prepare / finish ────────────────────────────────────────────────────────

func TestPrepare_InvalidInputs(t *testing.T) {
	t.Parallel()
	if _, _, _, problems := prepare(options{}); len(problems) == 0 {
		t.Error("missing -run-state should be a problem")
	}
	if _, _, _, problems := prepare(options{runState: "/nonexistent/run.json"}); len(problems) == 0 {
		t.Error("unreadable run state should be a problem")
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "run.json")
	if err := os.WriteFile(p, []byte(`{"schema_version":1,"repo":{"worktree":"/wt","base_ref":"origin/main","base_sha":"a"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	_, _, _, problems := prepare(options{runState: p, config: "testdata/example-gates.json"})
	if len(problems) == 0 || !strings.Contains(problems[0], "classification") {
		t.Errorf("problems = %v, want one naming the missing classification", problems)
	}
}

func TestPrepare_NoGoModulesIsInvalid(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	dir := t.TempDir()
	p := filepath.Join(dir, "run.json")
	state := `{"schema_version":1,"repo":{"worktree":"` + wt + `","base_ref":"origin/main","base_sha":"a"},
	  "classification":{"risk":"high","changed_files":[{"path":"README.md"}]}}`
	if err := os.WriteFile(p, []byte(state), 0644); err != nil {
		t.Fatal(err)
	}
	// An explicit -config is required here: config resolution now precedes
	// module discovery, since module_marker comes from the config.
	_, _, _, problems := prepare(options{runState: p, config: "testdata/example-gates.json"})
	if len(problems) == 0 || !strings.Contains(problems[0], "no Go modules") {
		t.Errorf("problems = %v, want the no-Go-modules refusal", problems)
	}
}

func TestOutDirFor(t *testing.T) {
	t.Parallel()
	if got := outDirFor(options{outDir: "/explicit"}); got != "/explicit" {
		t.Errorf("explicit out dir = %q", got)
	}
	if got := outDirFor(options{runState: "/tmp/x/run.json"}); got != "/tmp/x/gate-output" {
		t.Errorf("derived out dir = %q", got)
	}
}

func TestFinish_ExitCodeReflectsFailures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "run.json")
	seed := `{"schema_version":1,"repo":{"worktree":"/wt","base_ref":"origin/main","base_sha":"a"},
	  "classification":{"risk":"high"}}`
	if err := os.WriteFile(p, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	opts := options{runState: p}
	state := &RunState{Classification: &Classification{Risk: "high"}}
	_ = state

	if code := finish(opts, state, nil, []result{{Key: "build", Gate: "build", Outcome: Gate{Status: "pass"}}}); code != 0 {
		t.Errorf("all-pass exit = %d, want 0", code)
	}
	if code := finish(opts, state, nil, []result{{Key: "lint", Gate: "lint", Outcome: Gate{Status: "fail"}}}); code != exitFail {
		t.Errorf("failing exit = %d, want %d", code, exitFail)
	}
	// A skipped gate is not a failure, but it is also not silent — the reason is
	// recorded in the run state.
	if code := finish(opts, state, nil, []result{{Key: "semgrep", Gate: "semgrep", Outcome: Gate{Status: "skipped", SkipReason: "waived: x"}}}); code != 0 {
		t.Errorf("waived exit = %d, want 0", code)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "waived: x") {
		t.Error("waiver reason not persisted to the run state")
	}
}

func TestPrintersDoNotPanic(t *testing.T) {
	t.Parallel()
	state := &RunState{TaskKey: "SMG-1", Classification: &Classification{Risk: "critical", Components: []string{"wallet"}}}
	mods := []Module{{Rel: "apps/w", Packages: []string{"apps/w/service"}}}
	plans := []plan{{Gate: "build", Spec: GateSpec{Command: "go build ./..."}, Module: &mods[0]}}

	printPlan(state, mods, plans)
	printReport(state, mods, []result{
		{Key: "build:apps/w", Gate: "build", Module: "apps/w", Outcome: Gate{Status: "pass", DurationMS: 1200}},
		{Key: "coverage:apps/w", Gate: "coverage", Module: "apps/w", Outcome: Gate{
			Status: "fail", Metrics: map[string]any{"violations": []string{"x at 10% < floor 80%"}}}},
	}, 1)
	printInvalid([]string{"something is wrong"})
}

// ─── small helpers ───────────────────────────────────────────────────────────

func TestExitCodeOf(t *testing.T) {
	t.Parallel()
	if got := exitCodeOf(nil); got != 0 {
		t.Errorf("nil err = %d, want 0", got)
	}
	cmd := exec.Command("sh", "-c", "exit 7")
	if got := exitCodeOf(cmd.Run()); got != 7 {
		t.Errorf("exit 7 = %d", got)
	}
}

func TestSanitizeAndTail(t *testing.T) {
	t.Parallel()
	if got := sanitize("coverage:apps/finance-domain/wallet"); strings.ContainsAny(got, "/:") {
		t.Errorf("sanitize left path separators: %q", got)
	}
	if got := tail("a\nb\nc\nd", 2); got != "c\nd" {
		t.Errorf("tail = %q", got)
	}
	if got := tail("only", 5); got != "only" {
		t.Errorf("short tail = %q", got)
	}
}

func TestToStringsAndSplitSet(t *testing.T) {
	t.Parallel()
	if got := toStrings([]any{"a", 2}); len(got) != 2 || got[1] != "2" {
		t.Errorf("toStrings = %v", got)
	}
	if got := toStrings("solo"); len(got) != 1 {
		t.Errorf("toStrings scalar = %v", got)
	}
	s := splitSet("a, b ,,c")
	if len(s) != 3 || !s["b"] {
		t.Errorf("splitSet = %v", s)
	}
}

func TestModuleHasBenchmarks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	m := &Module{Root: dir, Rel: ".", Packages: []string{"pkg"}}
	if moduleHasBenchmarks(m) {
		t.Error("no _test.go yet, should be false")
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "x_test.go"),
		[]byte("package p\n\nfunc BenchmarkThing(b *testing.B) {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !moduleHasBenchmarks(m) {
		t.Error("BenchmarkThing not detected")
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

// An unprepared worktree and broken code look identical to a compiler's exit
// code and are opposite problems. Smoke-tested against evenplay-mono PR 1353,
// where a bare `git worktree add` failed build, complexity, staticcheck and
// gosec with one shared root cause.
func TestDetectEnvironmentFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{
			"missing generated protobuf",
			"common/transactions/t.go:3:8: no required module provides package " +
				"github.com/EvenPlay/evenplay-mono/apps/finance-domain/wallet/pb; to add it:",
			true,
		},
		{
			"missing sqlc output",
			"db/x.go:10:2: no required module provides package " +
				"github.com/EvenPlay/evenplay-mono/apps/finance-domain/wallet/db/sqlc; to add it:",
			true,
		},
		{"missing embed asset dir", "docs/embed.go:7:12: pattern swagger/*: no matching files found", true},
		{"empty package name", `could not import x/pb (invalid package name: "")`, true},
		{
			"a real compile error is NOT an environment failure",
			"store/wager.go:42:5: undefined: computeAdjustment",
			false,
		},
		{"a real test failure is not either", "--- FAIL: TestDebit (0.01s)\n    want 100, got 99", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := detectEnvironmentFailure(tt.out)
			if (got != "") != tt.want {
				t.Errorf("detectEnvironmentFailure = %q, want non-empty=%v", got, tt.want)
			}
			if tt.want && !strings.Contains(got, "worktree") {
				t.Errorf("reason should name the worktree so it is actionable: %q", got)
			}
		})
	}
}

func TestRunOne_RecordsEnvironmentCause(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := plan{Gate: "build", Spec: GateSpec{TimeoutSeconds: 30}}
	cmd := `echo "x.go:3:8: no required module provides package a/b/pb; to add it:" >&2; exit 1`

	g := runOne(p, cmd, dir, filepath.Join(dir, "b.log"))
	if g.Status != "fail" {
		t.Fatalf("status = %q, want fail", g.Status)
	}
	if _, ok := g.Metrics["environment"]; !ok {
		t.Errorf("environment cause not recorded: %+v", g.Metrics)
	}
}

// gremlins' --threshold-efficacy does not affect its exit code: 80.77% against
// a threshold of 95 still exits 0 (verified against the real tool). So the
// floor is ours to enforce, and an unreadable score fails closed.
func TestEnforceMutationScore(t *testing.T) {
	t.Parallel()
	cfg := &Config{MutationMinScore: 80}

	pass := enforceMutationScore(cfg, Gate{Status: "pass", Metrics: map[string]any{"mutation_score": 83.33}})
	if pass.Status != "pass" {
		t.Errorf("83.33%% >= 80%% should pass: %+v", pass)
	}

	// The exact real-world case: gremlins scored 80.77% against
	// --threshold-efficacy 95 and still exited 0. Gates must fail it.
	strict := enforceMutationScore(&Config{MutationMinScore: 95},
		Gate{Status: "pass", Metrics: map[string]any{"mutation_score": 80.77}})
	if strict.Status != "fail" {
		t.Errorf("80.77%% < 95%% floor must fail even though gremlins exits 0: %+v", strict)
	}

	// Boundary: exactly at the floor passes.
	at := enforceMutationScore(cfg, Gate{Status: "pass", Metrics: map[string]any{"mutation_score": 80.0}})
	if at.Status != "pass" {
		t.Errorf("exactly at the floor should pass: %+v", at)
	}

	below := enforceMutationScore(cfg, Gate{Status: "pass", Metrics: map[string]any{"mutation_score": 62.5}})
	if below.Status != "fail" {
		t.Fatalf("62.5%% < 80%% must fail: %+v", below)
	}
	if v := toStrings(below.Metrics["violations"]); len(v) != 1 || !strings.Contains(v[0], "62.50%") {
		t.Errorf("violation should name the score: %v", v)
	}

	unparsed := enforceMutationScore(cfg, Gate{Status: "pass", Metrics: map[string]any{}})
	if unparsed.Status != "fail" {
		t.Errorf("an unparsable score must fail closed: %+v", unparsed)
	}
}

// A gate table from another project would run the wrong commands and grade
// coverage against directories that do not exist here, so a missing config is
// an error rather than a fallback.
func TestPrepare_MissingGatesConfigIsInvalid(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "go.mod"), []byte("module x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "run.json")
	state := `{"schema_version":1,"repo":{"worktree":"` + wt + `","base_ref":"origin/main","base_sha":"a"},
	  "classification":{"risk":"high","changed_files":[{"path":"main.go"}]}}`
	if err := os.WriteFile(p, []byte(state), 0644); err != nil {
		t.Fatal(err)
	}

	_, _, _, problems := prepare(options{runState: p})
	if len(problems) == 0 || !strings.Contains(problems[0], "project-specific") {
		t.Fatalf("problems = %v, want the missing-config refusal", problems)
	}
	joined := strings.Join(problems, " ")
	if !strings.Contains(joined, "example-gates.json") {
		t.Error("the message should say how to create one")
	}
}

func TestGatesConfigCandidates_NoCrossProjectFallback(t *testing.T) {
	t.Parallel()
	got := gatesConfigCandidates("/some/project")
	for _, c := range got {
		if strings.Contains(c, "claude-workflow") {
			t.Errorf("candidate %q reaches into the tooling repo — that is the cross-project bug", c)
		}
	}
	if len(got) == 0 || !strings.HasPrefix(got[0], "/some/project") {
		t.Errorf("candidates = %v, want the project first", got)
	}
}

// The schema was documentation only, so a node writing a malformed state
// produced a confusing failure three steps later instead of a clear one here.
func TestValidateRunState(t *testing.T) {
	t.Parallel()
	ok := &RunState{SchemaVersion: 1, Repo: Repo{Worktree: "/wt"},
		Classification: &Classification{Risk: "critical", Components: []string{"wallet"}},
		Gates:          map[string]Gate{"test": {Status: "pass"}}}
	if p := validateRunState(ok); len(p) != 0 {
		t.Errorf("valid state rejected: %v", p)
	}

	tests := []struct {
		name  string
		state *RunState
		want  string
	}{
		{"wrong schema version", &RunState{SchemaVersion: 99, Repo: Repo{Worktree: "/wt"}}, "schema_version"},
		{"no worktree", &RunState{SchemaVersion: 1}, "repo.worktree"},
		{
			"bad risk",
			&RunState{SchemaVersion: 1, Repo: Repo{Worktree: "/wt"},
				Classification: &Classification{Risk: "extreme"}},
			"is not low|medium|high|critical",
		},
		{
			"unknown component would be rejected downstream",
			&RunState{SchemaVersion: 1, Repo: Repo{Worktree: "/wt"},
				Classification: &Classification{Risk: "high", Components: []string{"wallett"}}},
			"cmd/reviewer would reject",
		},
		{
			"bad gate status",
			&RunState{SchemaVersion: 1, Repo: Repo{Worktree: "/wt"},
				Gates: map[string]Gate{"test": {Status: "probably-fine"}}},
			"is not pass|fail|skipped|unavailable",
		},
		{
			"skip with no reason is the silent-pass shape",
			&RunState{SchemaVersion: 1, Repo: Repo{Worktree: "/wt"},
				Gates: map[string]Gate{"semgrep": {Status: "skipped"}}},
			"indistinguishable from a pass",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			problems := validateRunState(tt.state)
			if len(problems) == 0 {
				t.Fatalf("expected a problem containing %q", tt.want)
			}
			if !strings.Contains(strings.Join(problems, " "), tt.want) {
				t.Errorf("problems = %v, want one containing %q", problems, tt.want)
			}
		})
	}
}

func TestReadRunState_RejectsMalformed(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "run.json")
	bad := `{"schema_version":1,"repo":{"worktree":"/wt"},
	  "gates":{"semgrep":{"status":"skipped"}}}`
	if err := os.WriteFile(p, []byte(bad), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunState(p); err == nil {
		t.Fatal("expected a reasonless skip to be rejected at load")
	}
}
