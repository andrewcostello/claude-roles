package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteRunStatePreservesForeignNumericEvidence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run.json")
	seed := []byte(`{"schema_version":1,"task_key":"X","created_at":"original", "status":"in_progress",
	"gates":{"test":{"status":"pass","metrics":{"units":9007199254740993}}},
	"rounds":[{"round":1,"opaque":{"integer":9007199254740993}}],
	"pr":{"number":9007199254740993},"extension":{"version":9007199254740993}}`)
	if err := os.WriteFile(path, seed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeRunState(path, "", Repo{Worktree: "/wt"}, &Classification{Risk: "high"}); err != nil {
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
	for key := range before {
		var want, got bytes.Buffer
		if err := json.Compact(&want, before[key]); err != nil {
			t.Fatal(err)
		}
		if err := json.Compact(&got, after[key]); err != nil {
			t.Errorf("foreign %s missing or malformed: %v", key, err)
			continue
		}
		if !bytes.Equal(want.Bytes(), got.Bytes()) {
			t.Errorf("foreign %s changed: got %s, want %s", key, got.Bytes(), want.Bytes())
		}
	}
}

func TestWriteRunStateRefusesUnsupportedState(t *testing.T) {
	t.Parallel()
	for _, seed := range []string{`{}`, `null`, `{"schema_version":2}`, `{"schema_version":1,"schema_version":2}`} {
		t.Run(seed, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.json")
			if err := os.WriteFile(path, []byte(seed), 0600); err != nil {
				t.Fatal(err)
			}
			if err := writeRunState(path, "", Repo{}, &Classification{Risk: "high"}); err == nil {
				t.Fatal("replaced unsupported state")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != seed {
				t.Fatalf("unsupported state changed: %s, %v", data, err)
			}
		})
	}
}
