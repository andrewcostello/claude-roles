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
	outputs[scoutIndex(t, "integrity")].Dimensions["compliance"] = DimensionResult{Pass: false, Notes: "no ledger entry"}

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
		"dataflow-spec": true,
		"auth-security": true,
		"integrity":     true,
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
	// Verify all 6 scouts exist (TestScoutCoverage already checks dim/score coverage)
	if len(reviewScouts) != 6 {
		t.Errorf("reviewScouts count = %d, want 6", len(reviewScouts))
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

// --- role slicing (token-efficiency: each scout gets ~1/7 of reviewer.md) ---

func loadRole(t *testing.T) string {
	t.Helper()
	role, _, err := readReviewerRole("../..")
	if err != nil {
		t.Fatalf("could not load reviewer.md: %v", err)
	}
	return role
}

func TestExtractRoleSliceHierarchy(t *testing.T) {
	role := "# Root\nintro\n\n## A\nabody\n### A1\na1body\n## B\nbbody\n### B1\nb1body\n"
	// H2 "A" must pull its H3 child A1 but stop at H2 "B".
	got := extractRoleSlice(role, []string{"A"})
	if !strings.Contains(got, "abody") || !strings.Contains(got, "a1body") {
		t.Fatalf("slice of A must include its body and child:\n%s", got)
	}
	if strings.Contains(got, "bbody") || strings.Contains(got, "b1body") {
		t.Fatalf("slice of A must NOT bleed into sibling B:\n%s", got)
	}
	// H3 "B1" alone captures only itself.
	if s := extractRoleSlice(role, []string{"B1"}); strings.Contains(s, "bbody") {
		t.Fatalf("H3 slice must not include parent body:\n%s", s)
	}
}

func TestScoutRoleSlicesResolve(t *testing.T) {
	role := loadRole(t)
	// Every declared section prefix must match a real reviewer.md heading, else
	// a rename would silently ship scouts an empty/partial role.
	for _, s := range reviewScouts {
		for _, sec := range s.roleSections {
			if extractRoleSlice(role, []string{sec}) == "" {
				t.Errorf("scout %q: roleSection %q matches no reviewer.md heading", s.name, sec)
			}
		}
	}
	for _, sec := range commonRoleSections {
		if extractRoleSlice(role, []string{sec}) == "" {
			t.Errorf("commonRoleSection %q matches no reviewer.md heading", sec)
		}
	}
}

func TestScoutRoleSlicesAreSmallerAndScoped(t *testing.T) {
	role := loadRole(t)
	for _, s := range reviewScouts {
		slice := scoutRole(role, s)
		if strings.TrimSpace(slice) == "" {
			t.Errorf("scout %q got an empty role slice", s.name)
			continue
		}
		if len(slice) >= len(role) {
			t.Errorf("scout %q slice (%d) is not smaller than full role (%d)", s.name, len(slice), len(role))
		}
		// Common framing must always be present.
		if !strings.Contains(slice, "Severity Calibration") {
			t.Errorf("scout %q slice missing shared Severity Calibration", s.name)
		}
	}
}

// --- degrade-don't-fail-closed ---

func TestDegradeUnverifiedHoldsRequestChanges(t *testing.T) {
	outputs := fullScoutOutputs()
	i := scoutIndex(t, "integrity") // sole owner of "compliance"
	outputs[i] = scoutOutput{}      // failed scout returns nothing
	errs := make([]error, len(outputs))
	errs[i] = context_deadline()

	resp, err := reduceScoutResultsWithStatus(outputs, errs)
	if err != nil {
		t.Fatalf("degrade must not error on a partial failure: %v", err)
	}
	if resp.Verdict != verdictRequestChanges {
		t.Errorf("verdict = %q, want REQUEST_CHANGES (unverified holds below APPROVE, never REJECT)", resp.Verdict)
	}
	if resp.CriticalDimensions.Compliance.Pass {
		t.Error("unverified compliance dimension must be held FAIL (fail-safe)")
	}
	if !strings.Contains(resp.Summary, "DEGRADED") {
		t.Error("summary must announce the degraded state")
	}
	var gotGap bool
	for _, f := range resp.Findings {
		if strings.Contains(f.Source, "degraded") {
			gotGap = true
		}
	}
	if !gotGap {
		t.Error("a coverage-gap finding must be emitted for the failed scout")
	}
}

func TestDegradeCoOwnedDimensionStaysVerified(t *testing.T) {
	// correctness is owned by BOTH dataflow-spec and test-docs. If one fails
	// but the other completes, correctness stays verified — no degrade.
	outputs := fullScoutOutputs()
	i := scoutIndex(t, "dataflow-spec")
	outputs[i] = scoutOutput{}
	errs := make([]error, len(outputs))
	errs[i] = context_deadline()

	resp, err := reduceScoutResultsWithStatus(outputs, errs)
	if err != nil {
		t.Fatalf("reduce errored: %v", err)
	}
	if !resp.CriticalDimensions.Correctness.Pass {
		t.Error("correctness must stay verified via its surviving co-owner test-docs")
	}
	if resp.Verdict != verdictApprove {
		t.Errorf("verdict = %q, want APPROVE (no dimension left unverified)", resp.Verdict)
	}
}

func TestDegradeNeverRejectsFromUnverifiedAlone(t *testing.T) {
	// Fail two score-owning scouts; unverified scores must not manufacture a REJECT.
	outputs := fullScoutOutputs()
	errs := make([]error, len(outputs))
	for _, name := range []string{"integrity", "auth-security"} {
		i := scoutIndex(t, name)
		outputs[i] = scoutOutput{}
		errs[i] = context_deadline()
	}
	resp, err := reduceScoutResultsWithStatus(outputs, errs)
	if err != nil {
		t.Fatalf("reduce errored: %v", err)
	}
	if resp.Verdict == verdictReject {
		t.Error("two UNVERIFIED critical dims must not yield REJECT — only genuine reported FAILs do")
	}
}

func context_deadline() error { return errDeadlineForTest }

var errDeadlineForTest = deadlineErr{}

type deadlineErr struct{}

func (deadlineErr) Error() string { return "scout timed out" }

// TestScoutRoleSectionsCoverAllDimensions guards the one residual coupling in
// role slicing: if someone adds a NEW dimension section to reviewer.md, this
// fails until a scout's roleSections claims it — so a scout can never silently
// stop covering a dimension because the role grew.
func TestScoutRoleSectionsCoverAllDimensions(t *testing.T) {
	role := loadRole(t)
	var union []string
	union = append(union, commonRoleSections...)
	for _, s := range reviewScouts {
		union = append(union, s.roleSections...)
	}
	slice := extractRoleSlice(role, union)

	var dims []string
	for _, ln := range strings.Split(role, "\n") {
		lvl, title, ok := headingLevel(ln)
		if !ok || lvl > 3 {
			continue
		}
		if strings.Contains(title, "PASS / FAIL") || strings.Contains(title, "(1-5)") || title == "Design Coherence" {
			dims = append(dims, title)
		}
	}
	if len(dims) == 0 {
		t.Fatal("no dimension headings found in reviewer.md — heading pattern drifted")
	}
	for _, d := range dims {
		if !strings.Contains(slice, d) {
			t.Errorf("reviewer.md dimension %q is covered by no scout's roleSections — assign it to a scout", d)
		}
	}
}

func TestScoutContributions(t *testing.T) {
	// Build outputs aligned to reviewScouts; give the first two scouts a shared
	// finding (same file:line) and the first scout one extra unique finding.
	outputs := make([]scoutOutput, len(reviewScouts))
	outputs[0].Findings = []Finding{
		{File: "a.go", Line: 10}, // shared with scout 1 → not unique
		{File: "a.go", Line: 20}, // unique to scout 0
	}
	outputs[1].Findings = []Finding{
		{File: "a.go", Line: 10}, // shared with scout 0 → not unique
	}
	got := scoutContributions(outputs)
	if got[0].total != 2 || got[0].unique != 1 {
		t.Errorf("scout 0 = %+v, want total 2 unique 1", got[0])
	}
	if got[1].total != 1 || got[1].unique != 0 {
		t.Errorf("scout 1 = %+v, want total 1 unique 0 (shared line)", got[1])
	}
	if got[0].name != reviewScouts[0].name {
		t.Errorf("contribution names must align to reviewScouts order")
	}
}

func TestScoutModelTiering(t *testing.T) {
	env := reviewEnv{claudeModel: "claude-sonnet-5", softModel: "claude-haiku-4-5-20251001"}
	hard := reviewScouts[scoutIndex(t, "dataflow-spec")]
	soft := reviewScouts[scoutIndex(t, "quality-scores")]
	if !soft.soft {
		t.Fatal("quality-scores must be marked soft")
	}
	if hard.soft {
		t.Fatal("dataflow-spec must NOT be soft (owns correctness)")
	}
	// tiering off: everyone on the main model
	if got := scoutModel(env, soft); got != "claude-sonnet-5" {
		t.Errorf("tiering off: soft scout = %q, want claude-sonnet-5", got)
	}
	// tiering on: soft → soft-model, hard → main
	env.scoutTiering = true
	if got := scoutModel(env, soft); got != "claude-haiku-4-5-20251001" {
		t.Errorf("tiering on: soft scout = %q, want haiku", got)
	}
	if got := scoutModel(env, hard); got != "claude-sonnet-5" {
		t.Errorf("tiering on: hard scout = %q, want claude-sonnet-5 (main)", got)
	}
	// concurrency-resilience must stay hard (races are correctness-grade)
	if reviewScouts[scoutIndex(t, "concurrency-resilience")].soft {
		t.Error("concurrency-resilience must stay hard")
	}
}

// TestDedupeFindingsMergesSameDefectAtIdenticalLine uses the verbatim titles
// and problem text four seats produced for ONE defect on PR #1416: the
// settle-path engine mint at session_logic.go:531. Before the exact-line
// threshold these clustered as THREE findings, which triple-counted the defect
// and made each seat look more original than it was. Their pairwise Jaccard is
// 0.245-0.281, so this test fails if exactLineJaccard drifts back above 0.24.
func TestDedupeFindingsMergesSameDefectAtIdenticalLine(t *testing.T) {
	const file = "apps/platform-domain/bay-session/cmd/bay-session/session_logic.go"
	responses := []NamedResponse{
		{Name: "claude", Response: ReviewResponse{Findings: []Finding{
			{Severity: "CRITICAL", File: file, Line: 531,
				Title:   "Settle-path engine mint still sends USD at a REAL_MONEY_GAMING-denied bay",
				Problem: "This PR adds resolveEngineJoinCurrency so JoinSession never asks the engine to mint USD at a denied bay. The sibling mint path signalEngineOnSettle was not changed: it passes the raw next_wager_currency straight to TriggerBetCreation. JoinSession also never persists that pin."},
		}}},
		{Name: "claude-scouts", Response: ReviewResponse{Findings: []Finding{
			{Severity: "CRITICAL", File: file, Line: 531, Source: "scout: integrity",
				Title:   "Settle re-arm mints USD at a REAL_MONEY_GAMING-denied station",
				Problem: "signalEngineOnSettle passes resolveNextWagerCurrency straight to TriggerBetCreation with no REAL_MONEY_GAMING check. The engine turns an empty currency into USD. The diff closed this exact hole on the join path only."},
		}}},
		{Name: "grok", Response: ReviewResponse{Findings: []Finding{
			{Severity: "HIGH", File: file, Line: 531,
				Title:   "Settle remint still mints USD at a denied bay",
				Problem: "What is wrong: signalEngineOnSettle still forwards an empty pin. The engine defaults empty to USD. What happens: After a POINTS join at a REAL_MONEY_GAMING-off bay, the next settle mints a USD accepted bet. AcceptBet would reject that bet."},
		}}},
	}

	groups := dedupeFindings(responses)
	if len(groups) != 1 {
		for _, g := range groups {
			t.Logf("group: [%s] %s:%d %q sources=%v", g.Severity, g.Finding.File, g.Finding.Line, g.Finding.Title, g.Sources)
		}
		t.Fatalf("groups = %d, want 1 — one defect reported by three seats", len(groups))
	}
	if len(groups[0].Sources) != 3 {
		t.Errorf("sources = %v, want all 3 seats credited", groups[0].Sources)
	}
	if groups[0].Severity != "CRITICAL" {
		t.Errorf("severity = %s, want CRITICAL (max across members)", groups[0].Severity)
	}
}

// TestDedupeFindingsKeepsDistinctDefectsAtIdenticalLine is the guard rail for
// the test above. Both findings are verbatim from PR #1416 at
// switch_wager_currency_logic.go:334 and are genuinely different defects — a
// missing tier gate and a pre-lock TOCTOU. Their Jaccard is 0.208, the nearest
// true negative to the 0.22 threshold, so this fails if the threshold is
// lowered far enough to start burying distinct defects.
func TestDedupeFindingsKeepsDistinctDefectsAtIdenticalLine(t *testing.T) {
	const file = "apps/platform-domain/bay-session/cmd/bay-session/switch_wager_currency_logic.go"
	responses := []NamedResponse{
		{Name: "claude-scouts", Response: ReviewResponse{Findings: []Finding{
			{Severity: "CRITICAL", File: file, Line: 334, Source: "scout: auth-security",
				Title:   "SwitchWagerCurrency mints a USD bet with no player-tier gate",
				Problem: "SwitchWagerCurrency inserts an accepted USD bet with no per-player tier gate, while AcceptBet, matched ResizeWager and SetWagerMode all gate that same decision, so an out-of-tier subject clears a compliance gate about themselves."},
		}}},
		{Name: "claude", Response: ReviewResponse{Findings: []Finding{
			{Severity: "HIGH", File: file, Line: 334,
				Title:   "Switch checks REAL_MONEY_GAMING pre-lock, mints the USD bet in a later tx",
				Problem: "gateSwitchRealMoneyCapability reads the projection through the pooled Store in prepareSwitchWork. The accepted USD bet is inserted in a later MutateStation transaction with a wallet RPC inside the window, so an admin disable during that window still mints the bet."},
		}}},
	}
	if groups := dedupeFindings(responses); len(groups) != 2 {
		t.Fatalf("groups = %d, want 2 — a missing tier gate and a TOCTOU are different defects", len(groups))
	}
}
