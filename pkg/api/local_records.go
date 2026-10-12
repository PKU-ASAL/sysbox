package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

var errCorruptLocalRecord = errors.New("corrupt local record")

// localRecordError identifies the damaged file without including its payload.
// Unwrap preserves the decoder error for diagnostics; Is classifies corruption.
type localRecordError struct {
	Path string
	Line int
	Err  error
}

func (e *localRecordError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("corrupt local record %s at line %d", e.Path, e.Line)
	}
	return fmt.Sprintf("corrupt local record %s", e.Path)
}
func (e *localRecordError) Unwrap() error        { return e.Err }
func (e *localRecordError) Is(target error) bool { return target == errCorruptLocalRecord }

func readLocalObject[T any](path string) (T, error) {
	var item T
	raw, err := os.ReadFile(path)
	if err != nil {
		return item, fmt.Errorf("read local record: %w", err)
	}
	if err := decodeLocalRecord(raw, &item); err != nil {
		return item, &localRecordError{Path: path, Err: err}
	}
	return item, nil
}

func decodeLocalRecord(raw []byte, value any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("expected a JSON object")
	}
	return json.Unmarshal(raw, value)
}

// A stable lock file orders readers with all appenders, including other API
// processes. The retry delay is not a timeout; the caller's context bounds it.
func lockLocalHistory(ctx context.Context, path string, shared bool) (*flock.Flock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock := flock.New(path + ".lock")
	var locked bool
	var err error
	if shared {
		locked, err = lock.TryRLockContext(ctx, 5*time.Millisecond)
	} else {
		locked, err = lock.TryLockContext(ctx, 5*time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, fmt.Errorf("local history lock not acquired: %s", path)
	}
	if err := ctx.Err(); err != nil {
		lock.Unlock()
		return nil, err
	}
	return lock, nil
}

func appendLocalHistory(ctx context.Context, path string, value any) (err error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	lock, err := lockLocalHistory(ctx, path, false)
	if err != nil {
		return err
	}
	defer func() {
		if unlockErr := lock.Unlock(); err == nil {
			err = unlockErr
		}
	}()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	raw = append(raw, byte(10))
	n, err := f.Write(raw)
	if err != nil {
		return err
	}
	if n != len(raw) {
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func readLocalObjects[T any](pattern string) ([]T, error) {
	files, err := localRecordPaths(pattern)
	if err != nil {
		return nil, fmt.Errorf("list local records %q: %w", pattern, err)
	}
	out := make([]T, 0, len(files))
	for _, path := range files {
		item, err := readLocalObject[T](path)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// filepath.Glob ignores directory I/O errors. A persistence inventory must
// distinguish those errors from a genuinely empty directory.
func localRecordPaths(pattern string) ([]string, error) {
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, err
	}
	if !strings.ContainsAny(pattern, "*?[\\") {
		if _, err := os.Lstat(pattern); os.IsNotExist(err) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		return []string{pattern}, nil
	}
	dir, name := filepath.Split(pattern)
	dirs, err := localRecordPaths(filepath.Clean(dir))
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, path := range dirs {
		if strings.ContainsAny(filepath.Clean(dir), "*?[\\") {
			info, err := os.Lstat(path)
			if err != nil {
				return nil, err
			}
			// A wildcard parent selects directories for the next path segment.
			// Ordinary sibling files are not records; explicit invalid paths and
			// symlink/permission/I/O failures still propagate below.
			if info.Mode().IsRegular() {
				continue
			}
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			matched, err := filepath.Match(name, entry.Name())
			if err != nil {
				return nil, err
			}
			if matched {
				matches = append(matches, filepath.Join(path, entry.Name()))
			}
		}
	}
	return matches, nil
}

func writeLocalObject(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".record-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	// CreateTemp starts private (0600); never publish an intermediate plaintext
	// guest request with broader permissions and tighten them afterward.
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

// readLocalHistory preserves JSONL append order. ReadBytes avoids Scanner's
// implicit 64KB record limit; malformed or interrupted records fail closed.
func readLocalHistory[T any](ctx context.Context, path string) (out []T, err error) {
	lock, err := lockLocalHistory(ctx, path, true)
	if err != nil {
		return nil, err
	}
	defer func() {
		if unlockErr := lock.Unlock(); err == nil && unlockErr != nil {
			out, err = nil, unlockErr
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open local history: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			out, err = nil, fmt.Errorf("close local history %s: %w", path, closeErr)
		}
	}()
	reader := bufio.NewReader(f)
	for line := 1; ; line++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, readErr := reader.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, fmt.Errorf("read local history %s: %w", path, readErr)
		}
		if len(raw) > 0 {
			if errors.Is(readErr, io.EOF) {
				return nil, &localRecordError{Path: path, Line: line, Err: io.ErrUnexpectedEOF}
			}
			var item T
			if err := decodeLocalRecord(raw, &item); err != nil {
				return nil, &localRecordError{Path: path, Line: line, Err: err}
			}
			out = append(out, item)
		}
		if errors.Is(readErr, io.EOF) {
			return out, nil
		}
	}
}
