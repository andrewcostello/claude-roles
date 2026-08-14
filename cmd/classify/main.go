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
	"path/filepath"
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
	SchemaVersion int `json:"schema_version"`
	// Scaffold marks a config produced by `classify init` that a human has not
	// yet completed. While set, classify refuses to certify a diff as
	// money-free: it forces the human PR gate and the full panel. A rule table
	// nobody has reviewed cannot prove anything is safe, and the alternative —
	// silently reporting financial_paths_touched=false — is how a generated
	// config becomes a footgun. Set it to false once the money paths are named.
	Scaffold                bool         `json:"scaffold,omitempty"`
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
	// PanelFloor pins a minimum panel preset for any diff touching this
	// rule's paths, independent of risk tier. Used for surfaces whose risk
	// tier alone under-panels them — e.g. client money UI at medium risk
	// still gets the full panel. Value must be a preset name.
	PanelFloor string `json:"panel_floor,omitempty"`
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
	Size                  Size        `json:"size"`
	RulePanelFloor        string      `json:"rule_panel_floor,omitempty"`
	RulePanelFloorRule    string      `json:"rule_panel_floor_rule,omitempty"`
	Panel                 Panel       `json:"panel"`
	FlowDiagram           bool        `json:"flow_diagram"`
	HumanPRGate           bool        `json:"human_pr_gate"`
	RecheckMinSeverity    string      `json:"recheck_min_severity"`
	Skills                []string    `json:"skills,omitempty"`
	ReviewerArgs          []string    `json:"reviewer_args,omitempty"`
	ChangedFiles          []FileClass `json:"changed_files,omitempty"`
	UnmatchedFiles        []string    `json:"unmatched_files,omitempty"`
	ConfigScaffold        bool        `json:"config_scaffold,omitempty"`
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
	// Preset is the panel level this run uses: solo | standard | full | deep.
	// Recommended is what the rules computed; Floor is the minimum the hard
	// blockers allow. Preset == Recommended unless -panel overrode it, in
	// which case Overridden is true. An override below Floor is rejected at
	// parse time, so Preset >= Floor always holds.
	Preset      string   `json:"preset"`
	Recommended string   `json:"recommended"`
	Floor       string   `json:"floor"`
	Overridden  bool     `json:"overridden,omitempty"`
	Reviewers   []string `json:"reviewers"`
}

// Size is the diff-size evidence the panel preset is derived from. Only
// ProductionLines drives decisions; test and generated volume is reported so
// a human can audit the split.
type Size struct {
	ProductionLines int `json:"production_lines"`
	ProductionFiles int `json:"production_files"`
	TestLines       int `json:"test_lines"`
	GeneratedLines  int `json:"generated_lines"`
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
	panel      string
	args       []string
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			os.Exit(cmdInit(os.Args[2:]))
		case "help", "-h", "--help":
			usage()
			os.Exit(0)
		}
	}
	os.Exit(run(parseFlags()))
}

func usage() {
	fmt.Fprint(os.Stderr, `classify — turn a diff into a risk classification

  classify [flags] [diff-file]   classify a diff (reads stdin when no file given)
  classify init [-worktree D]    scaffold this project's .claude/risk-paths.json

The rule table is PROJECT-SPECIFIC and lives in the project, not in this repo:
it names one repository's money, auth and client paths. Applied to a different
repository it would classify confidently and wrongly, so there is no default.

Exit codes: 0 classified, 3 INVALID_INPUT
`)
}

func parseFlags() options {
	configFlag := flag.String("config", "", "Path to risk-paths.json (default: <this binary's repo>/config/risk-paths.json)")
	worktreeFlag := flag.String("worktree", ".", "Worktree holding the change")
	baseFlag := flag.String("base", "origin/main", "Base ref to diff against. A local branch name whose origin/ counterpart differs is rejected — the remote is the truth.")
	taskFlag := flag.String("task", "", "Task key, e.g. SMG-3966")
	outFlag := flag.String("out", "", "Run-state JSON to create or update. Preserves gates/rounds/pr written by other nodes.")
	jsonFlag := flag.Bool("json", false, "Print the classification JSON to stdout instead of the report")
	noGitFlag := flag.Bool("no-git", false, "Skip base/head resolution. For classifying a bare diff with no worktree.")
	panelFlag := flag.String("panel", "", "Override the panel preset (solo|standard|full|deep). An override below the computed floor is rejected — money/critical/gate-signal floors cannot be waived from the command line.")
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
		panel:      *panelFlag,
		args:       flag.Args(),
	}
}

func run(opts options) int {
	cfgPath, ok := resolveConfigPath(opts)
	if !ok {
		printInvalidInput(Repo{Worktree: opts.worktree}, missingConfigMessage(opts.worktree))
		return exitInvalid
	}

	cfg, diff, files, err := loadInputs(opts, cfgPath)
	if err != nil {
		log.Fatalf("%v", err)
	}

	repo, problems := validateInput(diff, files, opts.worktree, opts.base, opts.noGit)
	if opts.panel != "" && !validPreset(opts.panel) {
		problems = append(problems, fmt.Sprintf("-panel %q is not a preset (want solo|standard|full|deep)", opts.panel))
	}
	if len(problems) > 0 {
		printInvalidInput(repo, problems)
		return exitInvalid
	}

	cls := buildClassification(cfg, files, diff, repo, cfgPath, opts.panel)
	if opts.panel != "" && !cls.Panel.Overridden {
		// The override named a preset below the floor. Refuse loudly rather
		// than silently upgrading: the caller's mental model is wrong and the
		// report says why.
		msg := []string{fmt.Sprintf("-panel %s is below the floor %s for this diff. Floor reasons:", opts.panel, cls.Panel.Floor)}
		for _, r := range cls.Panel.Reasons {
			msg = append(msg, "      "+r)
		}
		printInvalidInput(repo, msg)
		return exitInvalid
	}
	emit(repo, cls, opts.json)
	return persist(opts, repo, cls)
}

// resolveConfigPath honours an explicit -config, else searches this project.
func resolveConfigPath(opts options) (string, bool) {
	if opts.configPath != "" {
		return opts.configPath, true
	}
	return findConfig(opts.worktree)
}

func loadInputs(opts options, cfgPath string) (*Config, string, []string, error) {
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

func buildClassification(cfg *Config, files []string, diff string, repo Repo, cfgPath string, panelOverride string) *Classification {
	cls := classify(cfg, files, diff, panelOverride)
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

// configCandidates lists where a project's rule table may live, in order.
//
// There is deliberately NO fallback to another checkout's config. The rule
// table names one repository's money, auth and client paths; applied to a
// different repository it produces confidently wrong answers — a diff certified
// as touching no financial path because the paths it names do not exist here.
// A missing config is INVALID_INPUT, not a default.
func configCandidates(worktree string) []string {
	var out []string
	if env := os.Getenv("RISK_PATHS_CONFIG"); env != "" {
		out = append(out, env)
	}
	for _, dir := range agentConfigDirs {
		if worktree != "" {
			out = append(out, filepath.Join(worktree, dir, "risk-paths.json"))
		}
	}
	out = append(out, filepath.Join(agentConfigDirs[0], "risk-paths.json"))
	return dedupePaths(out)
}

// agentConfigDirs is the search order for agent-tooling config, preferring the
// vendor-neutral directory.
//
// These tables are consumed by Go binaries, not by Claude: risk-paths.json says
// which paths are money and gates.json says which checks run. Naming them after
// one assistant implies they only matter to that assistant, and the next tool to
// need them would reasonably put its own copy somewhere else. `.claude/` stays
// supported because projects already have one.
var agentConfigDirs = []string{".agent", ".claude"}

func dedupePaths(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range in {
		abs, err := filepath.Abs(c)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, c)
	}
	return out
}

func findConfig(worktree string) (string, bool) {
	for _, c := range configCandidates(worktree) {
		if fileExists(c) {
			return c, true
		}
	}
	return "", false
}

// missingConfigMessage is what a caller sees instead of a wrong answer.
func missingConfigMessage(worktree string) []string {
	msg := []string{
		"no risk-paths config found — classification needs a rule table for THIS project",
		"",
		"  Looked in:",
	}
	for _, c := range configCandidates(worktree) {
		msg = append(msg, "    "+c)
	}
	return append(msg,
		"",
		"  The rule table is project-specific: it names this repository's money, auth,",
		"  migration and client paths. Another project's table would classify this diff",
		"  confidently and wrongly, so there is no default.",
		"",
		"  Create one:",
		"    classify init -worktree "+emptyDot(worktree),
		"",
		"  That scaffolds .claude/risk-paths.json from what is actually in the repo. It",
		"  is marked scaffold:true until a human names the money paths, and while marked",
		"  it forces the human PR gate and the full panel on every change.")
}

func emptyDot(s string) string {
	if s == "" {
		return "."
	}
	return s
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
		if r.PanelFloor != "" && !validPreset(r.PanelFloor) {
			return nil, fmt.Errorf("rule %q: panel_floor %q is not a preset (want solo|standard|full|deep)", r.ID, r.PanelFloor)
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
	PanelFloor   string
	FloorRule    string
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
		if presetRank[r.PanelFloor] > presetRank[v.PanelFloor] {
			v.PanelFloor = r.PanelFloor
			v.FloorRule = r.ID
		}
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

// parseDiffStats counts added + deleted lines per file. Keyed by the same
// path form parseDiffFiles emits, so the two can be joined. Hunk context and
// the +++/--- headers do not count.
func parseDiffStats(diff string) map[string]int {
	stats := map[string]int{}
	current := ""
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			current = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "--- a/") && current == "":
			current = strings.TrimPrefix(line, "--- a/")
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			// /dev/null or a non-git header — keep the current file.
		case strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-"):
			if current != "" {
				stats[current]++
			}
		case strings.HasPrefix(line, "diff --git "):
			current = ""
		}
	}
	return stats
}

// isTestPath and isGeneratedPath split the diff into the three size buckets.
// The lists are heuristics, not policy: a wrong bucket changes a size number,
// never a risk tier or a floor.
func isTestPath(p string) bool {
	base := p[strings.LastIndex(p, "/")+1:]
	for _, s := range []string{"_test.go", ".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx", ".spec.js", "_test.py"} {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	for _, d := range []string{"/__tests__/", "/testdata/", "/tests/e2e/", "/test/"} {
		if strings.Contains(p, d) {
			return true
		}
	}
	return strings.HasPrefix(base, "test_")
}

func isGeneratedPath(p string) bool {
	base := p[strings.LastIndex(p, "/")+1:]
	for _, s := range []string{".pb.go", "_pb.ts", ".pb.gw.go", ".connect.go", "_grpc.pb.go", ".sql.go", ".swagger.json", ".gen.go", ".generated.ts", "go.sum", "package-lock.json", "yarn.lock"} {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	return strings.Contains(p, "/generated/") || strings.Contains(p, "/sqlc/")
}

func measureSize(files []string, diff string) Size {
	stats := parseDiffStats(diff)
	var s Size
	for _, f := range files {
		lines := stats[f]
		switch {
		case isGeneratedPath(f):
			s.GeneratedLines += lines
		case isTestPath(f):
			s.TestLines += lines
		default:
			s.ProductionLines += lines
			s.ProductionFiles++
		}
	}
	return s
}

func classify(cfg *Config, files []string, diff string, panelOverride string) *Classification {
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
		if presetRank[v.PanelFloor] > presetRank[cls.RulePanelFloor] {
			cls.RulePanelFloor = v.PanelFloor
			cls.RulePanelFloorRule = v.FloorRule
		}

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

	// A scaffold config has never had its money paths named by a human, so a
	// "financial_paths_touched: false" from it is an absence of evidence, not
	// evidence of absence. Refuse to certify: force the gate and the full panel
	// until someone completes the table and clears the flag.
	cls.ConfigScaffold = cfg.Scaffold

	cls.Size = measureSize(files, diff)
	cls.Panel = decidePanel(cls, panelOverride)
	// A flow/state diagram earns its place when the change alters behaviour a
	// reader has to hold as a machine — gates, migrations, component
	// lifecycles, High+ risk — and is big enough that prose alone gets long.
	cls.FlowDiagram = cls.Size.ProductionLines >= diagramMinProductionLines &&
		(len(cls.GateSignals) > 0 || cls.Migration || riskRank[cls.Risk] >= riskRank["high"] || len(cls.Components) > 0)
	cls.HumanPRGate = cls.Risk == "critical" || cls.FinancialPathsTouched
	if cfg.Scaffold {
		cls.HumanPRGate = true
	}
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

// Panel presets, smallest to largest. The preset names the seat list; the
// consensus floor in cmd/reviewer scales with whatever list it receives, so
// no floor number is recited here.
const (
	presetSolo     = "solo"
	presetStandard = "standard"
	presetFull     = "full"
	presetDeep     = "deep"
)

var presetRank = map[string]int{presetSolo: 0, presetStandard: 1, presetFull: 2, presetDeep: 3}

var presetReviewers = map[string][]string{
	presetSolo:     {"claude"},
	presetStandard: {"claude", "codex", "agy"},
	presetFull:     {"claude", "claude-scouts", "codex", "grok", "agy"},
	presetDeep:     {"claude", "claude-scouts", "grok-scouts", "codex", "grok", "agy"},
}

func validPreset(p string) bool { _, ok := presetRank[p]; return ok }

// Size thresholds for preset selection, in production lines changed
// (adds + deletes, tests and generated files excluded). Small enough that a
// single-function fix stays solo-eligible; large enough that a real feature
// always clears them.
const (
	soloMaxProductionLines     = 80
	standardMaxProductionLines = 400
	diagramMinProductionLines  = 150
)

// decidePanel keeps tasker.md's mandatory-panel rule — Required is always
// true, no PR ships with zero AI review — but grades the panel by risk and
// size instead of the old 5-or-1 binary.
//
// Two layers, deliberately separate:
//
//   - floor: the minimum the HARD blockers allow. Money, Critical risk, a
//     scaffold config and unclassified paths force deep; gate signals,
//     migrations, High risk and component presets force full. The floor can
//     never be overridden from the command line.
//   - recommendation: the floor, upgraded by size when the diff is large for
//     its tier, or relaxed within the safe band (Low/Medium, no blockers)
//     when the diff is small.
//
// An override (the -panel flag) is honoured only at or above the floor.
// Overridden=false on a rejected override; run() turns that into
// INVALID_INPUT rather than silently upgrading.
func decidePanel(cls *Classification, override string) Panel {
	floor, floorReasons := panelFloor(cls)
	recommended, sizeReasons := recommendPreset(cls, floor)
	reasons := append(floorReasons, sizeReasons...)

	preset := recommended
	overridden := false
	if override != "" {
		if presetRank[override] >= presetRank[floor] {
			preset = override
			overridden = true
			if override != recommended {
				reasons = append(reasons, fmt.Sprintf("-panel %s override (recommended %s, floor %s)", override, recommended, floor))
			}
		}
		// Below-floor override: leave preset at recommended and Overridden
		// false; run() rejects the invocation.
	}

	reviewers := presetReviewers[preset]
	return Panel{
		Required:    true,
		Seats:       len(reviewers),
		Reduced:     preset == presetSolo,
		Reasons:     reasons,
		Preset:      preset,
		Recommended: recommended,
		Floor:       floor,
		Overridden:  overridden,
		Reviewers:   reviewers,
	}
}

// panelFloor names the minimum preset the hard blockers allow, with one
// reason per blocker. An empty blocker set floors at solo.
func panelFloor(cls *Classification) (string, []string) {
	floor := presetSolo
	var reasons []string
	raise := func(to, why string) {
		if presetRank[to] > presetRank[floor] {
			floor = to
		}
		reasons = append(reasons, why)
	}

	if cls.FinancialPathsTouched {
		raise(presetDeep, "financial path touched — deep floor")
	}
	if cls.Risk == "critical" {
		raise(presetDeep, "risk tier critical — deep floor")
	}
	if cls.ConfigScaffold {
		raise(presetDeep, "risk-paths config is still a scaffold — its money paths have not been reviewed, so nothing can be certified safe")
	}
	if len(cls.UnmatchedFiles) > 0 {
		raise(presetDeep, fmt.Sprintf("%d unclassified path(s) — fail closed to deep", len(cls.UnmatchedFiles)))
	}
	if cls.Risk == "high" {
		raise(presetFull, "risk tier high — full floor")
	}
	if len(cls.GateSignals) > 0 {
		raise(presetFull, fmt.Sprintf("gate/guard/flag signal in changed lines (%s) — decides whether something is allowed, not presentation", signalNames(cls.GateSignals)))
	}
	if cls.Migration {
		raise(presetFull, "schema migration touched — full floor")
	}
	if len(cls.Components) > 0 {
		raise(presetFull, "component preset applies: "+strings.Join(cls.Components, ","))
	}
	if cls.RulePanelFloor != "" && presetRank[cls.RulePanelFloor] > presetRank[presetSolo] {
		raise(cls.RulePanelFloor, fmt.Sprintf("rule %q pins panel floor %s", cls.RulePanelFloorRule, cls.RulePanelFloor))
	}
	return floor, reasons
}

// recommendPreset upgrades or relaxes within the band the floor leaves open,
// using production diff size as the only extra signal.
func recommendPreset(cls *Classification, floor string) (string, []string) {
	prod := cls.Size.ProductionLines

	// The floor already answers everything at full or above; size only adds.
	if presetRank[floor] >= presetRank[presetFull] {
		if floor == presetFull && prod > standardMaxProductionLines {
			return presetDeep, []string{fmt.Sprintf("%d production lines at a full floor — upgraded to deep", prod)}
		}
		return floor, nil
	}

	// Below the full floor the risk is low or medium with no hard blockers.
	if cls.Risk == "medium" {
		if prod > standardMaxProductionLines {
			return presetFull, []string{fmt.Sprintf("medium risk, %d production lines (> %d) — full panel", prod, standardMaxProductionLines)}
		}
		return presetStandard, []string{fmt.Sprintf("medium risk, %d production lines — standard panel", prod)}
	}

	// Low risk. The old client-only carve-out keeps its solo seat; a small
	// server-side diff earns standard, a large one earns full.
	if cls.ClientOnly && !cls.ServerSurface {
		return presetSolo, []string{"client-only presentation, no server/money/auth surface, no gate signals — solo reviewer"}
	}
	if prod <= soloMaxProductionLines {
		return presetStandard, []string{fmt.Sprintf("low risk, %d production lines (<= %d) — standard panel", prod, soloMaxProductionLines)}
	}
	if prod > standardMaxProductionLines {
		return presetFull, []string{fmt.Sprintf("low risk but %d production lines (> %d) — full panel", prod, standardMaxProductionLines)}
	}
	return presetStandard, []string{fmt.Sprintf("low risk, %d production lines — standard panel", prod)}
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
	// The preset owns the seat list; emit it explicitly so nobody
	// hand-assembles -reviewers downstream.
	args = append(args, "-reviewers", strings.Join(cls.Panel.Reviewers, ","))
	if cls.FlowDiagram {
		args = append(args, "-flow-diagram")
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
		// Continuation and blank lines are prose belonging to the problem above;
		// marking each one as its own failure reads as a wall of errors.
		if p == "" || strings.HasPrefix(p, " ") {
			fmt.Println(p)
			continue
		}
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

	fmt.Printf("Size:       %d production lines in %d files (tests %d, generated %d — not counted)\n",
		cls.Size.ProductionLines, cls.Size.ProductionFiles, cls.Size.TestLines, cls.Size.GeneratedLines)

	overrideTag := ""
	if cls.Panel.Overridden {
		overrideTag = " (OVERRIDDEN by -panel)"
	}
	fmt.Printf("\nPanel:      %s — %d seat(s): %s%s\n", strings.ToUpper(cls.Panel.Preset), cls.Panel.Seats, strings.Join(cls.Panel.Reviewers, ","), overrideTag)
	fmt.Printf("            recommended %s · floor %s · override with -panel <preset> (never below the floor)\n", cls.Panel.Recommended, cls.Panel.Floor)
	for _, r := range cls.Panel.Reasons {
		fmt.Printf("            · %s\n", r)
	}
	if cls.FlowDiagram {
		fmt.Println("Diagram:    flow/state diagram requested from the broad reviewer seat")
	}

	if len(cls.GateSignals) > 0 {
		fmt.Printf("\nGate signals (%d) — full panel regardless of surface:\n", len(cls.GateSignals))
		for _, h := range cls.GateSignals {
			fmt.Printf("  [%s] %s\n      %s\n", h.Signal, h.File, h.Sample)
		}
	}

	if cls.ConfigScaffold {
		fmt.Printf("\n⚠ SCAFFOLD CONFIG (%s)\n", cls.ConfigPath)
		fmt.Println("  Its money/auth paths have not been reviewed by a human, so this run")
		fmt.Println("  forces the human PR gate and the full panel. Name the financial paths,")
		fmt.Println("  then set \"scaffold\": false to get proportionate classification.")
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

func reducedTag(reduced bool) string { // retained for run-state readers
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
