package revisiongate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const bindingFile = ".sysbox-revision-registry.json"

func Catalog(root string) (string, error) {
	info, statErr := os.Lstat(filepath.Join(root, bindingFile))
	if statErr == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return "", fmt.Errorf("unsafe registry binding")
	}
	raw, err := os.ReadFile(filepath.Join(root, bindingFile))
	if os.IsNotExist(err) {
		return filepath.Join(root, "global-revisions"), nil
	}
	if err != nil {
		return "", err
	}
	var value struct {
		Registry string `json:"registry"`
	}
	if json.Unmarshal(raw, &value) != nil || !filepath.IsAbs(value.Registry) {
		return "", fmt.Errorf("invalid registry binding")
	}
	return value.Registry, nil
}
func writeRegistryRecord(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".registry-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Enroll binds a stopped/drained local writer root to a shared independent
// registry. The durable consumer list is monotonic: offline roots block deletion.
func Enroll(ctx context.Context, root, registry string) error {
	for _, p := range []string{root, registry} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return fmt.Errorf("absolute canonical registry paths required")
		}
		if err := os.MkdirAll(p, 0700); err != nil {
			return err
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil || real != p {
			return fmt.Errorf("registry symlink forbidden")
		}
	}
	prior, err := Catalog(root)
	if err != nil {
		return err
	}
	if prior != filepath.Join(root, "global-revisions") && prior != registry {
		return fmt.Errorf("registry binding cannot be changed")
	}
	if prior != registry {
		if entries, err := os.ReadDir(prior); err == nil && len(entries) > 0 {
			return fmt.Errorf("existing local registry requires explicit migration")
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	unlock, err := Lock(ctx, registry)
	if err != nil {
		return err
	}
	defer unlock()
	_, boundErr := os.Lstat(filepath.Join(root, bindingFile))
	if boundErr == nil {
		if _, err := os.Lstat(filepath.Join(registry, "consumers.json")); err != nil {
			return fmt.Errorf("bound registry enrollment evidence missing: %w", err)
		}
	} else if !os.IsNotExist(boundErr) {
		return boundErr
	}
	if _, err := os.Lstat(filepath.Join(registry, "consumers.json")); os.IsNotExist(err) {
		entries, e := os.ReadDir(registry)
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if entry.Name() != "catalog.lock" {
				return fmt.Errorf("existing registry requires explicit consumer migration")
			}
		}
	} else if err != nil {
		return err
	}
	roots, err := Consumers(registry)
	if err != nil {
		return err
	}
	found := false
	for _, r := range roots {
		if r == root {
			found = true
		}
	}
	if !found {
		roots = append(roots, root)
		if err = writeRegistryRecord(filepath.Join(registry, "consumers.json"), roots); err != nil {
			return err
		}
	}
	return writeRegistryRecord(filepath.Join(root, bindingFile), map[string]string{"registry": registry})
}
func Consumers(registry string) ([]string, error) {
	info, statErr := os.Lstat(filepath.Join(registry, "consumers.json"))
	if statErr == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("unsafe registry consumers")
	}
	raw, err := os.ReadFile(filepath.Join(registry, "consumers.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var roots []string
	if json.Unmarshal(raw, &roots) != nil || len(roots) == 0 || len(roots) > 1024 {
		return nil, fmt.Errorf("invalid registry consumers")
	}
	for _, root := range roots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return nil, fmt.Errorf("invalid consumer path")
		}
	}
	return roots, nil
}
