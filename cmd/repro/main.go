// Command repro runs bug-reproduction harness specs with deterministic
// evidence: a stack-version preflight that refuses to run against the wrong
// build, an N-run RED/GREEN/FLAKY classifier, and a Jira comment that carries
// the spec's human-readable steps plus the exact SHAs the verdict was
// produced against.
//
// Usage:
//
//	repro preflight -worktree ~/Project/evenplay-mono-wt-SMG-XXXX [-clients burrito-golf,skillstrike]
//	repro run -worktree <path> -spec tests/cross-app/foo.spec.ts [-runs 3] [-expect red] \
//	          [-clients burrito-golf,skillstrike] [-jira SMG-XXXX] [-post]
//
// Suites are detected from the spec path: *.spec.ts → playwright
// (tests/e2e/playwright), *.feature → karate (tests/e2e/karate). Anything
// else needs -cmd (run verbatim via bash -c) and optionally -dir.
//
// The preflight exists because a harness verdict is only evidence about the
// code the stack actually serves: RED against a build that predates the fix,
// or GREEN against a build that predates the bug, is silent noise. It checks
// (1) which checkout the running tilt was started from and its SHA vs the
// worktree under test, (2) that every tilt resource is built and healthy
// (stale images are the documented trap — CLAUDE.md "Local Stack (tilt)
// Gotchas" #1), and (3) that any required web/mobile dev client is running
// from the same code.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "preflight":
		os.Exit(cmdPreflight(os.Args[2:]))
	case "run":
		os.Exit(cmdRun(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  repro preflight -worktree <path> [-clients burrito-golf,skillstrike] [-strict]
  repro run -worktree <path> -spec <path> [-runs N] [-expect red|green]
            [-clients ...] [-skip-preflight] [-strict] [-timeout 15m]
            [-cmd "..."] [-dir <path>] [-jira SMG-XXXX] [-post] [-forecast-config <path>]

exit codes: 0 verdict matches expectation (or no expectation and not flaky),
            1 verdict mismatch, 2 flaky, 3 preflight or setup failure`)
}

// --- clients ----------------------------------------------------------------

// clientAppDirs maps the -clients names to the app directory each dev server
// runs from (and whose source it serves).
var clientAppDirs = map[string]string{
	"burrito-golf": "apps/burrito-golf-web",
	"skillstrike":  "apps/skillstrike-mobile",
}

// devServerTokens mark a process as plausibly a dev server rather than an
// editor or grep that happens to sit in the app directory.
var devServerTokens = []string{"node", "vite", "npm", "pnpm", "yarn", "expo", "metro", "bun"}

// --- preflight --------------------------------------------------------------

type stackProcess struct {
	PID      int
	Cwd      string
	Cmdline  string
	RepoRoot string
	SHA      string
	Dirty    bool
}

type preflightResult struct {
	WorktreeRoot string
	WorktreeSHA  string
	WorktreeDirty bool
	Tilt         *stackProcess
	ResourcesOK  bool
	NotOK        []string
	Clients      map[string]*stackProcess
	Failures     []string
	Warnings     []string
	Infos        []string
}

func cmdPreflight(args []string) int {
	fs := flag.NewFlagSet("preflight", flag.ExitOnError)
	worktree := fs.String("worktree", "", "Worktree whose code the stack must be serving (required)")
	clients := fs.String("clients", "", "Comma-separated dev clients that must run this code: burrito-golf, skillstrike")
	strict := fs.Bool("strict", false, "Treat warnings (unverifiable checks) as failures")
	fs.Parse(args)
	if *worktree == "" {
		fmt.Fprintln(os.Stderr, "-worktree is required")
		return 3
	}
	res := runPreflight(*worktree, splitTrim(*clients), *strict)
	printPreflight(res)
	if len(res.Failures) > 0 {
		return 3
	}
	return 0
}

func runPreflight(worktree string, clients []string, strict bool) preflightResult {
	res := preflightResult{Clients: map[string]*stackProcess{}}

	root, err := gitRoot(worktree)
	if err != nil {
		res.Failures = append(res.Failures, fmt.Sprintf("worktree %s is not a git checkout: %v", worktree, err))
		return res
	}
	res.WorktreeRoot = root
	res.WorktreeSHA, _ = gitSHA(root)
	res.WorktreeDirty = gitDirty(root)

	procs := scanProcs()

	// 1. Which checkout is tilt serving from?
	tilt := findTilt(procs)
	if tilt == nil {
		// Pods outlive the tilt process: a dead `tilt up` with live pods is a
		// FROZEN stack serving stale images of unknowable provenance — worse
		// than no stack, because everything still responds.
		if n := runningPodCount(); n > 0 {
			res.Failures = append(res.Failures, fmt.Sprintf("tilt is not running but %d pods are still serving — the stack is FROZEN at whatever images tilt last built (their source SHA is unknowable). Restart `tilt up` from the checkout under test, wait for green, then re-run", n))
		} else {
			res.Failures = append(res.Failures, "tilt is not running ('tilt up' process not found) — start the stack before running specs")
		}
	} else {
		resolveRepo(tilt)
		res.Tilt = tilt
		switch {
		case tilt.RepoRoot == "":
			res.Failures = append(res.Failures, fmt.Sprintf("tilt (pid %d) runs from %s, which is not a git checkout", tilt.PID, tilt.Cwd))
		case sameDir(tilt.RepoRoot, root):
			res.Infos = append(res.Infos, "tilt runs from the worktree under test — stack serves this working tree (including uncommitted changes)")
		case tilt.SHA != res.WorktreeSHA:
			res.Failures = append(res.Failures, fmt.Sprintf("STACK VERSION MISMATCH: tilt serves %s @ %s but the worktree under test is @ %s — run tilt from the worktree, or check this branch out in tilt's checkout and `tilt trigger` the affected services", tilt.RepoRoot, short(tilt.SHA), short(res.WorktreeSHA)))
		case res.WorktreeDirty:
			res.Failures = append(res.Failures, "worktree has UNCOMMITTED changes that a stack running from a different checkout cannot be serving — commit them or run tilt from this worktree")
		case tilt.Dirty:
			res.Failures = append(res.Failures, fmt.Sprintf("tilt's checkout %s has uncommitted changes — the running stack may diverge from SHA %s", tilt.RepoRoot, short(tilt.SHA)))
		default:
			res.Infos = append(res.Infos, fmt.Sprintf("tilt checkout matches worktree SHA %s", short(res.WorktreeSHA)))
		}
	}

	// 2. Are all tilt resources built and healthy? (stale-image trap)
	if tilt != nil {
		notOK, err := tiltResourcesNotOK()
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("could not verify tilt resource status: %v", err))
		} else if len(notOK) > 0 {
			res.Failures = append(res.Failures, fmt.Sprintf("tilt resources not built/healthy: %s — wait for green or `tilt trigger` them; a stale image silently serves old code", strings.Join(notOK, ", ")))
		} else {
			res.ResourcesOK = true
		}
	}

	// 3. Required dev clients run this code?
	for _, name := range clients {
		appDir, ok := clientAppDirs[name]
		if !ok {
			res.Failures = append(res.Failures, fmt.Sprintf("unknown client %q (known: burrito-golf, skillstrike)", name))
			continue
		}
		p := findClient(procs, appDir)
		if p == nil {
			res.Failures = append(res.Failures, fmt.Sprintf("client %q dev server not found — kill any stale instance and start it from %s", name, filepath.Join(root, appDir)))
			continue
		}
		resolveRepo(p)
		res.Clients[name] = p
		switch {
		case p.RepoRoot == "":
			res.Failures = append(res.Failures, fmt.Sprintf("client %q (pid %d) runs from %s, not a git checkout", name, p.PID, p.Cwd))
		case sameDir(p.RepoRoot, root):
			// serving the tree under test, dirty or not
		case p.SHA != res.WorktreeSHA || res.WorktreeDirty || p.Dirty:
			res.Failures = append(res.Failures, fmt.Sprintf("CLIENT VERSION MISMATCH: %q serves %s @ %s%s but the worktree under test is @ %s%s — kill it and restart from the worktree", name, p.RepoRoot, short(p.SHA), dirtyTag(p.Dirty), short(res.WorktreeSHA), dirtyTag(res.WorktreeDirty)))
		}
	}

	if strict {
		res.Failures = append(res.Failures, res.Warnings...)
		res.Warnings = nil
	}
	return res
}

func printPreflight(res preflightResult) {
	fmt.Printf("Preflight — worktree %s @ %s%s\n", res.WorktreeRoot, short(res.WorktreeSHA), dirtyTag(res.WorktreeDirty))
	if res.Tilt != nil {
		fmt.Printf("  tilt (pid %d): %s @ %s%s\n", res.Tilt.PID, res.Tilt.RepoRoot, short(res.Tilt.SHA), dirtyTag(res.Tilt.Dirty))
	}
	if res.ResourcesOK {
		fmt.Println("  tilt resources: all built and healthy")
	}
	for name, p := range res.Clients {
		fmt.Printf("  client %s (pid %d): %s @ %s%s\n", name, p.PID, p.RepoRoot, short(p.SHA), dirtyTag(p.Dirty))
	}
	for _, s := range res.Infos {
		fmt.Printf("  info: %s\n", s)
	}
	for _, s := range res.Warnings {
		fmt.Printf("  WARN: %s\n", s)
	}
	for _, s := range res.Failures {
		fmt.Printf("  FAIL: %s\n", s)
	}
	if len(res.Failures) == 0 {
		fmt.Println("PREFLIGHT: PASS")
	} else {
		fmt.Println("PREFLIGHT: FAIL — a spec run against this stack would not be evidence about the worktree under test")
	}
}

// --- run --------------------------------------------------------------------

type runConfig struct {
	worktree  string
	spec      string
	runs      int
	expect    string
	timeout   time.Duration
	suite     string
	dir       string
	argv      []string
	specAbs   string
	jiraKey   string
	post      bool
	forecast  string
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	worktree := fs.String("worktree", "", "Worktree under test (required)")
	spec := fs.String("spec", "", "Spec path: *.spec.ts (playwright), *.feature (karate), else use -cmd")
	custom := fs.String("cmd", "", "Custom run command (bash -c) for suites repro doesn't know")
	customDir := fs.String("dir", "", "Working directory for -cmd (default: worktree root)")
	runs := fs.Int("runs", 3, "Times to run the spec (flake detection needs >1)")
	expect := fs.String("expect", "", "Expected verdict: red (bug reproduces) or green (guard passes)")
	clients := fs.String("clients", "", "Dev clients the spec drives: burrito-golf, skillstrike")
	skipPreflight := fs.Bool("skip-preflight", false, "Skip the stack-version preflight (the verdict will say so)")
	strict := fs.Bool("strict", false, "Preflight warnings are failures")
	timeout := fs.Duration("timeout", 15*time.Minute, "Per-run timeout")
	jiraKey := fs.String("jira", "", "Jira key to build the evidence comment for (print always; -post to send)")
	post := fs.Bool("post", false, "Post the comment AND attach the spec file to -jira via forecast")
	forecastCfg := fs.String("forecast-config", "", "forecast config path (default: <worktree>/.forecast/config.yaml, else ~/Project/evenplay-mono/.forecast/config.yaml)")
	fs.Parse(args)

	if *worktree == "" || (*spec == "" && *custom == "") {
		fmt.Fprintln(os.Stderr, "-worktree and one of -spec/-cmd are required")
		return 3
	}

	cfg := runConfig{worktree: *worktree, spec: *spec, runs: *runs, expect: strings.ToLower(*expect),
		timeout: *timeout, jiraKey: *jiraKey, post: *post, forecast: *forecastCfg}
	if err := resolveSuite(&cfg, *custom, *customDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}

	root, err := gitRoot(cfg.worktree)
	if err != nil {
		fmt.Fprintf(os.Stderr, "worktree %s is not a git checkout: %v\n", cfg.worktree, err)
		return 3
	}
	sha, _ := gitSHA(root)

	var pre preflightResult
	preflightNote := "SKIPPED (-skip-preflight)"
	if !*skipPreflight {
		pre = runPreflight(cfg.worktree, splitTrim(*clients), *strict)
		printPreflight(pre)
		if len(pre.Failures) > 0 {
			return 3
		}
		preflightNote = "PASS"
	}

	env := os.Environ()
	if cfg.suite == "playwright" {
		pwEnv, err := playwrightEnv()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to read dev-jwt secrets for playwright env: %v\n", err)
			return 3
		}
		env = append(env, pwEnv...)
	}

	logDir, err := os.MkdirTemp("", "repro-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}

	var passes []bool
	var lastOutput string
	for i := 1; i <= cfg.runs; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
		cmd := exec.CommandContext(ctx, cfg.argv[0], cfg.argv[1:]...)
		cmd.Dir = cfg.dir
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		runErr := cmd.Run()
		cancel()
		pass := runErr == nil
		passes = append(passes, pass)
		lastOutput = out.String()
		os.WriteFile(filepath.Join(logDir, fmt.Sprintf("run-%d.log", i)), out.Bytes(), 0644)
		fmt.Printf("run %d/%d: %s\n", i, cfg.runs, passFailWord(pass))
	}

	verdict := classifyRuns(passes)
	steps, stepsErr := extractSteps(cfg.specAbs)

	report := buildReport(cfg, verdict, passes, sha, gitDirty(root), pre, preflightNote, steps, stepsErr, lastOutput, logDir)
	fmt.Println(report)

	if cfg.jiraKey != "" {
		comment := buildJiraComment(cfg, verdict, passes, root, sha, gitDirty(root), pre, preflightNote, steps, lastOutput)
		fmt.Println("\n----- Jira comment -----")
		fmt.Println(comment)
		if cfg.post {
			if len(steps) == 0 {
				fmt.Fprintln(os.Stderr, "refusing to -post: the spec has no 'Steps:' block in its docstring — a human cannot confirm the reproduction steps from the ticket. Add the block and re-run.")
				return 3
			}
			if err := postToJira(cfg, root, comment); err != nil {
				fmt.Fprintf(os.Stderr, "posting to Jira failed (comment printed above for manual paste): %v\n", err)
			} else {
				fmt.Printf("posted comment + attached spec to %s\n", cfg.jiraKey)
			}
		}
	}

	switch {
	case verdict == "FLAKY":
		return 2
	case cfg.expect == "":
		return 0
	case strings.EqualFold(verdict, cfg.expect):
		return 0
	default:
		return 1
	}
}

func resolveSuite(cfg *runConfig, custom, customDir string) error {
	switch {
	case custom != "":
		cfg.suite = "custom"
		cfg.dir = customDir
		if cfg.dir == "" {
			cfg.dir = cfg.worktree
		}
		cfg.argv = []string{"bash", "-c", custom}
		if cfg.spec != "" {
			cfg.specAbs = absUnder(cfg.worktree, cfg.spec)
		}
	case strings.HasSuffix(cfg.spec, ".spec.ts"):
		cfg.suite = "playwright"
		cfg.dir = filepath.Join(cfg.worktree, "tests/e2e/playwright")
		rel := relToSuite(cfg.spec, "tests/e2e/playwright/")
		cfg.argv = []string{"npx", "playwright", "test", rel, "--reporter=list"}
		cfg.specAbs = filepath.Join(cfg.dir, rel)
	case strings.HasSuffix(cfg.spec, ".feature"):
		cfg.suite = "karate"
		cfg.dir = filepath.Join(cfg.worktree, "tests/e2e/karate")
		rel := relToSuite(cfg.spec, "tests/e2e/karate/")
		cfg.argv = []string{"bash", "run-tests.sh", rel}
		cfg.specAbs = filepath.Join(cfg.dir, rel)
	default:
		return fmt.Errorf("cannot infer a suite from %q — pass -cmd (and optionally -dir) for suites repro doesn't know", cfg.spec)
	}
	return nil
}

// relToSuite normalizes a spec path to be relative to the suite directory,
// tolerating absolute paths and repo-relative paths that include the suite
// prefix.
func relToSuite(spec, suitePrefix string) string {
	if idx := strings.Index(spec, suitePrefix); idx >= 0 {
		return spec[idx+len(suitePrefix):]
	}
	return strings.TrimPrefix(spec, "./")
}

func absUnder(worktree, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(worktree, p)
}

func classifyRuns(passes []bool) string {
	if len(passes) == 0 {
		return "FLAKY"
	}
	pass, fail := 0, 0
	for _, p := range passes {
		if p {
			pass++
		} else {
			fail++
		}
	}
	switch {
	case fail == len(passes):
		return "RED"
	case pass == len(passes):
		return "GREEN"
	default:
		return "FLAKY"
	}
}

// extractSteps pulls the human-readable "Steps:" block out of the spec's
// docstring so the Jira comment shows WHAT the spec does, not just that it
// passed or failed. Capture starts at a comment line containing "Steps:" and
// ends at the first blank comment line or the end of the comment block.
func extractSteps(specPath string) ([]string, error) {
	if specPath == "" {
		return nil, fmt.Errorf("no spec file to extract steps from")
	}
	data, err := os.ReadFile(specPath)
	if err != nil {
		return nil, err
	}
	var steps []string
	capturing := false
	for _, line := range strings.Split(string(data), "\n") {
		stripped := stripCommentLeader(line)
		if !capturing {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(stripped)), "steps:") {
				capturing = true
				rest := strings.TrimSpace(stripped[strings.Index(strings.ToLower(stripped), "steps:")+len("steps:"):])
				if rest != "" {
					steps = append(steps, rest)
				}
			}
			continue
		}
		trimmed := strings.TrimSpace(stripped)
		if trimmed == "" || !looksLikeComment(line) {
			break
		}
		steps = append(steps, trimmed)
	}
	return steps, nil
}

var commentLeader = regexp.MustCompile(`^\s*(\*+/?|//+|#+)\s?`)

func stripCommentLeader(line string) string {
	return commentLeader.ReplaceAllString(line, "")
}

func looksLikeComment(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "/*")
}

// --- report + jira ----------------------------------------------------------

func buildReport(cfg runConfig, verdict string, passes []bool, sha string, dirty bool, pre preflightResult, preflightNote string, steps []string, stepsErr error, lastOutput, logDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n# Repro Run Report\n")
	fmt.Fprintf(&b, "**Spec:** `%s` (suite: %s)\n", cfg.spec, cfg.suite)
	fmt.Fprintf(&b, "**Verdict:** %s (%s)\n", verdict, runTally(passes))
	fmt.Fprintf(&b, "**Worktree:** %s @ %s%s\n", cfg.worktree, short(sha), dirtyTag(dirty))
	fmt.Fprintf(&b, "**Preflight:** %s\n", preflightNote)
	if cfg.expect != "" {
		fmt.Fprintf(&b, "**Expected:** %s → %s\n", strings.ToUpper(cfg.expect), matchWord(verdict, cfg.expect))
	}
	if len(steps) > 0 {
		b.WriteString("\n**Steps the spec takes:**\n")
		for i, s := range steps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, s)
		}
	} else if stepsErr != nil || cfg.specAbs != "" {
		b.WriteString("\n⚠️ No 'Steps:' block found in the spec docstring — add one so a human can confirm the reproduction from the ticket.\n")
	}
	fmt.Fprintf(&b, "\nLogs: %s\n", logDir)
	fmt.Fprintf(&b, "\n## Last run output (tail)\n```\n%s\n```\n", tail(lastOutput, 30))
	return b.String()
}

func buildJiraComment(cfg runConfig, verdict string, passes []bool, root, sha string, dirty bool, pre preflightResult, preflightNote string, steps []string, lastOutput string) string {
	var b strings.Builder
	b.WriteString("## Harness reproduction run (cmd/repro)\n\n")
	fmt.Fprintf(&b, "**Spec:** `%s` — attached to this ticket\n", cfg.spec)
	fmt.Fprintf(&b, "**Verdict:** %s (%s)%s\n", verdict, runTally(passes), verdictGloss(verdict))
	fmt.Fprintf(&b, "**Worktree under test:** `%s` @ `%s`%s\n", root, short(sha), dirtyTag(dirty))
	if pre.Tilt != nil {
		fmt.Fprintf(&b, "**Server stack:** tilt from `%s` @ `%s`%s — resources %s\n", pre.Tilt.RepoRoot, short(pre.Tilt.SHA), dirtyTag(pre.Tilt.Dirty), okWord(pre.ResourcesOK))
	} else {
		fmt.Fprintf(&b, "**Server stack:** preflight %s\n", preflightNote)
	}
	for name, p := range pre.Clients {
		fmt.Fprintf(&b, "**Client %s:** `%s` @ `%s`%s\n", name, p.RepoRoot, short(p.SHA), dirtyTag(p.Dirty))
	}
	b.WriteString("\n### Steps the spec takes — please confirm these match the reported bug\n")
	if len(steps) == 0 {
		b.WriteString("_(spec has no Steps: block — do not trust this run until one is added)_\n")
	}
	for i, s := range steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, s)
	}
	fmt.Fprintf(&b, "\n### Sample run output (tail)\n```\n%s\n```\n", tail(lastOutput, 25))
	return b.String()
}

func verdictGloss(v string) string {
	switch v {
	case "RED":
		return " — bug reproduces; spec lands as regression seal"
	case "GREEN":
		return " — did not reproduce at this layer; spec lands as forward-looking guard, bug is likely elsewhere"
	default:
		return " — DO NOT trust this run; investigate the flake before concluding anything"
	}
}

func postToJira(cfg runConfig, root, comment string) error {
	cfgPath := cfg.forecast
	if cfgPath == "" {
		for _, cand := range []string{
			filepath.Join(root, ".forecast/config.yaml"),
			filepath.Join(os.Getenv("HOME"), "Project/evenplay-mono/.forecast/config.yaml"),
		} {
			if _, err := os.Stat(cand); err == nil {
				cfgPath = cand
				break
			}
		}
	}
	if cfgPath == "" {
		return fmt.Errorf("no forecast config found; pass -forecast-config")
	}
	forecast := filepath.Join(os.Getenv("HOME"), "Project/forecast/forecast")
	if _, err := exec.LookPath("forecast"); err == nil {
		forecast = "forecast"
	}
	cmt := exec.Command(forecast, "--config", cfgPath, "jira", "comment", cfg.jiraKey, "--body", comment)
	if out, err := cmt.CombinedOutput(); err != nil {
		return fmt.Errorf("comment failed: %v: %s", err, tail(string(out), 5))
	}
	if cfg.specAbs != "" {
		att := exec.Command(forecast, "--config", cfgPath, "jira", "attach", cfg.jiraKey, cfg.specAbs)
		if out, err := att.CombinedOutput(); err != nil {
			return fmt.Errorf("comment posted but attaching spec failed: %v: %s", err, tail(string(out), 5))
		}
	}
	return nil
}

// --- stack discovery --------------------------------------------------------

type procInfo struct {
	PID     int
	Cwd     string
	Cmdline string
}

func scanProcs() []procInfo {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var procs []procInfo
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd"))
		if err != nil {
			continue // other users' processes
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		procs = append(procs, procInfo{PID: pid, Cwd: cwd, Cmdline: strings.ReplaceAll(strings.TrimRight(string(raw), "\x00"), "\x00", " ")})
	}
	return procs
}

func findTilt(procs []procInfo) *stackProcess {
	for _, p := range procs {
		fields := strings.Fields(p.Cmdline)
		if len(fields) >= 2 && filepath.Base(fields[0]) == "tilt" && fields[1] == "up" {
			return &stackProcess{PID: p.PID, Cwd: p.Cwd, Cmdline: p.Cmdline}
		}
	}
	return nil
}

func findClient(procs []procInfo, appDir string) *stackProcess {
	appBase := filepath.Base(appDir)
	for _, p := range procs {
		if !hasDevServerToken(p.Cmdline) {
			continue
		}
		if strings.Contains(p.Cwd, appDir) || strings.Contains(p.Cmdline, appBase) {
			return &stackProcess{PID: p.PID, Cwd: p.Cwd, Cmdline: p.Cmdline}
		}
	}
	return nil
}

func hasDevServerToken(cmdline string) bool {
	lower := strings.ToLower(cmdline)
	argv0 := ""
	if fields := strings.Fields(lower); len(fields) > 0 {
		argv0 = filepath.Base(fields[0])
	}
	for _, tok := range devServerTokens {
		if argv0 == tok || strings.Contains(lower, tok) {
			return true
		}
	}
	return false
}

func resolveRepo(p *stackProcess) {
	root, err := gitRoot(p.Cwd)
	if err != nil {
		return
	}
	p.RepoRoot = root
	p.SHA, _ = gitSHA(root)
	p.Dirty = gitDirty(root)
}

// --- tilt resources ---------------------------------------------------------

type uiResourceList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			UpdateStatus  string `json:"updateStatus"`
			RuntimeStatus string `json:"runtimeStatus"`
		} `json:"status"`
	} `json:"items"`
}

func runningPodCount() int {
	out, err := exec.Command("kubectl", "get", "pods", "--no-headers").Output()
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(line, "Running") {
			count++
		}
	}
	return count
}

func tiltResourcesNotOK() ([]string, error) {
	out, err := exec.Command("tilt", "get", "uiresources", "-o", "json").Output()
	if err != nil {
		return nil, err
	}
	return parseUIResources(out)
}

func parseUIResources(data []byte) ([]string, error) {
	var list uiResourceList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	var notOK []string
	for _, item := range list.Items {
		u, r := item.Status.UpdateStatus, item.Status.RuntimeStatus
		if u == "error" || u == "pending" || u == "in_progress" || r == "error" || r == "pending" {
			notOK = append(notOK, fmt.Sprintf("%s (update=%s runtime=%s)", item.Metadata.Name, u, r))
		}
	}
	return notOK, nil
}

// --- playwright env ---------------------------------------------------------

func playwrightEnv() ([]string, error) {
	out, err := exec.Command("kubectl", "get", "secret", "dev-jwt", "-o", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl get secret dev-jwt: %w (is the stack up?)", err)
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &secret); err != nil {
		return nil, err
	}
	decode := func(key string) (string, error) {
		raw, ok := secret.Data[key]
		if !ok {
			return "", fmt.Errorf("dev-jwt secret has no %q key", key)
		}
		b, err := base64.StdEncoding.DecodeString(raw)
		return string(b), err
	}
	token, err := decode("token")
	if err != nil {
		return nil, err
	}
	simToken, err := decode("simulator-token")
	if err != nil {
		return nil, err
	}
	return []string{
		"DEV_JWT_TOKEN=" + token,
		"SIMULATOR_TOKEN=" + simToken,
		"SIMULATOR_SECRET=test-hmac-secret-for-e2e",
	}, nil
}

// --- git + small helpers ------------------------------------------------------

func gitRoot(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitSHA(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitDirty(dir string) bool {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return ra == rb
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	if sha == "" {
		return "(unknown)"
	}
	return sha
}

func dirtyTag(dirty bool) string {
	if dirty {
		return " (dirty)"
	}
	return ""
}

func passFailWord(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

func okWord(ok bool) string {
	if ok {
		return "all built and healthy"
	}
	return "NOT verified"
}

func matchWord(verdict, expect string) string {
	if strings.EqualFold(verdict, expect) {
		return "as expected"
	}
	return "MISMATCH"
}

func runTally(passes []bool) string {
	pass := 0
	for _, p := range passes {
		if p {
			pass++
		}
	}
	return fmt.Sprintf("%d/%d runs passed", pass, len(passes))
}

func tail(s string, lines int) string {
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

func splitTrim(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
