// Package revisiongate serializes participating local metadata writers with
// file revision retirement. The catalog directory is never deleted or replaced.
package revisiongate

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/gofrs/flock"
)

var digest = regexp.MustCompile(`sha256:[a-f0-9]{64}`)

// References includes decoded snapshot byte fields as well as ordinary JSON
// strings. Conservative false positives retain data rather than lose evidence.
func References(raw []byte) (map[string]bool, error) {
	refs := map[string]bool{}
	budget := 64 << 20
	var scan func([]byte, int) error
	scan = func(raw []byte, depth int) error {
		budget -= len(raw)
		if budget < 0 || depth > 32 {
			return fmt.Errorf("revision reference scan budget exceeded")
		}
		for _, id := range digest.FindAllString(string(raw), -1) {
			refs[id] = true
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		if value == nil {
			return fmt.Errorf("null metadata is not evidence")
		}
		var visit func(any) error
		visit = func(v any) error {
			switch x := v.(type) {
			case map[string]any:
				for key, child := range x {
					if err := visit(key); err != nil {
						return err
					}
					if err := visit(child); err != nil {
						return err
					}
				}
			case []any:
				for _, child := range x {
					if err := visit(child); err != nil {
						return err
					}
				}
			case string:
				for _, id := range digest.FindAllString(x, -1) {
					refs[id] = true
				}
				decoded, err := base64.StdEncoding.DecodeString(x)
				if err == nil {
					for _, id := range digest.FindAllString(string(decoded), -1) {
						refs[id] = true
					}
					trimmed := bytes.TrimSpace(decoded)
					if len(trimmed) > 0 && json.Valid(trimmed) && (trimmed[0] == '{' || trimmed[0] == '[') {
						if err := scan(trimmed, depth+1); err != nil {
							return err
						}
					}
				}
			}
			return nil
		}
		return visit(value)
	}
	if err := scan(raw, 0); err != nil {
		return nil, err
	}
	return refs, nil
}
func Lock(ctx context.Context, root string) (func(), error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe revision catalog")
	}
	f := flock.New(filepath.Join(root, "catalog.lock"))
	held, err := f.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil || !held {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, ctx.Err()
	}
	return func() { _ = f.Close() }, nil
}

// Guard must span final publication, after caller-specific locks have been
// acquired. Retirement scans raw files without acquiring those inner locks.
func Guard(ctx context.Context, path string, raw []byte) (func(), error) {
	parent, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	catalog := ""
	choose := func(candidate string) error {
		if catalog != "" && catalog != candidate {
			return fmt.Errorf("conflicting nested revision catalogs")
		}
		catalog = candidate
		return nil
	}
	for dir := parent; ; dir = filepath.Dir(dir) {
		if info, e := os.Lstat(filepath.Join(dir, remoteRootAnchor)); e == nil {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("unsafe remote root marker")
			}
			candidate := filepath.Join(dir, "global-revisions")
			_, exists, e := readRemoteBinding(candidate)
			if e != nil || !exists {
				return nil, fmt.Errorf("required remote registry catalog unavailable")
			}
			if e = choose(candidate); e != nil {
				return nil, e
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}

		if _, e := os.Lstat(filepath.Join(dir, "consumers.json")); e == nil {
			roots, e := Consumers(dir)
			if e != nil {
				return nil, e
			}
			for _, root := range roots {
				bound, e := Catalog(root)
				if e != nil || bound != dir {
					return nil, fmt.Errorf("registry consumer binding invalid")
				}
			}
			if e = choose(dir); e != nil {
				return nil, e
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
		if _, e := os.Lstat(filepath.Join(dir, bindingFile)); e == nil {
			bound, e := Catalog(dir)
			if e != nil {
				return nil, e
			}
			if e = choose(bound); e != nil {
				return nil, e
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
		candidate := filepath.Join(dir, "global-revisions")
		if info, e := os.Lstat(candidate); e == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("unsafe revision registry")
			}
			if e = choose(candidate); e != nil {
				return nil, e
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	if catalog == "" {
		return func() {}, nil
	}
	return GuardCatalog(ctx, catalog, raw)
}

// GuardCatalog protects a publisher with an explicitly selected catalog.
// It remains effective even when catalog discovery metadata has been lost.
func GuardCatalog(ctx context.Context, catalog string, raw []byte) (func(), error) {
	unlock, err := Lock(ctx, catalog)
	if err != nil {
		return nil, err
	}
	refs, err := References(raw)
	if err != nil {
		unlock()
		return nil, err
	}
	for id := range refs {
		retired, err := Retired(catalog, id)
		if err != nil {
			unlock()
			return nil, err
		}
		if retired {
			unlock()
			return nil, fmt.Errorf("revision %s is retired", id)
		}
	}

	binding, remote, err := readRemoteBinding(catalog)
	if err != nil {
		unlock()
		return nil, err
	}
	if filepath.Base(catalog) == "global-revisions" {
		info, e := os.Lstat(filepath.Join(filepath.Dir(catalog), remoteRootAnchor))
		if e == nil && (!info.Mode().IsRegular() || !remote) {
			unlock()
			return nil, fmt.Errorf("required remote registry binding missing")
		}
		if e != nil && !os.IsNotExist(e) {
			unlock()
			return nil, e
		}
	}
	if remote && len(refs) > 0 {
		ids := []string{}
		for id := range refs {
			ids = append(ids, id)
		}
		if err = postRemote(ctx, binding, ids, false); err != nil {
			unlock()
			return nil, err
		}
	}
	return unlock, nil
}
