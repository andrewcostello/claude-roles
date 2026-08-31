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
	"errors"
	"flag"
	"fmt"
	"io"
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
	// contractVersion is the RAW flag value, validated in run() rather than
	// here. Parsing it in parseFlags would have to log.Fatalf, which exits 1;
	// a mistyped contract is INVALID_INPUT and owes the caller exit 3.
	contractVersion string
	args            []string
}

// main is the process entry point and OWNS THE ONLY os.Exit on the classify
// path. Everything below it returns a code.
//
// CONTRACT (GO-1-1 scaffold; see wiring.go for the whole of it). main cannot
// be driven by an in-process test: it reads os.Args and exits the process. It
// is therefore not sealed BEHAVIOURALLY but
// STRUCTURALLY — GO-1-3 makes the classify arm below a one-line delegation to
// RunWiring, and GO-1-2's row scans this package's source to assert it. If the
// delegation is ever replaced by a second, parallel spine, every row that calls
// RunWiring is vacuous by construction and the source scan is the only thing
// that would notice.
//
// The three pre-flag-parse arms are part of the mapping, not around it: the
// capabilities probe dispatches ahead of flag.Parse on purpose, and its exit
// code is RunWiring's answer too.
func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			os.Exit(cmdInit(os.Args[2:]))
		case probeSubcommand:
			// Dispatched here, ahead of flag parsing, so the probe cannot be
			// perturbed by any other argv and can answer at preflight — where
			// there is no config, no repo and no stdin to work with.
			os.Exit(cmdCapabilities(os.Args[2:]))
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
  classify capabilities          report what this binary can do, as JSON

The rule table is PROJECT-SPECIFIC and lives in the project, not in this repo:
it names one repository's money, auth and client paths. Applied to a different
repository it would classify confidently and wrongly, so there is no default.

Exit codes: 0 classified, 3 INVALID_INPUT, 4 CAPABILITY_INCOMPLETE (probe only)
`)
}

// parseFlags reads the process's argv into options.
//
// CONTRACT (GO-1-1 scaffold). This function is untestable AS WRITTEN and that
// is the whole of why -contract-version has no seal: it registers on the global
// flag.CommandLine, parses the global os.Args, and configures the process-wide
// logger as a side effect. A test can drive it neither twice nor with its own
// argv. GO-1-3 reduces it to a caller of parseInvocationFlags (wiring.go) over
// flag.CommandLine and os.Args[1:], keeping the log.SetFlags/log.SetPrefix pair
// HERE — process-wide logger configuration is main's, not a function a row
// calls a hundred times.
//
// Nothing today proves any binary accepts -contract-version. What becomes
// provable is narrower than that sentence sounds; wiring.go's Q2 states which
// half is owed and which half is not.
func parseFlags() options {
	configFlag := flag.String("config", "", "Path to risk-paths.json (default: <this binary's repo>/config/risk-paths.json)")
	worktreeFlag := flag.String("worktree", ".", "Worktree holding the change")
	baseFlag := flag.String("base", "origin/main", "Base ref to diff against. A local branch name whose origin/ counterpart differs is rejected — the remote is the truth.")
	taskFlag := flag.String("task", "", "Task key, e.g. SMG-3966")
	outFlag := flag.String("out", "", "Run-state JSON to create or update. Preserves gates/rounds/pr written by other nodes.")
	jsonFlag := flag.Bool("json", false, "Print the classification JSON to stdout instead of the report")
	noGitFlag := flag.Bool("no-git", false, "Skip base/head resolution. For classifying a bare diff with no worktree.")
	contractFlag := registerContractVersionFlag(flag.CommandLine)
	flag.Parse()

	log.SetFlags(0)
	log.SetPrefix("classify: ")

	return options{
		configPath:      *configFlag,
		worktree:        *worktreeFlag,
		base:            *baseFlag,
		task:            *taskFlag,
		out:             *outFlag,
		json:            *jsonFlag,
		noGit:           *noGitFlag,
		contractVersion: *contractFlag,
		args:            flag.Args(),
	}
}

// registerContractVersionFlag registers -contract-version through the
// capability registry rather than by calling flag.String here.
//
// That indirection is the point: it makes the capability observable BEFORE
// flag.Parse runs, which the probe subcommand requires because it dispatches
// ahead of flag parsing. It also means the probe's answer and the flag's
// existence are the same fact.
//
// A nil registrar is not a silent default. It would mean this binary has no
// -contract-version flag at all, so the only contract it could honour is the
// compiled-in default, and saying so here keeps that an explicit value rather
// than an empty string that ParseContractVersion would later reject with a
// message about the operator's argv — which would name the wrong culprit.
func registerContractVersionFlag(fs *flag.FlagSet) *string {
	if contractFlagRegistrar == nil {
		fallback := defaultContractVersion.String()
		return &fallback
	}
	return contractFlagRegistrar.RegisterContractVersionFlag(fs)
}

// run is the classify path: argv already parsed, in, exit code out.
//
// CONTRACT (GO-1-1 scaffold; wiring.go states it in full and GO-1-2 seals it).
// The mapping under review is (contract x -out x -json) -> artifact set + exit
// code, and no test decides it: every seal calls
// EmitV1/EmitV2/WriteV2Sidecar/ParseContractVersion as a LIBRARY, so none of
// them notices which one THIS function chooses. That is a property of how the
// seals are written, not a count of them — the count is in DECISIONS.md, where
// a measurement belongs.
//
// Two obligations this function's shape already carries, stated so GO-1-3 does
// not quietly drop either while making it callable in process:
//
//   - THE CONTRACT IS VALIDATED FIRST, before resolveConfigPath and before any
//     input is read. So -contract-version 3 against a worktree with no config
//     table exits 3 reporting the CONTRACT problem, not the config one, and
//     writes and removes NOTHING — a v2 sidecar beside -out survives
//     byte-identical. That ordering is the contract, not an accident of layout.
//   - THE EXIT CODE IS RETURNED, NEVER TAKEN. exitInvalid (3) is returned here;
//     the seven log.Fatalf calls reachable from this function and persist() exit
//     1, a code usage() does not advertise (wiring.go hole H2). GO-1-3 turns
//     those into returned codes; it does not silently renumber them.
func run(opts options) int {
	// The contract is a genesis decision recorded by the caller, resolved once
	// here and never inferred per-parse. It is validated before any work so a
	// mistyped -contract-version costs nothing and is reported as the operator's
	// argv problem it is.
	contract, err := ParseContractVersion(opts.contractVersion)
	if err != nil {
		printInvalidInput(Repo{Worktree: opts.worktree}, []string{err.Error()})
		return exitInvalid
	}

	cfgPath, outcome, cfgErr := resolveConfigPath(opts)
	if outcome != ConfigSearchResolved {
		reportConfigSearch(opts.worktree, outcome, cfgErr)
		return exitInvalid
	}

	cfg, diff, files, err := loadInputs(opts, cfgPath)
	if err != nil {
		log.Fatalf("%v", err)
	}

	repo, problems := validateInput(diff, files, opts.worktree, opts.base, opts.noGit)
	if len(problems) > 0 {
		printInvalidInput(repo, problems)
		return exitInvalid
	}

	cls := buildClassification(cfg, files, diff, repo, cfgPath)
	if err := emit(repo, cls, opts.json, contract); err != nil {
		log.Fatalf("%v", err)
	}
	return persist(opts, repo, cls, contract)
}

// resolveConfigPath honours an explicit -config, else searches this project
// THROUGH ResolveConfigDual.
//
// It used to call findConfig, which returned the first candidate that EXISTS
// and never compared the two. ResolveConfigDual — the function that implements
// the §3.3 dual-config rule, and that six green seals certify — was never
// reached from production at all. Six seals on a function nothing calls certify
// a rule that never runs; the live-resolution row exists to seal this call
// site, and it is the only thing that makes those six seals mean anything.
//
// An explicit -config still short-circuits the search. Its SCOPE note on
// ResolveConfigDual says why: the flag names exactly one file, so there is no
// second table to compare it against, and comparing is the whole of the dual
// rule.
//
// The outcome is returned as a named state rather than a bool. "Not resolved"
// is three different facts — nothing is there, two tables disagree, a table is
// unreadable — and they call for different messages and, in the differing case,
// a different diagnosis entirely. A bool collapses all three into the
// missing-config report, which would tell an operator to create a table they
// already have two of.
func resolveConfigPath(opts options) (string, ConfigSearchOutcome, error) {
	if opts.configPath != "" {
		return opts.configPath, ConfigSearchResolved, nil
	}
	path, err := ResolveConfigDual(opts.worktree)
	if err == nil {
		return path, ConfigSearchResolved, nil
	}
	var searchErr *ConfigSearchError
	if errors.As(err, &searchErr) {
		return "", searchErr.Outcome, err
	}
	// An error from the resolver that does not carry a named outcome is not a
	// case to absorb: it means the resolver grew a failure mode nobody named,
	// and guessing which one it is here is how a differing-table refusal turns
	// back into a missing-config message.
	return "", ConfigSearchUnset, err
}

// reportConfigSearch turns a non-resolved search outcome into the operator's
// INVALID_INPUT report. It is exhaustive over ConfigSearchOutcome and raises on
// anything outside the closed set — including Resolved, which is not this
// function's business and reaching it here means the caller's switch is wrong.
func reportConfigSearch(worktree string, outcome ConfigSearchOutcome, err error) {
	repo := Repo{Worktree: worktree}
	switch outcome {
	case ConfigSearchAbsent:
		// The existing missing-config report, unchanged: it lists where it
		// looked and how to scaffold a table.
		printInvalidInput(repo, missingConfigMessage(worktree))
	case ConfigSearchDiffering, ConfigSearchUnreadable:
		// The resolver's own message names the files, which is the entire
		// content of the diagnosis in both cases.
		printInvalidInput(repo, []string{err.Error()})
	case ConfigSearchUnset:
		printInvalidInput(repo, []string{fmt.Sprintf("the config search returned no outcome: %v — %q means nobody decided, and it is never a legal answer", err, ConfigSearchUnset)})
	case ConfigSearchResolved:
		printInvalidInput(repo, []string{fmt.Sprintf("internal: the config search resolved, but the failure path was taken anyway (%v)", err)})
	default:
		printInvalidInput(repo, []string{fmt.Sprintf("the config search returned outcome %q, which is outside the closed set (%v)", outcome, err)})
	}
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

func buildClassification(cfg *Config, files []string, diff string, repo Repo, cfgPath string) *Classification {
	cls := classify(cfg, files, diff)
	cls.ConfigPath = cfgPath
	cls.ClassifiedAt = time.Now().UTC().Format(time.RFC3339)
	cls.ReviewerArgs = reviewerArgs(repo, cls)
	return cls
}

func persist(opts options, repo Repo, cls *Classification, contract ContractVersion) int {
	if opts.out == "" {
		return 0
	}
	// The shared run-state stays v1 under BOTH contracts. cmd/gates and
	// cmd/iterate read it and are frozen; writing a v2 payload there would
	// strand gates' changed_files dependency and iterate's severity floor. The
	// v2 envelope lands in the sidecar and nowhere else.
	if err := writeRunState(opts.out, opts.task, repo, cls); err != nil {
		log.Fatalf("write run state %s: %v", opts.out, err)
	}
	log.Printf("run state written to %s", opts.out)

	// EVERY branch says what happens to the sidecar. It used to be written
	// under ContractV2 and simply not mentioned otherwise, which is not the
	// same as "there is no sidecar": a v1 re-run over an -out that a v2 run had
	// already used left the PREVIOUS run's sidecar in place, still asserting the
	// superseded verdict. A consumer reading it was told there was no human PR
	// gate for a critical money diff. WriteV2Sidecar makes a failed write a hard
	// error because "a silently missing sidecar is indistinguishable at the
	// consumer from an old run"; a silently PRESENT one from an old run is
	// indistinguishable from a current one, and it carries a verdict instead of
	// nothing, which is worse.
	//
	// Exhaustive, with no default arm falling through to "leave it alone".
	switch contract {
	case ContractV2:
		// Only under ContractV2 and only with -out: those two conditions are
		// WriteV2Sidecar's stated guard, and this is the caller that owes it.
		if err := WriteV2Sidecar(opts.out, cls); err != nil {
			// A hard error, not a warning. A silently missing sidecar is
			// indistinguishable at the consumer from an old run, which routes
			// it down the in-flight mirror path and loses the v2 facts.
			log.Fatalf("%v", err)
		}
		log.Printf("v2 sidecar written to %s", V2SidecarPath(opts.out))
	case ContractV1:
		// A v1 run emits no v2 facts, so no v2 sidecar may be readable beside
		// its run-state afterwards. Removal, not rewriting: the run has no v2
		// envelope to put there, and inventing one would be a v1 run publishing
		// a v2 claim.
		removed, err := RemoveV2Sidecar(opts.out)
		if err != nil {
			// Same hard-error reasoning as the write. A sidecar this run failed
			// to tear down is a verdict this run did not make, still readable.
			log.Fatalf("%v", err)
		}
		if removed {
			log.Printf("stale v2 sidecar removed from %s", V2SidecarPath(opts.out))
		}
	case ContractVersionUnset:
		log.Fatalf("persist: contract is %s — nobody decided which contract this run is under, and the sidecar's fate is that decision", ContractVersionUnset)
	default:
		log.Fatalf("persist: contract %s is outside the closed set, so whether a sidecar should exist is undecided", contract)
	}
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

// emit writes the machine payload for the contract in force, or the human
// report, which is the same under both contracts.
func emit(repo Repo, cls *Classification, asJSON bool, contract ContractVersion) error {
	if !asJSON {
		printReport(repo, cls)
		return nil
	}
	// Exhaustive over the closed set, with no default arm falling through to
	// v1: run() has already rejected anything outside it, and an arm that
	// re-derived a default here would be a second place the contract is decided.
	switch contract {
	case ContractV1:
		return EmitV1(os.Stdout, cls)
	case ContractV2:
		return EmitV2(os.Stdout, cls)
	case ContractVersionUnset:
		return fmt.Errorf("emit: contract is %s — nobody decided which wire to write", ContractVersionUnset)
	default:
		return fmt.Errorf("emit: contract %s is outside the closed set", contract)
	}
}

// ─── config loading ──────────────────────────────────────────────────────────

// configCandidates lists where a project's rule table may live, in order.
//
// EVERY CANDIDATE IS ANCHORED TO THE WORKTREE UNDER REVIEW. There is
// deliberately NO fallback to another checkout's config. The rule table names
// one repository's money, auth and client paths; applied to a different
// repository it produces confidently wrong answers — a diff certified as
// touching no financial path because the paths it names do not exist here. A
// missing config is INVALID_INPUT, not a default.
//
// WHAT THIS FUNCTION USED TO DO, and why it does not any more. It appended
// agentConfigDirs[0]+"/risk-paths.json" with NO worktree prefix, which is
// CWD-relative. The Tasker runs classify from the tooling checkout with
// -worktree pointing at the project under review, and this repo ships
// .agent/risk-paths.json at its root, so an empty worktree silently borrowed
// THIS repository's money table — precisely the outcome the paragraph above
// says deliberately cannot happen. TestConfigCandidates_PrefersVendorNeutralDir
// did not catch it: it rejects candidates whose SPELLING contains
// "claude-workflow", and the relative string ".agent/risk-paths.json" contains
// no such substring while resolving into exactly that repo. A check on a
// spelling is not a check on where a path lands.
//
// An unset -worktree still means "the directory I am standing in", so the
// anchor falls back to "." — which filepath.Join cleans away, leaving the two
// candidates byte-identical to what the old tail produced for that case. What
// is gone is the cross-checkout leak that appeared only when a worktree WAS
// named and had no table of its own.
//
// $RISK_PATHS_CONFIG IS NOT CONSULTED. It used to head this list, ahead of both
// directories, so an agent that could set an environment variable redirected
// the entire money-path table and the dual-config check never ran. Setting an
// environment variable is not the same act as an operator naming a rule table:
// -config still names one file explicitly and is honoured ahead of this search
// (resolveConfigPath), which is that flag's whole contract. Restoring the
// variable here reopens a money-gate bypass;
// TestSeal_Repair_EnvVarMustNotOutrankTheWorktreeMoneyTable blocks it.
func configCandidates(worktree string) []string {
	// An unset worktree is the current directory, not "nowhere": naming the
	// anchor explicitly is what keeps every candidate inside the tree under
	// review instead of leaking into whichever checkout classify was started
	// from.
	root := worktree
	if root == "" {
		root = "."
	}
	out := make([]string, 0, len(agentConfigDirs))
	for _, dir := range agentConfigDirs {
		out = append(out, filepath.Join(root, dir, "risk-paths.json"))
	}
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

// loadConfig parses the rule table at p.
//
// It does NOT necessarily read p. If the agreement check already read that
// exact path, those are the bytes parsed here, handed over by
// consumeCertifiedConfigRead. The check and the use are one read, which is the
// whole point: see certifiedConfigRead in contract.go for why re-opening the
// path would leave a window the check cannot see across.
func loadConfig(p string) (*Config, error) {
	data, err := consumeCertifiedConfigRead(p)
	if err != nil {
		return nil, err
	}
	// The digest the response wrapper echoes is over the bytes THIS PROCESS
	// CONSUMED, so it is recorded here, at the read — not recomputed from the
	// path at emission time, which would digest whatever is on disk then and
	// would be a different claim.
	unframedDigests.recordConfig(data)
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
		unframedDigests.recordDiff(data)
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
	// Recorded on both branches, and on the exact bytes: the diff channel's
	// digest must describe what was classified, whichever way it arrived.
	unframedDigests.recordDiff(data)
	return string(data), nil
}

// readAll drains f, and a read failure is an ERROR.
//
// There are exactly two ways this loop ends and they are named separately:
// io.EOF, which is the stream finishing cleanly and is not a failure; and
// anything else, which is a failure and is returned as one. The bytes read so
// far are returned alongside the error so a caller that wants to report the
// truncation can say how far it got — but no caller may treat them as a
// complete stream, and none does: readDiff wraps the error and run() aborts.
//
// WHY THIS IS NOT TIDINESS. readDiff records whatever it got as the diff
// channel's digest, and unframedDigestSource exists on the premise that the
// digest describes "the bytes I classified". A diff truncated by a failing pipe
// would otherwise be classified as the whole change and attested with a
// valid-looking SHA-256; the files that fell off the end match no rule, so the
// fail-closed tier never fires for a file that is not there.
//
// The EOF test is errors.Is(err, io.EOF), not a comparison against the string
// "EOF". Deciding clean termination by a message means any wrapper, any
// translated errno whose text happens to read "EOF", ends the stream
// successfully.
func readAll(f *os.File) ([]byte, error) {
	var out []byte
	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, fmt.Errorf("read after %d bytes: %w", len(out), err)
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

	// A scaffold config has never had its money paths named by a human, so a
	// "financial_paths_touched: false" from it is an absence of evidence, not
	// evidence of absence. Refuse to certify: force the gate and the full panel
	// until someone completes the table and clears the flag.
	cls.ConfigScaffold = cfg.Scaffold

	cls.Panel = decidePanel(cls)
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
	if cls.ConfigScaffold {
		blockers = append(blockers, "risk-paths config is still a scaffold — its money paths have not been reviewed, so nothing can be certified safe")
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
