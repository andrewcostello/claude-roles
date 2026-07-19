package main

import (
	"strings"
	"testing"
)

func priorFindings(n int) []ExportFinding {
	out := make([]ExportFinding, n)
	for i := range out {
		out[i] = ExportFinding{Severity: "HIGH", File: "a.go", Line: 10 * (i + 1), Title: "finding"}
	}
	return out
}

func resolvedAll(n int) []verification {
	out := make([]verification, n)
	for i := range out {
		out[i] = verification{Index: i, Status: "RESOLVED", Evidence: "fixed at a.go"}
	}
	return out
}

func TestComputeVerdict(t *testing.T) {
	prior := priorFindings(2)

	// All resolved, nothing new → APPROVE / 0.
	v, code := computeVerdict(prior, verifierOutput{Verifications: resolvedAll(2)}, 0)
	if v != "APPROVE" || code != 0 {
		t.Errorf("clean = %s/%d, want APPROVE/0", v, code)
	}

	// Any STILL_OPEN → ESCALATE / 2, regardless of anything else.
	vs := resolvedAll(2)
	vs[1].Status = "STILL_OPEN"
	v, code = computeVerdict(prior, verifierOutput{Verifications: vs}, 5)
	if v != "ESCALATE" || code != 2 {
		t.Errorf("still-open = %s/%d, want ESCALATE/2", v, code)
	}

	// REGRESSED → ESCALATE.
	vs = resolvedAll(2)
	vs[0].Status = "REGRESSED"
	if v, _ = computeVerdict(prior, verifierOutput{Verifications: vs}, 5); v != "ESCALATE" {
		t.Errorf("regressed = %s, want ESCALATE", v)
	}

	// Resolved + 1 new HIGH under max-new 2 → ITERATE / 1 (converging).
	out := verifierOutput{Verifications: resolvedAll(2), NewFindings: []newFinding{{Severity: "HIGH"}}}
	v, code = computeVerdict(prior, out, 2)
	if v != "ITERATE" || code != 1 {
		t.Errorf("converging = %s/%d, want ITERATE/1", v, code)
	}

	// Resolved + new count at max-new → ESCALATE (not strictly decreasing).
	out.NewFindings = []newFinding{{Severity: "HIGH"}, {Severity: "CRITICAL"}}
	if v, _ = computeVerdict(prior, out, 2); v != "ESCALATE" {
		t.Errorf("non-decreasing = %s, want ESCALATE", v)
	}

	// max-new 0 (default): ANY new C/H escalates.
	out.NewFindings = []newFinding{{Severity: "HIGH"}}
	if v, _ = computeVerdict(prior, out, 0); v != "ESCALATE" {
		t.Errorf("default any-new = %s, want ESCALATE", v)
	}

	// New MEDIUM findings do not block approval.
	out.NewFindings = []newFinding{{Severity: "MEDIUM"}}
	if v, _ = computeVerdict(prior, out, 0); v != "APPROVE" {
		t.Errorf("new medium = %s, want APPROVE", v)
	}

	// Blocking overrides severity label.
	out.NewFindings = []newFinding{{Severity: "MEDIUM", Blocking: true}}
	if v, _ = computeVerdict(prior, out, 0); v != "ESCALATE" {
		t.Errorf("blocking medium = %s, want ESCALATE", v)
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

func TestCriticalHighFilter(t *testing.T) {
	findings := []ExportFinding{
		{Severity: "CRITICAL"},
		{Severity: "HIGH"},
		{Severity: "MEDIUM"},
		{Severity: "MEDIUM", Blocking: true},
		{Severity: "LOW"},
	}
	if got := len(criticalHigh(findings)); got != 3 {
		t.Errorf("criticalHigh = %d, want 3 (CRITICAL + HIGH + blocking MEDIUM)", got)
	}
}

func TestVerifierPromptCarriesScopeAndFindings(t *testing.T) {
	findings := []ExportFinding{{Severity: "HIGH", File: "stream.go", Line: 42, Title: "unbounded enrichment", Problem: "no deadline"}}
	prompt := buildVerifierPrompt(findings, "diff --git a/stream.go ...", []string{"stream.go", "stream_test.go"})
	for _, want := range []string{"Finding 0", "stream.go:42", "ONLY in the files changed", "RESOLVED", "REGRESSED", "Do NOT audit unchanged files"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
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
