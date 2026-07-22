package main

import (
	"strings"
	"testing"
)

func resolvedAll(n int) []verification {
	out := make([]verification, n)
	for i := range out {
		out[i] = verification{Index: i, Status: "RESOLVED", Evidence: "fixed at a.go"}
	}
	return out
}

func TestComputeVerdict(t *testing.T) {
	const floor = "HIGH"

	// All resolved, nothing new → APPROVE / 0.
	v, code := computeVerdict(verifierOutput{Verifications: resolvedAll(2)}, 0, floor)
	if v != "APPROVE" || code != 0 {
		t.Errorf("clean = %s/%d, want APPROVE/0", v, code)
	}

	// Any STILL_OPEN → ESCALATE / 2, regardless of anything else.
	vs := resolvedAll(2)
	vs[1].Status = "STILL_OPEN"
	v, code = computeVerdict(verifierOutput{Verifications: vs}, 5, floor)
	if v != "ESCALATE" || code != 2 {
		t.Errorf("still-open = %s/%d, want ESCALATE/2", v, code)
	}

	// REGRESSED → ESCALATE.
	vs = resolvedAll(2)
	vs[0].Status = "REGRESSED"
	if v, _ = computeVerdict(verifierOutput{Verifications: vs}, 5, floor); v != "ESCALATE" {
		t.Errorf("regressed = %s, want ESCALATE", v)
	}

	// Resolved + 1 new HIGH under max-new 2 → ITERATE / 1 (converging).
	out := verifierOutput{Verifications: resolvedAll(2), NewFindings: []newFinding{{Severity: "HIGH"}}}
	v, code = computeVerdict(out, 2, floor)
	if v != "ITERATE" || code != 1 {
		t.Errorf("converging = %s/%d, want ITERATE/1", v, code)
	}

	// Resolved + new count at max-new → ESCALATE (not strictly decreasing).
	out.NewFindings = []newFinding{{Severity: "HIGH"}, {Severity: "CRITICAL"}}
	if v, _ = computeVerdict(out, 2, floor); v != "ESCALATE" {
		t.Errorf("non-decreasing = %s, want ESCALATE", v)
	}

	// max-new 0 (default): ANY new C/H escalates.
	out.NewFindings = []newFinding{{Severity: "HIGH"}}
	if v, _ = computeVerdict(out, 0, floor); v != "ESCALATE" {
		t.Errorf("default any-new = %s, want ESCALATE", v)
	}

	// At the HIGH floor, new MEDIUM findings do not block approval.
	out.NewFindings = []newFinding{{Severity: "MEDIUM"}}
	if v, _ = computeVerdict(out, 0, floor); v != "APPROVE" {
		t.Errorf("new medium at HIGH floor = %s, want APPROVE", v)
	}

	// Blocking overrides severity label.
	out.NewFindings = []newFinding{{Severity: "MEDIUM", Blocking: true}}
	if v, _ = computeVerdict(out, 0, floor); v != "ESCALATE" {
		t.Errorf("blocking medium = %s, want ESCALATE", v)
	}
}

func TestComputeVerdictMediumFloor(t *testing.T) {
	// The same new MEDIUM that APPROVEs at the HIGH floor must block at MEDIUM.
	out := verifierOutput{Verifications: resolvedAll(1), NewFindings: []newFinding{{Severity: "MEDIUM"}}}
	if v, _ := computeVerdict(out, 0, "HIGH"); v != "APPROVE" {
		t.Errorf("new medium at HIGH floor = %s, want APPROVE", v)
	}
	if v, _ := computeVerdict(out, 0, "MEDIUM"); v != "ESCALATE" {
		t.Errorf("new medium at MEDIUM floor = %s, want ESCALATE (critical-system bar)", v)
	}
	// A converging MEDIUM under max-new iterates at the MEDIUM floor.
	if v, code := computeVerdict(out, 2, "MEDIUM"); v != "ITERATE" || code != 1 {
		t.Errorf("converging medium at MEDIUM floor = %s/%d, want ITERATE/1", v, code)
	}
	// LOW never blocks, even at the MEDIUM floor.
	low := verifierOutput{Verifications: resolvedAll(1), NewFindings: []newFinding{{Severity: "LOW"}}}
	if v, _ := computeVerdict(low, 0, "MEDIUM"); v != "APPROVE" {
		t.Errorf("new low at MEDIUM floor = %s, want APPROVE", v)
	}
}

func TestValidateVerificationsFailsClosed(t *testing.T) {
	// Missing a finding → error.
	out := verifierOutput{Verifications: resolvedAll(1)}
	if err := validateVerifications(out, 2); err == nil {
		t.Error("accepted 1 verification for 2 findings")
	}
	// Duplicate index hiding a skipped finding → error.
	vs := resolvedAll(2)
	vs[1].Index = 0
	if err := validateVerifications(verifierOutput{Verifications: vs}, 2); err == nil {
		t.Error("accepted duplicate verification indices")
	}
	// Out-of-range index → error.
	vs = resolvedAll(2)
	vs[1].Index = 7
	if err := validateVerifications(verifierOutput{Verifications: vs}, 2); err == nil {
		t.Error("accepted out-of-range index")
	}
	if err := validateVerifications(verifierOutput{Verifications: resolvedAll(3)}, 3); err != nil {
		t.Errorf("rejected valid verifications: %v", err)
	}
}

func TestAtOrAboveFloor(t *testing.T) {
	findings := []ExportFinding{
		{Severity: "CRITICAL"},
		{Severity: "HIGH"},
		{Severity: "MEDIUM"},
		{Severity: "MEDIUM", Blocking: true},
		{Severity: "LOW"},
	}
	// HIGH floor: CRITICAL + HIGH + blocking MEDIUM (plain MEDIUM/LOW excluded).
	if got := len(atOrAboveFloor(findings, "HIGH")); got != 3 {
		t.Errorf("atOrAboveFloor(HIGH) = %d, want 3", got)
	}
	// MEDIUM floor: adds the plain MEDIUM → 4 (LOW still excluded).
	if got := len(atOrAboveFloor(findings, "MEDIUM")); got != 4 {
		t.Errorf("atOrAboveFloor(MEDIUM) = %d, want 4", got)
	}
	// CRITICAL floor: CRITICAL + blocking MEDIUM only.
	if got := len(atOrAboveFloor(findings, "CRITICAL")); got != 2 {
		t.Errorf("atOrAboveFloor(CRITICAL) = %d, want 2", got)
	}
}

func TestNormalizeFloor(t *testing.T) {
	cases := map[string]string{
		"high": "HIGH", "HIGH": "HIGH", "": "HIGH", "garbage": "HIGH", "low": "HIGH",
		"medium": "MEDIUM", " Medium ": "MEDIUM", "critical": "CRITICAL",
	}
	for in, want := range cases {
		if got := normalizeFloor(in); got != want {
			t.Errorf("normalizeFloor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVerifierPromptSeverityLanguageByFloor(t *testing.T) {
	findings := []ExportFinding{{Severity: "HIGH", File: "stream.go", Line: 42, Title: "unbounded enrichment", Problem: "no deadline"}}
	// Shared scaffolding present at any floor.
	high := buildVerifierPrompt(findings, "diff --git a/stream.go ...", []string{"stream.go", "stream_test.go"}, "HIGH")
	for _, want := range []string{"Finding 0", "stream.go:42", "ONLY in the files changed", "RESOLVED", "REGRESSED", "Do NOT audit unchanged files"} {
		if !strings.Contains(high, want) {
			t.Errorf("HIGH prompt missing %q", want)
		}
	}
	// HIGH floor excludes MEDIUM/LOW.
	if !strings.Contains(high, "Do not report MEDIUM/LOW") {
		t.Errorf("HIGH prompt should exclude MEDIUM/LOW:\n%s", high)
	}
	// MEDIUM floor hunts mediums and only excludes LOW.
	med := buildVerifierPrompt(findings, "diff", []string{"a.go"}, "MEDIUM")
	if !strings.Contains(med, "MEDIUM-or-higher") || !strings.Contains(med, "Do not report LOW") {
		t.Errorf("MEDIUM prompt must hunt MEDIUM-or-higher and exclude only LOW:\n%s", med)
	}
}

func TestParseVerifierOutputEnvelope(t *testing.T) {
	inner := `{"verifications":[{"index":0,"status":"RESOLVED","evidence":"bounded at stream.go:1322"}],"new_findings":[],"summary":"fix verified"}`
	envelope := `{"type":"result","result":"{\"verifications\":[{\"index\":0,\"status\":\"RESOLVED\",\"evidence\":\"bounded at stream.go:1322\"}],\"new_findings\":[],\"summary\":\"fix verified\"}"}`

	for _, raw := range []string{inner, envelope} {
		out, err := parseVerifierOutput(raw, 1)
		if err != nil {
			t.Fatalf("parse failed for %q-style input: %v", raw[:20], err)
		}
		if out.Verifications[0].Status != "RESOLVED" {
			t.Errorf("status = %s", out.Verifications[0].Status)
		}
	}
}
