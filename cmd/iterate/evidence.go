package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yourorg/claude-workflow/statefile"
)

func readToolResult(path string, required ...string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("result is not a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := statefile.Parse(data)
	if err != nil {
		return nil, err
	}
	for _, key := range required {
		value, ok := doc[key]
		// The reviewer exports a nil slice as null when it has no findings.
		if !ok || (key != "findings" && strings.TrimSpace(string(value)) == "null") {
			return nil, fmt.Errorf("result is missing required %s", key)
		}
	}
	return data, nil
}

func validCommitID(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validateFullExport(d decision, export FindingsExport) error {
	if !validCommitID(export.ReviewedSHA) || (d.HeadSHA != "" && export.ReviewedSHA != d.HeadSHA) {
		return fmt.Errorf("reviewed_sha does not identify this invocation's exact revision")
	}
	if export.BaseRef == "" || (d.BaseSHA != "" && export.BaseRef != d.BaseSHA) {
		return fmt.Errorf("review base does not match the pinned base")
	}
	if !validRisk(export.Risk) || (d.Risk != "" && export.Risk != d.Risk) {
		return fmt.Errorf("review risk does not match classification")
	}
	if export.Verdict != "APPROVE" && export.Verdict != "ITERATE" && export.Verdict != "REJECT" {
		return fmt.Errorf("unsupported reviewer verdict %q", export.Verdict)
	}
	if _, ok := severityRank[strings.ToUpper(d.Floor)]; !ok {
		return fmt.Errorf("unsupported severity floor %q", d.Floor)
	}
	for _, finding := range export.Findings {
		if _, ok := severityRank[strings.ToUpper(finding.Severity)]; !ok {
			return fmt.Errorf("unsupported finding severity %q", finding.Severity)
		}
	}
	if export.Verdict == "APPROVE" && countAtOrAbove(export.Findings, d.Floor) != 0 {
		return fmt.Errorf("APPROVE contradicts findings at the configured severity floor")
	}
	return nil
}

func validRisk(risk string) bool {
	switch risk {
	case "low", "medium", "high", "critical":
		return true
	default:
		return false
	}
}

func validateRecheckResult(d decision, rr RoundResult, code int) error {
	if rr.Tool != "recheck" || !strings.EqualFold(rr.Floor, d.Floor) || rr.MaxNewGiven != maxNewFor(d.MaxNew) {
		return fmt.Errorf("recheck result does not match the requested tool, floor or convergence budget")
	}
	if !validCommitID(rr.HeadSHA) || !validCommitID(rr.ReviewedSHA) || (d.HeadSHA != "" && rr.HeadSHA != d.HeadSHA) {
		return fmt.Errorf("recheck result does not identify the exact reviewed and current revisions")
	}
	if d.PriorSHA != "" && (rr.ReviewedSHA != d.PriorSHA || rr.PriorChecked != d.PriorCount) {
		return fmt.Errorf("recheck result does not account for the supplied prior findings")
	}
	if rr.PriorChecked < 0 || rr.Resolved < 0 || rr.StillOpen < 0 || rr.Regressed < 0 || rr.NewAtFloor < 0 || rr.ChangedFiles < 0 {
		return fmt.Errorf("recheck result has negative counts")
	}
	// Subtraction avoids overflow from adding hostile or corrupted counts.
	if rr.Resolved > rr.PriorChecked || rr.StillOpen > rr.PriorChecked-rr.Resolved || rr.Regressed != rr.PriorChecked-rr.Resolved-rr.StillOpen {
		return fmt.Errorf("recheck verification counts do not account for every prior finding")
	}
	want := "APPROVE"
	if rr.StillOpen > 0 || rr.Regressed > 0 || (rr.NewAtFloor > 0 && (rr.MaxNewGiven == 0 || rr.NewAtFloor >= rr.MaxNewGiven)) {
		want = "ESCALATE"
	} else if rr.NewAtFloor > 0 {
		want = "ITERATE"
	}
	if rr.Verdict != want || rr.ExitCode != verdictExit(want) || code != rr.ExitCode {
		return fmt.Errorf("recheck verdict, process exit and finding counts disagree")
	}
	return nil
}

// This checks the evidence v1 actually carries. It cannot prove the gate plan
// is complete or that a stored gate result belongs to this revision or policy.
func approvalEvidence(state *RunState, last Round) error {
	if last.Status != "review_complete" || !validCommitID(last.ReviewedSHA) || last.ReviewedSHA != state.Repo.HeadSHA || state.Repo.Dirty {
		return fmt.Errorf("approval is incomplete or belongs to a different revision; reclassify and rerun verification")
	}
	if last.NewFindingCount != 0 || last.PriorStillOpen != 0 || last.PriorRegressed != 0 {
		return fmt.Errorf("approval contradicts recorded unresolved findings")
	}
	if len(state.Gates) == 0 {
		return fmt.Errorf("approval has no recorded deterministic gates")
	}
	for name, gate := range state.Gates {
		if gate.Status == "pass" {
			continue
		}
		_, reason, waived := strings.Cut(strings.ToUpper(gate.SkipReason), "WAIVED:")
		if gate.Status == "skipped" && waived && strings.TrimSpace(reason) != "" {
			continue
		}
		return fmt.Errorf("approval has an unsuccessful or unwaived gate %q", name)
	}
	return nil
}

func checkReviewSubject(state *RunState) error {
	if !filepath.IsAbs(state.Repo.Worktree) {
		return fmt.Errorf("review needs an absolute classified worktree path")
	}
	if !validCommitID(state.Repo.HeadSHA) || !validCommitID(state.Repo.BaseSHA) || state.Repo.Dirty {
		return fmt.Errorf("review needs full base/head commit IDs and a clean classification; rerun classify after committing changes")
	}
	head, err := subjectGit(state.Repo.Worktree, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(head) != state.Repo.HeadSHA {
		return fmt.Errorf("HEAD changed since classification; reclassify and rerun verification")
	}
	status, err := subjectGit(state.Repo.Worktree, "-c", "core.fsmonitor=false", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("worktree is dirty; review evidence requires committed code")
	}
	// Detect a branch move while status was being inspected as well.
	after, err := subjectGit(state.Repo.Worktree, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return err
	}
	if after != head {
		return fmt.Errorf("HEAD moved while checking the review subject")
	}
	return nil
}

func subjectGit(worktree string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("check review subject with git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// Stop decisions update controller status, never invent a completed review.
func recordStop(path string, state *RunState, d decision) error {
	return statefile.Update(path, nil, func(doc statefile.Document) error {
		if err := doc.CheckUnchanged(state.snapshot, "schema_version", "task_key", "repo", "classification", "gates", "rounds", "round", "verdict", "status"); err != nil {
			return err
		}
		for key, value := range map[string]string{
			"verdict": strings.ToLower(d.Verdict), "status": "escalated",
			"escalation_reason": strings.Join(d.Reasons, "; "), "updated_at": time.Now().UTC().Format(time.RFC3339),
		} {
			if err := doc.Set(key, value); err != nil {
				return err
			}
		}
		return nil
	})
}

func bindRecheckInput(d *decision, state *RunState) error {
	path, err := filepath.Abs(d.PriorPath)
	if err != nil {
		return err
	}
	data, err := readToolResult(path, "reviewed_sha", "base_ref", "risk", "verdict", "findings")
	if err != nil {
		return err
	}
	var prior FindingsExport
	if err := json.Unmarshal(data, &prior); err != nil {
		return err
	}
	if err := validateFullExport(decision{Floor: d.Floor, BaseSHA: d.BaseSHA, Risk: d.Risk}, prior); err != nil {
		return err
	}
	if len(state.Rounds) == 0 || prior.ReviewedSHA != state.Rounds[len(state.Rounds)-1].ReviewedSHA {
		return fmt.Errorf("prior findings revision does not match the preceding round")
	}
	d.PriorPath, d.PriorSHA, d.PriorData = path, prior.ReviewedSHA, data
	d.PriorCount = countAtOrAbove(prior.Findings, d.Floor)
	return nil
}

func checkPriorInput(d decision) error {
	if d.PriorData == nil {
		return nil
	}
	data, err := os.ReadFile(d.PriorPath)
	if err != nil {
		return fmt.Errorf("reread prior findings: %w", err)
	}
	if !bytes.Equal(data, d.PriorData) {
		return fmt.Errorf("prior findings changed during recheck")
	}
	return nil
}
