package main

import (
	"strings"
	"testing"
)

// fullScoutOutputs returns one output per reviewScouts entry, all passing,
// all scores 5, no findings — the APPROVE baseline.
func fullScoutOutputs() []scoutOutput {
	outputs := make([]scoutOutput, len(reviewScouts))
	for i, s := range reviewScouts {
		out := scoutOutput{Summary: s.name + " ok"}
		if len(s.dims) > 0 {
			out.Dimensions = map[string]DimensionResult{}
			for _, d := range s.dims {
				out.Dimensions[d] = DimensionResult{Pass: true, Notes: "ok"}
			}
		}
		if len(s.scores) > 0 {
			out.Scores = map[string]int{}
			for _, sc := range s.scores {
				out.Scores[sc] = 5
			}
		}
		outputs[i] = out
	}
	return outputs
}

func scoutIndex(t *testing.T, name string) int {
	t.Helper()
	for i, s := range reviewScouts {
		if s.name == name {
			return i
		}
	}
	t.Fatalf("no scout named %q", name)
	return -1
}

func TestScoutCoverage(t *testing.T) {
	dims := map[string]bool{}
	scores := map[string]bool{}
	for _, s := range reviewScouts {
		for _, d := range s.dims {
			dims[d] = true
		}
		for _, sc := range s.scores {
			scores[sc] = true
		}
	}
	for _, d := range []string{"correctness", "security", "compliance", "exploitability"} {
		if !dims[d] {
			t.Errorf("critical dimension %q has no scout owner", d)
		}
	}
	for _, sc := range []string{"resilience", "idempotency", "observability", "performance", "maintainability"} {
		if !scores[sc] {
			t.Errorf("quality score %q has no scout owner", sc)
		}
	}
}

func TestReduceApproveBaseline(t *testing.T) {
	resp, err := reduceScoutResults(fullScoutOutputs())
	if err != nil {
		t.Fatalf("reduce failed: %v", err)
	}
	if resp.Verdict != verdictApprove {
		t.Errorf("verdict = %s, want APPROVE", resp.Verdict)
	}
	if resp.QualityScore != 25 {
		t.Errorf("quality score = %d, want 25", resp.QualityScore)
	}
	if resp.Status != statusReviewComplete {
		t.Errorf("status = %s, want REVIEW_COMPLETE", resp.Status)
	}
}

func TestReduceDimensionANDAcrossOwners(t *testing.T) {
	// correctness has two owners (dataflow-spec, test-docs); one FAIL must
	// fail the dimension even though the other passes.
	outputs := fullScoutOutputs()
	i := scoutIndex(t, "test-docs")
	outputs[i].Dimensions["correctness"] = DimensionResult{Pass: false, Notes: "vacuous seal"}

	resp, err := reduceScoutResults(outputs)
	if err != nil {
		t.Fatalf("reduce failed: %v", err)
	}
	if resp.CriticalDimensions.Correctness.Pass {
		t.Error("correctness passed despite one owner reporting FAIL")
	}
	if resp.Verdict != verdictRequestChanges {
		t.Errorf("verdict = %s, want REQUEST_CHANGES", resp.Verdict)
	}
	if !strings.Contains(resp.CriticalDimensions.Correctness.Notes, "vacuous seal") {
		t.Error("failing owner's notes not propagated")
	}
}

func TestReduceScoreMinAcrossOwners(t *testing.T) {
	outputs := fullScoutOutputs()
	i := scoutIndex(t, "concurrency-resilience")
	outputs[i].Scores["idempotency"] = 3

	resp, err := reduceScoutResults(outputs)
	if err != nil {
		t.Fatalf("reduce failed: %v", err)
	}
	if resp.QualityScores.Idempotency != 3 {
		t.Errorf("idempotency = %d, want 3", resp.QualityScores.Idempotency)
	}
	if resp.Verdict != verdictRequestChanges {
		t.Errorf("verdict = %s, want REQUEST_CHANGES for score below 4", resp.Verdict)
	}
}

func TestReduceBlockingFindingForcesRequestChanges(t *testing.T) {
	outputs := fullScoutOutputs()
	i := scoutIndex(t, "auth-security")
	outputs[i].Findings = []Finding{{
		Severity: "HIGH", File: "handler.go", Line: 42,
		Title: "missing auth", Problem: "mutation lacks auth check",
		Blocking: true,
	}}

	resp, err := reduceScoutResults(outputs)
	if err != nil {
		t.Fatalf("reduce failed: %v", err)
	}
	if resp.Verdict != verdictRequestChanges {
		t.Errorf("verdict = %s, want REQUEST_CHANGES for blocking finding", resp.Verdict)
	}
	if len(resp.Findings) != 1 {
		t.Fatalf("findings = %d, want 1 (reduce must never drop findings)", len(resp.Findings))
	}
}

func TestReduceTwoDimensionFailuresReject(t *testing.T) {
	outputs := fullScoutOutputs()
	outputs[scoutIndex(t, "auth-security")].Dimensions["security"] = DimensionResult{Pass: false, Notes: "injection"}
	outputs[scoutIndex(t, "db-compliance")].Dimensions["compliance"] = DimensionResult{Pass: false, Notes: "no ledger entry"}

	resp, err := reduceScoutResults(outputs)
	if err != nil {
		t.Fatalf("reduce failed: %v", err)
	}
	if resp.Verdict != verdictReject {
		t.Errorf("verdict = %s, want REJECT for two critical-dimension failures", resp.Verdict)
	}
}

func TestReduceFailsClosedOnCoverageGap(t *testing.T) {
	outputs := fullScoutOutputs()
	i := scoutIndex(t, "auth-security")
	outputs[i].Dimensions = nil // scout returned but reported nothing

	if _, err := reduceScoutResults(outputs); err == nil {
		t.Error("reduce accepted a missing security dimension; must fail closed")
	}
}

func passingResponse() ReviewResponse {
	var r ReviewResponse
	r.Status = statusReviewComplete
	r.Verdict = verdictApprove
	r.CriticalDimensions.Correctness = DimensionResult{Pass: true}
	r.CriticalDimensions.Security = DimensionResult{Pass: true}
	r.CriticalDimensions.Compliance = DimensionResult{Pass: true}
	r.CriticalDimensions.Exploitability = DimensionResult{Pass: true}
	r.QualityScores = QualityScores{Resilience: 4, Idempotency: 4, Observability: 4, Performance: 4, Maintainability: 4}
	return r
}

func TestConsensusFloor(t *testing.T) {
	cases := []struct {
		risk     string
		expected int
		want     int
	}{
		{"critical", 5, 5},
		{"critical", 4, 4},
		{"high", 5, 4},
		{"high", 4, 3},
		{"high", 2, 2},
		{"medium", 5, 3},
		{"medium", 1, 1},
		{"low", 5, 1},
	}
	for _, c := range cases {
		if got := consensusFloor(c.risk, c.expected); got != c.want {
			t.Errorf("consensusFloor(%s, %d) = %d, want %d", c.risk, c.expected, got, c.want)
		}
	}
}

func TestMergeNMinusOneConsensus(t *testing.T) {
	floors, _ := buildFloors("high", "", "")
	responses := []NamedResponse{
		{Name: "agy", Response: passingResponse()},
		{Name: "claude-scouts", Response: passingResponse()},
		{Name: "codex", Response: passingResponse()},
		{Name: "grok", Response: passingResponse()},
	}

	// 4/5 at high risk meets the floor (4) → verdict proceeds.
	status, verdict := mergeReviewStatus(responses, 5, "high", floors)
	if status != statusReviewComplete || verdict != verdictApprove {
		t.Errorf("high 4/5 = %s/%s, want REVIEW_COMPLETE/APPROVE", status, verdict)
	}

	// 4/5 at critical risk is below the floor (5) → unavailable.
	status, _ = mergeReviewStatus(responses, 5, "critical", floors)
	if status != statusReviewUnavailable {
		t.Errorf("critical 4/5 = %s, want REVIEW_UNAVAILABLE", status)
	}

	// 3/5 at high risk is below the floor (4) → unavailable.
	status, _ = mergeReviewStatus(responses[:3], 5, "high", floors)
	if status != statusReviewUnavailable {
		t.Errorf("high 3/5 = %s, want REVIEW_UNAVAILABLE", status)
	}
}

func TestMergeComponentFloors(t *testing.T) {
	floors, err := buildFloors("high", "wallet", "")
	if err != nil {
		t.Fatalf("buildFloors: %v", err)
	}
	if floors.Idempotency != 5 || floors.Performance != 5 || floors.Resilience != 5 {
		t.Fatalf("wallet floors = %+v, want perf/idem/resil 5", floors)
	}
	if floors.Observability != 4 || floors.Maintainability != 4 {
		t.Fatalf("wallet floors = %+v, want obs/maint at base 4", floors)
	}

	// Idempotency 4/5 passes the generic tier floor but violates the wallet
	// component floor → ITERATE.
	resp := passingResponse() // all 4s
	_, verdict := mergeReviewStatus([]NamedResponse{{Name: "codex", Response: resp}}, 1, "high", floors)
	if verdict != verdictIterate {
		t.Errorf("wallet component with idem 4/5 = %s, want ITERATE", verdict)
	}
}

func TestBuildFloorsOverridesAndErrors(t *testing.T) {
	floors, err := buildFloors("medium", "", "obs=5")
	if err != nil {
		t.Fatalf("buildFloors: %v", err)
	}
	if floors.Observability != 5 || floors.Performance != 3 {
		t.Errorf("medium+obs=5 floors = %+v, want obs 5, others 3", floors)
	}
	if _, err := buildFloors("high", "not-a-component", ""); err == nil {
		t.Error("unknown component accepted; want error")
	}
	if _, err := buildFloors("high", "", "obs=9"); err == nil {
		t.Error("out-of-range floor accepted; want error")
	}
}

func TestDedupeFindingsMergesNearDuplicates(t *testing.T) {
	toctou := func(line int, title, problem string) Finding {
		return Finding{
			Severity: "HIGH", File: "service/full-swing-session-service.go", Line: line,
			Title: title, Problem: problem,
		}
	}
	responses := []NamedResponse{
		{Name: "claude", Response: ReviewResponse{Findings: []Finding{
			toctou(1651, "Peer-credential SetActivePlayer bypasses roster gate (TOCTOU)", "roster membership checked against SessionRoster snapshot then SetActivePlayer force-joins departed player"),
		}}},
		{Name: "claude-scouts", Response: ReviewResponse{Findings: []Finding{
			{Severity: "CRITICAL", File: "service/full-swing-session-service.go", Line: 1639,
				Title:   "Roster-membership gate client-side only: force-join TOCTOU of departed player",
				Problem: "roster membership snapshot check then SetActivePlayer force-joins a departed player via peer credential", Source: "scout: dataflow-spec"},
		}}},
		{Name: "codex", Response: ReviewResponse{Findings: []Finding{
			toctou(1635, "Peer SetActivePlayer can force-join after stale roster check", "membership roster snapshot check is TOCTOU; SetActivePlayer force-joins departed player"),
			{Severity: "HIGH", File: "cmd/app/full-swing.go", Line: 934,
				Title: "SelectPlayer reachable without simulator-token validation", Problem: "handler never validates request token before mutation"},
		}}},
	}

	groups := dedupeFindings(responses)
	if len(groups) != 2 {
		for _, g := range groups {
			t.Logf("group: [%s] %s:%d %q sources=%v", g.Severity, g.Finding.File, g.Finding.Line, g.Finding.Title, g.Sources)
		}
		t.Fatalf("groups = %d, want 2 (TOCTOU cluster + token finding)", len(groups))
	}
	// Sorted by severity: the TOCTOU cluster escalated to CRITICAL comes first.
	if groups[0].Severity != "CRITICAL" {
		t.Errorf("cluster severity = %s, want CRITICAL (max across members)", groups[0].Severity)
	}
	if len(groups[0].Sources) != 3 {
		t.Errorf("cluster sources = %v, want 3 reviewers", groups[0].Sources)
	}
}

func TestDedupeFindingsKeepsDistinctIssuesAtSameSite(t *testing.T) {
	responses := []NamedResponse{
		{Name: "codex", Response: ReviewResponse{Findings: []Finding{
			{Severity: "HIGH", File: "settle.go", Line: 100, Title: "Missing timeout on canonical DB query",
				Problem: "synchronous query inside bet-settle lock has no context deadline; slow canonical stalls settlement"},
			{Severity: "HIGH", File: "settle.go", Line: 104, Title: "Nullable venue_id scanned into string",
				Problem: "sql scan of NULL venue into primitive causes driver error aborting dual-read for unassigned hardware"},
		}}},
	}
	if groups := dedupeFindings(responses); len(groups) != 2 {
		t.Fatalf("groups = %d, want 2 — different defects at nearby lines must not merge", len(groups))
	}
}

func TestDeepseekScoutModelAssignment(t *testing.T) {
	hardModels := map[string]bool{
		"deepseek-v4-pro": true,
	}
	easyModels := map[string]bool{
		"deepseek-v4-flash": true,
	}

	hardScouts := map[string]bool{
		"dataflow-spec":      true,
		"auth-security":      true,
		"financial-fairness": true,
		"db-compliance":      true,
	}
	easyScouts := map[string]bool{
		"concurrency-resilience": true,
		"test-docs":              true,
		"quality-scores":         true,
	}

	for _, s := range reviewScouts {
		if hardScouts[s.name] {
			if !hardModels[s.model] {
				t.Errorf("hard scout %s has model %q, want deepseek-reasoner", s.name, s.model)
			}
		} else if easyScouts[s.name] {
			if !easyModels[s.model] {
				t.Errorf("easy scout %s has model %q, want deepseek-chat", s.name, s.model)
			}
		} else {
			t.Errorf("scout %s not classified as hard or easy", s.name)
		}
	}
}

func TestDeepseekScoutCoverage(t *testing.T) {
	// Verify all 7 scouts exist (TestScoutCoverage already checks dim/score coverage)
	if len(reviewScouts) != 7 {
		t.Errorf("reviewScouts count = %d, want 7", len(reviewScouts))
	}
	seen := map[string]bool{}
	for _, s := range reviewScouts {
		if s.model == "" {
			t.Errorf("scout %s has no model assigned", s.name)
		}
		if seen[s.name] {
			t.Errorf("duplicate scout name: %s", s.name)
		}
		seen[s.name] = true
	}
}

func TestFullScoutOutputsAfterModelFieldAddition(t *testing.T) {
	// fullScoutOutputs must still produce valid outputs that reduce cleanly
	// after the scout struct gained the model field.
	outputs := fullScoutOutputs()
	if len(outputs) != len(reviewScouts) {
		t.Fatalf("fullScoutOutputs len = %d, want %d", len(outputs), len(reviewScouts))
	}

	resp, err := reduceScoutResults(outputs)
	if err != nil {
		t.Fatalf("reduceScoutResults failed: %v", err)
	}
	if resp.Verdict != verdictApprove {
		t.Errorf("verdict = %s, want APPROVE", resp.Verdict)
	}
	if resp.QualityScore != 25 {
		t.Errorf("quality score = %d, want 25", resp.QualityScore)
	}
}

func TestDeepseekScoutsReduceWithPartialFailure(t *testing.T) {
	// One scout missing a dimension → fail closed
	outputs := fullScoutOutputs()
	i := scoutIndex(t, "auth-security")
	outputs[i].Dimensions = nil // security dimension unreported

	_, err := reduceScoutResults(outputs)
	if err == nil {
		t.Error("reduce accepted missing security dimension from deepseek scout; must fail closed")
	}
	if err != nil && !strings.Contains(err.Error(), "security") {
		t.Errorf("error should mention 'security', got: %v", err)
	}
}

func TestKimiTextFromStreamJSON(t *testing.T) {
	// Real event shape observed from kimi 0.26.0: assistant tool-call turns
	// carry tool_calls and no content; tool results are role "tool"; a
	// session meta line trails. The LAST text-bearing assistant event is the
	// final answer.
	stream := `{"role":"assistant","tool_calls":[{"type":"function","id":"Bash_0","function":{"name":"Bash","arguments":"{\"command\":\"ls\"}"}}]}
{"role":"tool","tool_call_id":"Bash_0","content":"file.go\n"}
{"role":"assistant","content":"intermediate note before more tools"}
{"role":"assistant","content":"{\"status\":\"REVIEW_COMPLETE\",\"verdict\":\"APPROVE\"}"}
{"role":"meta","type":"session.resume_hint","session_id":"session_x","content":"To resume this session: kimi -r session_x"}
`
	got, err := kimiTextFromStreamJSON(stream)
	if err != nil {
		t.Fatalf("kimiTextFromStreamJSON: %v", err)
	}
	want := `{"status":"REVIEW_COMPLETE","verdict":"APPROVE"}`
	if got != want {
		t.Errorf("got %q, want last assistant text %q", got, want)
	}
}

func TestKimiTextFromStreamJSONSkipsBadLines(t *testing.T) {
	// Blank lines, non-JSON noise, and whitespace-only assistant content must
	// not cost the seat — one corrupt line in a long stream is not a failure.
	stream := "not json\n\n{\"role\":\"assistant\",\"content\":\"  \"}\n{broken\n{\"role\":\"assistant\",\"content\":\"final answer\"}\n"
	got, err := kimiTextFromStreamJSON(stream)
	if err != nil {
		t.Fatalf("kimiTextFromStreamJSON: %v", err)
	}
	if got != "final answer" {
		t.Errorf("got %q, want %q", got, "final answer")
	}
}

func TestKimiTextFromStreamJSONNoAssistantText(t *testing.T) {
	stream := `{"role":"tool","content":"x"}` + "\n" + `{"role":"assistant","tool_calls":[]}`
	if _, err := kimiTextFromStreamJSON(stream); err == nil {
		t.Error("expected error when the stream has no assistant text")
	}
}

func TestKimiBroadPromptReferencesFiles(t *testing.T) {
	// The argv prompt must stay tiny and point at the on-disk request + schema
	// — that indirection is what dodges the kernel's 128KB argv ceiling.
	env := reviewEnv{promptPath: "/tmp/reviewer-x/review-request.md", schemaPath: "/tmp/reviewer-x/review-response.schema.json"}
	p := kimiBroadPrompt(env)
	if !strings.Contains(p, env.promptPath) {
		t.Errorf("prompt does not reference the review request path %s", env.promptPath)
	}
	if !strings.Contains(p, env.schemaPath) {
		t.Errorf("prompt does not reference the schema path %s", env.schemaPath)
	}
	if !strings.Contains(p, "FINAL message must be ONLY a raw JSON object") {
		t.Error("prompt must carry the JSON-only final-message contract")
	}
	if len(p) > 4096 {
		t.Errorf("argv prompt is %d bytes; it must stay tiny (role + diff live in the referenced file)", len(p))
	}
}

func TestFirstLinesTruncatesOverlongMatchLines(t *testing.T) {
	giant := strings.Repeat("A", 3*1024*1024)
	got := firstLines("short line\n"+giant+"\nanother", 20)
	if len(got) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(got))
	}
	if len(got[1]) > maxTraceLineBytes+len(" …[line truncated]") {
		t.Fatalf("overlong line not truncated: %d bytes", len(got[1]))
	}
	if got[0] != "short line" || got[2] != "another" {
		t.Fatalf("short lines must pass through unchanged: %q, %q", got[0], got[2])
	}
}
