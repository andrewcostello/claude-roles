// Command gates runs the deterministic verification gates and records the
// result — with raw output on disk — into the run state.
//
// It replaces the parts of coder.md Phase 4/4.5 and critical-review-dispatch.md
// Phase 3.0.1-3.0.3 that instructed a model to run commands and paste the
// output. Those are not judgment calls; they are exit codes. Asking an agent to
// self-report them has failed in three distinct ways:
//
//	PR 1294        shipped a gofmt failure behind a self-reported "lint: PASS".
//	mutation gate  pointed at apps/finance-domain/wallet/payout/ — a directory
//	               that does not exist — so it ran against nothing and reported
//	               success. It also invoked `gremlins-go`, which is not the
//	               binary name (`gremlins`), so it never launched at all.
//	`go build ./...` at the repo root cannot work in evenplay-mono: there is no
//	               root go.mod. Every app is its own module, so gates must be
//	               module-scoped or they are testing nothing.
//
// The design rule throughout: there is no status meaning "we did not check but
// it is fine". A gate that cannot run FAILS unless a human waives it by name
// with a reason, and the waiver is recorded.
//
// Which gates run is derived from the run state's classification, so it cannot
// be forgotten by a caller.
//
// Exit codes: 0 all required gates pass, 1 a required gate failed or could not
// run, 3 INVALID_INPUT.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	schemaVersion = 1
	exitFail      = 1
	exitInvalid   = 3
	maxTailLines  = 40
)

// moneyComponents trigger the mutation gate. Mirrors the money subset of
// cmd/reviewer's componentFloorPresets; responsible-gambling is enforcement
// logic rather than arithmetic, so it does not pull in mutation testing.
var moneyComponents = map[string]bool{
	"wallet":         true,
	"bet-settlement": true,
	"bet-placement":  true,
	"jackpot":        true,
}

// ─── config ──────────────────────────────────────────────────────────────────

type Config struct {
	SchemaVersion         int                 `json:"schema_version"`
	ModuleMarker          string              `json:"module_marker"`
	Gates                 map[string]GateSpec `json:"gates"`
	CoverageFloors        []CoverageFloor     `json:"coverage_floors"`
	CoverageExempt        []string            `json:"coverage_exempt"`
	MutationMinScore      float64             `json:"mutation_min_score"`
	BenchMaxRegressionPct float64             `json:"bench_max_regression_pct"`
}

type GateSpec struct {
	Command              string `json:"command,omitempty"`
	DerivedFrom          string `json:"derived_from,omitempty"`
	Trigger              string `json:"trigger"`
	Scope                string `json:"scope"`
	TimeoutSeconds       int    `json:"timeout_seconds,omitempty"`
	RequiresFile         string `json:"requires_file,omitempty"`
	EmptyOutputIsFailure bool   `json:"empty_output_is_failure,omitempty"`
	ZeroUnitsIsFailure   bool   `json:"zero_units_is_failure,omitempty"`
	Note                 string `json:"note,omitempty"`
}

type CoverageFloor struct {
	Paths []string `json:"paths"`
	Min   float64  `json:"min"`
	Note  string   `json:"note,omitempty"`
}

// ─── run state (see config/run-state.schema.json) ────────────────────────────

type RunState struct {
	SchemaVersion    int             `json:"schema_version"`
	TaskKey          string          `json:"task_key,omitempty"`
	CreatedAt        string          `json:"created_at,omitempty"`
	UpdatedAt        string          `json:"updated_at,omitempty"`
	Repo             Repo            `json:"repo"`
	Classification   *Classification `json:"classification,omitempty"`
	Gates            map[string]Gate `json:"gates,omitempty"`
	Rounds           []any           `json:"rounds,omitempty"`
	Round            int             `json:"round,omitempty"`
	Verdict          string          `json:"verdict,omitempty"`
	Status           string          `json:"status,omitempty"`
	EscalationReason string          `json:"escalation_reason,omitempty"`
	PR               map[string]any  `json:"pr,omitempty"`
	DeferredFindings []any           `json:"deferred_findings,omitempty"`
}

type Repo struct {
	Worktree string `json:"worktree"`
	BaseRef  string `json:"base_ref"`
	BaseSHA  string `json:"base_sha"`
	HeadSHA  string `json:"head_sha,omitempty"`
	Dirty    bool   `json:"dirty,omitempty"`
	Branch   string `json:"branch,omitempty"`
}

type Classification struct {
	Risk         string      `json:"risk"`
	Components   []string    `json:"components,omitempty"`
	ChangedFiles []FileClass `json:"changed_files,omitempty"`
}

type FileClass struct {
	Path string `json:"path"`
}

type Gate struct {
	Status     string         `json:"status"`
	ExitCode   int            `json:"exit_code,omitempty"`
	Command    string         `json:"command,omitempty"`
	RanAt      string         `json:"ran_at,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	OutputPath string         `json:"output_path,omitempty"`
	SkipReason string         `json:"skip_reason,omitempty"`
	Metrics    map[string]any `json:"metrics,omitempty"`
}

// ─── planning ────────────────────────────────────────────────────────────────

type Module struct {
	Root       string   // absolute
	Rel        string   // worktree-relative, "." for the worktree root itself
	ImportPath string   // the module path from go.mod, used to map test output back to dirs
	Packages   []string // worktree-relative dirs of changed files in this module
}

type plan struct {
	Gate   string
	Spec   GateSpec
	Module *Module // nil for scope=repo
}

type result struct {
	Key     string // gate name, or gate:module for module-scoped
	Gate    string
	Module  string
	Outcome Gate
}

type options struct {
	runState    string
	config      string
	outDir      string
	phase       string
	declare     string
	waivers     stringList
	only        string
	benchAbsCmd string
	dryRun      bool
}

// stringList collects a repeatable flag. -waive must be repeatable rather than
// comma-separated: a waiver reason is prose and routinely contains commas.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, "; ") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// runOpts groups the per-execution knobs that travel together, so execute()
// takes a small number of coupled arguments rather than a positional list.
type runOpts struct {
	OutDir      string
	Only        map[string]bool
	Waivers     map[string]string
	BenchAbsCmd string
}

func main() {
	os.Exit(run(parseFlags()))
}

func parseFlags() options {
	var o options
	flag.StringVar(&o.runState, "run-state", "", "Run-state JSON from cmd/classify (required)")
	flag.StringVar(&o.config, "config", "", "Path to gates.json (default: alongside this binary's repo config/)")
	flag.StringVar(&o.outDir, "out-dir", "", "Directory for raw gate output (default: <run-state dir>/gate-output)")
	flag.StringVar(&o.phase, "phase", "implementation", "implementation | iteration. iteration adds the domain_suite regression gate.")
	flag.StringVar(&o.declare, "declare", "", "Comma-separated gates the Task Assignment declares applicable (bench_absolute, differential)")
	flag.Var(&o.waivers, "waive", "<gate>=<reason> waiver for a gate that cannot run. Repeat the flag for several gates; the reason may contain commas. Recorded in the run state.")
	flag.StringVar(&o.only, "only", "", "Comma-separated gates to run; others are recorded as skipped with a reason")
	flag.StringVar(&o.benchAbsCmd, "bench-absolute-command", "", "Command for the bench_absolute gate when declared")
	flag.BoolVar(&o.dryRun, "dry-run", false, "Print the derived plan and exit without running anything")
	flag.Parse()

	log.SetFlags(0)
	log.SetPrefix("gates: ")
	return o
}

func run(opts options) int {
	cfg, state, modules, problems := prepare(opts)
	if len(problems) > 0 {
		printInvalid(problems)
		return exitInvalid
	}

	declared := splitSet(opts.declare)
	waivers, err := parseWaivers(opts.waivers)
	if err != nil {
		printInvalid([]string{fmt.Sprintf("-waive: %v", err)})
		return exitInvalid
	}

	plans := derivePlan(cfg, state.Classification, modules, opts.phase, declared)
	if opts.dryRun {
		printPlan(state, modules, plans)
		return 0
	}

	ro := runOpts{
		OutDir:      outDirFor(opts),
		Only:        splitSet(opts.only),
		Waivers:     waivers,
		BenchAbsCmd: opts.benchAbsCmd,
	}
	return finish(opts, state, modules, execute(cfg, state, plans, ro))
}

// prepare loads and validates everything the plan depends on. It returns the
// full problem list rather than exiting, so a caller reports all of them at once.
func prepare(opts options) (*Config, *RunState, []Module, []string) {
	if opts.runState == "" {
		return nil, nil, nil, []string{"-run-state is required — run cmd/classify first; the gate set is derived from its classification"}
	}
	state, err := readRunState(opts.runState)
	if err != nil {
		return nil, nil, nil, []string{fmt.Sprintf("read run state: %v", err)}
	}
	if state.Classification == nil {
		return nil, nil, nil, []string{"run state has no classification — run cmd/classify first"}
	}
	if state.Repo.Worktree == "" {
		return nil, nil, nil, []string{"run state has no repo.worktree"}
	}

	cfg, err := loadConfig(configPathOrDefault(opts.config))
	if err != nil {
		return nil, nil, nil, []string{fmt.Sprintf("config: %v", err)}
	}

	modules, err := discoverModules(state.Repo.Worktree, cfg.ModuleMarker, changedPaths(state.Classification))
	if err != nil {
		return nil, nil, nil, []string{fmt.Sprintf("discover modules: %v", err)}
	}
	if len(modules) == 0 {
		return nil, nil, nil, []string{"no Go modules own any changed file — nothing this tool can gate. If the change is non-Go, record that explicitly rather than running gates."}
	}
	return cfg, state, modules, nil
}

func outDirFor(opts options) string {
	if opts.outDir != "" {
		return opts.outDir
	}
	return filepath.Join(filepath.Dir(opts.runState), "gate-output")
}

func finish(opts options, state *RunState, modules []Module, results []result) int {
	failed := 0
	for _, r := range results {
		if r.Outcome.Status == "fail" {
			failed++
		}
	}
	printReport(state, modules, results, failed)

	if err := mergeGates(opts.runState, results); err != nil {
		log.Printf("WARNING: failed to write gates into run state: %v", err)
	} else {
		log.Printf("gate results written to %s", opts.runState)
	}
	if failed > 0 {
		return exitFail
	}
	return 0
}

// ─── config loading ──────────────────────────────────────────────────────────

func configPathOrDefault(p string) string {
	if p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		if c := filepath.Join(filepath.Dir(exe), "..", "..", "config", "gates.json"); fileExists(c) {
			return c
		}
	}
	if home := os.Getenv("HOME"); home != "" {
		if c := filepath.Join(home, "Project/claude-workflow/config/gates.json"); fileExists(c) {
			return c
		}
	}
	return "config/gates.json"
}

func fileExists(p string) bool {
	// #nosec G703 -- p is a config path, module root, or gate-output path derived
	// from the run state; naming files to inspect is this tool's contract.
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func loadConfig(p string) (*Config, error) {
	// #nosec G304 -- p is the -config flag: naming the gate table is the point.
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return parseConfig(data)
}

func parseConfig(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if cfg.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf("schema_version %d unsupported (want %d)", cfg.SchemaVersion, schemaVersion)
	}
	if cfg.ModuleMarker == "" {
		return nil, fmt.Errorf("module_marker is required")
	}
	if len(cfg.Gates) == 0 {
		return nil, fmt.Errorf("no gates defined")
	}
	validTriggers := map[string]bool{
		"always": true, "high_or_critical": true, "financial": true,
		"has_benchmarks": true, "declared": true, "iteration": true,
	}
	for name, g := range cfg.Gates {
		if !validTriggers[g.Trigger] {
			return nil, fmt.Errorf("gate %q: unknown trigger %q", name, g.Trigger)
		}
		if g.Scope != "module" && g.Scope != "repo" {
			return nil, fmt.Errorf("gate %q: scope must be module or repo, got %q", name, g.Scope)
		}
		if g.Command == "" && g.DerivedFrom == "" && g.Trigger != "declared" {
			return nil, fmt.Errorf("gate %q: needs command or derived_from", name)
		}
		if g.DerivedFrom != "" {
			if _, ok := cfg.Gates[g.DerivedFrom]; !ok {
				return nil, fmt.Errorf("gate %q: derived_from %q is not a gate", name, g.DerivedFrom)
			}
		}
	}
	return &cfg, nil
}

// ─── module discovery ────────────────────────────────────────────────────────

func changedPaths(cls *Classification) []string {
	out := make([]string, 0, len(cls.ChangedFiles))
	for _, f := range cls.ChangedFiles {
		out = append(out, f.Path)
	}
	return out
}

// discoverModules groups changed files by the nearest ancestor directory holding
// the module marker. A repo with no root module (evenplay-mono) yields one entry
// per touched app; a single-module repo yields one entry.
func discoverModules(worktree, marker string, files []string) ([]Module, error) {
	abs, err := filepath.Abs(worktree)
	if err != nil {
		return nil, err
	}
	byRel := map[string]*Module{}

	for _, f := range files {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		rel, ok := findModuleRoot(abs, marker, f)
		if !ok {
			continue
		}
		m := byRel[rel]
		if m == nil {
			root := abs
			if rel != "." {
				root = filepath.Join(abs, rel)
			}
			m = &Module{Root: root, Rel: rel, ImportPath: readModulePath(filepath.Join(root, marker))}
			byRel[rel] = m
		}
		if d := path.Dir(f); !containsStr(m.Packages, d) {
			m.Packages = append(m.Packages, d)
		}
	}

	out := make([]Module, 0, len(byRel))
	for _, m := range byRel {
		sort.Strings(m.Packages)
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

// findModuleRoot walks up from the file's directory looking for the marker,
// returning the worktree-relative module root.
func findModuleRoot(worktreeAbs, marker, file string) (string, bool) {
	dir := path.Dir(file)
	for {
		candidate := filepath.Join(worktreeAbs, filepath.FromSlash(dir), marker)
		if dir == "." {
			candidate = filepath.Join(worktreeAbs, marker)
		}
		if fileExists(candidate) {
			return dir, true
		}
		if dir == "." || dir == "/" || dir == "" {
			return "", false
		}
		dir = path.Dir(dir)
	}
}

// ─── plan derivation ─────────────────────────────────────────────────────────

func derivePlan(cfg *Config, cls *Classification, modules []Module, phase string, declared map[string]bool) []plan {
	names := make([]string, 0, len(cfg.Gates))
	for n := range cfg.Gates {
		names = append(names, n)
	}
	sort.Strings(names)
	// Ordered so a build failure surfaces before slower gates.
	order := map[string]int{"build": 0, "test": 1, "coverage": 2, "lint": 3, "complexity": 4}
	sort.SliceStable(names, func(i, j int) bool {
		oi, iok := order[names[i]]
		oj, jok := order[names[j]]
		if iok && jok {
			return oi < oj
		}
		if iok != jok {
			return iok
		}
		return names[i] < names[j]
	})

	var plans []plan
	for _, name := range names {
		spec := cfg.Gates[name]
		if !triggerFires(spec.Trigger, cls, phase, declared, name) {
			continue
		}
		if spec.Scope == "repo" {
			plans = append(plans, plan{Gate: name, Spec: spec})
			continue
		}
		for i := range modules {
			m := &modules[i]
			if spec.Trigger == "has_benchmarks" && !moduleHasBenchmarks(m) {
				continue
			}
			if spec.Trigger == "financial" && !moduleTouchesMoney(cfg, m) {
				continue
			}
			plans = append(plans, plan{Gate: name, Spec: spec, Module: m})
		}
	}
	return plans
}

func triggerFires(trigger string, cls *Classification, phase string, declared map[string]bool, name string) bool {
	switch trigger {
	case "always":
		return true
	case "high_or_critical":
		return cls.Risk == "high" || cls.Risk == "critical"
	case "financial":
		for _, c := range cls.Components {
			if moneyComponents[c] {
				return true
			}
		}
		return false
	case "has_benchmarks":
		return true // narrowed per module in derivePlan
	case "declared":
		return declared[name]
	case "iteration":
		return phase == "iteration"
	}
	return false
}

func moduleHasBenchmarks(m *Module) bool {
	found := false
	for _, pkg := range m.Packages {
		dir := pkgAbs(m, pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			// #nosec G304 -- dir is a discovered package dir; name is a _test.go entry.
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			if benchFuncRE.Match(data) {
				found = true
				return found
			}
		}
	}
	return found
}

var benchFuncRE = regexp.MustCompile(`(?m)^func Benchmark[A-Z_]`)

// moduleTouchesMoney reports whether any changed package in this module sits
// under a coverage floor marked as financial-grade (the 95% tier). That tier is
// exactly the money/state-machine path set, so it doubles as the mutation-gate
// scope without a second list to drift.
func moduleTouchesMoney(cfg *Config, m *Module) bool {
	for _, pkg := range m.Packages {
		if floorFor(cfg, pkg+"/x.go") >= 95 {
			return true
		}
	}
	return false
}

func pkgAbs(m *Module, pkg string) string {
	if m.Rel == "." {
		return filepath.Join(m.Root, filepath.FromSlash(pkg))
	}
	trimmed := strings.TrimPrefix(pkg, m.Rel)
	trimmed = strings.TrimPrefix(trimmed, "/")
	return filepath.Join(m.Root, filepath.FromSlash(trimmed))
}

// ─── execution ───────────────────────────────────────────────────────────────

func execute(cfg *Config, state *RunState, plans []plan, ro runOpts) []result {
	var results []result
	testOutputs := map[string]string{} // module rel -> test gate raw output

	// #nosec G301 -- gate logs are build artifacts a human and other nodes read.
	if err := os.MkdirAll(ro.OutDir, 0755); err != nil {
		log.Fatalf("create out dir: %v", err)
	}

	for _, p := range plans {
		id := gateKey(p)
		results = append(results, result{id.Key, p.Gate, id.ModRel, executeOne(cfg, state, p, ro, id, testOutputs)})
	}
	return results
}

// gateID is the identity of one planned gate: its run-state key and the module
// it belongs to. The two are always derived together and always travel together.
type gateID struct {
	Key    string
	ModRel string
}

func gateKey(p plan) gateID {
	if p.Module == nil {
		return gateID{Key: p.Gate}
	}
	return gateID{Key: p.Gate + ":" + p.Module.Rel, ModRel: p.Module.Rel}
}

// executeOne resolves one planned gate to an outcome. Every early return is a
// status a human can act on; none of them is a silent pass.
func executeOne(cfg *Config, state *RunState, p plan, ro runOpts, id gateID, testOutputs map[string]string) Gate {
	if len(ro.Only) > 0 && !ro.Only[p.Gate] {
		return Gate{Status: "skipped", SkipReason: "not selected by -only — this is NOT a pass"}
	}
	// Derived gates evaluate another gate's captured output.
	if p.Spec.DerivedFrom != "" {
		return evaluateCoverage(cfg, p, testOutputs[id.ModRel])
	}

	cmdStr, gate, ok := resolveCommand(cfg, state, p, ro, id.Key)
	if !ok {
		return gate
	}

	logPath := filepath.Join(ro.OutDir, sanitize(id.Key)+".log")
	g := runOne(p, cmdStr, gateCwd(state, p), logPath)

	if p.Gate == "test" {
		// #nosec G304 -- logPath was written by runOne moments ago.
		if data, err := os.ReadFile(logPath); err == nil {
			testOutputs[id.ModRel] = string(data)
		}
	}
	if p.Gate == "bench_relative" && g.Status == "pass" {
		return evaluateBenchRelative(cfg, state, p, ro.OutDir, logPath)
	}
	return g
}

// resolveCommand expands the gate's command and checks its preconditions. ok is
// false when the gate cannot run, in which case the returned Gate is the
// waived-or-failed outcome.
func resolveCommand(cfg *Config, state *RunState, p plan, ro runOpts, key string) (string, Gate, bool) {
	cmdStr := p.Spec.Command

	if p.Gate == "bench_absolute" {
		if ro.BenchAbsCmd == "" {
			return "", waiveOrFail(ro.Waivers, p.Gate,
				"declared applicable but no -bench-absolute-command given; p99/throughput targets come from the Task Assignment"), false
		}
		cmdStr = ro.BenchAbsCmd
	}

	if p.Spec.RequiresFile != "" && !fileExists(filepath.Join(state.Repo.Worktree, p.Spec.RequiresFile)) {
		return "", waiveOrFail(ro.Waivers, p.Gate,
			fmt.Sprintf("required file %s not found in the worktree — that file IS the gate", p.Spec.RequiresFile)), false
	}

	cmdStr = expand(cmdStr, map[string]string{
		"coverprofile": filepath.Join(ro.OutDir, sanitize(key)+".coverprofile"),
		"output_json":  filepath.Join(ro.OutDir, sanitize(key)+".results.json"),
		"rules":        filepath.Join(state.Repo.Worktree, p.Spec.RequiresFile),
		"min_score":    trimFloat(cfg.MutationMinScore),
	})

	if _, err := exec.LookPath(firstWord(cmdStr)); err != nil {
		return "", waiveOrFail(ro.Waivers, p.Gate, fmt.Sprintf("%s not on PATH", firstWord(cmdStr))), false
	}
	return cmdStr, Gate{}, true
}

func gateCwd(state *RunState, p plan) string {
	if p.Module != nil {
		return p.Module.Root
	}
	return state.Repo.Worktree
}

func runOne(p plan, cmdStr, cwd, logPath string) Gate {
	timeout := time.Duration(p.Spec.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	// #nosec G204 -- cmdStr comes from gates.json, a repo-controlled config, with
	// only {{placeholder}} paths interpolated. Running configured build commands
	// IS this tool's function; there is no untrusted input on this path.
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	cmd.Dir = cwd
	raw, runErr := cmd.CombinedOutput()
	dur := time.Since(start)

	// #nosec G306 -- raw gate output is evidence a human reads and the summary
	// quotes; it holds compiler and linter text, never credentials.
	_ = os.WriteFile(logPath, raw, 0644)

	g := Gate{
		Command:    cmdStr,
		RanAt:      start.UTC().Format(time.RFC3339),
		DurationMS: dur.Milliseconds(),
		OutputPath: logPath,
		Metrics:    map[string]any{},
	}
	g.ExitCode = exitCodeOf(runErr)

	switch {
	case ctx.Err() != nil:
		g.Status = "fail"
		g.Metrics["timed_out_after_seconds"] = int(timeout.Seconds())
		return g
	case runErr != nil:
		g.Status = "fail"
	default:
		g.Status = "pass"
	}

	out := string(raw)
	if p.Spec.EmptyOutputIsFailure && strings.TrimSpace(out) == "" {
		g.Status = "fail"
		g.Metrics["no_op"] = "command produced no output — it tested nothing"
		return g
	}
	if p.Spec.ZeroUnitsIsFailure {
		if zero, why := detectZeroUnits(out); zero {
			g.Status = "fail"
			g.Metrics["no_op"] = why
			return g
		}
	}
	if p.Gate == "mutation" {
		if score, ok := parseMutationScore(out); ok {
			g.Metrics["mutation_score"] = score
		}
	}
	return g
}

// detectZeroUnits catches the vacuous run: the tool exited 0 having done no
// work. That is the exact shape of the mutation gate that "passed" for months
// while pointed at a directory that did not exist.
func detectZeroUnits(out string) (bool, string) {
	lower := strings.ToLower(out)
	for _, marker := range []string{
		"no packages to test",
		"no go files",
		"matched no packages",
		"no test files",
		"0 mutants",
		"no mutants",
		"nothing to do",
	} {
		if strings.Contains(lower, marker) {
			return true, "output reports no work performed: " + marker
		}
	}
	if m := mutantTotalRE.FindStringSubmatch(out); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n == 0 {
			return true, "zero mutants generated — the gate ran against nothing"
		}
	}
	return false, ""
}

var (
	mutantTotalRE   = regexp.MustCompile(`(?i)(\d+)\s+mutants?\s+(?:generated|found|total)`)
	mutationScoreRE = regexp.MustCompile(`(?i)(?:test\s+)?efficacy[^0-9]*([0-9]+(?:\.[0-9]+)?)\s*%`)
	// The package is the token immediately after the status word. An earlier
	// version used a loose `\S*\s*(\S+)` prefix, which captured the DURATION
	// field ("0.011s") as the package name — so the changed-package filter below
	// matched nothing and the gate passed without evaluating anything. Keep this
	// anchored.
	coverageLineRE = regexp.MustCompile(`(?m)^(?:ok|FAIL)\s+(\S+)\s+.*?coverage:\s+([0-9]+(?:\.[0-9]+)?)%`)
	noStatementsRE = regexp.MustCompile(`(?m)^(?:ok|FAIL)\s+(\S+)\s+.*?coverage:\s+\[no statements\]`)
	noTestFilesRE  = regexp.MustCompile(`(?m)^\s*\?\s+(\S+)\s+\[no test files\]`)
	benchstatRowRE = regexp.MustCompile(`(?m)^(\S+)\s+.*?([+-][0-9]+\.[0-9]+)%`)
)

func parseMutationScore(out string) (float64, bool) {
	if m := mutationScoreRE.FindStringSubmatch(out); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			return v, true
		}
	}
	return 0, false
}

// ─── coverage (derived from the test gate) ───────────────────────────────────

type pkgCoverage struct {
	Pkg          string
	Pct          float64
	NoTests      bool
	NoStatements bool
}

func parseCoverage(out string) []pkgCoverage {
	var cov []pkgCoverage
	seen := map[string]bool{}

	for _, m := range coverageLineRE.FindAllStringSubmatch(out, -1) {
		pkg := m[1]
		pct, err := strconv.ParseFloat(m[2], 64)
		if err != nil || seen[pkg] {
			continue
		}
		seen[pkg] = true
		cov = append(cov, pkgCoverage{Pkg: pkg, Pct: pct})
	}
	for _, m := range noStatementsRE.FindAllStringSubmatch(out, -1) {
		if pkg := m[1]; !seen[pkg] {
			seen[pkg] = true
			cov = append(cov, pkgCoverage{Pkg: pkg, NoStatements: true})
		}
	}
	for _, m := range noTestFilesRE.FindAllStringSubmatch(out, -1) {
		pkg := m[1]
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		cov = append(cov, pkgCoverage{Pkg: pkg, NoTests: true})
	}
	sort.Slice(cov, func(i, j int) bool { return cov[i].Pkg < cov[j].Pkg })
	return cov
}

func evaluateCoverage(cfg *Config, p plan, testOutput string) Gate {
	g := Gate{
		Status:  "pass",
		RanAt:   time.Now().UTC().Format(time.RFC3339),
		Metrics: map[string]any{},
	}
	if strings.TrimSpace(testOutput) == "" {
		g.Status = "fail"
		g.SkipReason = "no test-gate output to evaluate — the test gate did not run or produced nothing"
		return g
	}

	cov := parseCoverage(testOutput)
	if len(cov) == 0 {
		g.Status = "fail"
		g.SkipReason = "test output contained no coverage lines — was -cover passed?"
		return g
	}

	var violations []string
	var exempt []string
	worst := 100.0
	matched, evaluated := 0, 0

	for _, c := range cov {
		// Only gate packages this change actually touched; the module may hold
		// many others whose coverage is not this task's business. The
		// worktree-relative path is what floors and exemptions are written against.
		wtPath, touched := changedPathFor(p.Module, c.Pkg)
		if !touched {
			continue
		}
		matched++
		if matchAny(cfg.CoverageExempt, wtPath+"/x.go") {
			exempt = append(exempt, wtPath)
			continue
		}
		evaluated++
		floor := floorFor(cfg, wtPath+"/x.go")

		switch {
		case c.NoStatements:
			// Nothing executable to cover (consts and types only).
		case c.NoTests:
			violations = append(violations, fmt.Sprintf("%s has NO TEST FILES (floor %.0f%%)", wtPath, floor))
			worst = 0
		default:
			if c.Pct < worst {
				worst = c.Pct
			}
			if c.Pct < floor {
				violations = append(violations, fmt.Sprintf("%s at %.1f%% < floor %.0f%%", wtPath, c.Pct, floor))
			}
		}
	}

	g.Metrics["worst_coverage_pct"] = worst
	g.Metrics["packages_evaluated"] = evaluated
	if len(exempt) > 0 {
		g.Metrics["exempt_packages"] = exempt
	}

	// Matching nothing means the gate did no work — that must never read as a
	// pass. It is exactly how a bad regex hid a 53.8%-covered package behind
	// "worst_coverage_pct: 100". All-exempt is different: the filter worked and
	// deliberately excluded generated code, so it passes with the list recorded.
	if matched == 0 {
		g.Status = "fail"
		g.SkipReason = fmt.Sprintf("no changed package matched any of the %d coverage lines — the gate evaluated nothing", len(cov))
		return g
	}
	if evaluated == 0 {
		g.Metrics["note"] = "every changed package is coverage-exempt (generated code)"
		return g
	}
	if len(violations) > 0 {
		g.Status = "fail"
		g.Metrics["violations"] = violations
	}
	return g
}

// readModulePath extracts the `module X` path from a go.mod. Empty on any
// problem — callers fall back to suffix matching.
func readModulePath(goMod string) string {
	// #nosec G304 -- path derived from discovered module roots, not user input.
	data, err := os.ReadFile(goMod)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// changedPathFor maps a Go import path from test output back to the
// worktree-relative directory of a changed package in this module, if any.
//
// With the module path known from go.mod the mapping is exact: strip it to get
// the module-relative package dir. Only when go.mod could not be read does this
// fall back to suffix matching, which cannot resolve the module-root package.
func changedPathFor(m *Module, importPath string) (string, bool) {
	if m == nil {
		return importPath, true
	}

	// When the module path is known it is authoritative in both directions: an
	// import path outside this module is not ours, full stop. Falling through to
	// suffix matching here would let a same-named package in a sibling module
	// (core/service vs wallet/service) satisfy the gate.
	if m.ImportPath != "" {
		if importPath != m.ImportPath && !strings.HasPrefix(importPath, m.ImportPath+"/") {
			return "", false
		}
		sub := strings.TrimPrefix(strings.TrimPrefix(importPath, m.ImportPath), "/")
		for _, pkg := range m.Packages {
			if moduleRelPkg(m, pkg) == sub {
				return pkg, true
			}
		}
		return "", false
	}

	for _, pkg := range m.Packages {
		rel := moduleRelPkg(m, pkg)
		if rel == "" {
			continue // the module-root package is not identifiable by suffix
		}
		if importPath == rel || strings.HasSuffix(importPath, "/"+rel) {
			return pkg, true
		}
	}
	return "", false
}

// moduleRelPkg converts a worktree-relative package dir into a module-relative
// one. "" means the module root package.
func moduleRelPkg(m *Module, pkg string) string {
	if m.Rel == "." {
		if pkg == "." {
			return ""
		}
		return pkg
	}
	return strings.TrimPrefix(strings.TrimPrefix(pkg, m.Rel), "/")
}

// floorFor returns the first matching coverage floor. Order in config is
// significant here (unlike risk-paths.json): the most specific tier is listed
// first and the catch-all last.
func floorFor(cfg *Config, file string) float64 {
	for _, f := range cfg.CoverageFloors {
		if matchAny(f.Paths, file) {
			return f.Min
		}
	}
	return 0
}

// ─── bench_relative ──────────────────────────────────────────────────────────

// evaluateBenchRelative runs the same benchmarks on a detached base worktree and
// compares with benchstat. A regression over the configured percentage fails.
func evaluateBenchRelative(cfg *Config, state *RunState, p plan, outDir, headLog string) Gate {
	g := Gate{
		Status:     "pass",
		RanAt:      time.Now().UTC().Format(time.RFC3339),
		OutputPath: headLog,
		Command:    p.Spec.Command,
		Metrics:    map[string]any{},
	}
	if _, err := exec.LookPath("benchstat"); err != nil {
		g.Status = "fail"
		g.SkipReason = "benchstat not on PATH — cannot compare against base"
		return g
	}

	baseWT, err := os.MkdirTemp("", "gates-bench-base-*")
	if err != nil {
		g.Status = "fail"
		g.SkipReason = fmt.Sprintf("mkdtemp: %v", err)
		return g
	}
	defer func() {
		_, _ = runCmd(state.Repo.Worktree, 2*time.Minute, "git", "worktree", "remove", "--force", baseWT)
		_ = os.RemoveAll(baseWT)
	}()

	if out, err := runCmd(state.Repo.Worktree, 5*time.Minute, "git", "worktree", "add", "--detach", baseWT, state.Repo.BaseSHA); err != nil {
		g.Status = "fail"
		g.SkipReason = fmt.Sprintf("git worktree add %s: %v: %s", state.Repo.BaseSHA, err, tail(out, 5))
		return g
	}

	baseModule := baseWT
	if p.Module.Rel != "." {
		baseModule = filepath.Join(baseWT, filepath.FromSlash(p.Module.Rel))
	}
	if !fileExists(filepath.Join(baseModule, "go.mod")) {
		g.Metrics["note"] = "module does not exist at base — new module, no relative comparison possible"
		return g
	}

	timeout := time.Duration(p.Spec.TimeoutSeconds) * time.Second
	baseOut, _ := runCmd(baseModule, timeout, "sh", "-c", p.Spec.Command)
	basePath := filepath.Join(outDir, sanitize("bench_base:"+p.Module.Rel)+".txt")
	// #nosec G306 -- benchmark output is human-read evidence.
	_ = os.WriteFile(basePath, []byte(baseOut), 0644)

	statOut, err := runCmd(state.Repo.Worktree, 5*time.Minute, "benchstat", basePath, headLog)
	statPath := filepath.Join(outDir, sanitize("benchstat:"+p.Module.Rel)+".txt")
	// #nosec G306 -- benchstat comparison is human-read evidence.
	_ = os.WriteFile(statPath, []byte(statOut), 0644)
	g.Metrics["benchstat_output"] = statPath
	if err != nil {
		g.Status = "fail"
		g.SkipReason = fmt.Sprintf("benchstat failed: %v: %s", err, tail(statOut, 5))
		return g
	}

	regressions, worst := parseBenchstatRegressions(statOut, cfg.BenchMaxRegressionPct)
	g.Metrics["worst_regression_pct"] = worst
	if len(regressions) > 0 {
		g.Status = "fail"
		g.Metrics["regressions"] = regressions
	}
	return g
}

// parseBenchstatRegressions returns rows whose delta exceeds the allowed
// regression percentage, plus the worst delta seen.
func parseBenchstatRegressions(out string, maxPct float64) ([]string, float64) {
	var bad []string
	worst := 0.0
	for _, m := range benchstatRowRE.FindAllStringSubmatch(out, -1) {
		pct, err := strconv.ParseFloat(strings.TrimPrefix(m[2], "+"), 64)
		if err != nil {
			continue
		}
		if pct > worst {
			worst = pct
		}
		if pct > maxPct {
			bad = append(bad, fmt.Sprintf("%s %+.2f%% (max %+.0f%%)", m[1], pct, maxPct))
		}
	}
	return bad, worst
}

// ─── waivers ─────────────────────────────────────────────────────────────────

func parseWaivers(entries []string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range entries {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 || strings.TrimSpace(kv[0]) == "" || strings.TrimSpace(kv[1]) == "" {
			return nil, fmt.Errorf("bad waiver %q (want <gate>=<reason>; repeat -waive for several gates)", part)
		}
		out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
	return out, nil
}

// waiveOrFail is the single place the "no silent pass" rule lives: a gate that
// cannot run fails unless someone named it and said why.
func waiveOrFail(waivers map[string]string, gate, why string) Gate {
	g := Gate{RanAt: time.Now().UTC().Format(time.RFC3339)}
	if reason, ok := waivers[gate]; ok {
		g.Status = "skipped"
		g.SkipReason = fmt.Sprintf("%s — WAIVED: %s", why, reason)
		return g
	}
	g.Status = "fail"
	g.SkipReason = fmt.Sprintf("required gate could not run: %s (waive explicitly with -waive %s=<reason>)", why, gate)
	return g
}

// ─── run state merge ─────────────────────────────────────────────────────────

func readRunState(p string) (*RunState, error) {
	// #nosec G304 -- p is the -run-state flag.
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var s RunState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func mergeGates(p string, results []result) error {
	state, err := readRunState(p)
	if err != nil {
		return err
	}
	if state.Gates == nil {
		state.Gates = map[string]Gate{}
	}
	for _, r := range results {
		state.Gates[r.Key] = r.Outcome
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	// #nosec G306 -- the run state is a shared build artifact other nodes and the
	// human read; it holds paths and verdicts, never credentials.
	return os.WriteFile(p, append(data, '\n'), 0644)
}

// ─── output ──────────────────────────────────────────────────────────────────

func printInvalid(problems []string) {
	fmt.Println("=== GATES: INVALID_INPUT ===")
	for _, p := range problems {
		fmt.Printf("  ✗ %s\n", p)
	}
}

func printPlan(state *RunState, modules []Module, plans []plan) {
	fmt.Println("=== GATE PLAN (dry run) ===")
	fmt.Printf("Risk: %s   Components: %s\n", state.Classification.Risk, emptyDash(strings.Join(state.Classification.Components, ",")))
	fmt.Printf("Modules owning changed files (%d):\n", len(modules))
	for _, m := range modules {
		fmt.Printf("  %-55s packages: %s\n", m.Rel, strings.Join(m.Packages, " "))
	}
	fmt.Printf("\nGates to run (%d):\n", len(plans))
	for _, p := range plans {
		scope := "repo"
		if p.Module != nil {
			scope = p.Module.Rel
		}
		fmt.Printf("  %-16s %-50s %s\n", p.Gate, scope, p.Spec.Command)
	}
	fmt.Println("\n=== END GATE PLAN ===")
}

func printReport(state *RunState, modules []Module, results []result, failed int) {
	fmt.Println("\n=== GATE RESULTS ===")
	fmt.Printf("Task: %s   Risk: %s   Components: %s\n",
		emptyDash(state.TaskKey), state.Classification.Risk,
		emptyDash(strings.Join(state.Classification.Components, ",")))
	fmt.Printf("Modules: %d\n\n", len(modules))

	for _, r := range results {
		fmt.Printf("%s %-16s %-45s", statusMark(r.Outcome.Status), r.Gate, emptyDash(r.Module))
		if r.Outcome.DurationMS > 0 {
			fmt.Printf(" %6.1fs", float64(r.Outcome.DurationMS)/1000)
		}
		fmt.Println()
		if r.Outcome.SkipReason != "" {
			fmt.Printf("      %s\n", r.Outcome.SkipReason)
		}
		if v, ok := r.Outcome.Metrics["violations"]; ok {
			for _, s := range toStrings(v) {
				fmt.Printf("      · %s\n", s)
			}
		}
		if v, ok := r.Outcome.Metrics["regressions"]; ok {
			for _, s := range toStrings(v) {
				fmt.Printf("      · %s\n", s)
			}
		}
		if v, ok := r.Outcome.Metrics["no_op"]; ok {
			fmt.Printf("      NO-OP: %v\n", v)
		}
		if r.Outcome.Status == "fail" && r.Outcome.OutputPath != "" {
			// #nosec G304 -- OutputPath was written by this process.
			if data, err := os.ReadFile(r.Outcome.OutputPath); err == nil && len(data) > 0 {
				fmt.Printf("      --- tail of %s ---\n", r.Outcome.OutputPath)
				for _, line := range strings.Split(tail(string(data), maxTailLines), "\n") {
					if strings.TrimSpace(line) != "" {
						fmt.Printf("      %s\n", line)
					}
				}
			}
		}
	}

	fmt.Println()
	if failed == 0 {
		fmt.Printf("=== GATES: PASS (%d gates) ===\n", len(results))
		return
	}
	fmt.Printf("=== GATES: FAIL (%d of %d) ===\n", failed, len(results))
	fmt.Println("Return to the Coder with the failing gate and its raw output. Do not spend reviewer tokens on code that fails deterministic checks.")
}

func statusMark(s string) string {
	switch s {
	case "pass":
		return "✓"
	case "fail":
		return "✗"
	case "skipped":
		return "○"
	}
	return "?"
}

// ─── small helpers ───────────────────────────────────────────────────────────

func runCmd(dir string, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// #nosec G204 -- callers pass literal binaries ("git", "benchstat", "sh") and
	// args from gates.json or SHAs already resolved by cmd/classify.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if asExitError(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func asExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}

func expand(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

func firstWord(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func sanitize(s string) string {
	r := strings.NewReplacer("/", "_", ":", "-", " ", "_", ".", "_")
	return r.Replace(s)
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func splitTrim(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, p := range splitTrim(s) {
		m[p] = true
	}
	return m
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// matchAny applies matchGlob against every pattern.
func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if matchGlob(p, name) {
			return true
		}
	}
	return false
}

// matchGlob mirrors cmd/classify's matcher: `**` spans path segments, `*` and
// `?` match within one. Duplicated rather than shared because each cmd/ tool is
// its own stdlib-only module, matching the existing convention in this repo.
func matchGlob(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pat, seg []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(seg); i++ {
				if matchSegments(pat[1:], seg[i:]) {
					return true
				}
			}
			return false
		}
		if len(seg) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], seg[0])
		if err != nil || !ok {
			return false
		}
		pat, seg = pat[1:], seg[1:]
	}
	return len(seg) == 0
}

func tail(s string, lines int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) <= lines {
		return strings.Join(parts, "\n")
	}
	return strings.Join(parts[len(parts)-lines:], "\n")
}

func toStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			out = append(out, fmt.Sprint(e))
		}
		return out
	}
	return []string{fmt.Sprint(v)}
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
