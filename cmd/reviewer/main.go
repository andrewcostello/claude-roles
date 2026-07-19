package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	statusReviewComplete    = "REVIEW_COMPLETE"
	statusInvalidInput      = "INVALID_INPUT"
	statusReviewUnavailable = "REVIEW_UNAVAILABLE"

	verdictApprove        = "APPROVE"
	verdictIterate        = "ITERATE"
	verdictReject         = "REJECT"
	verdictRequestChanges = "REQUEST_CHANGES"
)

type ReviewResponse struct {
	Status                string             `json:"status"`
	Verdict               string             `json:"verdict"`
	Summary               string             `json:"summary"`
	CriticalDimensions    CriticalDimensions `json:"critical_dimensions"`
	QualityScores         QualityScores      `json:"quality_scores"`
	QualityScore          int                `json:"quality_score"`
	TestQualityAssessment string             `json:"test_quality_assessment"`
	DesignCoherence       string             `json:"design_coherence"`
	DataFlowTrace         string             `json:"data_flow_trace"`
	Findings              []Finding          `json:"findings"`
}

type CriticalDimensions struct {
	Correctness    DimensionResult `json:"correctness"`
	Security       DimensionResult `json:"security"`
	Compliance     DimensionResult `json:"compliance"`
	Exploitability DimensionResult `json:"exploitability"`
}

type DimensionResult struct {
	Pass  bool   `json:"pass"`
	Notes string `json:"notes"`
}

type QualityScores struct {
	Resilience      int `json:"resilience"`
	Idempotency     int `json:"idempotency"`
	Observability   int `json:"observability"`
	Performance     int `json:"performance"`
	Maintainability int `json:"maintainability"`
}

type Finding struct {
	Severity  string `json:"severity"`
	Dimension string `json:"dimension"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Title     string `json:"title"`
	Problem   string `json:"problem"`
	Evidence  string `json:"evidence"`
	Principle string `json:"principle"`
	Fix       string `json:"fix"`
	Source    string `json:"source"`
	Blocking  bool   `json:"blocking"`
}

type NamedResponse struct {
	Name     string
	Response ReviewResponse
}

type ReviewInputContext struct {
	Worktree      string
	Branch        string
	HeadSHA       string
	BaseRef       string
	GitStatus     string
	Dirty         bool
	ChangedFiles  []ChangedFile
	SiblingTraces []SiblingTrace
}

type ChangedFile struct {
	Path   string
	Status string
	Exists bool
}

type SiblingTrace struct {
	Symbol  string
	Matches []string
	Error   string
}

type DiffFile struct {
	OldPath string
	NewPath string
	Deleted bool
}

func main() {
	cwdFlag := flag.String("cwd", ".", "Workspace context for the review")
	reviewersFlag := flag.String("reviewers", "claude,claude-scouts,codex,grok,agy", "Comma-separated list of reviewers to run (claude, claude-scouts, codex, grok, agy, deepseek-scouts, kimi)")
	baseFlag := flag.String("base", "", "Base branch or commit for review metadata")
	strictCleanFlag := flag.Bool("strict-clean", false, "Treat a dirty worktree as INVALID_INPUT")
	riskFlag := flag.String("risk", "medium", "Risk tier of the change (critical|high|medium|low). Scales reviewer reasoning effort, consensus floor, and Claude focused-scope fan-out.")
	componentFlag := flag.String("component", "", "Comma-separated component presets applying hard dimension floors: wallet, bet-settlement, bet-placement, jackpot, responsible-gambling")
	floorsFlag := flag.String("floors", "", "Explicit dimension floor overrides, e.g. \"idem=5,resil=5\" (dims: resil, idem, obs, perf, maint). Floors only raise, never lower.")
	findingsOutFlag := flag.String("findings-out", "", "Write the deduplicated findings + review metadata as JSON to this path (machine input for cmd/recheck)")
	claudeModelFlag := flag.String("claude-model", "claude-fable-5", "Model for the claude broad seat + all scouts + focused scopes (e.g. claude-fable-5, claude-opus-4-8). For model-tiering A/B.")
	deepseekModelFlag := flag.String("deepseek-model", "deepseek-v4-flash", "Model for the deepseek broad seat (deepseek-v4-flash or deepseek-v4-pro). For A/B testing.")
	kimiModelFlag := flag.String("kimi-model", "", "Model alias for the kimi broad seat (e.g. moonshot-ai/kimi-k3). Empty inherits the kimi CLI's configured default_model.")
	flag.Parse()

	log.Println("Starting Multi-Agent Code Review Orchestrator...")
	log.Printf("Target Workspace: %s\n", *cwdFlag)

	risk := strings.ToLower(strings.TrimSpace(*riskFlag))
	switch risk {
	case "critical", "high", "medium", "low":
	default:
		log.Printf("WARNING: unknown -risk %q, treating as medium", *riskFlag)
		risk = "medium"
	}
	log.Printf("Risk tier: %s (claude effort: %s, grok effort: %s)\n", risk, claudeEffort(risk), grokEffort(risk))

	floors, err := buildFloors(risk, *componentFlag, *floorsFlag)
	if err != nil {
		log.Fatalf("Invalid floors configuration: %v", err)
	}
	log.Printf("Quality floors: %s\n", floors)

	reviewers := parseReviewers(*reviewersFlag)
	if len(reviewers) == 0 {
		log.Fatal("Error: no reviewers configured")
	}

	diff, err := readDiff(flag.Args())
	if err != nil {
		log.Fatal(err)
	}
	if strings.TrimSpace(diff) == "" {
		log.Fatal("Error: Diff is empty. Pass a file as an argument or pipe a diff to stdin.")
	}

	tempDir, err := os.MkdirTemp("", "reviewer-*")
	if err != nil {
		log.Fatalf("Failed to create temp dir: %v", err)
	}
	log.Printf("Working directory for raw output: %s\n", tempDir)

	inputCtx, inputProblems := buildReviewInputContext(*cwdFlag, diff, *baseFlag, *strictCleanFlag)
	if len(inputProblems) > 0 {
		printInvalidInput(inputCtx, inputProblems)
		os.Exit(3)
	}

	schemaBytes, err := json.MarshalIndent(reviewResponseSchema(), "", "  ")
	if err != nil {
		log.Fatalf("Failed to marshal schema: %v", err)
	}
	schemaStr := string(schemaBytes)
	schemaPath := filepath.Join(tempDir, "review-response.schema.json")
	if err := os.WriteFile(schemaPath, schemaBytes, 0644); err != nil {
		log.Fatalf("Failed to write schema file: %v", err)
	}

	roleText, rolePath, err := readReviewerRole(*cwdFlag)
	if err != nil {
		log.Fatalf("FATAL: Could not read reviewer.md from %s: %v. Missing submodule path. Cannot perform deep review.", *cwdFlag, err)
	}
	log.Printf("Loaded reviewer role: %s\n", rolePath)

	sharedBody := buildSharedBody(inputCtx, diff)
	promptBody := buildPrompt(roleText, sharedBody)
	promptPath := filepath.Join(tempDir, "review-request.md")
	if err := os.WriteFile(promptPath, []byte(promptBody), 0644); err != nil {
		log.Fatalf("Failed to write prompt file: %v", err)
	}

	env := reviewEnv{
		promptPath:    promptPath,
		schemaPath:    schemaPath,
		schemaStr:     schemaStr,
		promptBody:    promptBody,
		sharedBody:    sharedBody,
		roleText:      roleText,
		claudeModel:   *claudeModelFlag,
		deepseekModel: *deepseekModelFlag,
		kimiModel:     *kimiModelFlag,
		tempDir:       tempDir,
		cwd:           *cwdFlag,
		risk:          risk,
		diff:          diff,
		deepseekTemp:  0.1,
	}
	log.Printf("Claude seats model: %s\n", *claudeModelFlag)
	log.Printf("DeepSeek broad model: %s\n", *deepseekModelFlag)
	kimiModelLog := *kimiModelFlag
	if kimiModelLog == "" {
		kimiModelLog = "(kimi CLI configured default_model)"
	}
	log.Printf("Kimi broad model: %s\n", kimiModelLog)

	results, reviewerErrors := dispatchReviewers(reviewers, env)
	finalStatus, finalVerdict := mergeReviewStatus(results, len(reviewers), risk, floors)
	consensus := fmt.Sprintf("%d/%d reviewers completed (floor %d for %s risk)", len(results), len(reviewers), consensusFloor(risk, len(reviewers)), risk)
	printReviewReport(finalStatus, finalVerdict, consensus, inputCtx, results, reviewerErrors)

	if *findingsOutFlag != "" {
		if err := writeFindingsJSON(*findingsOutFlag, inputCtx, risk, finalVerdict, results); err != nil {
			log.Printf("WARNING: failed to write findings JSON to %s: %v", *findingsOutFlag, err)
		} else {
			log.Printf("Findings JSON written to %s (input for cmd/recheck targeted verification)", *findingsOutFlag)
		}
	}

	if finalStatus == statusReviewUnavailable {
		os.Exit(2)
	}
}

func parseReviewers(reviewersFlag string) []string {
	rawReviewers := strings.Split(reviewersFlag, ",")
	var reviewers []string
	for _, r := range rawReviewers {
		if r = strings.TrimSpace(r); r != "" {
			reviewers = append(reviewers, r)
		}
	}
	return reviewers
}

func readDiff(args []string) (string, error) {
	if len(args) > 0 {
		b, err := os.ReadFile(args[0])
		if err != nil {
			return "", fmt.Errorf("failed to read diff file: %w", err)
		}
		return string(b), nil
	}

	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("failed to read diff from stdin: %w", err)
	}
	return string(b), nil
}

// reviewEnv bundles everything a provider dispatch needs. sharedBody is the
// review metadata + diff (the per-panel VARIABLE content); roleText (the ~52KB
// reviewer.md, identical across every panel) is passed separately into the
// claude SYSTEM prompt so prompt caching shares it across all role-carrying
// seats and across panels (a stdin-embedded role sits mid-prompt where
// prefix-caching can't reuse it). Decomposed providers (claude-scouts) prepend
// their scope to sharedBody in stdin; the role stays in the cached system slot.
type reviewEnv struct {
	promptPath    string
	schemaPath    string
	schemaStr     string
	promptBody    string
	sharedBody    string
	roleText      string
	claudeModel   string
	deepseekModel string
	kimiModel     string
	tempDir       string
	cwd           string
	risk          string
	diff          string
	deepseekTemp  float64 // investigation-phase temperature; bumped on retry
}

// providerTimeout is the per-attempt ceiling for one reviewer. Bake-off data
// (2026-07-16, PRs 1276-1280): claude at --effort xhigh completed medium diffs
// in 8.5-9 min and was killed at exactly 10:00 on the two larger diffs — both
// attempts, wasting 20 min per PR; the claude-scouts long pole (dataflow-spec)
// ran 7m36s, only 2.4 min from the same ceiling. A kill costs the panel a
// whole seat, so these ceilings are sized generously above observed runtimes.
func providerTimeout(name string) time.Duration {
	switch name {
	case "claude", "claude-scouts", "deepseek-scouts":
		return 20 * time.Minute
	default:
		return 10 * time.Minute
	}
}

func dispatchReviewers(reviewers []string, env reviewEnv) ([]NamedResponse, []error) {
	var wg sync.WaitGroup
	results := make(chan NamedResponse, len(reviewers))
	errors := make(chan error, len(reviewers))

	for _, reviewer := range reviewers {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()

			log.Printf("Dispatching review to %s...\n", name)

			// Focused Tier-1 scope reviewers (roles/reviewer.md Step 0.5) run
			// programmatically, in parallel with the broad Claude review, so
			// their findings cost no extra wall-clock time. Failures are
			// non-fatal — the broad review stands on its own.
			var focused <-chan []Finding
			if name == "claude" && env.risk != "low" {
				focused = dispatchClaudeFocusedScopes(env.diff, env.cwd, env.risk, env.tempDir, env.claudeModel)
			}

			var resp ReviewResponse
			var err error
			for attempt := 1; attempt <= 2; attempt++ {
				// Bump deepseek temperature on retry so the second attempt
				// explores a different reasoning path (temp 0.0 would
				// produce identical output to the first attempt).
				if name == "deepseek" && attempt == 2 {
					env.deepseekTemp = 0.2
				}
				ctx, cancel := context.WithTimeout(context.Background(), providerTimeout(name))
				resp, err = callLLMProvider(ctx, name, env)
				cancel()
				if err == nil {
					break
				}
				if attempt == 1 {
					log.Printf("%s failed; retrying once with the same review request: %v\n", name, err)
				}
			}
			if err != nil {
				errors <- fmt.Errorf("%s failed: %w", name, err)
				return
			}
			if focused != nil {
				if ff := <-focused; len(ff) > 0 {
					log.Printf("claude: merged %d focused-scope finding(s)\n", len(ff))
					resp.Findings = append(resp.Findings, ff...)
				}
			}
			results <- NamedResponse{Name: name, Response: resp}
			log.Printf("%s completed successfully.\n", name)
		}(reviewer)
	}

	wg.Wait()
	close(results)
	close(errors)

	var allResponses []NamedResponse
	for resp := range results {
		allResponses = append(allResponses, resp)
	}

	var allErrors []error
	for err := range errors {
		log.Printf("ERROR: %v\n", err)
		allErrors = append(allErrors, err)
	}

	sort.Slice(allResponses, func(i, j int) bool {
		return allResponses[i].Name < allResponses[j].Name
	})

	return allResponses, allErrors
}

// qualityFloors are the per-dimension minimum scores below which the verdict
// cannot APPROVE. The base floor comes from the risk tier (4 for
// critical/high, 3 for medium/low); -component presets and -floors overrides
// raise individual dimensions on top of that. Canonical component table:
// tasker.md Phase 4.2 / critical-review-dispatch.md "Component-specific
// dimension floors".
type qualityFloors struct {
	Resilience      int
	Idempotency     int
	Observability   int
	Performance     int
	Maintainability int
}

func (f qualityFloors) String() string {
	return fmt.Sprintf("resil>=%d idem>=%d obs>=%d perf>=%d maint>=%d",
		f.Resilience, f.Idempotency, f.Observability, f.Performance, f.Maintainability)
}

// componentFloorPresets mirror the table in tasker.md Phase 4.2. A zero field
// means "no component-specific floor" — the base tier floor applies.
var componentFloorPresets = map[string]qualityFloors{
	"wallet":               {Performance: 5, Idempotency: 5, Resilience: 5},
	"bet-settlement":       {Performance: 5, Idempotency: 5, Resilience: 5, Observability: 5},
	"bet-placement":        {Performance: 5, Idempotency: 5, Resilience: 5},
	"jackpot":              {Performance: 4, Idempotency: 5, Resilience: 5},
	"responsible-gambling": {Observability: 5},
}

func buildFloors(risk, components, overrides string) (qualityFloors, error) {
	base := 4
	if risk == "medium" || risk == "low" {
		base = 3
	}
	f := qualityFloors{base, base, base, base, base}
	raise := func(dst *int, v int) {
		if v > *dst {
			*dst = v
		}
	}

	for _, c := range splitTrim(components) {
		preset, ok := componentFloorPresets[strings.ToLower(c)]
		if !ok {
			return f, fmt.Errorf("unknown -component %q (known: wallet, bet-settlement, bet-placement, jackpot, responsible-gambling)", c)
		}
		raise(&f.Resilience, preset.Resilience)
		raise(&f.Idempotency, preset.Idempotency)
		raise(&f.Observability, preset.Observability)
		raise(&f.Performance, preset.Performance)
		raise(&f.Maintainability, preset.Maintainability)
	}

	for _, kv := range splitTrim(overrides) {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return f, fmt.Errorf("bad -floors entry %q (want dim=value)", kv)
		}
		v, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || v < 1 || v > 5 {
			return f, fmt.Errorf("bad -floors value in %q (want 1-5)", kv)
		}
		switch strings.ToLower(strings.TrimSpace(parts[0])) {
		case "resil", "resilience":
			raise(&f.Resilience, v)
		case "idem", "idempotency":
			raise(&f.Idempotency, v)
		case "obs", "observability":
			raise(&f.Observability, v)
		case "perf", "performance":
			raise(&f.Performance, v)
		case "maint", "maintainability":
			raise(&f.Maintainability, v)
		default:
			return f, fmt.Errorf("unknown floor dimension %q (want resil|idem|obs|perf|maint)", parts[0])
		}
	}
	return f, nil
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

// consensusFloor is the minimum number of completed reviews required to emit
// a verdict, per tier: Critical demands the full panel; High tolerates one
// absence on a 4-5 seat panel (ceil(3N/4), min 2); Medium a majority; Low one.
func consensusFloor(risk string, expected int) int {
	switch risk {
	case "critical":
		return expected
	case "high":
		f := (3*expected + 3) / 4
		if f < 2 {
			f = 2
		}
		if f > expected {
			f = expected
		}
		return f
	case "medium":
		f := (expected + 1) / 2
		if f < 1 {
			f = 1
		}
		return f
	default:
		return 1
	}
}

func mergeReviewStatus(responses []NamedResponse, expectedReviewers int, risk string, floors qualityFloors) (string, string) {
	floor := consensusFloor(risk, expectedReviewers)
	if len(responses) < floor {
		log.Printf("WARNING: only %d/%d reviewers completed; consensus floor for %s risk is %d. Marking REVIEW_UNAVAILABLE.", len(responses), expectedReviewers, risk, floor)
		return statusReviewUnavailable, verdictIterate
	}
	if len(responses) < expectedReviewers {
		log.Printf("WARNING: %d/%d reviewers completed — proceeding at or above the %s-risk consensus floor (%d); every absence is flagged in the report.", len(responses), expectedReviewers, risk, floor)
	}

	finalVerdict := verdictApprove
	for _, nr := range responses {
		resp := nr.Response
		if resp.Status == statusInvalidInput {
			return statusInvalidInput, verdictIterate
		}
		if resp.Status != "" && resp.Status != statusReviewComplete {
			return statusReviewUnavailable, verdictIterate
		}
		if resp.Verdict == verdictReject {
			finalVerdict = verdictReject
		}
		if resp.Verdict == verdictRequestChanges || resp.Verdict == verdictIterate {
			if finalVerdict != verdictReject {
				finalVerdict = verdictIterate
			}
		}
		if !resp.CriticalDimensions.Correctness.Pass ||
			!resp.CriticalDimensions.Security.Pass ||
			!resp.CriticalDimensions.Compliance.Pass ||
			!resp.CriticalDimensions.Exploitability.Pass {
			if finalVerdict != verdictReject {
				finalVerdict = verdictIterate
			}
		}
		if resp.QualityScores.Maintainability < floors.Maintainability ||
			resp.QualityScores.Idempotency < floors.Idempotency ||
			resp.QualityScores.Resilience < floors.Resilience ||
			resp.QualityScores.Observability < floors.Observability ||
			resp.QualityScores.Performance < floors.Performance {
			if finalVerdict != verdictReject {
				finalVerdict = verdictIterate
			}
		}
		for _, f := range resp.Findings {
			if f.Blocking || f.Severity == "CRITICAL" || f.Severity == "HIGH" {
				if finalVerdict != verdictReject {
					finalVerdict = verdictIterate
				}
			}
		}
	}

	return statusReviewComplete, finalVerdict
}

func printReviewReport(status, verdict, consensus string, inputCtx ReviewInputContext, responses []NamedResponse, reviewerErrors []error) {
	fmt.Printf("\n# Code Review Report\n")
	fmt.Printf("**Status:** `%s`\n", status)
	fmt.Printf("**Final Verdict:** `%s`\n", verdict)
	fmt.Printf("**Consensus:** %s\n", consensus)
	fmt.Printf("**Reviewed SHA:** `%s`\n", emptyDash(inputCtx.HeadSHA))
	fmt.Printf("**Base Ref:** `%s`\n\n", emptyDash(inputCtx.BaseRef))

	if len(reviewerErrors) > 0 {
		fmt.Println("## Reviewer Availability")
		for _, err := range reviewerErrors {
			fmt.Printf("- %v\n", err)
		}
		fmt.Println()
	}

	fmt.Println("## Executive Summaries")
	for _, nr := range responses {
		fmt.Printf("### %s\n", strings.ToUpper(nr.Name))
		fmt.Printf("%s\n\n", nr.Response.Summary)
	}

	fmt.Println("## Critical Dimensions")
	for _, nr := range responses {
		d := nr.Response.CriticalDimensions
		fmt.Printf("### %s\n", strings.ToUpper(nr.Name))
		fmt.Printf("- Correctness: %s — %s\n", passFail(d.Correctness.Pass), d.Correctness.Notes)
		fmt.Printf("- Security: %s — %s\n", passFail(d.Security.Pass), d.Security.Notes)
		fmt.Printf("- Compliance: %s — %s\n", passFail(d.Compliance.Pass), d.Compliance.Notes)
		fmt.Printf("- Exploitability: %s — %s\n\n", passFail(d.Exploitability.Pass), d.Exploitability.Notes)
	}

	fmt.Println("## Critical & High Findings (deduplicated)")
	groups := dedupeFindings(responses)
	findingsCount := 0
	for _, g := range groups {
		if g.Severity == "CRITICAL" || g.Severity == "HIGH" || g.Blocking {
			findingsCount++
			f := g.Finding
			fmt.Printf("- **[%s] %s:%d — %s** *(found by %s)*\n", g.Severity, f.File, f.Line, f.Title, strings.Join(g.Sources, ", "))
			fmt.Printf("  - %s\n", f.Problem)
			if f.Evidence != "" {
				fmt.Printf("  - *Evidence:* %s\n", f.Evidence)
			}
			fmt.Printf("  - *Fix:* %s\n", f.Fix)
		}
	}
	if findingsCount == 0 {
		fmt.Println("*No critical or high findings reported.*")
	}

	log.Printf("\n=== FINAL STATUS: %s / VERDICT: %s ===\n", status, verdict)
}

// findingGroup is a cluster of near-duplicate findings reported independently
// by multiple reviewers. Severity is the MAX across members and every source
// is listed — dedup can only raise a finding's visibility, never bury it. The
// verdict is computed from the raw per-reviewer findings, not these groups.
type findingGroup struct {
	Finding  Finding
	Severity string
	Blocking bool
	Sources  []string
	minLine  int
	maxLine  int
}

var severityRank = map[string]int{"CRITICAL": 3, "HIGH": 2, "MEDIUM": 1, "LOW": 0}

// dedupeFindings clusters findings that name the same file within 15 lines
// AND share enough title+problem vocabulary (Jaccard >= 0.3) — proximity
// alone is not enough, so two different defects at one call site stay
// separate. The representative is the most detailed member (longest
// problem+evidence). Deterministic: responses arrive sorted by reviewer name.
func dedupeFindings(responses []NamedResponse) []findingGroup {
	var groups []findingGroup
	for _, nr := range responses {
		for _, f := range nr.Response.Findings {
			src := nr.Name
			if f.Source != "" && f.Source != nr.Name {
				src = fmt.Sprintf("%s (%s)", nr.Name, f.Source)
			}
			placed := false
			for i := range groups {
				if matchesGroup(&groups[i], f) {
					g := &groups[i]
					g.Sources = appendUnique(g.Sources, src)
					if severityRank[f.Severity] > severityRank[g.Severity] {
						g.Severity = f.Severity
					}
					if f.Blocking {
						g.Blocking = true
					}
					if f.Line < g.minLine {
						g.minLine = f.Line
					}
					if f.Line > g.maxLine {
						g.maxLine = f.Line
					}
					if len(f.Problem)+len(f.Evidence) > len(g.Finding.Problem)+len(g.Finding.Evidence) {
						g.Finding = f
					}
					placed = true
					break
				}
			}
			if !placed {
				groups = append(groups, findingGroup{Finding: f, Severity: f.Severity, Blocking: f.Blocking, Sources: []string{src}, minLine: f.Line, maxLine: f.Line})
			}
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return severityRank[groups[i].Severity] > severityRank[groups[j].Severity]
	})
	return groups
}

// matchesGroup reports whether f is a near-duplicate of an existing cluster:
// same file, within 15 lines of the cluster's observed line RANGE (reviewers
// anchor the same bug at different lines of the same flow — bake-off evidence:
// one TOCTOU reported at 1635/1639/1651), and enough shared vocabulary that
// two different defects at one call site stay separate.
func matchesGroup(g *findingGroup, f Finding) bool {
	if g.Finding.File != f.File {
		return false
	}
	if f.Line < g.minLine-15 || f.Line > g.maxLine+15 {
		return false
	}
	return jaccard(findingWords(g.Finding), findingWords(f)) >= 0.3
}

func findingWords(f Finding) map[string]bool {
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(f.Title+" "+f.Problem), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(w) >= 4 {
			words[w] = true
		}
	}
	return words
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// FindingsExport is the machine-readable contract between cmd/reviewer and
// cmd/recheck: the deduplicated finding groups plus the metadata recheck needs
// to scope its verification (which SHA this review saw, at what tier).
type FindingsExport struct {
	ReviewedSHA string          `json:"reviewed_sha"`
	BaseRef     string          `json:"base_ref"`
	Risk        string          `json:"risk"`
	Verdict     string          `json:"verdict"`
	Findings    []ExportFinding `json:"findings"`
}

type ExportFinding struct {
	Severity string   `json:"severity"`
	Blocking bool     `json:"blocking"`
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Title    string   `json:"title"`
	Problem  string   `json:"problem"`
	Evidence string   `json:"evidence"`
	Fix      string   `json:"fix"`
	Sources  []string `json:"sources"`
}

func writeFindingsJSON(path string, inputCtx ReviewInputContext, risk, verdict string, responses []NamedResponse) error {
	export := FindingsExport{
		ReviewedSHA: inputCtx.HeadSHA,
		BaseRef:     inputCtx.BaseRef,
		Risk:        risk,
		Verdict:     verdict,
	}
	for _, g := range dedupeFindings(responses) {
		export.Findings = append(export.Findings, ExportFinding{
			Severity: g.Severity,
			Blocking: g.Blocking,
			File:     g.Finding.File,
			Line:     g.Finding.Line,
			Title:    g.Finding.Title,
			Problem:  g.Finding.Problem,
			Evidence: g.Finding.Evidence,
			Fix:      g.Finding.Fix,
			Sources:  g.Sources,
		})
	}
	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func appendUnique(list []string, s string) []string {
	for _, existing := range list {
		if existing == s {
			return list
		}
	}
	return append(list, s)
}

func printInvalidInput(inputCtx ReviewInputContext, problems []string) {
	fmt.Printf("\n# Code Review Report\n")
	fmt.Printf("**Status:** `%s`\n", statusInvalidInput)
	fmt.Printf("**Final Verdict:** `%s`\n", verdictIterate)
	fmt.Printf("**Reviewed SHA:** `%s`\n", emptyDash(inputCtx.HeadSHA))
	fmt.Printf("**Base Ref:** `%s`\n\n", emptyDash(inputCtx.BaseRef))
	fmt.Println("## Input Problems")
	for _, problem := range problems {
		fmt.Printf("- %s\n", problem)
	}
	fmt.Println()
	fmt.Println("## Changed Files")
	for _, file := range inputCtx.ChangedFiles {
		fmt.Printf("- `%s` (%s, exists=%t)\n", file.Path, file.Status, file.Exists)
	}
}

func callLLMProvider(ctx context.Context, provider string, env reviewEnv) (ReviewResponse, error) {
	if provider == "claude-scouts" {
		return runClaudeScouts(ctx, env)
	}
	if provider == "deepseek-scouts" {
		return runDeepseekScouts(ctx, env)
	}

	var cmd *exec.Cmd

	switch provider {
	case "grok":
		cmd = exec.CommandContext(ctx, "grok",
			"--prompt-file", env.promptPath,
			"--cwd", env.cwd,
			"--always-approve",
			"--tools", "read_file,grep,list_dir,run_terminal_cmd",
			"--disallowed-tools", "search_replace,write",
			"--max-turns", "50",
			"--reasoning-effort", grokEffort(env.risk),
			"--json-schema", env.schemaStr,
			"--output-format", "json",
		)
	case "codex":
		outFilePath := filepath.Join(env.tempDir, "codex.last.json")
		codexArgs := []string{"exec",
			"-C", env.cwd,
			"-s", "read-only",
			"--ephemeral",
			"--output-schema", env.schemaPath,
			"-o", outFilePath,
		}
		if env.risk == "critical" || env.risk == "high" {
			codexArgs = append(codexArgs, "-c", "model_reasoning_effort=high")
		}
		codexArgs = append(codexArgs, "-")
		cmd = exec.CommandContext(ctx, "codex", codexArgs...)
		cmd.Dir = env.cwd
		cmd.Stdin = strings.NewReader(env.promptBody)
	case "agy":
		agyPrompt := fmt.Sprintf("Read the role instructions from %s and perform a code review on the diff located at %s. You MUST output your response strictly as a JSON object matching the schema at %s. DO NOT output any conversational text or markdown other than the JSON block.", env.promptPath, filepath.Join(env.tempDir, "review-request.md"), env.schemaPath)
		cmd = exec.CommandContext(ctx, "agy", "--dangerously-skip-permissions", "--print-timeout", "15m", "--print", agyPrompt)
		cmd.Dir = env.cwd
	case "claude":
		cmd = claudeCmd(ctx, env.cwd, claudeEffort(env.risk), env.schemaStr, env.roleText, env.claudeModel)
		cmd.Stdin = strings.NewReader(buildClaudeBroadStdin(env.sharedBody))
	case "deepseek":
		cmd = deepseekBroadCmd(ctx, env.cwd, env.deepseekModel, env.schemaStr, env.roleText, env.risk, env.deepseekTemp)
		cmd.Stdin = strings.NewReader(buildClaudeBroadStdin(env.sharedBody))
	case "kimi":
		cmd = kimiBroadCmd(ctx, env)
	default:
		return ReviewResponse{}, fmt.Errorf("unknown provider: %s", provider)
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		dumpPathErr := filepath.Join(env.tempDir, fmt.Sprintf("%s.err", provider))
		dumpPathOut := filepath.Join(env.tempDir, fmt.Sprintf("%s.out", provider))
		os.WriteFile(dumpPathErr, errBuf.Bytes(), 0644)
		os.WriteFile(dumpPathOut, outBuf.Bytes(), 0644)
		return ReviewResponse{}, fmt.Errorf("%s failed: %v\nStdout dumped to %s\nStderr dumped to %s", provider, err, dumpPathOut, dumpPathErr)
	}

	rawOutput := outBuf.String()
	if provider == "codex" {
		outFilePath := filepath.Join(env.tempDir, "codex.last.json")
		b, err := os.ReadFile(outFilePath)
		if err == nil {
			rawOutput = string(b)
		}
	}
	if provider == "kimi" {
		// kimi ran with --output-format stream-json: stdout is an event log,
		// not the answer. Keep the raw stream for debugging and reduce it to
		// the final assistant message before the shared normalize/parse path.
		streamPath := filepath.Join(env.tempDir, "kimi.stream")
		os.WriteFile(streamPath, outBuf.Bytes(), 0644)
		text, err := kimiTextFromStreamJSON(rawOutput)
		if err != nil {
			return ReviewResponse{}, fmt.Errorf("kimi produced no final assistant message: %v\nRaw stream dumped to %s", err, streamPath)
		}
		rawOutput = text
	}

	dumpPath := filepath.Join(env.tempDir, fmt.Sprintf("%s.raw", provider))
	os.WriteFile(dumpPath, []byte(rawOutput), 0644)
	errDumpPath := filepath.Join(env.tempDir, fmt.Sprintf("%s.err", provider))
	os.WriteFile(errDumpPath, errBuf.Bytes(), 0644)

	rawJSON := normalizeProviderJSON(provider, rawOutput)

	var resp ReviewResponse
	if err := json.Unmarshal([]byte(rawJSON), &resp); err != nil {
		return ReviewResponse{}, fmt.Errorf("failed to parse JSON from %s: %v\nRaw output dumped to %s\nStderr dumped to %s", provider, err, dumpPath, errDumpPath)
	}
	return resp, nil
}

func normalizeProviderJSON(provider, rawOutput string) string {
	rawJSON := strings.TrimSpace(rawOutput)

	if strings.HasPrefix(rawJSON, "{") {
		if provider == "grok" {
			var envelope struct {
				StructuredOutput json.RawMessage `json:"structuredOutput"`
				Text             string          `json:"text"`
			}
			if err := json.Unmarshal([]byte(rawJSON), &envelope); err == nil {
				if len(envelope.StructuredOutput) > 0 {
					rawJSON = string(envelope.StructuredOutput)
				} else if envelope.Text != "" {
					rawJSON = strings.TrimSpace(envelope.Text)
				}
			}
		} else if provider == "claude" {
			var envelope struct {
				Result json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal([]byte(rawJSON), &envelope); err == nil && len(envelope.Result) > 0 {
				var strResult string
				if err := json.Unmarshal(envelope.Result, &strResult); err == nil {
					rawJSON = strings.TrimSpace(strResult)
				} else {
					rawJSON = strings.TrimSpace(string(envelope.Result))
				}
			}
		}
	}

	if strings.HasPrefix(rawJSON, "```json") {
		rawJSON = strings.TrimPrefix(rawJSON, "```json")
		rawJSON = strings.TrimSuffix(rawJSON, "```")
	} else if strings.HasPrefix(rawJSON, "```") {
		rawJSON = strings.TrimPrefix(rawJSON, "```")
		rawJSON = strings.TrimSuffix(rawJSON, "```")
	}
	return strings.TrimSpace(rawJSON)
}

func buildReviewInputContext(cwd, diff, baseRef string, strictClean bool) (ReviewInputContext, []string) {
	var problems []string
	ctx := ReviewInputContext{
		Worktree: cwd,
		BaseRef:  baseRef,
	}

	if branch, err := runCommand(cwd, "git", "branch", "--show-current"); err == nil {
		ctx.Branch = strings.TrimSpace(branch)
	} else {
		problems = append(problems, fmt.Sprintf("could not read git branch: %v", err))
	}
	if head, err := runCommand(cwd, "git", "rev-parse", "HEAD"); err == nil {
		ctx.HeadSHA = strings.TrimSpace(head)
	} else {
		problems = append(problems, fmt.Sprintf("could not read reviewed SHA: %v", err))
	}
	if ctx.BaseRef == "" {
		if upstream, err := runCommand(cwd, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
			ctx.BaseRef = strings.TrimSpace(upstream)
		} else {
			ctx.BaseRef = "not provided"
		}
	}
	if status, err := runCommand(cwd, "git", "status", "--porcelain"); err == nil {
		ctx.GitStatus = strings.TrimSpace(status)
		ctx.Dirty = ctx.GitStatus != ""
		if strictClean && ctx.Dirty {
			problems = append(problems, "worktree is dirty and --strict-clean was set")
		}
	} else {
		problems = append(problems, fmt.Sprintf("could not read git status: %v", err))
	}

	diffFiles := parseDiffFiles(diff)
	if len(diffFiles) == 0 {
		problems = append(problems, "could not parse changed file paths from diff")
	}

	for _, file := range diffFiles {
		path := file.NewPath
		status := "modified"
		if file.Deleted {
			path = file.OldPath
			status = "deleted"
		} else if file.OldPath == "" || file.OldPath == "/dev/null" {
			status = "added"
		}

		exists := false
		if path != "" && path != "/dev/null" {
			_, statErr := os.Stat(filepath.Join(cwd, path))
			exists = statErr == nil
			if !file.Deleted && !exists {
				problems = append(problems, fmt.Sprintf("changed file %q does not exist in reviewed worktree", path))
			}
		}
		ctx.ChangedFiles = append(ctx.ChangedFiles, ChangedFile{Path: path, Status: status, Exists: exists})
	}

	ctx.SiblingTraces = buildSiblingTraces(cwd, extractSymbols(diff))
	return ctx, problems
}

func parseDiffFiles(diff string) []DiffFile {
	lines := strings.Split(diff, "\n")
	var files []DiffFile
	current := -1
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				files = append(files, DiffFile{
					OldPath: trimDiffPath(fields[2]),
					NewPath: trimDiffPath(fields[3]),
				})
				current = len(files) - 1
			}
			continue
		}
		if current < 0 {
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			files[current].OldPath = trimDiffPath(strings.TrimSpace(strings.TrimPrefix(line, "--- ")))
		}
		if strings.HasPrefix(line, "+++ ") {
			newPath := trimDiffPath(strings.TrimSpace(strings.TrimPrefix(line, "+++ ")))
			files[current].NewPath = newPath
			files[current].Deleted = newPath == "/dev/null"
		}
	}

	seen := map[string]bool{}
	var deduped []DiffFile
	for _, file := range files {
		key := file.OldPath + "\x00" + file.NewPath
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, file)
	}
	return deduped
}

func trimDiffPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	return path
}

func extractSymbols(diff string) []string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\bfunc\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)`),
		regexp.MustCompile(`\btype\s+([A-Za-z_][A-Za-z0-9_]*)`),
		regexp.MustCompile(`\bconst\s+([A-Za-z_][A-Za-z0-9_]*)`),
		regexp.MustCompile(`\bvar\s+([A-Za-z_][A-Za-z0-9_]*)`),
		regexp.MustCompile(`(?i)\b(?:from|join|update|into)\s+([A-Za-z_][A-Za-z0-9_\.]*)`),
	}

	seen := map[string]bool{}
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") {
			continue
		}
		for _, pattern := range patterns {
			for _, match := range pattern.FindAllStringSubmatch(line, -1) {
				if len(match) > 1 {
					seen[match[1]] = true
				}
			}
		}
	}

	var symbols []string
	for symbol := range seen {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	if len(symbols) > 12 {
		symbols = symbols[:12]
	}
	return symbols
}

func buildSiblingTraces(cwd string, symbols []string) []SiblingTrace {
	var traces []SiblingTrace
	for _, symbol := range symbols {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := exec.CommandContext(ctx, "rg",
			"-n",
			"--fixed-strings",
			"--glob", "!.git",
			"--glob", "!vendor",
			"--glob", "!node_modules",
			symbol,
			cwd,
		)
		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		err := cmd.Run()
		cancel()

		trace := SiblingTrace{Symbol: symbol}
		if err != nil && outBuf.Len() == 0 {
			if ctx.Err() == context.DeadlineExceeded {
				trace.Error = "rg timed out"
			} else {
				trace.Error = strings.TrimSpace(errBuf.String())
			}
		}
		trace.Matches = firstLines(outBuf.String(), 20)
		traces = append(traces, trace)
	}
	return traces
}

// maxTraceLineBytes bounds a single sibling-trace match line. rg matches land
// in the review prompt verbatim, and a match inside a data-URI or minified
// asset can be a single multi-megabyte line (a 3.1MB base64 PNG in a plans
// HTML doc once blew the claude/codex seats past their input limits and took
// the whole panel to REVIEW_UNAVAILABLE). Line-count caps alone don't protect
// against that.
const maxTraceLineBytes = 500

func firstLines(s string, max int) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) > maxTraceLineBytes {
			line = line[:maxTraceLineBytes] + " …[line truncated]"
		}
		lines = append(lines, line)
		if len(lines) >= max {
			break
		}
	}
	return lines
}

// broadOverrideInstruction is the stable broad-review contract prepended to
// every broad-review prompt (claude/codex/grok/agy). It never varies, so on the
// claude path it precedes the cached system-prompt role harmlessly.
const broadOverrideInstruction = `CRITICAL INSTRUCTION: Your FINAL message must be ONLY a raw JSON object matching the provided schema — no markdown fences, tables, Jira comments, or conversational text. The reviewer role instructions may ask for markdown output; map that content into the schema fields instead.

Before the final message, investigate thoroughly with your tools: read the changed files in full, grep sibling call sites, and work through the role's data-flow and sibling-surface traces in intermediate turns. Intermediate output is discarded, so do the deep analysis there — only the final message format is constrained, not the depth of investigation.

Set status to REVIEW_COMPLETE when you completed the review. Set verdict to APPROVE, REQUEST_CHANGES, or REJECT using reviewer.md rules. The orchestrator maps REQUEST_CHANGES to ITERATE.

Use the Review Metadata and Precomputed Context as evidence. Do not report dirty worktree or missing files as code findings; the orchestrator handles INVALID_INPUT before dispatch. For this structured CLI path, do not spawn focused sub-agents. Run applicable focused scopes yourself in intermediate turns and label finding sources with the scope name. Do not edit any files.

	Key source files are pre-loaded under "Pre-loaded Source Files" — read them there first before using tools. Use your tools only for files not pre-loaded or for grep searches.`

// buildPrompt is the FULL broad-review prompt WITH the role inline — used by the
// codex/grok/agy seats (which have no system-prompt slot) and written to the
// shared review-request.md that agy reads.
func buildPrompt(roleText, sharedBody string) string {
	return fmt.Sprintf("%s\n\n%s\n\n%s", broadOverrideInstruction, roleText, sharedBody)
}

// buildClaudeBroadStdin is the broad-review stdin for the claude seat, which
// carries the role in its --append-system-prompt (cached) rather than inline —
// so the role is deliberately omitted here.
func buildClaudeBroadStdin(sharedBody string) string {
	return fmt.Sprintf("%s\n\n%s", broadOverrideInstruction, sharedBody)
}

// buildSharedBody assembles the per-panel VARIABLE content — review metadata +
// diff, WITHOUT the role. The role (reviewer.md) now rides the claude system
// prompt (see reviewEnv.roleText) so it's a cacheable shared prefix; keeping it
// out of stdin is what lets the cache actually hit across seats/panels.
func buildSharedBody(inputCtx ReviewInputContext, diff string) string {
	preloaded := preloadSourceFiles(inputCtx.Worktree, inputCtx.ChangedFiles)
	return fmt.Sprintf("%s\n\n%s\n\n## Review Request\n\n### Diff\n%s",
		formatReviewContext(inputCtx),
		preloaded,
		diff,
	)
}

func formatReviewContext(ctx ReviewInputContext) string {
	var b strings.Builder
	b.WriteString("## Review Metadata\n\n")
	fmt.Fprintf(&b, "- Worktree: `%s`\n", ctx.Worktree)
	fmt.Fprintf(&b, "- Branch: `%s`\n", emptyDash(ctx.Branch))
	fmt.Fprintf(&b, "- Reviewed SHA: `%s`\n", emptyDash(ctx.HeadSHA))
	fmt.Fprintf(&b, "- Base ref: `%s`\n", emptyDash(ctx.BaseRef))
	fmt.Fprintf(&b, "- Dirty worktree: `%t`\n\n", ctx.Dirty)

	b.WriteString("### Git Status (`git status --porcelain`)\n\n")
	b.WriteString("```text\n")
	if ctx.GitStatus == "" {
		b.WriteString("(clean)\n")
	} else {
		b.WriteString(ctx.GitStatus)
		b.WriteString("\n")
	}
	b.WriteString("```\n\n")

	b.WriteString("### Changed Files\n\n")
	b.WriteString("| Path | Status | Exists in worktree |\n")
	b.WriteString("|------|--------|--------------------|\n")
	for _, file := range ctx.ChangedFiles {
		fmt.Fprintf(&b, "| `%s` | %s | %t |\n", file.Path, file.Status, file.Exists)
	}
	b.WriteString("\n")

	b.WriteString("### Precomputed Sibling Surface Trace\n\n")
	if len(ctx.SiblingTraces) == 0 {
		b.WriteString("No symbols extracted from diff for sibling trace.\n")
		return b.String()
	}
	for _, trace := range ctx.SiblingTraces {
		fmt.Fprintf(&b, "#### `%s`\n", trace.Symbol)
		if trace.Error != "" {
			fmt.Fprintf(&b, "- rg error: %s\n", trace.Error)
		}
		if len(trace.Matches) == 0 {
			b.WriteString("- No matches found.\n\n")
			continue
		}
		b.WriteString("```text\n")
		for _, match := range trace.Matches {
			b.WriteString(match)
			b.WriteString("\n")
		}
		b.WriteString("```\n\n")
	}
	return b.String()
}

// preloadSourceFiles reads the full contents of high-signal changed source files
// (new or modified, excluding lockfiles, generated code, test fixtures, and
// deleted files). Each file is truncated at 64KB, total output capped at 512KB.
// This lets reviewers skip file-reading tool turns and go straight to analysis.
func preloadSourceFiles(cwd string, files []ChangedFile) string {
	var b strings.Builder
	b.WriteString("### Pre-loaded Source Files\n\n")
	b.WriteString("The full contents of key changed files are included below. ")
	b.WriteString("You may still read additional files with your tools if needed.\n\n")

	var totalBytes int
	const maxFileBytes = 64 * 1024   // 64KB per file
	const maxTotalBytes = 512 * 1024 // 512KB total

	for _, f := range files {
		if !f.Exists || f.Status == "deleted" {
			continue
		}
		if !isPreloadableFile(f.Path) {
			continue
		}
		if totalBytes >= maxTotalBytes {
			b.WriteString(fmt.Sprintf("*(%d additional source files omitted — total pre-load cap reached)*\n", remainingPreloadable(files, f.Path)))
			break
		}

		fullPath := filepath.Join(cwd, f.Path)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		n := len(data)
		if n > maxFileBytes {
			data = data[:maxFileBytes]
			b.WriteString(fmt.Sprintf("#### `%s` (%s, %d bytes — first %d shown)\n\n", f.Path, f.Status, n, maxFileBytes))
		} else {
			b.WriteString(fmt.Sprintf("#### `%s` (%s, %d bytes)\n\n", f.Path, f.Status, n))
		}

		ext := strings.TrimPrefix(filepath.Ext(f.Path), ".")
		if ext == "" {
			ext = "text"
		}
		b.WriteString(fmt.Sprintf("```%s\n%s\n```\n\n", ext, string(data)))
		totalBytes += n
	}

	if totalBytes == 0 {
		return ""
	}
	return b.String()
}

// isPreloadableFile returns true for source files worth pre-loading:
// production code, configs, SQL, protos — but NOT lockfiles, generated stubs,
// test files, or build artifacts.
func isPreloadableFile(path string) bool {
	base := filepath.Base(path)
	ext := filepath.Ext(path)

	// Skip lockfiles and generated artifacts
	skipBase := map[string]bool{
		"package-lock.json": true, "yarn.lock": true, "go.sum": true,
		"pnpm-lock.yaml": true, "Cargo.lock": true, "Gemfile.lock": true,
	}
	if skipBase[base] {
		return false
	}

	// Skip generated code
	genSuffixes := []string{"_pb.ts", "_pb.d.ts", ".gen.go", ".generated.ts", ".pb.go", ".sqlc.go"}
	for _, s := range genSuffixes {
		if strings.HasSuffix(base, s) {
			return false
		}
	}

	// Skip test files
	if strings.Contains(path, "/__tests__/") || strings.Contains(path, "/tests/") ||
		strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, ".test.ts") ||
		strings.HasSuffix(base, ".test.tsx") || strings.HasSuffix(base, ".spec.ts") ||
		strings.HasSuffix(base, ".spec.tsx") {
		return false
	}

	// Accept source files by extension
	sourceExts := map[string]bool{
		".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
		".sql": true, ".yaml": true, ".yml": true, ".json": true,
		".proto": true, ".tf": true, ".hcl": true,
		".py": true, ".rs": true, ".java": true, ".kt": true,
		".css": true, ".scss": true, ".less": true,
		".md": true, ".txt": true,
	}
	return sourceExts[ext]
}

func remainingPreloadable(files []ChangedFile, afterPath string) int {
	seen := false
	count := 0
	for _, f := range files {
		if f.Path == afterPath {
			seen = true
			continue
		}
		if seen && f.Exists && f.Status != "deleted" && isPreloadableFile(f.Path) {
			count++
		}
	}
	return count
}

func runCommand(cwd string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return outBuf.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return outBuf.String(), nil
}

func reviewResponseSchema() map[string]any {
	stringSchema := map[string]any{"type": "string"}
	intSchema := map[string]any{"type": "integer"}
	boolSchema := map[string]any{"type": "boolean"}
	dimensionSchema := objectSchema(
		map[string]any{
			"pass":  boolSchema,
			"notes": stringSchema,
		},
		[]string{"pass", "notes"},
	)

	return objectSchema(
		map[string]any{
			"status": map[string]any{
				"type": "string",
				"enum": []string{statusReviewComplete, statusInvalidInput},
			},
			"verdict": map[string]any{
				"type": "string",
				"enum": []string{verdictApprove, verdictRequestChanges, verdictReject},
			},
			"summary": stringSchema,
			"critical_dimensions": objectSchema(
				map[string]any{
					"correctness":    dimensionSchema,
					"security":       dimensionSchema,
					"compliance":     dimensionSchema,
					"exploitability": dimensionSchema,
				},
				[]string{"correctness", "security", "compliance", "exploitability"},
			),
			"quality_scores": objectSchema(
				map[string]any{
					"resilience":      intSchema,
					"idempotency":     intSchema,
					"observability":   intSchema,
					"performance":     intSchema,
					"maintainability": intSchema,
				},
				[]string{"resilience", "idempotency", "observability", "performance", "maintainability"},
			),
			"quality_score":           intSchema,
			"test_quality_assessment": stringSchema,
			"design_coherence":        stringSchema,
			"data_flow_trace":         stringSchema,
			"findings": map[string]any{
				"type":  "array",
				"items": findingSchema(),
			},
		},
		[]string{"status", "verdict", "summary", "critical_dimensions", "quality_scores", "quality_score", "test_quality_assessment", "design_coherence", "data_flow_trace", "findings"},
	)
}

func findingSchema() map[string]any {
	stringSchema := map[string]any{"type": "string"}
	return objectSchema(
		map[string]any{
			"severity": map[string]any{
				"type": "string",
				"enum": []string{"CRITICAL", "HIGH", "MEDIUM", "LOW"},
			},
			"dimension": stringSchema,
			"file":      stringSchema,
			"line":      map[string]any{"type": "integer"},
			"title":     stringSchema,
			"problem":   stringSchema,
			"evidence":  stringSchema,
			"principle": stringSchema,
			"fix":       stringSchema,
			"source":    stringSchema,
			"blocking":  map[string]any{"type": "boolean"},
		},
		[]string{"severity", "dimension", "file", "line", "title", "problem", "evidence", "principle", "fix", "source", "blocking"},
	)
}

func focusedFindingsSchema() map[string]any {
	return objectSchema(
		map[string]any{
			"findings": map[string]any{
				"type":  "array",
				"items": findingSchema(),
			},
		},
		[]string{"findings"},
	)
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             required,
	}
}

func readReviewerRole(cwd string) (string, string, error) {
	candidates := []string{
		filepath.Join(cwd, ".claude/workflow/roles/reviewer.md"),
		filepath.Join(cwd, "roles/reviewer.md"),
	}

	var errs []string
	for _, path := range candidates {
		roleData, err := os.ReadFile(path)
		if err == nil {
			return string(roleData), path, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", path, err))
	}

	return "", "", fmt.Errorf(strings.Join(errs, "; "))
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func passFail(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

// claudeEffort maps risk tier to the claude CLI --effort level for the broad
// review. Grok already runs --reasoning-effort high; without this, the claude
// slot ran at whatever the account default happened to be.
func claudeEffort(risk string) string {
	switch risk {
	case "critical":
		return "max"
	case "high":
		return "xhigh"
	case "low":
		return "medium"
	default:
		return "high"
	}
}

// grokEffort maps risk tier to grok --reasoning-effort. xhigh is reserved for
// Critical per critical-review-dispatch.md.
func grokEffort(risk string) string {
	if risk == "critical" {
		return "xhigh"
	}
	return "high"
}

// deepseekMaxTurns returns the investigation turn ceiling for a deepseek seat,
// risk-tiered so complex reviews get more room to investigate. Scouts get +5
// turns on top of the base because their narrower scope exploits fewer turns
// per file but needs more files read.
func deepseekMaxTurns(risk string, isScout bool) int {
	base := 20
	if isScout {
		base = 25
	}
	switch risk {
	case "critical":
		return base * 2
	case "high":
		return base + 10
	default:
		return base
	}
}

// claudeCmd builds a headless claude invocation hardened for panel review:
// pinned model (no inheritance of user-settings variants), explicit effort,
// --safe-mode to strip plugins/hooks/MCP/skills (measured: ~26k → ~3.6k tokens
// of boot context; plugins load from installed state, so --setting-sources
// alone does not remove them), read-only (no Edit/Write), and no Task/Agent
// fan-out — focused scopes are dispatched programmatically by the orchestrator
// instead. --safe-mode also skips CLAUDE.md auto-load; reviewers still read it
// on demand via tools (reviewer.md instructs them to).
// systemPrompt, when non-empty, is appended to claude's system prompt via
// --append-system-prompt. Pass the (identical-across-seats) reviewer.md role
// here so prompt caching treats it as a shared cacheable prefix — the single
// biggest input-cost lever, since the 52KB role is otherwise re-sent uncached
// to the broad seat + all 7 scouts on every panel.
func claudeCmd(ctx context.Context, cwd, effort, schemaStr, systemPrompt, model string) *exec.Cmd {
	if model == "" {
		model = "claude-fable-5"
	}
	args := []string{
		"-p",
		"--model", model,
		"--effort", effort,
		"--safe-mode",
		"--strict-mcp-config",
		"--no-session-persistence",
		"--disallowedTools", "Edit,Write,NotebookEdit,Task,Agent",
		"--dangerously-skip-permissions",
		"--output-format", "json",
		"--json-schema", schemaStr,
	}
	if systemPrompt != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = cwd
	return cmd
}

// deepseekBroadCmd builds a headless deepseek CLI invocation for the broad
// (single-seat full-reviewer.md) review. The deepseek binary handles its own
// tool-use loop + forced-JSON final turn internally. risk tiers the turn budget;
// temperature varies the investigation-phase temperature (retries use a higher
// value for a different reasoning path).
func deepseekBroadCmd(ctx context.Context, cwd, model, schemaStr, systemPrompt, risk string, temperature float64) *exec.Cmd {
	if model == "" {
		model = "deepseek-v4-flash"
	}
	args := []string{
		"--model", model,
		"--json-schema", schemaStr,
		"--cwd", cwd,
		"--max-turns", strconv.Itoa(deepseekMaxTurns(risk, false)),
		"--timeout", "19m",
		"--temperature", strconv.FormatFloat(temperature, 'f', 2, 64),
	}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}
	exePath, _ := os.Executable()
	deepseekBin := filepath.Join(filepath.Dir(exePath), "deepseek")
	if _, err := os.Stat(deepseekBin); os.IsNotExist(err) {
		deepseekBin = filepath.Join(filepath.Dir(exePath), "..", "deepseek", "deepseek")
	}
	cmd := exec.CommandContext(ctx, deepseekBin, args...)
	cmd.Dir = cwd
	return cmd
}

// kimiBroadCmd builds a headless kimi CLI invocation for the broad seat.
// Unlike claude, the kimi CLI has no --json-schema, no --append-system-prompt,
// and no stdin prompt mode (-p takes the prompt as an argv string, capped by
// the kernel's ~128KB per-argument limit) — so the full review request (role
// inline, already on disk for the agy seat) is referenced by PATH and kimi
// reads it with its own Read tool. Structured output relies on prompt
// instruction + the lenient normalize/retry path; the final assistant message
// is recovered from the stream-json event log (see kimiTextFromStreamJSON).
//
// Isolation notes: -p runs under the auto permission policy (read tools
// auto-approved, nothing blocks on a prompt headless). There is no
// --safe-mode equivalent: overriding KIMI_CODE_HOME for full isolation would
// drop the user's OAuth credentials, so user-level MCP/hooks stay loaded;
// skills are replaced with an empty dir. Each run writes a resumable session
// in the user's kimi home (no --no-session-persistence equivalent exists).
func kimiBroadCmd(ctx context.Context, env reviewEnv) *exec.Cmd {
	skillsDir := filepath.Join(env.tempDir, "kimi-no-skills")
	os.MkdirAll(skillsDir, 0755)
	args := []string{
		"-p", kimiBroadPrompt(env),
		"--output-format", "stream-json",
		"--skills-dir", skillsDir,
	}
	if env.kimiModel != "" {
		args = append(args, "-m", env.kimiModel)
	}
	cmd := exec.CommandContext(ctx, "kimi", args...)
	cmd.Dir = env.cwd
	return cmd
}

// kimiBroadPrompt is the short argv instruction that points kimi at the
// on-disk review request + schema. Keeping it tiny is what dodges the argv
// size ceiling — the 52KB role and the diff never touch the command line.
func kimiBroadPrompt(env reviewEnv) string {
	return fmt.Sprintf(`Read %s — it is your complete code review request: role instructions, review metadata, and the diff. Follow it exactly.

Investigate with your read-only tools: read the changed files in full and grep sibling call sites in the repository. Do NOT edit any files.

Your FINAL message must be ONLY a raw JSON object matching the schema at %s — no markdown fences, tables, or conversational text. Do the deep analysis in intermediate turns; only the final message format is constrained. Set status to REVIEW_COMPLETE when done, and verdict to APPROVE, REQUEST_CHANGES, or REJECT per the role's verdict rules.`, env.promptPath, env.schemaPath)
}

// kimiTextFromStreamJSON recovers the final assistant text from kimi's
// stream-json event log (one JSON object per line). Assistant tool-call turns
// carry tool_calls and no content; tool results are role "tool"; a session
// meta line trails — so the LAST text-bearing assistant event is the final
// answer. Malformed lines are skipped defensively: one bad line in a long
// stream must not cost the seat.
func kimiTextFromStreamJSON(raw string) (string, error) {
	var last string
	found := false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Role != "assistant" || len(event.Content) == 0 {
			continue
		}
		var text string
		if err := json.Unmarshal(event.Content, &text); err != nil {
			continue // content present but not a plain string
		}
		if strings.TrimSpace(text) != "" {
			last = text
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("no assistant message with text content in stream")
	}
	return last, nil
}

// focusedScope mirrors a Tier-1 mandatory scope from roles/reviewer.md Step
// 0.5. Triggers are content-based regexes over the diff, matching the role's
// "based on code content" table.
type focusedScope struct {
	name    string
	concern string
	re      *regexp.Regexp
}

var focusedScopes = []focusedScope{
	{
		name:    "db-query",
		concern: "Verify parameterized inputs on every query, check for N+1 patterns, validate index coverage for new queries, check migration idempotency.",
		re:      regexp.MustCompile(`(?i)\b(select|insert|update|delete)\b|sqlc|migration|create table|alter table|\.Query|\.Exec`),
	},
	{
		name:    "financial-integrity",
		concern: "Trace every arithmetic path for overflow, verify ledger entries exist for every balance mutation, check rounding consistency.",
		re:      regexp.MustCompile(`(?i)balance|amount|payout|\bbets?\b|wager|stake|ledger|\bcredit|\bdebit`),
	},
	{
		name:    "auth-permissions",
		concern: "Map every endpoint/mutation to its auth check, verify no path skips authorization, check token validation and expiry handling.",
		re:      regexp.MustCompile(`(?i)\bauth|authoriz|token|session|permission|jwt|login|credential`),
	},
	{
		name:    "concurrency",
		concern: "Identify all shared mutable state and verify every access is protected; authorization/precondition checks execute inside the same lock/tx as the mutation they gate; check lock ordering, TOCTOU windows, and context cancellation. Races are Correctness FAILs regardless of likelihood.",
		re:      regexp.MustCompile(`go func|sync\.|Mutex|\bchan\b|atomic\.|WaitGroup|FOR UPDATE|goroutine`),
	},
}

// dispatchClaudeFocusedScopes selects Tier-1 scopes whose content triggers
// match the diff and runs one focused claude reviewer per scope, all in
// parallel. Returns a channel that yields the merged findings exactly once.
func dispatchClaudeFocusedScopes(diff, cwd, risk, tempDir, model string) <-chan []Finding {
	out := make(chan []Finding, 1)
	go func() {
		defer close(out)

		// Regex triage is a cost-saving heuristic and its failure mode is a
		// false negative (a payout variable named `value`, a shared map
		// mutated without the word Mutex). At critical/high the tier itself
		// says the change matters — dispatch every scope unconditionally and
		// let irrelevant ones return empty. Triage only gates medium.
		var matched []focusedScope
		if risk == "critical" || risk == "high" {
			matched = focusedScopes
		} else {
			for _, s := range focusedScopes {
				if s.re.MatchString(diff) {
					matched = append(matched, s)
				}
			}
		}
		if len(matched) == 0 {
			out <- nil
			return
		}
		var names []string
		for _, s := range matched {
			names = append(names, s.name)
		}
		log.Printf("claude: dispatching %d focused scope reviewer(s): %s\n", len(matched), strings.Join(names, ", "))

		var mu sync.Mutex
		var all []Finding
		var wg sync.WaitGroup
		for _, s := range matched {
			wg.Add(1)
			go func(s focusedScope) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
				defer cancel()
				fs, err := runClaudeFocusedScope(ctx, s, diff, cwd, risk, tempDir, model)
				if err != nil {
					log.Printf("claude focused scope %s failed (non-fatal): %v\n", s.name, err)
					return
				}
				log.Printf("claude focused scope %s completed: %d finding(s)\n", s.name, len(fs))
				mu.Lock()
				all = append(all, fs...)
				mu.Unlock()
			}(s)
		}
		wg.Wait()
		out <- all
	}()
	return out
}

func runClaudeFocusedScope(ctx context.Context, s focusedScope, diff, cwd, risk, tempDir, model string) ([]Finding, error) {
	// Focused scopes are narrow; high effort suffices except on Critical.
	effort := "high"
	if risk == "critical" {
		effort = "xhigh"
	}

	schemaBytes, err := json.Marshal(focusedFindingsSchema())
	if err != nil {
		return nil, fmt.Errorf("failed to marshal focused schema: %w", err)
	}

	prompt := fmt.Sprintf(`You are a focused code reviewer. Your scope is strictly: %s.
Do not comment on anything outside this scope.

Concern: %s

Use your tools to read the full files behind the diff and grep sibling call sites in the repository. Do NOT edit any files.

Severity calibration: severity is the worst plausible production impact assuming an adversarial user and unlucky timing — never discounted by likelihood. CRITICAL = exploitable now (money loss/duplication, data corruption, auth bypass, compliance breach). HIGH = reachable correctness defect (race, missing auth on a mutation, spec violation). MEDIUM = robustness gap with no incorrect behavior today. LOW = style/docs. Set blocking=true only on CRITICAL/HIGH findings.

Your FINAL message must be ONLY a raw JSON object matching the provided schema: {"findings": [...]}. If the scope is clean, return {"findings": []}. Do the analysis in intermediate turns; the final message is JSON only.

## Diff
%s`, s.name, s.concern, diff)

	// Focused scopes are self-contained (they do not carry the full reviewer.md),
	// so no shared system-prompt role — the cross-seat cache win is on the broad
	// review + scouts, which do carry it.
	cmd := claudeCmd(ctx, cwd, effort, string(schemaBytes), "", model)
	cmd.Stdin = strings.NewReader(prompt)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	os.WriteFile(filepath.Join(tempDir, fmt.Sprintf("claude-%s.raw", s.name)), outBuf.Bytes(), 0644)
	os.WriteFile(filepath.Join(tempDir, fmt.Sprintf("claude-%s.err", s.name)), errBuf.Bytes(), 0644)
	if runErr != nil {
		return nil, fmt.Errorf("claude focused scope %s failed: %v (raw output in %s)", s.name, runErr, tempDir)
	}

	rawJSON := normalizeProviderJSON("claude", outBuf.String())
	var wrapper struct {
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &wrapper); err != nil {
		return nil, fmt.Errorf("failed to parse focused scope %s JSON: %v (raw output in %s)", s.name, err, tempDir)
	}
	for i := range wrapper.Findings {
		wrapper.Findings[i].Source = fmt.Sprintf("focused agent: %s", s.name)
	}
	return wrapper.Findings, nil
}

// --- claude-scouts: fully decomposed review slot ----------------------------
//
// The "claude-scouts" provider replaces the single broad reviewer with seven
// parallel scouts, each owning a slice of reviewer.md, plus a DETERMINISTIC
// reduce in Go: dimension pass/fail = AND across owners, score = min across
// owners, verdict = reviewer.md's mechanical rules. No LLM synthesizer sits
// between scout findings and the report, so nothing can be anchored, diluted,
// or downgraded in a merge step. Wall-clock ≈ the slowest single scout.
//
// Dimension ownership must cover all 4 critical dimensions and all 5 quality
// scores across the scout set — reduceScoutResults fails closed if it doesn't.

type scout struct {
	name   string
	dims   []string // critical dimensions this scout owns (pass/fail)
	scores []string // quality scores this scout owns (1-5)
	extras []string // extra required string fields in its output schema
	scope  string
	model  string // per-scout model override (deepseek-chat or deepseek-reasoner)
}

var reviewScouts = []scout{
	{
		name:   "dataflow-spec",
		dims:   []string{"correctness"},
		extras: []string{"data_flow_trace"},
		scope:  "Spec-vs-implementation correctness and the seams between components: run reviewer.md Step 1 (Understand the Spec), Step 2 (Trace the Data Flow) and Step 2.5 (Sibling-Surface Trace) in full. Trace every entry point's inputs to storage/response/error paths, run the sentinel audit, enumerate sibling surfaces for every touched symbol, check lifecycle symmetry and legacy-path supersession. Summarize your trace in the data_flow_trace field. You own the Correctness pass/fail verdict from the logic side.",
		model:  "deepseek-v4-pro",
	},
	{
		name:  "db-compliance",
		dims:  []string{"compliance"},
		scope: "Database and compliance: parameterized inputs on every query, N+1 patterns, index coverage for new queries, the full migration checklist (idempotency, down migration, FK indexes, NOT NULL/DEFAULT strategy, version ordering, schema qualification, writers for new projections), immutable audit records, ledger entries for money movement, soft-delete rules, and the responsible-gambling checks. You own the Compliance pass/fail verdict.",
		model: "deepseek-v4-pro",
	},
	{
		name:  "financial-fairness",
		dims:  []string{"exploitability"},
		scope: "Financial integrity and exploitability/fairness: trace every arithmetic path for overflow, integer-only money math, rounding consistency and direction across debit/credit, ledger entries for every balance mutation, CSPRNG for game outcomes, server-anchored time for time-gated mechanics, and no client-side outcome/paytable/RTP leakage. You own the Exploitability & Fairness pass/fail verdict.",
		model: "deepseek-v4-pro",
	},
	{
		name:  "auth-security",
		dims:  []string{"security"},
		scope: "Security and authorization: map every endpoint/mutation to its auth check, all-writers enumeration for every gate/lock enum touched, gate parity on mutate-after-accept paths, fail-closed proof for gated capability decisions, input validation at every trust boundary, injection, PII in code or logs, token validation and expiry. You own the Security pass/fail verdict.",
		model: "deepseek-v4-pro",
	},
	{
		name:   "concurrency-resilience",
		scores: []string{"resilience", "idempotency"},
		scope:  "Concurrency, idempotency, and resilience: shared mutable state protection, precondition checks inside the same lock/tx as the mutation they gate, TOCTOU windows, lock ordering, context cancellation; idempotency keys inside transactions, dual-write convergence (the 4-cell outcome table), reserve-first CAS ordering, replay returning the original result; timeouts, retries with backoff, error-guard specificity, graceful degradation. Score Resilience and Idempotency 1-5 per reviewer.md anchors. Races are Correctness-grade defects — report them as CRITICAL/HIGH findings regardless of likelihood.",
		model:  "deepseek-v4-flash",
	},
	{
		name:   "test-docs",
		dims:   []string{"correctness"},
		extras: []string{"test_quality_assessment"},
		scope:  "Test quality and docs truth: run reviewer.md Step 4 in full (behavior vs implementation coupling, the litmus test, vacuous/false-passing seals, sleep-as-synchronization, RED-then-GREEN proof for regression seals, mock-contract drift) and Step 4.5 (assert every comment/docstring/PR claim against the final code). Summarize in the test_quality_assessment field. You own the Correctness pass/fail verdict from the testing side: a spec'd edge case with no test, or tests that pass without proving correctness, is a Correctness FAIL.",
		model:  "deepseek-v4-flash",
	},
	{
		name:   "quality-scores",
		scores: []string{"observability", "performance", "maintainability"},
		extras: []string{"design_coherence"},
		scope:  "Observability (the 3am test, correlation IDs, silent failures, best-effort steps that downstream invariants depend on), Performance (N+1, unbounded queries, pagination, locks across I/O, missing indexes), Maintainability (complexity hard caps and the named override patterns, project conventions, naming, magic numbers), and Design Coherence (does this change fit the system's existing patterns — summarize in the design_coherence field). Score those three dimensions 1-5 per reviewer.md anchors.",
		model:  "deepseek-v4-flash",
	},
}

type scoutOutput struct {
	Summary               string                     `json:"summary"`
	Dimensions            map[string]DimensionResult `json:"dimensions"`
	Scores                map[string]int             `json:"scores"`
	Findings              []Finding                  `json:"findings"`
	DataFlowTrace         string                     `json:"data_flow_trace"`
	TestQualityAssessment string                     `json:"test_quality_assessment"`
	DesignCoherence       string                     `json:"design_coherence"`
}

func scoutSchema(s scout) map[string]any {
	stringSchema := map[string]any{"type": "string"}
	props := map[string]any{
		"summary": stringSchema,
		"findings": map[string]any{
			"type":  "array",
			"items": findingSchema(),
		},
	}
	required := []string{"summary", "findings"}

	if len(s.dims) > 0 {
		dimProps := map[string]any{}
		for _, d := range s.dims {
			dimProps[d] = objectSchema(
				map[string]any{"pass": map[string]any{"type": "boolean"}, "notes": stringSchema},
				[]string{"pass", "notes"},
			)
		}
		props["dimensions"] = objectSchema(dimProps, s.dims)
		required = append(required, "dimensions")
	}
	if len(s.scores) > 0 {
		scoreProps := map[string]any{}
		for _, sc := range s.scores {
			scoreProps[sc] = map[string]any{"type": "integer", "minimum": 1, "maximum": 5}
		}
		props["scores"] = objectSchema(scoreProps, s.scores)
		required = append(required, "scores")
	}
	for _, extra := range s.extras {
		props[extra] = stringSchema
		required = append(required, extra)
	}
	return objectSchema(props, required)
}

func runClaudeScouts(ctx context.Context, env reviewEnv) (ReviewResponse, error) {
	log.Printf("claude-scouts: dispatching %d scouts in parallel\n", len(reviewScouts))

	outputs := make([]scoutOutput, len(reviewScouts))
	errs := make([]error, len(reviewScouts))
	var wg sync.WaitGroup
	for i, s := range reviewScouts {
		wg.Add(1)
		go func(i int, s scout) {
			defer wg.Done()
			var out scoutOutput
			var err error
			for attempt := 1; attempt <= 2; attempt++ {
				// Each attempt gets its own ceiling (within the provider
				// context) so a retry isn't strangled by whatever the first
				// attempt left on a shared clock.
				attemptCtx, cancel := context.WithTimeout(ctx, 12*time.Minute)
				out, err = runOneScout(attemptCtx, s, env)
				cancel()
				if err == nil {
					break
				}
				if attempt == 1 {
					log.Printf("scout %s failed; retrying once: %v\n", s.name, err)
				}
			}
			if err != nil {
				errs[i] = err
				return
			}
			log.Printf("scout %s completed: %d finding(s)\n", s.name, len(out.Findings))
			outputs[i] = out
		}(i, s)
	}
	wg.Wait()

	var failed []string
	for i, e := range errs {
		if e != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", reviewScouts[i].name, e))
		}
	}
	// A missing scout means a dimension has no owner — fail the whole slot
	// rather than emit a verdict with silent coverage gaps.
	if len(failed) > 0 {
		return ReviewResponse{}, fmt.Errorf("claude-scouts incomplete (%d/%d scouts failed): %s", len(failed), len(reviewScouts), strings.Join(failed, "; "))
	}

	return reduceScoutResults(outputs)
}

func runOneScout(ctx context.Context, s scout, env reviewEnv) (scoutOutput, error) {
	effort := "high"
	if env.risk == "critical" {
		effort = "xhigh"
	}

	schemaBytes, err := json.Marshal(scoutSchema(s))
	if err != nil {
		return scoutOutput{}, fmt.Errorf("failed to marshal scout schema: %w", err)
	}

	prompt := fmt.Sprintf(`You are one focused scout in a decomposed code review panel. Your scope is strictly: %s.

%s

Apply ONLY the sections of the reviewer role (provided in your system instructions) that fall inside your scope — other scouts own the rest. Do not report findings outside your scope. For pass/fail dimensions you own, apply the role's PASS checklists literally with no partial credit. For scores you own, use the role's anchors. Rate severity per the role's Severity Calibration table; set blocking=true only on CRITICAL/HIGH findings.

Use your tools: read the changed files in full, grep sibling call sites, and read the repository's CLAUDE.md for conventions. Do NOT edit any files. Do the deep analysis in intermediate turns — your FINAL message must be ONLY a raw JSON object matching the provided schema.

%s`, s.name, s.scope, env.sharedBody)

	cmd := claudeCmd(ctx, env.cwd, effort, string(schemaBytes), env.roleText, env.claudeModel)
	cmd.Stdin = strings.NewReader(prompt)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	os.WriteFile(filepath.Join(env.tempDir, fmt.Sprintf("claude-scout-%s.raw", s.name)), outBuf.Bytes(), 0644)
	os.WriteFile(filepath.Join(env.tempDir, fmt.Sprintf("claude-scout-%s.err", s.name)), errBuf.Bytes(), 0644)
	if runErr != nil {
		return scoutOutput{}, fmt.Errorf("scout %s failed: %v (raw output in %s)", s.name, runErr, env.tempDir)
	}

	rawJSON := normalizeProviderJSON("claude", outBuf.String())
	var out scoutOutput
	if err := json.Unmarshal([]byte(rawJSON), &out); err != nil {
		return scoutOutput{}, fmt.Errorf("failed to parse scout %s JSON: %v (raw output in %s)", s.name, err, env.tempDir)
	}
	for i := range out.Findings {
		out.Findings[i].Source = fmt.Sprintf("scout: %s", s.name)
	}
	return out, nil
}

// reduceScoutResults is the deterministic reduce: it holds no opinions, only
// applies reviewer.md's verdict rules to what the scouts reported. It cannot
// drop or downgrade a finding.
func reduceScoutResults(outputs []scoutOutput) (ReviewResponse, error) {
	var resp ReviewResponse
	resp.Status = statusReviewComplete

	dimPass := map[string]bool{}
	dimNotes := map[string][]string{}
	scoreMin := map[string]int{}

	var summaries []string
	for i, out := range outputs {
		s := reviewScouts[i]
		summaries = append(summaries, fmt.Sprintf("[%s] %s", s.name, out.Summary))

		for dim, dr := range out.Dimensions {
			pass, seen := dimPass[dim]
			dimPass[dim] = (!seen || pass) && dr.Pass
			if strings.TrimSpace(dr.Notes) != "" {
				dimNotes[dim] = append(dimNotes[dim], fmt.Sprintf("[%s] %s", s.name, dr.Notes))
			}
		}
		for score, v := range out.Scores {
			if cur, ok := scoreMin[score]; !ok || v < cur {
				scoreMin[score] = v
			}
		}
		resp.Findings = append(resp.Findings, out.Findings...)
		if out.DataFlowTrace != "" {
			resp.DataFlowTrace = out.DataFlowTrace
		}
		if out.TestQualityAssessment != "" {
			resp.TestQualityAssessment = out.TestQualityAssessment
		}
		if out.DesignCoherence != "" {
			resp.DesignCoherence = out.DesignCoherence
		}
	}

	// Fail closed on coverage gaps: every dimension and score must have at
	// least one scout owner that actually reported it.
	for _, dim := range []string{"correctness", "security", "compliance", "exploitability"} {
		if _, ok := dimPass[dim]; !ok {
			return ReviewResponse{}, fmt.Errorf("claude-scouts: no scout reported critical dimension %q", dim)
		}
	}
	for _, score := range []string{"resilience", "idempotency", "observability", "performance", "maintainability"} {
		if _, ok := scoreMin[score]; !ok {
			return ReviewResponse{}, fmt.Errorf("claude-scouts: no scout reported quality score %q", score)
		}
	}

	joinNotes := func(dim string) string { return strings.Join(dimNotes[dim], " ; ") }
	resp.CriticalDimensions.Correctness = DimensionResult{Pass: dimPass["correctness"], Notes: joinNotes("correctness")}
	resp.CriticalDimensions.Security = DimensionResult{Pass: dimPass["security"], Notes: joinNotes("security")}
	resp.CriticalDimensions.Compliance = DimensionResult{Pass: dimPass["compliance"], Notes: joinNotes("compliance")}
	resp.CriticalDimensions.Exploitability = DimensionResult{Pass: dimPass["exploitability"], Notes: joinNotes("exploitability")}

	resp.QualityScores = QualityScores{
		Resilience:      scoreMin["resilience"],
		Idempotency:     scoreMin["idempotency"],
		Observability:   scoreMin["observability"],
		Performance:     scoreMin["performance"],
		Maintainability: scoreMin["maintainability"],
	}
	resp.QualityScore = resp.QualityScores.Resilience + resp.QualityScores.Idempotency +
		resp.QualityScores.Observability + resp.QualityScores.Performance + resp.QualityScores.Maintainability

	failCount := 0
	for _, pass := range dimPass {
		if !pass {
			failCount++
		}
	}
	blocking := false
	for _, f := range resp.Findings {
		if f.Blocking || f.Severity == "CRITICAL" || f.Severity == "HIGH" {
			blocking = true
			break
		}
	}
	lowScore := false
	for _, v := range scoreMin {
		if v < 4 {
			lowScore = true
			break
		}
	}
	switch {
	case failCount >= 2:
		resp.Verdict = verdictReject
	case failCount == 1 || blocking || lowScore:
		resp.Verdict = verdictRequestChanges
	default:
		resp.Verdict = verdictApprove
	}

	resp.Summary = fmt.Sprintf("Decomposed review: %d parallel scouts, verdict computed mechanically (dimension = AND of owners, score = min of owners, reviewer.md verdict rules).\n%s",
		len(outputs), strings.Join(summaries, "\n"))

	return resp, nil
}

// --- deepseek-scouts: decomposed review via DeepSeek API --------------------
//
// The "deepseek-scouts" provider mirrors claude-scouts but dispatches scouts
// through the headless deepseek CLI (cmd/deepseek) instead of claude. Each
// scout gets its own model via the scout.model field: deepseek-reasoner (R1)
// for hard dimensions (dataflow-spec, auth-security, financial-fairness,
// db-compliance) and deepseek-chat (V3) for the rest.
//
// The deterministic reduce (reduceScoutResults) is shared with claude-scouts
// — dimension = AND of owners, score = min of owners, verdict = reviewer.md
// mechanical rules. No LLM sits in the merge path.

func runDeepseekScouts(ctx context.Context, env reviewEnv) (ReviewResponse, error) {
	log.Printf("deepseek-scouts: dispatching %d scouts in parallel\n", len(reviewScouts))

	outputs := make([]scoutOutput, len(reviewScouts))
	errs := make([]error, len(reviewScouts))
	var wg sync.WaitGroup
	for i, s := range reviewScouts {
		wg.Add(1)
		go func(i int, s scout) {
			defer wg.Done()
			var out scoutOutput
			var err error
			for attempt := 1; attempt <= 2; attempt++ {
				temp := 0.1
				if attempt == 2 {
					temp = 0.2 // different reasoning path on retry
				}
				attemptCtx, cancel := context.WithTimeout(ctx, 12*time.Minute)
				out, err = runOneDeepseekScout(attemptCtx, s, env, temp)
				cancel()
				if err == nil {
					break
				}
				if attempt == 1 {
					log.Printf("deepseek scout %s failed; retrying once: %v\n", s.name, err)
				}
			}
			if err != nil {
				errs[i] = err
				return
			}
			log.Printf("deepseek scout %s completed: %d finding(s)\n", s.name, len(out.Findings))
			outputs[i] = out
		}(i, s)
	}
	wg.Wait()

	var failed []string
	for i, e := range errs {
		if e != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", reviewScouts[i].name, e))
		}
	}
	if len(failed) > 0 {
		return ReviewResponse{}, fmt.Errorf("deepseek-scouts incomplete (%d/%d scouts failed): %s", len(failed), len(reviewScouts), strings.Join(failed, "; "))
	}

	return reduceScoutResults(outputs)
}

func runOneDeepseekScout(ctx context.Context, s scout, env reviewEnv, temperature float64) (scoutOutput, error) {
	model := s.model
	if model == "" {
		model = "deepseek-v4-flash"
	}

	schemaBytes, err := json.Marshal(scoutSchema(s))
	if err != nil {
		return scoutOutput{}, fmt.Errorf("failed to marshal scout schema: %w", err)
	}

	prompt := fmt.Sprintf(`You are one focused scout in a decomposed code review panel. Your scope is strictly: %s.

%s

Apply ONLY the sections of the reviewer role (provided in your system instructions) that fall inside your scope — other scouts own the rest. Do not report findings outside your scope. For pass/fail dimensions you own, apply the role's PASS checklists literally with no partial credit. For scores you own, use the role's anchors. Rate severity per the role's Severity Calibration table; set blocking=true only on CRITICAL/HIGH findings.

Use your tools: read the changed files in full, grep sibling call sites, and list directories to explore the codebase. Do NOT edit any files. Do the deep analysis in intermediate turns — your FINAL message must be ONLY a raw JSON object matching the provided schema.

%s`, s.name, s.scope, env.sharedBody)

	cmd := deepseekCmd(ctx, env.cwd, model, string(schemaBytes), env.roleText, env.risk, temperature)
	cmd.Stdin = strings.NewReader(prompt)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	os.WriteFile(filepath.Join(env.tempDir, fmt.Sprintf("deepseek-scout-%s.raw", s.name)), outBuf.Bytes(), 0644)
	os.WriteFile(filepath.Join(env.tempDir, fmt.Sprintf("deepseek-scout-%s.err", s.name)), errBuf.Bytes(), 0644)
	if runErr != nil {
		return scoutOutput{}, fmt.Errorf("deepseek scout %s failed: %v (raw output in %s)", s.name, runErr, env.tempDir)
	}

	rawJSON := normalizeProviderJSON("deepseek", outBuf.String())
	var out scoutOutput
	if err := json.Unmarshal([]byte(rawJSON), &out); err != nil {
		return scoutOutput{}, fmt.Errorf("failed to parse deepseek scout %s JSON: %v (raw output in %s)", s.name, err, env.tempDir)
	}
	for i := range out.Findings {
		out.Findings[i].Source = fmt.Sprintf("deepseek-scout: %s", s.name)
	}
	return out, nil
}

// deepseekCmd builds a headless deepseek CLI invocation. The deepseek binary
// implements its own tool-use loop against the DeepSeek HTTP API — no
// external agent framework needed. risk tiers the turn budget; temperature
// varies the investigation-phase temperature (retries use a higher value for
// a different reasoning path).
func deepseekCmd(ctx context.Context, cwd, model, schemaStr, systemPrompt, risk string, temperature float64) *exec.Cmd {
	args := []string{
		"--model", model,
		"--json-schema", schemaStr,
		"--cwd", cwd,
		"--max-turns", strconv.Itoa(deepseekMaxTurns(risk, true)),
		"--timeout", "11m",
		"--temperature", strconv.FormatFloat(temperature, 'f', 2, 64),
	}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}
	// Resolve the deepseek binary relative to the reviewer binary.
	exePath, _ := os.Executable()
	deepseekBin := filepath.Join(filepath.Dir(exePath), "deepseek")
	if _, err := os.Stat(deepseekBin); os.IsNotExist(err) {
		// Fallback: sibling cmd/deepseek directory
		deepseekBin = filepath.Join(filepath.Dir(exePath), "..", "deepseek", "deepseek")
	}
	cmd := exec.CommandContext(ctx, deepseekBin, args...)
	cmd.Dir = cwd
	return cmd
}
