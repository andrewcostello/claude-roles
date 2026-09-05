// Package statefile updates cooperating tools' JSON state without re-encoding
// fields through a partial schema. Callers remain responsible for field ownership.
package statefile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var (
	ErrBusy     = errors.New("state file is locked")
	ErrConflict = errors.New("state file changed during update")
)

type Document map[string]json.RawMessage

func (d Document) Set(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", key, err)
	}
	d[key] = data
	return nil
}

// Decode reads a field without converting other fields' numbers to float64.
// An absent field leaves dst unchanged; a present malformed field is an error.
func (d Document) Decode(key string, dst any) error {
	data, ok := d[key]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("decode %s: %w", key, err)
	}
	return nil
}

// CheckUnchanged refuses evidence computed from a different input snapshot.
// Whitespace is ignored; reordered keys or alternative value spellings may
// conservatively count as a change.
func (d Document) CheckUnchanged(before Document, keys ...string) error {
	if before == nil {
		return fmt.Errorf("%w: no input snapshot", ErrConflict)
	}
	for _, key := range keys {
		old, err := json.Marshal(before[key])
		if err != nil {
			return fmt.Errorf("encode input %s: %w", key, err)
		}
		current, err := json.Marshal(d[key])
		if err != nil {
			return fmt.Errorf("encode current %s: %w", key, err)
		}
		_, wasPresent := before[key]
		_, isPresent := d[key]
		if wasPresent != isPresent || !bytes.Equal(old, current) {
			return fmt.Errorf("%w: %s changed since execution started", ErrConflict, key)
		}
	}
	return nil
}

// Parse rejects ambiguous duplicate keys, including in fields a caller does not
// understand. Raw values retain numeric precision and unknown nested fields.
func Parse(data []byte) (Document, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := checkValue(dec, 0); err != nil {
		return nil, fmt.Errorf("invalid state JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("invalid trailing state JSON: %w", err)
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("state must be an object: %w", err)
	}
	if doc == nil {
		return nil, errors.New("state must be an object, not null")
	}
	return doc, nil
}

func checkValue(dec *json.Decoder, depth int) error {
	if depth > 1000 {
		return errors.New("JSON nesting exceeds limit")
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	keys := map[string]bool{}
	for dec.More() {
		if delim == '{' {
			tok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := tok.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if keys[key] {
				return fmt.Errorf("duplicate object key %q", key)
			}
			keys[key] = true
		}
		if err := checkValue(dec, depth+1); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

// Update locks, reads, mutates and atomically replaces a regular file. A non-nil
// initial document permits creation; it is used only when the file is absent. All
// writers must use this protocol. The lock is fail-fast, not a waiting queue.
// A crash may leave path.lock; an operator must establish that no writer is
// active before removing it. Never infer ownership from a lock's age alone.
//
// A returned error never authorizes success, even if replacement already
// happened and a later directory sync or lock cleanup failed. This is local
// coordination, not a security boundary against arbitrary filesystem writers.
func Update(path string, initial Document, mutate func(Document) error) (err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve state path: %w", err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return fmt.Errorf("resolve state directory: %w", err)
	}
	path = filepath.Join(dir, filepath.Base(abs))
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrBusy, lockPath)
		}
		return fmt.Errorf("lock state: %w", err)
	}
	defer func() { err = errors.Join(err, removeFile(lockPath)) }()
	if err := lock.Close(); err != nil {
		return fmt.Errorf("close state lock: %w", err)
	}

	original, info, err := readRegular(path)
	if err != nil && !(initial != nil && errors.Is(err, os.ErrNotExist)) {
		return fmt.Errorf("read state: %w", err)
	}
	doc := Document{}
	mode := os.FileMode(0644)
	if info == nil {
		for key, value := range initial {
			doc[key] = bytes.Clone(value)
		}
	} else {
		doc, err = Parse(original)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	}
	if err := mutate(doc); err != nil {
		return fmt.Errorf("update state: %w", err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if _, err := Parse(data); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".statefile-*")
	if err != nil {
		return fmt.Errorf("create replacement state: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { err = errors.Join(err, removeFile(tmpPath)) }()
	if err := writeReplacement(tmp, append(data, '\n'), mode); err != nil {
		return err
	}
	current, currentInfo, readErr := readRegular(path)
	if info == nil {
		if !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("destination appeared: %w", errors.Join(ErrConflict, readErr))
		}
	} else if readErr != nil || !os.SameFile(info, currentInfo) || currentInfo.Mode().Perm() != mode || !bytes.Equal(original, current) {
		return fmt.Errorf("destination no longer matches the read snapshot: %w", errors.Join(ErrConflict, readErr))
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open state directory for sync: %w", err)
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func readRegular(path string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("state path is not a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	return data, info, err
}

func writeReplacement(file *os.File, data []byte, mode os.FileMode) (err error) {
	defer func() { err = errors.Join(err, file.Close()) }()
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("set state permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync state: %w", err)
	}
	return nil
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
