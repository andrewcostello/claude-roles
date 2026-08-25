package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	FlowDiagram           string             `json:"flow_diagram,omitempty"`
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
	reviewersFlag := flag.String("reviewers", "claude,claude-scouts,codex,grok", "Comma-separated list of reviewers to run (claude, claude-scouts, grok-scouts, codex, grok, agy, deepseek-scouts, kimi). agy is dispatchable by name but is in no preset — see presetReviewers in cmd/classify.")
	baseFlag := flag.String("base", "", "Base branch or commit for review metadata")
	strictCleanFlag := flag.Bool("strict-clean", false, "Treat a dirty worktree as INVALID_INPUT")
	riskFlag := flag.String("risk", "medium", "Risk tier of the change (critical|high|medium|low). Scales reviewer reasoning effort, consensus floor, and Claude focused-scope fan-out.")
	componentFlag := flag.String("component", "", "Comma-separated component presets applying hard dimension floors: wallet, bet-settlement, bet-placement, jackpot, responsible-gambling")
	floorsFlag := flag.String("floors", "", "Explicit dimension floor overrides, e.g. \"idem=5,resil=5\" (dims: resil, idem, obs, perf, maint). Floors only raise, never lower.")
	findingsOutFlag := flag.String("findings-out", "", "Write the deduplicated findings + review metadata as JSON to this path (machine input for cmd/recheck)")
	claudeModelFlag := flag.String("claude-model", "claude-opus-5", "Model for the claude broad seat + all scouts + focused scopes (e.g. claude-opus-5, claude-haiku-4-5-20251001). Default claude-opus-5 (cheaper than fable for this account; haiku for budget A/B).")
	deepseekModelFlag := flag.String("deepseek-model", "deepseek-v4-flash", "Model for the deepseek broad seat (deepseek-v4-flash or deepseek-v4-pro). For A/B testing.")
	codexModelFlag := flag.String("codex-model", "", "Model for the codex broad seat (e.g. gpt-5.5, gpt-5.6). Empty inherits the codex CLI's config.toml default. gpt-5.6 requires an OpenAI API-key login; ChatGPT-account auth rejects it.")
	codexEffortFlag := flag.String("codex-effort", "high", "model_reasoning_effort for the codex broad seat on high/critical risk (high|xhigh). Default high; xhigh for the strongest pass on money PRs.")
	kimiModelFlag := flag.String("kimi-model", "", "Model alias for the kimi broad seat (e.g. moonshot-ai/kimi-k3). Empty inherits the kimi CLI's configured default_model.")
	scoutPreloadFlag := flag.String("scout-preload", "none", "Scout file-preload mode. \"none\" (default): preload no file contents — each scout tool-reads what it needs from the full diff + Changed Files table it always receives (robust: coverage never keyed on guessed paths, and it can't exceed the context window). \"all\": paste every high-signal changed file into every scout — measured to OVERFLOW the context window on a 26-file PR (209k > 200k tokens) and is expensive elsewhere; only safe for small diffs.")
	flowDiagramFlag := flag.Bool("flow-diagram", false, "Ask the broad claude seat to also emit a mermaid flow/state diagram of the changed behaviour (flow_diagram field). Set by cmd/classify when the diff touches gates, migrations, components, or High+ risk at size.")
	changeMapFlag := flag.Bool("scout-change-map", false, "EXPERIMENTAL: before dispatching scouts, run one cheap map-model pass over the diff to produce a per-file orientation map, injected as a shared cached prefix in every scout's system prompt. Aims to cut scout investigation turns. The map orients only — scouts still read code for verdicts.")
	mapModelFlag := flag.String("map-model", "claude-haiku-4-5-20251001", "Model for the -scout-change-map orientation pass (cheap by design).")
	sharedContextFlag := flag.Bool("shared-context", false, "EXPERIMENTAL: move the review context (metadata, changed-file table, precomputed sibling trace) + the diff — all identical across scouts — into a SHARED cached system-prompt prefix instead of re-sending them in each scout's stdin. Content-addressed prompt caching then pays them once (cache-create) + N-1 cache-reads (~10% price) rather than 6x fresh. With -scout-preload all, the (capped) read-pack rides the same shared prefix. Off by default until A/B-validated.")
	scoutTieringFlag := flag.Bool("scout-tiering", false, "EXPERIMENTAL: run the SOFT scouts (quality-scores — obs/perf/maint/design, which own no money/correctness/security verdict) on -soft-model while the money/correctness/security scouts stay on -claude-model. Validated on PR-1353: the hard scouts on Sonnet-5 catch the Critical + vacuous-seal that haiku missed; the soft scout is safe to drop to haiku. Off by default.")
	softModelFlag := flag.String("soft-model", "claude-haiku-4-5-20251001", "Model for SOFT scouts when -scout-tiering is on.")
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

	sharedBody := buildSharedBody(inputCtx, diff, nil)
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
		flowDiagram:   *flowDiagramFlag,
		claudeModel:   *claudeModelFlag,
		deepseekModel: *deepseekModelFlag,
		codexModel:    *codexModelFlag,
		codexEffort:   strings.ToLower(strings.TrimSpace(*codexEffortFlag)),
		kimiModel:     *kimiModelFlag,
		tempDir:       tempDir,
		cwd:           *cwdFlag,
		risk:          risk,
		diff:          diff,
		inputCtx:      inputCtx,
		scoutPreload:  strings.ToLower(strings.TrimSpace(*scoutPreloadFlag)),
		changeMapOn:   *changeMapFlag,
		mapModel:      *mapModelFlag,
		sharedContext: *sharedContextFlag,
		scoutTiering:  *scoutTieringFlag,
		softModel:     *softModelFlag,
	}
	if env.scoutTiering {
		log.Printf("EXPERIMENTAL: scout tiering ON — soft scouts on %s, hard scouts on %s\n", env.softModel, env.claudeModel)
	}
	log.Printf("Scout preload mode: %s\n", env.scoutPreload)
	if env.changeMapOn {
		log.Printf("EXPERIMENTAL: scout change-map ENABLED (map model: %s)\n", env.mapModel)
	}
	log.Printf("Claude seats model: %s\n", *claudeModelFlag)
	log.Printf("DeepSeek broad model: %s\n", *deepseekModelFlag)
	kimiModelLog := *kimiModelFlag
	if kimiModelLog == "" {
		kimiModelLog = "(kimi CLI configured default_model)"
	}
	log.Printf("Kimi broad model: %s\n", kimiModelLog)

	// The workspace is EVIDENCE, and evidence a witness can edit is not
	// evidence. Fingerprinted before and after the fan-out; see
	// workspaceFingerprint for the incident that put this here.
	fpBefore, fpErr := workspaceFingerprint(*cwdFlag)
	if fpErr != nil {
		log.Printf("WARNING: could not fingerprint workspace before review (%v); a mid-run mutation will NOT be detected", fpErr)
	}

	results, reviewerErrors := dispatchReviewers(reviewers, env)

	if fpErr == nil {
		if fpAfter, err := workspaceFingerprint(*cwdFlag); err != nil {
			log.Printf("WARNING: could not fingerprint workspace after review (%v); cannot confirm it was unchanged", err)
		} else if fpAfter != fpBefore {
			log.Printf("FATAL: the workspace CHANGED during the review (%s -> %s). A reviewer mutated the artifact it was judging, so every finding in this run is unattributable and the reviewed SHA is meaningless. Restore the tree, then re-run.", fpBefore, fpAfter)
			printReviewReport(statusReviewUnavailable, verdictIterate,
				"workspace mutated mid-run — no verdict is possible", inputCtx, results, reviewerErrors)
			os.Exit(2)
		}
	}

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

// workspaceFingerprint identifies the exact tree a review judged: the commit,
// plus the porcelain status so uncommitted edits count too.
//
// Why this exists. On 2026-08-08, during a six-slice panel, a review agent
// COMMITTED a file into the branch it was reviewing — `.shadow.py`, an
// `override_gate` stub, placed inside the generated safety-truth package to
// demonstrate a real finding about the drift gate's dot-prefix blind spot. The
// finding was true. But the slices that ran afterwards reported "the reviewed
// SHA is its own counterexample", which was true only because a reviewer had
// made it so. A panel that can edit its own evidence can manufacture a finding,
// and every "reviewed SHA" it prints is unfalsifiable.
//
// This does not PREVENT the write — reviewers need real tool access to do their
// job, and sandboxing them is a larger change. It makes the write DETECTABLE and
// fatal, which is the difference between a contaminated run we know about and
// one we do not. A run whose tree moved is REVIEW_UNAVAILABLE: its findings may
// be real, but they are unattributable, so no verdict may rest on them.
//
// Errors are returned rather than swallowed: "could not fingerprint" and
// "unchanged" are different states, and only one of them is evidence.
func workspaceFingerprint(dir string) (string, error) {
	head, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD: %w", err)
	}
	status, err := gitOutput(dir, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("status --porcelain: %w", err)
	}
	sum := sha256.Sum256([]byte(head + "\x00" + status))
	return fmt.Sprintf("%s+%x", head, sum[:6]), nil
}

func gitOutput(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
	flowDiagram   bool // broad claude seat also emits a mermaid flow/state diagram
	claudeModel   string
	deepseekModel string
	codexModel    string // model for the codex broad seat; empty = codex CLI config default
	codexEffort   string // model_reasoning_effort for codex on high/critical risk (high|xhigh)
	kimiModel     string
	tempDir       string
	cwd           string
	risk          string
	diff          string
	inputCtx      ReviewInputContext
	scoutPreload  string // "all" or "none" (default) — see buildScoutBody
	changeMapOn   bool   // EXPERIMENTAL: generate + inject the change-map (see runClaudeScouts)
	mapModel      string // model for the change-map pass
	changeMap     string // generated orientation map, injected into scout system prompts
	sharedContext bool   // EXPERIMENTAL: shared cached context+diff prefix (see buildScoutBody / runOneScout)
	scoutTiering  bool   // EXPERIMENTAL: soft scouts on softModel, hard scouts on claudeModel
	softModel     string // model for soft scouts when scoutTiering is on
	scoutProvider string // which CLI runs the scout fan-out: "claude" (default) or "grok"
}

// providerTimeout is the per-attempt ceiling for one reviewer. Bake-off data
// (2026-07-16, PRs 1276-1280): claude at --effort xhigh completed medium diffs
// in 8.5-9 min and was killed at exactly 10:00 on the two larger diffs — both
// attempts, wasting 20 min per PR; the claude-scouts long pole (dataflow-spec)
// ran 7m36s, only 2.4 min from the same ceiling. A kill costs the panel a
// whole seat, so these ceilings are sized generously above observed runtimes.
//
// Raised 2026-08-07 after the A-stream slice run re-ran that experiment and
// lost. On ~4.5k-line slices at -risk critical (claude at --effort max, not
// xhigh) two seats died at their ceiling to the second: grok at 10:00 on the
// fsmgen slice, claude at 20:00 on the generated slice. Both panels degraded
// to 4/5 and so could not clear the critical-risk consensus floor of 5 —
// REVIEW_UNAVAILABLE, findings kept but no verdict. The lesson is that a
// ceiling tight enough to bite is worse than a slow panel: it converts a
// complete review into an unusable one. Slice size is deliberately NOT the
// lever here (shrinking slices costs cross-cutting findings that only appear
// when a seat sees a whole subsystem); the ceilings move instead.
func providerTimeout(name string) time.Duration {
	// Raised again 2026-08-08: the post-repair A-stream's `seals` slice is 8.6k
	// lines, up from 5.3k. Operator's standing preference is explicit — buy the
	// seats more time rather than cut the slices, because a slice small enough
	// to finish fast is also small enough to hide the cross-cutting findings
	// that only appear when one seat sees a whole subsystem.
	switch name {
	case "claude-scouts", "grok-scouts":
		// Heavy scouts (dataflow-spec, test-docs) ran 14m on a 4.5k slice.
		return 60 * time.Minute
	case "claude", "deepseek-scouts":
		// --effort max ran ~20m on 4.5k; 8.6k needs room well past that.
		return 70 * time.Minute
	case "codex":
		// codex at xhigh (esp. gpt-5.6-sol) runs long; give one attempt room to
		// COMPLETE rather than being killed early and wastefully retried.
		return 90 * time.Minute
	default:
		// grok/agy/kimi/deepseek broad seats. grok died at the old 10m ceiling.
		return 40 * time.Minute
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

			// The broad `claude` seat and the 7-way `claude-scouts` slot each
			// cover all 8 dimensions and were each surfacing findings the other
			// missed, so both are kept. The old per-seat focused Tier-1 scopes
			// (auth/db/financial/concurrency) were a THIRD pass over dimensions
			// the scouts already own — pure token duplication — and have been
			// dropped. Scouts run those exact traces inside their scopes.
			var resp ReviewResponse
			var err error
			for attempt := 1; attempt <= 2; attempt++ {
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

	if d := firstFlowDiagram(responses); d != "" {
		fmt.Println("## Change Flow Diagram")
		fmt.Println("```mermaid")
		fmt.Println(strings.TrimSpace(d))
		fmt.Println("```")
		fmt.Println()
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
// AND share enough title+problem vocabulary (see exactLineJaccard /
// nearLineJaccard) — proximity alone is not enough, so two different defects
// at one call site stay separate. The representative is the most detailed
// member (longest problem+evidence). Deterministic: responses arrive sorted by
// reviewer name.
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

// Vocabulary thresholds for clustering, graded by how precisely two findings
// agree on WHERE the defect is. An exact line match is much stronger evidence
// of the same defect than "somewhere in this 30-line window", so it buys a
// lower vocabulary bar.
//
// Calibrated on 76 same-file/same-line finding pairs across four real panel
// runs (PR #1416, SMG-4111 r1/r2/r4). At an exact line the 0.15-0.30 band is
// almost entirely genuine duplicates — it contained two findings with
// IDENTICAL titles (Jaccard 0.279) and one defect reported by four seats in
// three separate clusters (0.245-0.281). The nearest true-negative in that
// band sits at 0.208 (a missing tier gate and a pre-lock TOCTOU, two real
// defects on the same call site), so 0.22 separates them.
//
// Raising either number buries corroboration and inflates every seat's
// "unique findings" count. Lowering exactLineJaccard below 0.21 starts
// merging distinct defects that share a call site. Re-run the calibration
// before changing them.
const (
	exactLineJaccard = 0.22
	nearLineJaccard  = 0.30
	exactLineWindow  = 2
	nearLineWindow   = 15
)

// matchesGroup reports whether f is a near-duplicate of an existing cluster:
// same file, within 15 lines of the cluster's observed line RANGE (reviewers
// anchor the same bug at different lines of the same flow — bake-off evidence:
// one TOCTOU reported at 1635/1639/1651), and enough shared vocabulary that
// two different defects at one call site stay separate.
func matchesGroup(g *findingGroup, f Finding) bool {
	if g.Finding.File != f.File {
		return false
	}
	if f.Line < g.minLine-nearLineWindow || f.Line > g.maxLine+nearLineWindow {
		return false
	}
	threshold := nearLineJaccard
	if f.Line >= g.minLine-exactLineWindow && f.Line <= g.maxLine+exactLineWindow {
		threshold = exactLineJaccard
	}
	return jaccard(findingWords(g.Finding), findingWords(f)) >= threshold
}

// findingWords tokenises a finding for the vocabulary comparison. The minimum
// length is 3, not 4: this domain's most discriminating tokens are exactly
// three characters (USD, SQL, PII, KYC, DOB, RTP), and dropping them made
// findings about different currencies look alike while findings about the same
// currency lost their strongest shared signal.
func findingWords(f Finding) map[string]bool {
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(f.Title+" "+f.Problem), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(w) >= 3 {
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
	FlowDiagram string          `json:"flow_diagram,omitempty"`
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
		FlowDiagram: firstFlowDiagram(responses),
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

// firstFlowDiagram returns the first non-empty flow_diagram across seats.
// Only the broad claude seat is instructed to produce one, so "first" is
// effectively "the broad seat's, if any".
func firstFlowDiagram(responses []NamedResponse) string {
	for _, nr := range responses {
		if strings.TrimSpace(nr.Response.FlowDiagram) != "" {
			return nr.Response.FlowDiagram
		}
	}
	return ""
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

// weakModelAddendum is appended to the grok and codex prompts ONLY. On PR-1353
// (N=3, SMG-3966 ground truth) these explicit written-artifact procedures lifted
// grok solo 2/3→3/3 and gpt-5.6-sol/gpt-5.5 to 3/3 on the involved-source leak,
// while the SAME text in the shared role REGRESSED claude-tiered 3/3→1/3 (the
// mandatory artifacts redistribute the stronger model's attention). So it lives
// here, model-scoped, not in reviewer.md. See memory: alt-model-seats-bakeoff.
const weakModelAddendum = `## MANDATORY REVIEW PROCEDURES (in addition to your role — non-optional written artifacts)

Produce these BEFORE any verdict on a money / multi-pool / financial diff:

1. **Fund-return pair audit.** For any path that returns/restores/refunds/adjusts-down/settles stake across pools (won/bought/refund/credited/bonus): grep the parent for every crediting helper (` + "`returnTo*`, `restore*`, `AddTo*`" + `, refund/settle loops) and fill one row each: helper | pool(s) credited | allocation/involved-source row written? (file:line) | same shape as siblings? A helper that credits a pool but writes NO involved-source/allocation row while a sibling DOES = CRITICAL Correctness FAIL (money reclassification), even if one op looks balanced. Name every later reader of that allocation table (refund/settle/adjust/preview) — stale rows they re-read are production money bugs.

2. **N-fold conservation.** For any balance mutation that can legally re-run (cumulative BET_ADJUST, partial refund, repeated restore, resize-after-accept): apply it N≥3× (not once) and write the trace — pool balances + sum(involved by type) + outstanding stake after EACH step. MUST hold every step or CRITICAL: sum(involved) == outstanding stake; no restricted pool (turnover-locked bonus) loses value another pool gains except via the named conversion gate; restricted→withdrawable only via that gate, never as an accounting side effect. A green single-op test or a one-step trace does NOT close this.

3. **Comments are unverified claims.** Never treat a comment/docstring as evidence the code is correct — read the body and confirm the behavior yourself; a comment that contradicts the body is itself a finding AND a breadcrumb to a bug. A fund-return helper whose comment says "drain", "mirror", "same as sibling", "restores involved sources", or "decrements allocation" — open the body and point to the actual write; comment-only with no matching Update*Involved*/equivalent = treat as CRITICAL money bug until you prove that table is never re-read.

4. **Automatic Correctness FAIL:** a financial fund-return/adjust path where pool credits and allocation/involved-source rows can diverge across repeated legal ops (prove conservation under N-fold, procedure 2, or FAIL); sibling fund-return helpers with asymmetric side effects on a table later ops re-read.`

func callLLMProvider(ctx context.Context, provider string, env reviewEnv) (ReviewResponse, error) {
	if provider == "claude-scouts" {
		return runScouts(ctx, env)
	}
	if provider == "grok-scouts" {
		env.scoutProvider = "grok"
		return runScouts(ctx, env)
	}
	if provider == "deepseek-scouts" {
		return runDeepseekScouts(ctx, env)
	}

	var cmd *exec.Cmd

	switch provider {
	case "grok":
		// grok gets the shared prompt + the weak-model addendum (model-scoped).
		grokPromptPath := filepath.Join(env.tempDir, "review-request-grok.md")
		_ = os.WriteFile(grokPromptPath, []byte(env.promptBody+"\n\n---\n\n"+weakModelAddendum), 0644)
		cmd = exec.CommandContext(ctx, "grok",
			"--prompt-file", grokPromptPath,
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
		if env.codexModel != "" {
			codexArgs = append(codexArgs, "-m", env.codexModel)
		}
		if env.risk == "critical" || env.risk == "high" {
			effort := env.codexEffort
			if effort != "xhigh" {
				effort = "high"
			}
			codexArgs = append(codexArgs, "-c", "model_reasoning_effort="+effort)
		}
		codexArgs = append(codexArgs, "-")
		cmd = exec.CommandContext(ctx, "codex", codexArgs...)
		cmd.Dir = env.cwd
		cmd.Stdin = strings.NewReader(env.promptBody + "\n\n---\n\n" + weakModelAddendum)
	case "agy":
		agyPrompt := fmt.Sprintf("Read the role instructions from %s and perform a code review on the diff located at %s. You MUST output your response strictly as a JSON object matching the schema at %s. DO NOT output any conversational text or markdown other than the JSON block.", env.promptPath, filepath.Join(env.tempDir, "review-request.md"), env.schemaPath)
		cmd = exec.CommandContext(ctx, "agy", "--dangerously-skip-permissions", "--print-timeout", "15m", "--print", agyPrompt)
		cmd.Dir = env.cwd
	case "claude":
		// The 52KB reviewer.md rides --append-system-prompt (cached, shared
		// prefix) rather than stdin — the single biggest input-cost lever.
		cmd = claudeCmd(ctx, env.cwd, claudeEffort(env.risk), env.schemaStr, env.roleText, env.claudeModel)
		broadStdin := buildClaudeBroadStdin(env.sharedBody)
		if env.flowDiagram {
			broadStdin = flowDiagramInstruction + "\n\n" + broadStdin
		}
		cmd.Stdin = strings.NewReader(broadStdin)
	case "deepseek":
		cmd = claudeCmd(ctx, env.cwd, claudeEffort(env.risk), env.schemaStr, env.roleText, env.deepseekModel)
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
		_ = os.WriteFile(dumpPathErr, errBuf.Bytes(), 0644)
		_ = os.WriteFile(dumpPathOut, outBuf.Bytes(), 0644)
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
		_ = os.WriteFile(streamPath, outBuf.Bytes(), 0644)
		text, err := kimiTextFromStreamJSON(rawOutput)
		if err != nil {
			return ReviewResponse{}, fmt.Errorf("kimi produced no final assistant message: %v\nRaw stream dumped to %s", err, streamPath)
		}
		rawOutput = text
	}

	dumpPath := filepath.Join(env.tempDir, fmt.Sprintf("%s.raw", provider))
	_ = os.WriteFile(dumpPath, []byte(rawOutput), 0644)
	errDumpPath := filepath.Join(env.tempDir, fmt.Sprintf("%s.err", provider))
	_ = os.WriteFile(errDumpPath, errBuf.Bytes(), 0644)

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
		} else if provider == "claude" || provider == "deepseek" {
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

// flowDiagramInstruction is prepended to the broad claude seat's stdin when
// classify requested a change diagram. Only this seat produces it; every other
// seat leaves flow_diagram empty.
const flowDiagramInstruction = `DIAGRAM REQUEST: this change alters a flow or state machine. In the flow_diagram field, return a mermaid diagram (stateDiagram-v2, sequenceDiagram, or flowchart — pick the one that fits) of the CHANGED behaviour. Rules: show before/after only where they differ, or mark changed transitions with (NEW) / (REMOVED); use the code's real state and message names; 15 nodes maximum — collapse anything that did not change; no styling directives. Return ONLY the mermaid source in the field, no fences. Leave the field empty if the change genuinely has no flow to draw.`

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
func buildSharedBody(inputCtx ReviewInputContext, diff string, filter preloadFilter) string {
	preloaded := preloadSourceFiles(inputCtx.Worktree, inputCtx.ChangedFiles, nil)
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

// stripGoComments removes Go-style line and block comments from source data,
// collapsing runs of blank lines. It is a lossy preload-only scanner —
// reviewers still read the real file with tools when they need precision.
func stripGoComments(data []byte) []byte {
	reLine := regexp.MustCompile(`//.*`)
	data = reLine.ReplaceAll(data, nil)
	reBlock := regexp.MustCompile(`/\*[\s\S]*?\*/`)
	data = reBlock.ReplaceAll(data, nil)
	reBlank := regexp.MustCompile(`\n{3,}`)
	data = reBlank.ReplaceAll(data, []byte("\n\n"))
	return bytes.TrimSpace(data)
}

// preloadFilter is a predicate that decides whether a file path should be
// preloaded. nil means "preload everything preloadable."
type preloadFilter func(path string) bool

// preloadSourceFiles reads the full contents of high-signal changed source files
// (new or modified, excluding lockfiles, generated code, test fixtures, and
// deleted files). Each file is truncated at 64KB, total output capped at 512KB.
// This lets reviewers skip file-reading tool turns and go straight to analysis.
func preloadSourceFiles(cwd string, files []ChangedFile, filter preloadFilter) string {
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
		if filter != nil && !filter(f.Path) {
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

		if strings.HasSuffix(f.Path, ".go") {
			data = stripGoComments(data)
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

	// Skip mock directories and generated code
	if strings.Contains(path, "/mock/") || strings.Contains(path, "/mocks/") {
		return false
	}
	genSuffixes := []string{"_pb.ts", "_pb.d.ts", ".gen.go", ".generated.ts", ".pb.go", ".pb.gw.go", ".pb.validate.go", ".sqlc.go"}
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
			"quality_score": intSchema,
			"flow_diagram": map[string]any{
				"type":        "string",
				"description": "Mermaid source for a change-flow diagram. Return \"\" unless your instructions explicitly request a diagram.",
			},
			"test_quality_assessment": stringSchema,
			"design_coherence":        stringSchema,
			"data_flow_trace":         stringSchema,
			"findings": map[string]any{
				"type":  "array",
				"items": findingSchema(),
			},
		},
		[]string{"status", "verdict", "summary", "critical_dimensions", "quality_scores", "quality_score", "flow_diagram", "test_quality_assessment", "design_coherence", "data_flow_trace", "findings"},
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

	return "", "", fmt.Errorf("%s", strings.Join(errs, "; "))
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

// grokEffort maps risk tier to grok --reasoning-effort. The grok CLI only
// accepts high|medium|low (it rejects xhigh outright), so high is grok's
// ceiling — Critical does not get an extra grok tier the way the claude seat
// does. Passing xhigh here silently dropped the grok seat from every Critical
// panel until this was clamped.
func grokEffort(risk string) string {
	return "high"
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
		model = "claude-opus-5"
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
	}
	if schemaStr != "" {
		args = append(args, "--json-schema", schemaStr)
	}
	if systemPrompt != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
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
	_ = os.MkdirAll(skillsDir, 0755)
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

// buildScoutBody constructs the full prompt body for a single scout:
// shared metadata + scope-filtered preloaded files + diff.
// buildScoutBody assembles a scout's prompt body. Coverage is deliberately NOT
// keyed on guessed path keywords: a static per-scout keyword filter silently
// under-covers any new package whose name doesn't match (it returns a healthy
// non-zero set that just omits the new files, so a "fall back when empty" guard
// never fires). Instead every scout ALWAYS receives the full Changed Files
// table and the full diff, so it sees the entire change surface and can
// tool-read any file. scoutPreload only tunes how much file CONTENT is pasted
// up front: "all" pastes every high-signal changed file (skip tool-reads, more
// input tokens); "none" pastes nothing (scout reads what it needs, fewer input
// tokens, more tool turns). This is the cost A/B — neither mode can drop a file
// from the scout's awareness.
func buildScoutBody(env reviewEnv, s scout) string {
	if env.sharedContext {
		return "" // context + diff ride the shared cached system prompt instead
	}
	return buildSharedContextBlock(env)
}

// buildSharedContextBlock is the review metadata + changed-file table +
// precomputed sibling trace + (optional) preload + diff — all IDENTICAL across
// scouts. In -shared-context mode it rides the cached system-prompt prefix (paid
// once + N-1 cheap cache-reads); otherwise each scout gets its own copy in stdin.
func buildSharedContextBlock(env reviewEnv) string {
	preloaded := ""
	if env.scoutPreload != "none" {
		preloaded = preloadSourceFiles(env.cwd, env.inputCtx.ChangedFiles, nil)
	}
	return fmt.Sprintf("%s\n\n%s\n\n## Review Request\n\n### Diff\n%s",
		formatReviewContext(env.inputCtx), preloaded, env.diff)
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
	model  string // per-scout model override (deepseek-v4-pro or deepseek-v4-flash)
	// roleSections are the reviewer.md heading prefixes this scout needs. Only
	// these sections (plus commonRoleSections) are sent to the scout instead of
	// the full 52KB role — each scout uses ~1/7 of it, so slicing cuts the
	// dominant input cost. Matched by exact-or-prefix against heading titles.
	roleSections []string
	// soft marks a scout that owns NO money/correctness/security verdict (only
	// quality scores), so -scout-tiering may run it on the cheaper soft-model.
	// The money/correctness/security scouts (incl. concurrency-resilience, since
	// races are correctness-grade) are NOT soft and stay on the main model.
	soft bool
}

// scoutModel is the effective claude model for a scout: the cheaper soft-model
// when tiering is on and the scout is soft, else the main claude model.
func scoutModel(env reviewEnv, s scout) string {
	if env.scoutTiering && s.soft {
		return env.softModel
	}
	return env.claudeModel
}

// commonRoleSections are the reviewer.md sections every scout needs regardless
// of scope: framing, how to grade severity, and how to turn dimension results
// into a verdict. Prepended to each scout's own sections.
var commonRoleSections = []string{
	"Mindset",
	"Severity Calibration",
	"Verdict Rules",
	"Critical Dimension Judgment Calls",
	"Common Review Mistakes",
	"Output Constraints",
}

var reviewScouts = []scout{
	{
		name:         "dataflow-spec",
		dims:         []string{"correctness"},
		extras:       []string{"data_flow_trace"},
		scope:        "Correctness from the logic side: spec-vs-implementation and the seams between components. Apply Steps 1, 2, and 2.5 of your role in full (Step 2.5 covers siblings, callers two layers up, and the removed-cap / asymmetric-op reads). Record your trace in the data_flow_trace field. You own the Correctness pass/fail.",
		model:        "deepseek-v4-pro",
		roleSections: []string{"Step 1:", "Step 2:", "Step 2.5:", "1. Correctness"},
	},
	{
		// Money & data integrity — merged from the former db-compliance and
		// financial-fairness scouts, which read the SAME wallet/ledger surface
		// and filed duplicate findings (both flagged the SMG-3966 leak). One
		// agent owning both dimensions removes a redundant opus seat and the
		// duplicate file-reading, and still reports a separate pass/fail per dim.
		name:         "integrity",
		dims:         []string{"compliance", "exploitability"},
		scope:        "Money & data integrity. Apply the '3. Compliance' and '4. Exploitability & Fairness' sections of your role in full. You own BOTH pass/fail verdicts — report a separate result for each.",
		model:        "deepseek-v4-pro",
		roleSections: []string{"3. Compliance", "4. Exploitability"},
	},
	{
		name:         "auth-security",
		dims:         []string{"security"},
		scope:        "Security & authorization. Apply the '2. Security' section of your role in full. You own the Security pass/fail.",
		model:        "deepseek-v4-pro",
		roleSections: []string{"2. Security"},
	},
	{
		name:         "concurrency-resilience",
		scores:       []string{"resilience", "idempotency"},
		scope:        "Concurrency, idempotency, and resilience. Apply the Resilience and Idempotency sections of your role and score each 1-5 per the anchors. One emphasis: a data race is a Correctness-grade defect — report it CRITICAL/HIGH regardless of how unlikely the interleaving looks.",
		model:        "deepseek-v4-flash",
		roleSections: []string{"4. Resilience", "5. Idempotency"},
	},
	{
		name:         "test-docs",
		dims:         []string{"correctness"},
		extras:       []string{"test_quality_assessment"},
		scope:        "Test quality and docs-truth. Apply Step 4 (test quality) and Step 4.5 (docs-truth) of your role in full. Record your assessment in the test_quality_assessment field. You own the Correctness pass/fail from the testing side: a spec'd edge case with no test, or a test that passes without proving correctness, is a Correctness FAIL.",
		model:        "deepseek-v4-flash",
		roleSections: []string{"Step 4:", "Step 4.5:", "1. Correctness"},
	},
	{
		name:         "quality-scores",
		scores:       []string{"observability", "performance", "maintainability"},
		extras:       []string{"design_coherence"},
		scope:        "Observability, Performance, and Maintainability: score each 1-5 per the anchors in your role's dimension sections. Also judge Design Coherence — does this change fit the system's existing patterns — and summarize it in the design_coherence field.",
		model:        "deepseek-v4-flash",
		roleSections: []string{"6. Observability", "7. Performance", "8. Maintainability", "Design Coherence"},
		soft:         true, // only quality scores; safe to run on the cheap tier
	},
}

// headingLevel reports the ATX heading level of a markdown line (1 for "# ",
// 2 for "## ", …) and the trimmed title text. ok is false for non-heading
// lines. A run of '#'s must be followed by a space to count as a heading.
func headingLevel(line string) (level int, title string, ok bool) {
	t := strings.TrimRight(line, " \t")
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(t) || t[n] != ' ' {
		return 0, "", false
	}
	return n, strings.TrimSpace(t[n+1:]), true
}

// extractRoleSlice returns only the reviewer.md sections whose heading title
// exactly-equals or is prefixed by one of `wanted`, each captured through the
// line before the next heading at the same-or-higher level (so an H2 pulls its
// H3 children). Sections are emitted in document order. A `wanted` entry that
// matches nothing is silently skipped — TestScoutRoleSlices guards against that
// so a reviewer.md rename fails the build, not a live review.
func extractRoleSlice(role string, wanted []string) string {
	lines := strings.Split(role, "\n")
	type hd struct {
		line, level int
		title       string
	}
	var hds []hd
	for i, ln := range lines {
		if lvl, title, ok := headingLevel(ln); ok {
			hds = append(hds, hd{i, lvl, title})
		}
	}
	matches := func(title string) bool {
		for _, w := range wanted {
			if title == w || strings.HasPrefix(title, w) {
				return true
			}
		}
		return false
	}
	var b strings.Builder
	for hi, h := range hds {
		if !matches(h.title) {
			continue
		}
		end := len(lines)
		for _, h2 := range hds[hi+1:] {
			if h2.level <= h.level {
				end = h2.line
				break
			}
		}
		for _, ln := range lines[h.line:end] {
			b.WriteString(ln)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

// scoutRole builds the sliced reviewer.md a single scout receives: the shared
// common sections plus the scout's own. Falls back to the full role if slicing
// somehow yields nothing (defensive — never send a scout an empty role).
func scoutRole(role string, s scout) string {
	slice := extractRoleSlice(role, append(append([]string{}, commonRoleSections...), s.roleSections...))
	if strings.TrimSpace(slice) == "" {
		return role
	}
	return "# Code Reviewer Role — scoped slice for the " + s.name + " scout\n\n" + slice
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

// buildChangeMap runs one cheap map-model pass over the diff to produce a
// per-file orientation map for the scouts. It ORIENTS only — the prompt is
// explicit that scouts read the code themselves for verdicts, so a lossy or
// slightly-stale summary can't silently drive a PASS/FAIL. The raw envelope is
// written to tempDir/change-map.raw for metric extraction. Failure is
// non-fatal: the caller proceeds without a map.
func buildChangeMap(ctx context.Context, env reviewEnv) (string, error) {
	prompt := fmt.Sprintf(`You are a fast pre-reviewer producing an ORIENTATION MAP for a panel of specialist code-review scouts. For each file in the diff below, output one or two terse lines: what the file does and where the risk / entry points are (money, auth, concurrency, migrations, tests). Keep the WHOLE map short. This map only helps the scouts decide what to read — they will read the code themselves for their verdicts, so do not editorialize or make judgments. You may read a file with your tools if a hunk is ambiguous, but do not over-investigate: one quick pass.

Output a markdown list keyed by file path. Final message = the map only.

## Changed Files
%s

## Diff
%s`, changedFilesList(env.inputCtx), env.diff)

	cmd := claudeCmd(ctx, env.cwd, "medium", "", "", env.mapModel)
	cmd.Stdin = strings.NewReader(prompt)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	runErr := cmd.Run()
	_ = os.WriteFile(filepath.Join(env.tempDir, "change-map.raw"), outBuf.Bytes(), 0644)
	_ = os.WriteFile(filepath.Join(env.tempDir, "change-map.err"), errBuf.Bytes(), 0644)
	if runErr != nil {
		return "", fmt.Errorf("change-map pass failed: %v", runErr)
	}
	return strings.TrimSpace(normalizeProviderJSON("claude", outBuf.String())), nil
}

// changedFilesList renders a compact "path (status)" list for the map prompt.
func changedFilesList(ctx ReviewInputContext) string {
	var b strings.Builder
	for _, f := range ctx.ChangedFiles {
		fmt.Fprintf(&b, "- %s (%s)\n", f.Path, f.Status)
	}
	return b.String()
}

// logScoutContribution emits per-scout telemetry: how many findings each scout
// produced and how many were UNIQUE (no other scout cited the same file:line).
// A scout that consistently contributes few unique findings across many PRs is
// a merge/removal candidate — this is the data behind decisions like folding
// db-compliance + financial-fairness into `integrity`. Uniqueness is by exact
// file:line, so near-duplicates count as unique — it under-reports overlap, a
// deliberately conservative bias against over-merging.
type scoutContribution struct {
	name          string
	total, unique int
}

// scoutContributions is the pure core of logScoutContribution: per scout, how
// many findings it produced and how many were unique (sole owner of that
// file:line across the set).
func scoutContributions(outputs []scoutOutput) []scoutContribution {
	type loc struct {
		file string
		line int
	}
	owners := map[loc]int{}
	for _, out := range outputs {
		for _, f := range out.Findings {
			owners[loc{f.File, f.Line}]++
		}
	}
	out := make([]scoutContribution, 0, len(outputs))
	for i, o := range outputs {
		unique := 0
		for _, f := range o.Findings {
			if owners[loc{f.File, f.Line}] == 1 {
				unique++
			}
		}
		name := ""
		if i < len(reviewScouts) {
			name = reviewScouts[i].name
		}
		out = append(out, scoutContribution{name: name, total: len(o.Findings), unique: unique})
	}
	return out
}

func logScoutContribution(outputs []scoutOutput) {
	for _, c := range scoutContributions(outputs) {
		log.Printf("scout-contribution %s: %d finding(s), %d unique\n", c.name, c.total, c.unique)
	}
}

func runScouts(ctx context.Context, env reviewEnv) (ReviewResponse, error) {
	prov := env.scoutProvider
	if prov == "" {
		prov = "claude"
	}
	// Optional orientation pass: one cheap map-model call, serial before the
	// fan-out (scouts need it), injected as a shared cached prefix by runOneScout.
	if env.changeMapOn {
		mapCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
		m, err := buildChangeMap(mapCtx, env)
		cancel()
		if err != nil {
			log.Printf("change-map pass failed (proceeding without it): %v\n", err)
		} else {
			env.changeMap = m
			log.Printf("change-map ready (%d bytes) — injecting into all %d scouts\n", len(m), len(reviewScouts))
		}
	}

	log.Printf("%s-scouts: dispatching %d scouts in parallel\n", prov, len(reviewScouts))

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
				// The first attempt gets a generous window so the heavy scouts
				// (dataflow-spec, test-docs) COMPLETE rather than being killed
				// at a tight ceiling; the retry is short because it only exists
				// for TRANSIENT failures (a killed scout wastes all its tokens).
				//
				// Raised 2026-08-08 from 18m/6m. This is the INNER budget and it
				// is easy to miss: providerTimeout() above is the outer ceiling
				// for the whole fan-out, and raising that alone does nothing for
				// a single slow scout. On the round-2 fsmgen slice (5.8k lines)
				// dataflow-spec — the heaviest scout, and the one that produced
				// the most CRITICALs in round 1 — was killed at exactly 18:00
				// while the provider ceiling sat at 60m, unused. Keep
				// first+retry comfortably under providerTimeout("claude-scouts").
				d := 40 * time.Minute
				if attempt == 2 {
					d = 10 * time.Minute
				}
				attemptCtx, cancel := context.WithTimeout(ctx, d)
				out, err = runOneScout(attemptCtx, s, env)
				timedOut := attemptCtx.Err() != nil // deadline or provider-ctx cancel
				cancel()
				if err == nil {
					break
				}
				// A timeout means the scout was working and ran out of clock —
				// retrying from scratch just burns another long window to time
				// out again. Only a transient error (exit 1, parse) earns a retry.
				if timedOut {
					log.Printf("scout %s timed out after %s; not retrying (would repeat) — degrading\n", s.name, d)
					break
				}
				if attempt == 1 {
					log.Printf("scout %s failed (transient); retrying once: %v\n", s.name, err)
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

	logScoutContribution(outputs)

	var failed []string
	for i, e := range errs {
		if e != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", reviewScouts[i].name, e))
		}
	}
	// Degrade, don't fail closed: one slow scout (the long-pole dataflow-spec /
	// test-docs scouts are the ones that time out under a heavier model) used to
	// error the WHOLE slot, discarding the other six good scouts. Now a failed
	// scout's owned dimensions are marked UNVERIFIED and the verdict is held
	// below APPROVE (fail-safe) with a loud coverage-gap finding — the six that
	// completed still count. Only a total wipe-out fails the slot.
	if len(failed) == len(reviewScouts) {
		return ReviewResponse{}, fmt.Errorf("%s-scouts: all %d scouts failed: %s", prov, len(reviewScouts), strings.Join(failed, "; "))
	}
	if len(failed) > 0 {
		log.Printf("%s-scouts: %d/%d scouts failed — degrading (dimensions flagged UNVERIFIED, not silently dropped): %s", prov, len(failed), len(reviewScouts), strings.Join(failed, "; "))
	}

	return reduceScoutResultsWithStatus(outputs, errs)
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

	scopedBody := buildScoutBody(env, s)
	if env.sharedContext {
		scopedBody = "The review context (metadata, changed-file table, precomputed sibling trace) and the full diff are in your SYSTEM PROMPT, above your role instructions — read them there."
	}
	prompt := fmt.Sprintf(`You are one focused scout in a decomposed code review panel. Your scope is strictly: %s.

%s

Your review instructions — the reviewer-role sections for your scope, with scoring anchors, the Severity Calibration table, and verdict rules — are in your system prompt. Apply them exactly. Do NOT edit any files.

Apply ONLY the sections that fall inside your scope — other scouts own the rest. Do not report findings outside your scope. For pass/fail dimensions you own, apply the role's PASS checklists literally with no partial credit. For scores you own, use the role's anchors. Rate severity per the Severity Calibration table; set blocking=true only on CRITICAL/HIGH findings.

Use your tools: the Precomputed Sibling Surface Trace is already in your context — start from it and grep only to extend it, don't re-derive it. Read the changed files in full and the repository's CLAUDE.md for conventions. Do NOT edit any files. Do the deep analysis in intermediate turns — your FINAL message must be ONLY a raw JSON object matching the provided schema.

%s`, s.name, s.scope, scopedBody)

	// Shared, identical-across-scouts content goes FIRST in the system prompt so
	// it forms a cacheable prefix (change-map, then the shared context+diff);
	// the per-scout role slice follows.
	sysPrompt := scoutRole(env.roleText, s)
	if env.sharedContext {
		sysPrompt = buildSharedContextBlock(env) + "\n\n---\n\n" + sysPrompt
	}
	if env.changeMap != "" {
		sysPrompt = "## Change Map (orientation only — decide what to read; read the code yourself for verdicts)\n\n" + env.changeMap + "\n\n" + sysPrompt
	}

	prov := env.scoutProvider
	if prov == "" {
		prov = "claude"
	}

	var cmd *exec.Cmd
	if prov == "grok" {
		// grok takes the scoped body from a file and the sliced role via
		// --system-prompt-override (mirrors the broad grok seat's flags).
		// grok effort is clamped to high (the CLI rejects xhigh); scouts on
		// critical would ask for xhigh but grok tops out at high.
		grokPromptPath := filepath.Join(env.tempDir, fmt.Sprintf("grok-scout-%s.prompt", s.name))
		_ = os.WriteFile(grokPromptPath, []byte(prompt), 0644)
		cmd = exec.CommandContext(ctx, "grok",
			"--prompt-file", grokPromptPath,
			"--system-prompt-override", sysPrompt,
			"--cwd", env.cwd,
			"--always-approve",
			"--tools", "read_file,grep,list_dir",
			"--disallowed-tools", "search_replace,write",
			"--max-turns", "50",
			"--reasoning-effort", grokEffort(env.risk),
			"--json-schema", string(schemaBytes),
			"--output-format", "json",
		)
	} else {
		cmd = claudeCmd(ctx, env.cwd, effort, string(schemaBytes), sysPrompt, scoutModel(env, s))
		cmd.Stdin = strings.NewReader(prompt)
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	_ = os.WriteFile(filepath.Join(env.tempDir, fmt.Sprintf("%s-scout-%s.raw", prov, s.name)), outBuf.Bytes(), 0644)
	_ = os.WriteFile(filepath.Join(env.tempDir, fmt.Sprintf("%s-scout-%s.err", prov, s.name)), errBuf.Bytes(), 0644)
	if runErr != nil {
		return scoutOutput{}, fmt.Errorf("scout %s failed: %v (raw output in %s)", s.name, runErr, env.tempDir)
	}

	rawJSON := normalizeProviderJSON(prov, outBuf.String())
	var out scoutOutput
	if err := json.Unmarshal([]byte(rawJSON), &out); err != nil {
		return scoutOutput{}, fmt.Errorf("failed to parse scout %s JSON: %v (raw output in %s)", s.name, err, env.tempDir)
	}
	for i := range out.Findings {
		out.Findings[i].Source = fmt.Sprintf("scout: %s", s.name)
	}
	return out, nil
}

// sortedKeys returns the keys of a set in deterministic order (report stability).
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// reduceScoutResults is the deterministic reduce over a fully-successful scout
// set (used by tests and callers that have no per-scout error state). It holds
// no opinions, only applies reviewer.md's verdict rules to what the scouts
// reported. It cannot drop or downgrade a finding.
func reduceScoutResults(outputs []scoutOutput) (ReviewResponse, error) {
	return reduceScoutResultsWithStatus(outputs, make([]error, len(outputs)))
}

// reduceScoutResultsWithStatus is the deterministic reduce with degrade
// semantics: errs[i] != nil means scout i failed (timed out / errored after
// retries). A failed scout's owned dimensions are marked UNVERIFIED and held
// FAIL (fail-safe), its owned scores forced below floor, and a loud
// coverage-gap finding is emitted — but the scouts that DID complete are still
// reduced normally. Unverified alone never yields REJECT (the code isn't proven
// bad, just unproven); it holds the verdict at REQUEST_CHANGES for a human.
func reduceScoutResultsWithStatus(outputs []scoutOutput, errs []error) (ReviewResponse, error) {
	var resp ReviewResponse
	resp.Status = statusReviewComplete

	dimPass := map[string]bool{}
	dimNotes := map[string][]string{}
	scoreMin := map[string]int{}

	var summaries []string
	for i, out := range outputs {
		s := reviewScouts[i]
		if i < len(errs) && errs[i] != nil {
			summaries = append(summaries, fmt.Sprintf("[%s] ⚠ UNVERIFIED — scout failed/timed out", s.name))
			continue // don't fold a zero-value output into the aggregate
		}
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

	// Degrade failed scouts: for each dimension/score a failed scout owned that
	// NO surviving scout covered, mark it unverified (fail-safe) rather than
	// leaving a silent gap. A dimension co-owned by a scout that DID complete
	// (e.g. correctness: dataflow-spec + test-docs) stays verified.
	degradedDims := map[string]bool{}
	degradedScores := map[string]bool{}
	var degradedScouts []string
	for i, e := range errs {
		if e == nil || i >= len(reviewScouts) {
			continue
		}
		s := reviewScouts[i]
		degradedScouts = append(degradedScouts, s.name)
		for _, d := range s.dims {
			if _, ok := dimPass[d]; !ok {
				dimPass[d] = false
				degradedDims[d] = true
				dimNotes[d] = append(dimNotes[d], fmt.Sprintf("[%s] UNVERIFIED — owning scout failed/timed out; treated as FAIL pending human review", s.name))
			}
		}
		for _, sc := range s.scores {
			if _, ok := scoreMin[sc]; !ok {
				scoreMin[sc] = 0 // below floor → blocks APPROVE
				degradedScores[sc] = true
			}
		}
	}

	// Fail closed on coverage gaps: every dimension and score must have at
	// least one owner (a completed report OR a degraded placeholder). A gap
	// here means a scout completed but silently omitted a dimension it owns.
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

	unverified := len(degradedDims) > 0 || len(degradedScores) > 0
	if unverified {
		resp.Findings = append(resp.Findings, Finding{
			Severity: "HIGH",
			Title:    "Review coverage gap — scout(s) failed",
			Problem: fmt.Sprintf("%d of %d scouts failed or timed out (%s). Unverified dimensions: [%s]; unverified scores: [%s]. These were held FAIL/0 as a fail-safe — a human must review the affected areas before this can APPROVE.",
				len(degradedScouts), len(reviewScouts), strings.Join(degradedScouts, ", "), strings.Join(sortedKeys(degradedDims), ", "), strings.Join(sortedKeys(degradedScores), ", ")),
			Source:   "claude-scouts (degraded)",
			Blocking: true,
		})
	}

	// Genuine reported FAILs (not degraded placeholders) drive REJECT. An
	// unverified dimension holds the verdict at REQUEST_CHANGES, never REJECT.
	failCount := 0
	for dim, pass := range dimPass {
		if !pass && !degradedDims[dim] {
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
	for score, v := range scoreMin {
		if v < 4 && !degradedScores[score] {
			lowScore = true
			break
		}
	}
	switch {
	case failCount >= 2:
		resp.Verdict = verdictReject
	case failCount == 1 || blocking || lowScore || unverified:
		resp.Verdict = verdictRequestChanges
	default:
		resp.Verdict = verdictApprove
	}

	degradeNote := ""
	if unverified {
		degradeNote = fmt.Sprintf(" ⚠ DEGRADED: %d/%d scouts failed — verdict held below APPROVE pending human review of unverified areas.", len(degradedScouts), len(reviewScouts))
	}
	resp.Summary = fmt.Sprintf("Decomposed review: %d parallel scouts, verdict computed mechanically (dimension = AND of owners, score = min of owners, reviewer.md verdict rules).%s\n%s",
		len(outputs), degradeNote, strings.Join(summaries, "\n"))

	return resp, nil
}

// --- deepseek-scouts: decomposed review via claude CLI (DeepSeek model) -----
//
// The "deepseek-scouts" provider mirrors claude-scouts but dispatches scouts
// through the claude CLI with a DeepSeek model (deepseek-v4-pro or
// deepseek-v4-flash) instead of a Claude model. Each scout gets its own model
// via the scout.model field: deepseek-v4-pro for hard dimensions
// (dataflow-spec, auth-security, financial-fairness, db-compliance) and
// deepseek-v4-flash for the rest.
//
// The deterministic reduce (reduceScoutResults) is shared with claude-scouts
// — dimension = AND of owners, score = min of owners, verdict = reviewer.md
// mechanical rules. No LLM sits in the merge path.

func runDeepseekScouts(ctx context.Context, env reviewEnv) (ReviewResponse, error) {
	log.Printf("deepseek-scouts: dispatching %d scouts in parallel (via claude CLI + DeepSeek models)\n", len(reviewScouts))

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
				attemptCtx, cancel := context.WithTimeout(ctx, 12*time.Minute)
				out, err = runOneDeepseekScout(attemptCtx, s, env)
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
	if len(failed) == len(reviewScouts) {
		return ReviewResponse{}, fmt.Errorf("deepseek-scouts: all %d scouts failed: %s", len(reviewScouts), strings.Join(failed, "; "))
	}
	if len(failed) > 0 {
		log.Printf("deepseek-scouts: %d/%d scouts failed — degrading (dimensions flagged UNVERIFIED): %s", len(failed), len(reviewScouts), strings.Join(failed, "; "))
	}

	return reduceScoutResultsWithStatus(outputs, errs)
}

func runOneDeepseekScout(ctx context.Context, s scout, env reviewEnv) (scoutOutput, error) {
	model := s.model
	if model == "" {
		model = "deepseek-v4-flash"
	}

	effort := "high"
	if env.risk == "critical" {
		effort = "xhigh"
	}

	schemaBytes, err := json.Marshal(scoutSchema(s))
	if err != nil {
		return scoutOutput{}, fmt.Errorf("failed to marshal scout schema: %w", err)
	}

	scopedBody := buildScoutBody(env, s)
	prompt := fmt.Sprintf(`You are one focused scout in a decomposed code review panel. Your scope is strictly: %s.

%s

Your review instructions — the reviewer-role sections for your scope, with scoring anchors, the Severity Calibration table, and verdict rules — are in your system prompt. Apply them exactly. Do NOT edit any files.

Apply ONLY the sections that fall inside your scope — other scouts own the rest. Do not report findings outside your scope. For pass/fail dimensions you own, apply the role's PASS checklists literally with no partial credit. For scores you own, use the role's anchors. Rate severity per the Severity Calibration table; set blocking=true only on CRITICAL/HIGH findings.

Use your tools: read the changed files in full, grep sibling call sites, and list directories to explore the codebase. Do NOT edit any files. Do the deep analysis in intermediate turns — your FINAL message must be ONLY a raw JSON object matching the provided schema.

%s`, s.name, s.scope, scopedBody)

	cmd := claudeCmd(ctx, env.cwd, effort, string(schemaBytes), scoutRole(env.roleText, s), model)
	cmd.Stdin = strings.NewReader(prompt)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	_ = os.WriteFile(filepath.Join(env.tempDir, fmt.Sprintf("deepseek-scout-%s.raw", s.name)), outBuf.Bytes(), 0644)
	_ = os.WriteFile(filepath.Join(env.tempDir, fmt.Sprintf("deepseek-scout-%s.err", s.name)), errBuf.Bytes(), 0644)
	if runErr != nil {
		return scoutOutput{}, fmt.Errorf("deepseek scout %s failed: %v (raw output in %s)", s.name, runErr, env.tempDir)
	}

	rawJSON := normalizeProviderJSON("claude", outBuf.String())
	var out scoutOutput
	if err := json.Unmarshal([]byte(rawJSON), &out); err != nil {
		return scoutOutput{}, fmt.Errorf("failed to parse deepseek scout %s JSON: %v (raw output in %s)", s.name, err, env.tempDir)
	}
	for i := range out.Findings {
		out.Findings[i].Source = fmt.Sprintf("deepseek-scout: %s", s.name)
	}
	return out, nil
}
