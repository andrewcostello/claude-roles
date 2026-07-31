// Command classify turns a diff into the run's classification: risk tier,
// component presets, panel shape, gate flags, and the argv for cmd/reviewer.
//
// It exists because the Tasker currently makes these calls by reading prose.
// Every one of them is a path-glob lookup or a regex scan over changed lines,
// and every one of them has been forgotten at least once:
//
//	tasker.md:231  "The CLI cannot infer the component from the diff; forgetting
//	                the flag silently reviews at the generic tier floor."
//	tasker.md:195  20 lines arguing that the panel is mandatory, after PR 1294
//	                shipped a customer-facing regression on a "read-path" rationale.
//	PR 1298        a "client-only" debug-panel change reviewed by one human at Low
//	                turned out to be a fail-open gate reachable in deployed envs.
//
// Risk is the MAX over matched rules, components the UNION, financial an OR.
// Adding a rule can only raise risk, so the config is safe to extend. A file no
// rule covers takes unmatched_risk (fail-closed) and is reported by name.
//
// Floors are deliberately NOT computed here — cmd/reviewer's buildFloors owns
// the component-to-floor table. Duplicating it would let the two drift.
//
// Exit codes match cmd/reviewer's convention: 0 classified, 3 INVALID_INPUT.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	schemaVersion  = 1
	exitInvalid    = 3
	maxSampleLen   = 120
	maxGateSamples = 40
)

// knownComponents mirrors the keys of cmd/reviewer's componentFloorPresets.
// Validated at config load so a typo in risk-paths.json cannot reach the
// reviewer as an unknown -component (which the reviewer rejects at runtime,
// after the panel has already been paid for).
var knownComponents = map[string]bool{
	"wallet":               true,
	"bet-settlement":       true,
	"bet-placement":        true,
	"jackpot":              true,
	"responsible-gambling": true,
}

var riskRank = map[string]int{"low": 1, "medium": 2, "high": 3, "critical": 4}

// ─── config ──────────────────────────────────────────────────────────────────

type Config struct {
	SchemaVersion           int          `json:"schema_version"`
	UnmatchedRisk           string       `json:"unmatched_risk"`
	ServerSurfaceExtensions []string     `json:"server_surface_extensions"`
	Rules                   []Rule       `json:"rules"`
	GateSignals             []GateSignal `json:"gate_signals"`
}

type Rule struct {
	ID           string   `json:"id"`
	Note         string   `json:"note,omitempty"`
	Paths        []string `json:"paths"`
	Risk         string   `json:"risk"`
	Components   []string `json:"components,omitempty"`
	Financial    bool     `json:"financial,omitempty"`
	Presentation bool     `json:"presentation,omitempty"`
	Migration    bool     `json:"migration,omitempty"`
}

type GateSignal struct {
	ID      string `json:"id"`
	Note    string `json:"note,omitempty"`
	Pattern string `json:"pattern"`

	re *regexp.Regexp
}

// ─── run state (subset this node owns; see config/run-state.schema.json) ─────

type RunState struct {
	SchemaVersion    int             `json:"schema_version"`
	TaskKey          string          `json:"task_key,omitempty"`
	CreatedAt        string          `json:"created_at,omitempty"`
	UpdatedAt        string          `json:"updated_at,omitempty"`
	Repo             Repo            `json:"repo"`
	Classification   *Classification `json:"classification,omitempty"`
	Gates            map[string]any  `json:"gates,omitempty"`
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
	Risk                  string      `json:"risk"`
	RiskReasons           []string    `json:"risk_reasons,omitempty"`
	Components            []string    `json:"components,omitempty"`
	FinancialPathsTouched bool        `json:"financial_paths_touched"`
	ClientOnly            bool        `json:"client_only"`
	ServerSurface         bool        `json:"server_surface"`
	Migration             bool        `json:"migration"`
	GateSignals           []GateHit   `json:"gate_signals,omitempty"`
	Panel                 Panel       `json:"panel"`
	HumanPRGate           bool        `json:"human_pr_gate"`
	RecheckMinSeverity    string      `json:"recheck_min_severity"`
	Skills                []string    `json:"skills,omitempty"`
	ReviewerArgs          []string    `json:"reviewer_args,omitempty"`
	ChangedFiles          []FileClass `json:"changed_files,omitempty"`
	UnmatchedFiles        []string    `json:"unmatched_files,omitempty"`
	ConfigPath            string      `json:"config_path"`
	ClassifiedAt          string      `json:"classified_at"`
}

type GateHit struct {
	Signal string `json:"signal"`
	File   string `json:"file"`
	Sample string `json:"sample,omitempty"`
}

type Panel struct {
	Required bool     `json:"required"`
	Seats    int      `json:"seats"`
	Reduced  bool     `json:"reduced"`
	Reasons  []string `json:"reasons,omitempty"`
}

type FileClass struct {
	Path  string   `json:"path"`
	Risk  string   `json:"risk"`
	Rules []string `json:"rules,omitempty"`
}

type options struct {
	configPath string
	worktree   string
	base       string
	task       string
	out        string
	json       bool
	noGit      bool
	args       []string
}

func main() {
	os.Exit(run(parseFlags()))
}

func parseFlags() options {
	configFlag := flag.String("config", "", "Path to risk-paths.json (default: <this binary's repo>/config/risk-paths.json)")
	worktreeFlag := flag.String("worktree", ".", "Worktree holding the change")
	baseFlag := flag.String("base", "origin/main", "Base ref to diff against. A local branch name whose origin/ counterpart differs is rejected — the remote is the truth.")
	taskFlag := flag.String("task", "", "Task key, e.g. SMG-3966")
	outFlag := flag.String("out", "", "Run-state JSON to create or update. Preserves gates/rounds/pr written by other nodes.")
	jsonFlag := flag.Bool("json", false, "Print the classification JSON to stdout instead of the report")
	noGitFlag := flag.Bool("no-git", false, "Skip base/head resolution. For classifying a bare diff with no worktree.")
	flag.Parse()

	log.SetFlags(0)
	log.SetPrefix("classify: ")

	return options{
		configPath: *configFlag,
		worktree:   *worktreeFlag,
		base:       *baseFlag,
		task:       *taskFlag,
		out:        *outFlag,
		json:       *jsonFlag,
		noGit:      *noGitFlag,
		args:       flag.Args(),
	}
}

func run(opts options) int {
	cfg, diff, files, err := loadInputs(opts)
	if err != nil {
		log.Fatalf("%v", err)
	}

	repo, problems := validateInput(diff, files, opts.worktree, opts.base, opts.noGit)
	if len(problems) > 0 {
		printInvalidInput(repo, problems)
		return exitInvalid
	}

	cls := buildClassification(cfg, files, diff, repo, opts.configPath)
	emit(repo, cls, opts.json)
	return persist(opts, repo, cls)
}

func loadInputs(opts options) (*Config, string, []string, error) {
	cfgPath := opts.configPath
	if cfgPath == "" {
		cfgPath = defaultConfigPath()
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return nil, "", nil, fmt.Errorf("config: %w", err)
	}
	diff, err := readDiff(opts.args)
	if err != nil {
		return nil, "", nil, err
	}
	return cfg, diff, parseDiffFiles(diff), nil
}

func buildClassification(cfg *Config, files []string, diff string, repo Repo, cfgPath string) *Classification {
	if cfgPath == "" {
		cfgPath = defaultConfigPath()
	}
	cls := classify(cfg, files, diff)
	cls.ConfigPath = cfgPath
	cls.ClassifiedAt = time.Now().UTC().Format(time.RFC3339)
	cls.ReviewerArgs = reviewerArgs(repo, cls)
	return cls
}

func persist(opts options, repo Repo, cls *Classification) int {
	if opts.out == "" {
		return 0
	}
	if err := writeRunState(opts.out, opts.task, repo, cls); err != nil {
		log.Fatalf("write run state %s: %v", opts.out, err)
	}
	log.Printf("run state written to %s", opts.out)
	return 0
}

// validateInput resolves the repo and collects every INVALID_INPUT problem in
// one pass, so the caller reports them together rather than failing on the first.
func validateInput(diff string, files []string, worktree, base string, noGit bool) (Repo, []string) {
	var problems []string
	if strings.TrimSpace(diff) == "" {
		problems = append(problems, "diff is empty — pass a file argument or pipe a diff to stdin")
	} else if len(files) == 0 {
		problems = append(problems, "diff parsed to zero changed files — is this a git diff?")
	}

	repo := Repo{Worktree: worktree, BaseRef: base}
	if !noGit {
		var gitProblems []string
		repo, gitProblems = resolveRepo(worktree, base)
		problems = append(problems, gitProblems...)
	}
	return repo, problems
}

func emit(repo Repo, cls *Classification, asJSON bool) {
	if !asJSON {
		printReport(repo, cls)
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cls); err != nil {
		log.Fatalf("encode: %v", err)
	}
}

// ─── config loading ──────────────────────────────────────────────────────────

func defaultConfigPath() string {
	exe, err := os.Executable()
	if err == nil {
		// cmd/classify/classify → ../../config/risk-paths.json
		if p := path.Join(path.Dir(exe), "..", "..", "config", "risk-paths.json"); fileExists(p) {
			return p
		}
	}
	if home := os.Getenv("HOME"); home != "" {
		if p := path.Join(home, "Project/claude-workflow/config/risk-paths.json"); fileExists(p) {
			return p
		}
	}
	return "config/risk-paths.json"
}

func fileExists(p string) bool {
	// #nosec G703 -- p is an operator-supplied or repo-derived config path; reading
	// caller-named config files is this tool's contract, and it reads nothing else.
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func loadConfig(p string) (*Config, error) {
	// #nosec G304 -- p is the -config flag: naming the rule table is the point.
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return parseConfig(data)
}

// parseConfig validates every field the rest of the tool assumes. A bad rule is
// a hard error, never a warning: a silently dropped wallet rule reviews money
// code at the generic floor, which is the failure this node exists to prevent.
func parseConfig(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if cfg.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf("schema_version %d unsupported (want %d)", cfg.SchemaVersion, schemaVersion)
	}
	if _, ok := riskRank[cfg.UnmatchedRisk]; !ok {
		return nil, fmt.Errorf("unmatched_risk %q invalid (want low|medium|high|critical)", cfg.UnmatchedRisk)
	}
	if len(cfg.Rules) == 0 {
		return nil, fmt.Errorf("no rules defined")
	}

	seen := map[string]bool{}
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		if r.ID == "" {
			return nil, fmt.Errorf("rule %d has no id", i)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true
		if _, ok := riskRank[r.Risk]; !ok {
			return nil, fmt.Errorf("rule %q: risk %q invalid", r.ID, r.Risk)
		}
		if len(r.Paths) == 0 {
			return nil, fmt.Errorf("rule %q: no paths", r.ID)
		}
		for _, c := range r.Components {
			if !knownComponents[c] {
				return nil, fmt.Errorf("rule %q: unknown component %q (cmd/reviewer would reject it)", r.ID, c)
			}
		}
		// A presentation rule that also carries money/component weight is a
		// contradiction: it would make a money change reduced-panel eligible.
		if r.Presentation && (r.Financial || len(r.Components) > 0) {
			return nil, fmt.Errorf("rule %q: presentation cannot be financial or carry components", r.ID)
		}
	}

	for i := range cfg.GateSignals {
		g := &cfg.GateSignals[i]
		if g.ID == "" {
			return nil, fmt.Errorf("gate_signal %d has no id", i)
		}
		re, err := regexp.Compile(g.Pattern)
		if err != nil {
			return nil, fmt.Errorf("gate_signal %q: bad pattern: %w", g.ID, err)
		}
		g.re = re
	}
	return &cfg, nil
}

// ─── diff handling ───────────────────────────────────────────────────────────

func readDiff(args []string) (string, error) {
	if len(args) > 0 {
		data, err := os.ReadFile(args[0])
		if err != nil {
			return "", fmt.Errorf("read diff %s: %w", args[0], err)
		}
		return string(data), nil
	}
	st, err := os.Stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("stat stdin: %w", err)
	}
	if st.Mode()&os.ModeCharDevice != 0 {
		return "", fmt.Errorf("no diff: pass a file argument or pipe `git diff BASE...HEAD`")
	}
	data, err := readAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return string(data), nil
}

func readAll(f *os.File) ([]byte, error) {
	var out []byte
	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return out, nil
			}
			if n == 0 {
				return out, nil
			}
			return out, nil
		}
	}
}

// parseDiffFiles returns the path of every file in the diff.
//
// Paths can contain spaces — evenplay-mono really has
// "apps/skillstrike-mobile/src/components/ SupportGuideHeader.tsx" — so the
// `diff --git a/X b/X` header cannot be split on whitespace. The `+++ b/X` and
// `--- a/X` lines carry exactly one path each and are used as the authoritative
// source; the header is the fallback for binary files, which have neither.
//
// This matters beyond tidiness: a mis-split path matches no rule, so it lands in
// unmatched_files and takes the fail-closed tier. Safe, but it misreports which
// file caused it and can't pick up the rule that should have applied.
func parseDiffFiles(diff string) []string {
	var out []string
	seen := map[string]bool{}

	add := func(p string) bool {
		if p == "" || p == "/dev/null" || seen[p] {
			return p != "" && p != "/dev/null"
		}
		seen[p] = true
		out = append(out, p)
		return true
	}

	header, minus := "", ""
	resolved := true

	flush := func() {
		if resolved {
			return
		}
		// Neither +++ nor --- yielded a path (binary file, or mode-only change).
		if minus != "" {
			add(minus)
		} else {
			add(header)
		}
	}

	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			header, minus, resolved = headerPath(line[len("diff --git "):]), "", false

		case strings.HasPrefix(line, "--- "):
			if p := trimDiffPath(strings.TrimRight(line[4:], " \t")); p != "/dev/null" {
				minus = p
			}

		case strings.HasPrefix(line, "+++ "):
			p := trimDiffPath(strings.TrimRight(line[4:], " \t"))
			if p == "/dev/null" {
				// Deletion: the a-side names the file.
				if add(minus) {
					resolved = true
				}
				continue
			}
			if add(p) {
				resolved = true
			}
		}
	}
	flush()
	return out
}

// headerPath extracts the path from the `a/X b/X` remainder of a `diff --git`
// line. For a non-rename both sides are identical, so the split point is
// determined by length rather than by whitespace — which is what makes it
// space-safe. Renames (a/old b/new) fall back to the last field; the following
// +++ line supersedes it anyway.
func headerPath(rest string) string {
	rest = strings.TrimRight(rest, " \t")
	if n := (len(rest) - 5) / 2; n > 0 && len(rest) >= 5 {
		a, b := rest[:2+n], rest[2+n+1:]
		if strings.HasPrefix(a, "a/") && strings.HasPrefix(b, "b/") && a[2:] == b[2:] {
			return a[2:]
		}
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	return trimDiffPath(fields[len(fields)-1])
}

func trimDiffPath(p string) string {
	p = strings.Trim(p, `"`)
	for _, prefix := range []string{"a/", "b/"} {
		if strings.HasPrefix(p, prefix) {
			return p[len(prefix):]
		}
	}
	return p
}

// ─── glob matching ───────────────────────────────────────────────────────────

// matchGlob matches repo-relative paths with `**` meaning zero or more path
// segments. Within a segment, path.Match semantics apply (`*`, `?`, `[...]`).
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

// ─── classification ──────────────────────────────────────────────────────────

// fileVerdict is one file's contribution to the classification, kept separate so
// the aggregation in classify() stays a fold rather than a nest of branches.
type fileVerdict struct {
	Class        FileClass
	Matched      bool
	Presentation bool
	Components   []string
	Financial    bool
	Migration    bool
}

// classifyOne applies every rule to one file. Risk is the max over matches,
// components the union, financial/migration an OR, and presentation holds only
// if every matched rule is presentational.
func classifyOne(cfg *Config, file string) fileVerdict {
	v := fileVerdict{
		Class:        FileClass{Path: file, Risk: "low"},
		Presentation: true,
	}
	for _, r := range cfg.Rules {
		if !ruleMatches(r, file) {
			continue
		}
		v.Matched = true
		v.Class.Rules = append(v.Class.Rules, r.ID)
		v.Class.Risk = maxRisk(v.Class.Risk, r.Risk)
		v.Components = append(v.Components, r.Components...)
		v.Financial = v.Financial || r.Financial
		v.Migration = v.Migration || r.Migration
		v.Presentation = v.Presentation && r.Presentation
	}
	if !v.Matched {
		// Fail closed: an unclassified path takes the configured tier and is
		// never treated as presentational.
		v.Class.Risk = cfg.UnmatchedRisk
		v.Presentation = false
	}
	return v
}

func reasonFor(v fileVerdict) string {
	if !v.Matched {
		return fmt.Sprintf("%s → %s (UNMATCHED — no rule covers this path)", v.Class.Path, v.Class.Risk)
	}
	return fmt.Sprintf("%s → %s via %s", v.Class.Path, v.Class.Risk, strings.Join(v.Class.Rules, "+"))
}

func classify(cfg *Config, files []string, diff string) *Classification {
	cls := &Classification{Risk: "low"}

	componentSet := map[string]bool{}
	reasons := map[string]string{} // risk tier -> first reason at that tier
	presentationalCount := 0

	for _, f := range files {
		v := classifyOne(cfg, f)

		for _, c := range v.Components {
			componentSet[c] = true
		}
		cls.FinancialPathsTouched = cls.FinancialPathsTouched || v.Financial
		cls.Migration = cls.Migration || v.Migration
		cls.ServerSurface = cls.ServerSurface || isServerSurface(cfg, f)
		cls.Risk = maxRisk(cls.Risk, v.Class.Risk)

		if !v.Matched {
			cls.UnmatchedFiles = append(cls.UnmatchedFiles, f)
		}
		if v.Presentation {
			presentationalCount++
		}
		if _, have := reasons[v.Class.Risk]; !have {
			reasons[v.Class.Risk] = reasonFor(v)
		}
		cls.ChangedFiles = append(cls.ChangedFiles, v.Class)
	}

	cls.ClientOnly = len(files) > 0 && presentationalCount == len(files)
	cls.Components = sortedKeys(componentSet)
	cls.GateSignals = scanGateSignals(cfg, diff)

	// Reasons, highest tier first — the derivation a human audits.
	for _, tier := range []string{"critical", "high", "medium", "low"} {
		if r, ok := reasons[tier]; ok {
			cls.RiskReasons = append(cls.RiskReasons, r)
		}
	}

	cls.Panel = decidePanel(cls)
	cls.HumanPRGate = cls.Risk == "critical" || cls.FinancialPathsTouched
	cls.RecheckMinSeverity = "high"
	if len(cls.Components) > 0 {
		// Critical systems converge to zero MEDIUM-or-higher (tasker.md:233).
		cls.RecheckMinSeverity = "medium"
	}
	cls.Skills = decideSkills(cls)
	return cls
}

func ruleMatches(r Rule, file string) bool {
	for _, p := range r.Paths {
		if matchGlob(p, file) {
			return true
		}
	}
	return false
}

func isServerSurface(cfg *Config, file string) bool {
	for _, ext := range cfg.ServerSurfaceExtensions {
		if strings.HasSuffix(file, ext) {
			return true
		}
	}
	return false
}

func maxRisk(a, b string) string {
	if riskRank[b] > riskRank[a] {
		return b
	}
	return a
}

// scanGateSignals looks at changed LINES, not paths. A gate/guard/flag decides
// whether something is allowed or shown, which carries a fail-open correctness
// dimension no path glob can see (PR 1298 / SMG-3880).
func scanGateSignals(cfg *Config, diff string) []GateHit {
	var hits []GateHit
	seen := map[string]bool{}
	current := ""

	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				current = trimDiffPath(fields[len(fields)-1])
			}
			continue
		}
		if !isChangedLine(line) {
			continue
		}
		body := line[1:]
		for _, g := range cfg.GateSignals {
			if g.re == nil || !g.re.MatchString(body) {
				continue
			}
			key := g.ID + "\x00" + current
			if seen[key] || len(hits) >= maxGateSamples {
				continue
			}
			seen[key] = true
			hits = append(hits, GateHit{Signal: g.ID, File: current, Sample: truncate(strings.TrimSpace(body), maxSampleLen)})
		}
	}
	return hits
}

func isChangedLine(line string) bool {
	if len(line) == 0 {
		return false
	}
	if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
		return false
	}
	return line[0] == '+' || line[0] == '-'
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// decidePanel encodes tasker.md's mandatory-panel rule. required is always
// true: no PR is ever raised with zero AI review. The only question is 5 seats
// or 1, and the carve-out is narrow by construction.
func decidePanel(cls *Classification) Panel {
	p := Panel{Required: true, Seats: 5}

	var blockers []string
	if !cls.ClientOnly {
		blockers = append(blockers, "not client-only presentation")
	}
	if cls.ServerSurface {
		blockers = append(blockers, "server/wire surface touched (.go/.java/.py/.sql/.proto/.tf)")
	}
	if cls.FinancialPathsTouched {
		blockers = append(blockers, "financial path touched")
	}
	if len(cls.Components) > 0 {
		blockers = append(blockers, "component preset applies: "+strings.Join(cls.Components, ","))
	}
	if len(cls.GateSignals) > 0 {
		blockers = append(blockers, fmt.Sprintf("gate/guard/flag signal in changed lines (%s) — decides whether something is allowed, not presentation", signalNames(cls.GateSignals)))
	}
	if riskRank[cls.Risk] > riskRank["low"] {
		blockers = append(blockers, "risk tier "+cls.Risk+" (>= medium always gets the panel)")
	}
	if len(cls.UnmatchedFiles) > 0 {
		blockers = append(blockers, fmt.Sprintf("%d unclassified path(s) — fail closed", len(cls.UnmatchedFiles)))
	}

	if len(blockers) == 0 {
		p.Seats = 1
		p.Reduced = true
		p.Reasons = []string{"client-only presentation, no server/money/auth surface, no gate signals — reduced single-reviewer panel"}
		return p
	}
	p.Reasons = blockers
	return p
}

func signalNames(hits []GateHit) string {
	set := map[string]bool{}
	for _, h := range hits {
		set[h.Signal] = true
	}
	return strings.Join(sortedKeys(set), ",")
}

// decideSkills resolves the path-derived rows of the Tasker's skill-routing
// table. Task-metadata rows (type: Fix, plan files) stay with the driver.
func decideSkills(cls *Classification) []string {
	var out []string
	if cls.Risk == "critical" || cls.Risk == "high" {
		out = append(out, "critical-review-dispatch")
	}
	if cls.Migration {
		out = append(out, "migration-checklist")
	}
	if cls.HumanPRGate {
		out = append(out, "pr-raise")
	}
	return out
}

func reviewerArgs(repo Repo, cls *Classification) []string {
	args := []string{"-cwd", repo.Worktree, "-base", repo.BaseRef, "-risk", cls.Risk}
	if len(cls.Components) > 0 {
		args = append(args, "-component", strings.Join(cls.Components, ","))
	}
	if cls.Panel.Reduced {
		args = append(args, "-reviewers", "claude")
	}
	return args
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ─── git resolution ──────────────────────────────────────────────────────────

// resolveRepo resolves the base exactly once for the whole run, and refuses a
// local branch whose origin/ counterpart has moved. Three false-divergence
// alarms came from diffing a stale local main; making it an INVALID_INPUT here
// removes the class rather than documenting it.
func resolveRepo(worktree, base string) (Repo, []string) {
	repo := Repo{Worktree: worktree, BaseRef: base}
	var problems []string

	if _, err := gitOut(worktree, "rev-parse", "--git-dir"); err != nil {
		return repo, []string{fmt.Sprintf("%s is not a git worktree: %v", worktree, err)}
	}

	if !strings.Contains(base, "/") {
		remote := "origin/" + base
		localSHA, lErr := gitOut(worktree, "rev-parse", base)
		remoteSHA, rErr := gitOut(worktree, "rev-parse", remote)
		if lErr == nil && rErr == nil && localSHA != remoteSHA {
			problems = append(problems, fmt.Sprintf(
				"-base %q is a local branch at %s but %s is at %s — diff against the remote. Re-run with -base %s.",
				base, short(localSHA), remote, short(remoteSHA), remote))
		}
	}

	sha, err := gitOut(worktree, "rev-parse", base)
	if err != nil {
		problems = append(problems, fmt.Sprintf("cannot resolve -base %q: %v (fetch first?)", base, err))
	}
	repo.BaseSHA = sha

	if head, err := gitOut(worktree, "rev-parse", "HEAD"); err == nil {
		repo.HeadSHA = head
	}
	if br, err := gitOut(worktree, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		repo.Branch = br
	}
	if st, err := gitOut(worktree, "status", "--porcelain"); err == nil {
		repo.Dirty = strings.TrimSpace(st) != ""
	}
	return repo, problems
}

func gitOut(dir string, args ...string) (string, error) {
	// #nosec G204 -- fixed binary "git"; every args value is a literal from this
	// file or a ref name already validated by rev-parse. No shell is involved.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// ─── output ──────────────────────────────────────────────────────────────────

// writeRunState merges into an existing run state, preserving the slices other
// nodes own. classify owns repo and classification and nothing else.
func writeRunState(p, taskKey string, repo Repo, cls *Classification) error {
	state := RunState{SchemaVersion: schemaVersion}
	// #nosec G304 -- p is the -out run-state path chosen by the caller.
	if data, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("existing run state is not valid JSON: %w", err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if state.CreatedAt == "" {
		state.CreatedAt = now
	}
	state.SchemaVersion = schemaVersion
	state.UpdatedAt = now
	if taskKey != "" {
		state.TaskKey = taskKey
	}
	state.Repo = repo
	state.Classification = cls
	if state.Status == "" {
		state.Status = "in_progress"
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	// #nosec G306 -- the run state is a shared build artifact other nodes and the
	// human read; it holds paths and verdicts, never credentials. 0644 is intended.
	return os.WriteFile(p, append(data, '\n'), 0644)
}

func printInvalidInput(repo Repo, problems []string) {
	fmt.Println("=== CLASSIFY: INVALID_INPUT ===")
	fmt.Printf("Worktree: %s\n", repo.Worktree)
	fmt.Printf("Base:     %s\n\n", repo.BaseRef)
	for _, p := range problems {
		fmt.Printf("  ✗ %s\n", p)
	}
	fmt.Println("\nFix the input and re-run. Do not proceed to review with an unclassified diff.")
}

func printReport(repo Repo, cls *Classification) {
	fmt.Println("=== CLASSIFICATION ===")
	fmt.Printf("Worktree:   %s\n", repo.Worktree)
	fmt.Printf("Base:       %s (%s)\n", repo.BaseRef, short(repo.BaseSHA))
	if repo.HeadSHA != "" {
		fmt.Printf("Head:       %s%s\n", short(repo.HeadSHA), dirtyTag(repo.Dirty))
	}
	fmt.Printf("Files:      %d\n\n", len(cls.ChangedFiles))

	fmt.Printf("Risk:       %s\n", strings.ToUpper(cls.Risk))
	for _, r := range cls.RiskReasons {
		fmt.Printf("            · %s\n", r)
	}
	fmt.Printf("Components: %s\n", emptyDash(strings.Join(cls.Components, ",")))
	fmt.Printf("Financial:  %v    Migration: %v    Server surface: %v    Client-only: %v\n",
		cls.FinancialPathsTouched, cls.Migration, cls.ServerSurface, cls.ClientOnly)

	fmt.Printf("\nPanel:      %d seat(s)%s\n", cls.Panel.Seats, reducedTag(cls.Panel.Reduced))
	for _, r := range cls.Panel.Reasons {
		fmt.Printf("            · %s\n", r)
	}

	if len(cls.GateSignals) > 0 {
		fmt.Printf("\nGate signals (%d) — full panel regardless of surface:\n", len(cls.GateSignals))
		for _, h := range cls.GateSignals {
			fmt.Printf("  [%s] %s\n      %s\n", h.Signal, h.File, h.Sample)
		}
	}

	if len(cls.UnmatchedFiles) > 0 {
		fmt.Printf("\n⚠ UNCLASSIFIED PATHS (%d) — took fail-closed risk; add rules to risk-paths.json:\n", len(cls.UnmatchedFiles))
		for _, f := range cls.UnmatchedFiles {
			fmt.Printf("  %s\n", f)
		}
	}

	fmt.Printf("\nHuman PR gate:        %v\n", cls.HumanPRGate)
	fmt.Printf("recheck -min-severity: %s\n", cls.RecheckMinSeverity)
	fmt.Printf("Skills to load:        %s\n", emptyDash(strings.Join(cls.Skills, ", ")))
	fmt.Printf("\nreviewer argv:\n  %s\n", strings.Join(cls.ReviewerArgs, " "))
	fmt.Println("\n=== END CLASSIFICATION ===")
}

func dirtyTag(dirty bool) string {
	if dirty {
		return " (DIRTY)"
	}
	return ""
}

func reducedTag(reduced bool) string {
	if reduced {
		return " — REDUCED carve-out"
	}
	return ""
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
