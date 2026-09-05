package statefile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"testing/quick"
	"time"
)

func TestUpdatePreservesUnownedValues(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run.json")
	seed := `{"foreign":{"integer":9007199254740993,"nested":[null,true,{"value":"é"}]},"owned":0}`
	if err := os.WriteFile(path, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, nil, func(doc Document) error { return doc.Set("owned", 1) }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		t.Fatal(err)
	}
	want := `{"foreign":{"integer":9007199254740993,"nested":[null,true,{"value":"é"}]},"owned":1}`
	if compact.String() != want {
		t.Fatalf("got %s, want %s", compact.String(), want)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private permissions lost: %v, %v", info, err)
	}
}

func TestParseRejectsAmbiguousOrMalformedDocuments(t *testing.T) {
	t.Parallel()
	for _, data := range []string{"null", "[]", "1", "", `{`, `{} {}`, `{"risk":1,"risk":2}`, `{"x":[{"a":1,"\u0061":2}]}`} {
		t.Run(data, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Fatal("accepted invalid document")
			}
			path := filepath.Join(t.TempDir(), "run.json")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if err := Update(path, Document{}, func(doc Document) error { return doc.Set("x", 1) }); err == nil {
				t.Fatal("overwrote invalid document")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != data {
				t.Fatalf("input changed: %s, %v", got, err)
			}
		})
	}
}

func TestParsePreservesGeneratedIntegers(t *testing.T) {
	t.Parallel()
	if err := quick.Check(func(n uint64) bool {
		data := []byte(fmt.Sprintf(`{"foreign":%d}`, n))
		doc, err := Parse(data)
		if err != nil {
			return false
		}
		if err := doc.Set("owned", true); err != nil {
			return false
		}
		return string(doc["foreign"]) == fmt.Sprint(n)
	}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateCreateAndAbort(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run.json")
	abort := errors.New("deliberate callback refusal")
	if err := Update(path, nil, func(doc Document) error { return nil }); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing state: %v", err)
	}
	if err := Update(path, Document{}, func(doc Document) error { return abort }); !errors.Is(err, abort) {
		t.Fatalf("callback error lost: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aborted create left state: %v", err)
	}
	if err := Update(path, Document{}, func(doc Document) error { return doc.Set("x", 1) }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(Document) error{
		func(doc Document) error { return abort },
		func(doc Document) error { doc["x"] = json.RawMessage("not JSON"); return nil },
		func(doc Document) error { return doc.Set("x", make(chan int)) },
	} {
		if err := Update(path, nil, mutate); err == nil {
			t.Fatal("invalid update succeeded")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("aborted update changed state: %s, %v", after, err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v, %v", entries, err)
	}
}

func TestUpdateRefusesSymlinkAndExistingLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target, path := filepath.Join(dir, "target.json"), filepath.Join(dir, "run.json")
	if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, Document{}, func(doc Document) error { return doc.Set("x", 1) }); err == nil {
		t.Fatal("updated through a symlink")
	}
	lock := target + ".lock"
	if err := os.WriteFile(lock, []byte("retained lock"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Update(target, nil, func(doc Document) error { return nil }); !errors.Is(err, ErrBusy) {
		t.Fatalf("existing lock did not refuse update: %v", err)
	}
	if data, err := os.ReadFile(lock); err != nil || string(data) != "retained lock" {
		t.Fatalf("another writer's lock changed: %s, %v", data, err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "{}" {
		t.Fatalf("target changed: %s, %v", data, err)
	}
}

func TestUpdateDetectsNonCooperatingWriter(t *testing.T) {
	t.Parallel()
	for _, create := range []bool{false, true} {
		t.Run(fmt.Sprint(create), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.json")
			if !create {
				if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var initial Document
			if create {
				initial = Document{}
			}
			err := Update(path, initial, func(doc Document) error {
				return os.WriteFile(path, []byte(`{"external":true}`), 0600)
			})
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("external update lost: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != `{"external":true}` {
				t.Fatalf("external state changed: %s, %v", data, err)
			}
		})
	}
}

func TestConcurrentAcceptedUpdatesAreNotLost(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run.json")
	if err := Update(path, Document{}, func(doc Document) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			deadline := time.Now().Add(5 * time.Second)
			for {
				err := Update(path, nil, func(doc Document) error { return doc.Set(fmt.Sprint(i), i) })
				if err == nil {
					return
				}
				if !errors.Is(err, ErrBusy) || time.Now().After(deadline) {
					t.Errorf("writer %d: %v", i, err)
					return
				}
				time.Sleep(time.Millisecond)
			}
		}(i)
	}
	wg.Wait()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(data)
	if err != nil || len(doc) != 16 {
		t.Fatalf("lost updates: %s, %v", data, err)
	}
}

func TestLockCoordinatesProcessesAndDirectoryAliases(t *testing.T) {
	if path := os.Getenv("STATEFILE_TEST_LOCK_PATH"); path != "" {
		if err := Update(path, nil, func(doc Document) error { return nil }); !errors.Is(err, ErrBusy) {
			t.Fatalf("child bypassed lock: %v", err)
		}
		return
	}
	t.Parallel()
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "run.json")
	if err := Update(path, Document{}, func(doc Document) error {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLockCoordinatesProcessesAndDirectoryAliases$")
		cmd.Env = append(os.Environ(), "STATEFILE_TEST_LOCK_PATH="+filepath.Join(alias, "run.json"))
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("child failed: %s: %w", out, err)
		}
		return doc.Set("ok", true)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckUnchanged(t *testing.T) {
	t.Parallel()
	before, err := Parse([]byte(`{"classification":{"risk":"high","human_pr_gate":true},"updated_at":"old"}`))
	if err != nil {
		t.Fatal(err)
	}
	after, err := Parse([]byte(`{"classification": { "risk": "high", "human_pr_gate": true },"updated_at":"new"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := after.CheckUnchanged(before, "classification"); err != nil {
		t.Fatalf("unrelated update refused: %v", err)
	}
	if err := after.CheckUnchanged(before, "updated_at"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed input accepted: %v", err)
	}
	if err := after.CheckUnchanged(nil, "classification"); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing snapshot accepted: %v", err)
	}
	if err := after.Set("classification", map[string]any{"risk": "high", "human_pr_gate": false}); err != nil {
		t.Fatal(err)
	}
	if err := after.CheckUnchanged(before, "classification"); !errors.Is(err, ErrConflict) {
		t.Fatalf("nested policy change accepted: %v", err)
	}
	if err := after.Set("missing", nil); err != nil {
		t.Fatal(err)
	}
	if err := after.CheckUnchanged(before, "missing"); !errors.Is(err, ErrConflict) {
		t.Fatalf("absence confused with null: %v", err)
	}
}

func TestReadersNeverObservePartialReplacement(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run.json")
	if err := Update(path, Document{}, func(doc Document) error { return doc.Set("iteration", 0) }); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("reader lost state file: %v", err)
				return
			}
			if _, err := Parse(data); err != nil {
				t.Errorf("reader saw incomplete state: %v", err)
				return
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	for i := 1; i <= 24; i++ {
		if err := Update(path, nil, func(doc Document) error { return doc.Set("iteration", i) }); err != nil {
			t.Fatal(err)
		}
	}
}
