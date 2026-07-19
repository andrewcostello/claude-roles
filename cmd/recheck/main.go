// Command recheck is the targeted-verification counterpart to cmd/reviewer,
// used for iteration rounds 3+ (iteration-protocol.md "Two-Tier Re-Review").
//
// Where the reviewer is an independent full audit that must NOT see prior
// findings, recheck is the opposite machine: it takes the reviewer's
// -findings-out JSON, shows a verifier each prior CRITICAL/HIGH finding plus
// the diff since that review, and demands a per-finding status — RESOLVED /
// STILL_OPEN / REGRESSED — with evidence, plus any NEW CRITICAL/HIGH strictly
// within the files changed since the prior review. The verdict is computed
// mechanically per the convergence rules:
//
//	exit 0 APPROVE   — all prior findings RESOLVED, no new CRITICAL/HIGH
//	exit 1 ITERATE   — all prior RESOLVED, new findings below -max-new
//	exit 2 ESCALATE  — any STILL_OPEN/REGRESSED, or new findings >= -max-new
//	exit 3 error     — bad input, verifier failure, git failure
//
// Usage:
//
//	recheck -worktree <path> -findings /tmp/findings-TASK-r2.json \
//	        -risk high [-max-new 2] [-runs-dir /tmp]
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// FindingsExport mirrors cmd/reviewer's export contract.
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

type verification struct {
	Index    int    `json:"index"`
	Status   string `json:"status"` // RESOLVED | STILL_OPEN | REGRESSED
	Evidence string `json:"evidence"`
}

type newFinding struct {
	Severity string `json:"severity"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Title    string `json:"title"`
	Problem  string `json:"problem"`
	Evidence string `json:"evidence"`
	Fix      string `json:"fix"`
	Blocking bool   `json:"blocking"`
}

type verifierOutput struct {
	Verifications []verification `json:"verifications"`
	NewFindings   []newFinding   `json:"new_findings"`
	Summary       string         `json:"summary"`
}

func main() {
	worktree := flag.String("worktree", "", "Worktree holding the fix commits (required)")
	findingsPath := flag.String("findings", "", "Path to cmd/reviewer's -findings-out JSON from the prior round (required)")
	risk := flag.String("risk", "", "Risk tier (default: taken from the findings file)")
	maxNew := flag.Int("max-new", 0, "Escalate if NEW CRITICAL/HIGH count is >= this (0 = any new finding escalates; pass prior round's new-count minus 1 to enforce strict decrease)")
	flag.Parse()

	if *worktree == "" || *findingsPath == "" {
		fmt.Fprintln(os.Stderr, "-worktree and -findings are required")
		os.Exit(3)
	}

	data, err := os.ReadFile(*findingsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read findings file: %v\n", err)
		os.Exit(3)
	}
	var export FindingsExport
	if err := json.Unmarshal(data, &export); err != nil {
		fmt.Fprintf(os.Stderr, "cannot parse findings file: %v\n", err)
		os.Exit(3)
	}
	tier := export.Risk
	if *risk != "" {
		tier = *risk
	}

	open := criticalHigh(export.Findings)
	if len(open) == 0 {
		fmt.Println("No CRITICAL/HIGH findings in the prior round — nothing to verify. APPROVE.")
		os.Exit(0)
	}

	iterDiff, changedFiles, headSHA, err := iterationDiff(*worktree, export.ReviewedSHA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot compute iteration diff since %s: %v\n", export.ReviewedSHA, err)
		os.Exit(3)
	}
	if strings.TrimSpace(iterDiff) == "" {
		fmt.Fprintf(os.Stderr, "no commits since the reviewed SHA %s — there is no fix to verify\n", short(export.ReviewedSHA))
		os.Exit(3)
	}

	log.Printf("Verifying %d prior CRITICAL/HIGH finding(s) against %d changed file(s) (%s..%s)",
		len(open), len(changedFiles), short(export.ReviewedSHA), short(headSHA))

	out, err := runVerifier(*worktree, tier, open, iterDiff, changedFiles)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verifier failed: %v\n", err)
		os.Exit(3)
	}

	verdict, code := computeVerdict(open, out, *maxNew)
	printReport(export, out, open, verdict, headSHA, changedFiles)
	os.Exit(code)
}

func criticalHigh(findings []ExportFinding) []ExportFinding {
	var out []ExportFinding
	for _, f := range findings {
		if f.Severity == "CRITICAL" || f.Severity == "HIGH" || f.Blocking {
			out = append(out, f)
		}
	}
	return out
}

func iterationDiff(worktree, reviewedSHA string) (diff string, files []string, head string, err error) {
	headOut, err := gitOut(worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", nil, "", err
	}
	head = strings.TrimSpace(headOut)
	diff, err = gitOut(worktree, "diff", reviewedSHA+"..HEAD")
	if err != nil {
		return "", nil, "", err
	}
	names, err := gitOut(worktree, "diff", "--name-only", reviewedSHA+"..HEAD")
	if err != nil {
		return "", nil, "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(names), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return diff, files, head, nil
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

// --- verifier dispatch --------------------------------------------------------

func verifierSchema(findingCount int) map[string]any {
	str := map[string]any{"type": "string"}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"verifications": map[string]any{
				"type":     "array",
				"minItems": findingCount,
				"maxItems": findingCount,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"index":    map[string]any{"type": "integer"},
						"status":   map[string]any{"type": "string", "enum": []string{"RESOLVED", "STILL_OPEN", "REGRESSED"}},
						"evidence": str,
					},
					"required": []string{"index", "status", "evidence"},
				},
			},
			"new_findings": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"severity": map[string]any{"type": "string", "enum": []string{"CRITICAL", "HIGH", "MEDIUM", "LOW"}},
						"file":     str, "line": map[string]any{"type": "integer"},
						"title": str, "problem": str, "evidence": str, "fix": str,
						"blocking": map[string]any{"type": "boolean"},
					},
					"required": []string{"severity", "file", "line", "title", "problem", "evidence", "fix", "blocking"},
				},
			},
			"summary": str,
		},
		"required": []string{"verifications", "new_findings", "summary"},
	}
}

func buildVerifierPrompt(findings []ExportFinding, iterDiff string, changedFiles []string) string {
	var b strings.Builder
	b.WriteString(`You are a fix VERIFIER, not a reviewer. A prior review produced the findings below; the author then committed fixes. Your job has exactly two parts:

1. For EACH numbered finding, determine its status against the current code:
   - RESOLVED: the defect is genuinely fixed (cite the fixing code file:line as evidence)
   - STILL_OPEN: the defect is still present (cite where)
   - REGRESSED: the fix changed behavior but the defect re-manifests another way (cite how)
   Read the actual current files with your tools — do not judge from the diff alone. Do not mark RESOLVED because a test was added; verify the defect itself is gone.

2. Hunt for NEW CRITICAL/HIGH defects ONLY in the files changed since the prior review (listed below). Do NOT audit unchanged files. Do not report MEDIUM/LOW. Severity per worst plausible production impact, never discounted by likelihood.

Your FINAL message must be ONLY a raw JSON object matching the provided schema, with exactly one verification entry per finding index. Do the analysis in intermediate turns. Do NOT edit any files.

`)
	b.WriteString("## Prior findings to verify\n\n")
	for i, f := range findings {
		fmt.Fprintf(&b, "### Finding %d — [%s] %s:%d — %s\n%s\n", i, f.Severity, f.File, f.Line, f.Title, f.Problem)
		if f.Evidence != "" {
			fmt.Fprintf(&b, "Original evidence: %s\n", f.Evidence)
		}
		if f.Fix != "" {
			fmt.Fprintf(&b, "Suggested fix was: %s\n", f.Fix)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Files changed since the prior review (your ONLY scope for new findings)\n\n")
	for _, f := range changedFiles {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	fmt.Fprintf(&b, "\n## Iteration diff (changes since the reviewed SHA)\n\n%s\n", iterDiff)
	return b.String()
}

func runVerifier(worktree, risk string, findings []ExportFinding, iterDiff string, changedFiles []string) (verifierOutput, error) {
	effort := "high"
	if risk == "critical" {
		effort = "xhigh"
	}
	schemaBytes, err := json.Marshal(verifierSchema(len(findings)))
	if err != nil {
		return verifierOutput{}, err
	}
	prompt := buildVerifierPrompt(findings, iterDiff, changedFiles)

	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		cmd := exec.CommandContext(ctx, "claude",
			"-p",
			"--model", "claude-fable-5",
			"--effort", effort,
			"--safe-mode",
			"--strict-mcp-config",
			"--no-session-persistence",
			"--disallowedTools", "Edit,Write,NotebookEdit,Task,Agent",
			"--dangerously-skip-permissions",
			"--output-format", "json",
			"--json-schema", string(schemaBytes),
		)
		cmd.Dir = worktree
		cmd.Stdin = strings.NewReader(prompt)
		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		runErr := cmd.Run()
		cancel()
		if runErr != nil {
			lastErr = fmt.Errorf("verifier run failed: %v: %s", runErr, tail(errBuf.String(), 3))
			continue
		}
		out, parseErr := parseVerifierOutput(outBuf.String(), len(findings))
		if parseErr != nil {
			lastErr = parseErr
			continue
		}
		return out, nil
	}
	return verifierOutput{}, lastErr
}

func parseVerifierOutput(raw string, findingCount int) (verifierOutput, error) {
	rawJSON := strings.TrimSpace(raw)
	if strings.HasPrefix(rawJSON, "{") {
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
	rawJSON = strings.TrimPrefix(rawJSON, "```json")
	rawJSON = strings.TrimSuffix(strings.TrimSpace(rawJSON), "```")

	var out verifierOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawJSON)), &out); err != nil {
		return out, fmt.Errorf("cannot parse verifier JSON: %v", err)
	}
	return out, validateVerifications(out, findingCount)
}

// validateVerifications fails closed: every prior finding must have exactly
// one status. A verifier that skips a finding cannot produce an APPROVE.
func validateVerifications(out verifierOutput, findingCount int) error {
	if len(out.Verifications) != findingCount {
		return fmt.Errorf("verifier returned %d verifications for %d findings", len(out.Verifications), findingCount)
	}
	seen := make(map[int]bool, findingCount)
	for _, v := range out.Verifications {
		if v.Index < 0 || v.Index >= findingCount {
			return fmt.Errorf("verification index %d out of range [0,%d)", v.Index, findingCount)
		}
		if seen[v.Index] {
			return fmt.Errorf("duplicate verification for finding %d", v.Index)
		}
		seen[v.Index] = true
	}
	return nil
}

// --- verdict ------------------------------------------------------------------

func computeVerdict(prior []ExportFinding, out verifierOutput, maxNew int) (string, int) {
	for _, v := range out.Verifications {
		if v.Status != "RESOLVED" {
			return "ESCALATE", 2
		}
	}
	newCH := 0
	for _, f := range out.NewFindings {
		if f.Severity == "CRITICAL" || f.Severity == "HIGH" || f.Blocking {
			newCH++
		}
	}
	switch {
	case newCH == 0:
		return "APPROVE", 0
	case maxNew > 0 && newCH < maxNew:
		return "ITERATE", 1
	default:
		return "ESCALATE", 2
	}
}

func printReport(export FindingsExport, out verifierOutput, prior []ExportFinding, verdict, headSHA string, changedFiles []string) {
	fmt.Printf("\n# Targeted Re-Verification Report\n")
	fmt.Printf("**Verdict:** `%s`\n", verdict)
	fmt.Printf("**Prior review:** %s (%s risk) → this fix HEAD: %s\n", short(export.ReviewedSHA), export.Risk, short(headSHA))
	fmt.Printf("**Scope:** %d changed file(s) since prior review\n\n", len(changedFiles))

	fmt.Println("## Prior Finding Statuses")
	for _, v := range out.Verifications {
		f := prior[v.Index]
		fmt.Printf("- **%s** — [%s] %s:%d — %s\n  - %s\n", v.Status, f.Severity, f.File, f.Line, f.Title, v.Evidence)
	}

	fmt.Println("\n## New Findings (changed files only)")
	if len(out.NewFindings) == 0 {
		fmt.Println("*None.*")
	}
	for _, f := range out.NewFindings {
		fmt.Printf("- **[%s] %s:%d — %s:** %s\n  - *Fix:* %s\n", f.Severity, f.File, f.Line, f.Title, f.Problem, f.Fix)
	}
	fmt.Printf("\n## Verifier Summary\n%s\n", out.Summary)
	log.Printf("=== RECHECK VERDICT: %s ===", verdict)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func tail(s string, lines int) string {
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}
