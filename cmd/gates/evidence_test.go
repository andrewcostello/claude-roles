package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourorg/claude-workflow/statefile"
)

func TestMergeGatesPreservesCompleteForeignEvidence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run.json")
	seed := []byte(`{"schema_version":1,"repo":{"worktree":"/wt","base_sha":"abc1234","base_ref":"main"},
	"classification":{"risk":"critical","human_pr_gate":true,"financial_paths_touched":true,"recheck_min_severity":"medium","reviewer_args":["-risk","critical"],"panel":{"preset":"full","seats":4}},
	"gates":{"old":{"status":"pass","metrics":{"units":9007199254740993}}},
	"rounds":[{"round":1,"kind":"full","verdict":"ITERATE","opaque":{"integer":9007199254740993}}],
	"pr":{"number":9007199254740993}}`)
	if err := os.WriteFile(path, seed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := mergeGates(path, []result{{Key: "new", Outcome: Gate{Status: "pass"}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal(seed, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"repo", "classification", "rounds", "pr"} {
		var want, got bytes.Buffer
		if err := json.Compact(&want, before[key]); err != nil {
			t.Fatal(err)
		}
		if err := json.Compact(&got, after[key]); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want.Bytes(), got.Bytes()) {
			t.Errorf("foreign %s changed: got %s, want %s", key, got.Bytes(), want.Bytes())
		}
	}
	if !bytes.Contains(after["gates"], []byte("9007199254740993")) {
		t.Error("an unmodified gate lost integer precision")
	}
}

func TestFinishRefusesSuccessWhenEvidenceCannotBeSaved(t *testing.T) {
	t.Parallel()
	state := &RunState{Classification: &Classification{Risk: "high"}}
	opts := options{runState: filepath.Join(t.TempDir(), "missing.json")}
	if code := finish(opts, state, nil, []result{{Key: "test", Outcome: Gate{Status: "pass"}}}); code == 0 {
		t.Fatal("reported success without persisted evidence")
	}
}

func TestGateEvidenceRefusesChangedClassification(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run.json")
	seed := []byte(`{"schema_version":1,"repo":{"worktree":"/wt"},"classification":{"risk":"high","human_pr_gate":true}}`)
	if err := os.WriteFile(path, seed, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := readRunState(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(seed, []byte(`"human_pr_gate":true`), []byte(`"human_pr_gate":false`), 1)
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := mergeGates(path, []result{{Key: "test", Outcome: Gate{Status: "pass"}}}, state.snapshot); !errors.Is(err, statefile.ErrConflict) {
		t.Fatalf("saved results against changed classification: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, changed) {
		t.Fatalf("conflict changed the newer state: %s: %v", after, err)
	}
}
