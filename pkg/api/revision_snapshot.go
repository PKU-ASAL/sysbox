package api

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// MaterializeWorkspaceSnapshot copies the current topology workspace into a
// run-scoped immutable directory for legacy destroy operations without a
// content-addressed revision.
func (s *WorkspaceService) MaterializeWorkspaceSnapshot(topology, runID string) (string, error) {
	if err := validatePathSegment(topology, "topology"); err != nil {
		return "", err
	}
	if runID == "" {
		return "", fmt.Errorf("run id is required")
	}
	source := filepath.Join(s.workspacesDir, topology)
	base := filepath.Join(s.runsDir, topology, "snapshots", runID)
	if err := copySnapshotTree(source, base); err != nil {
		return "", err
	}
	hcl := filepath.Join(base, "field.sysbox.hcl")
	if _, err := os.Stat(hcl); err != nil {
		return "", fmt.Errorf("topology workspace has no field.sysbox.hcl: %w", err)
	}
	return hcl, nil
}

func copySnapshotTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("snapshot contains non-regular file %s", rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// MaterializeRevision publishes the entire verified tree with one rename.
// Concurrent publishers use distinct staging directories; an existing tree
// must match the digest rather than being modified in place.
func (s *WorkspaceService) MaterializeRevision(topology, revision string, files map[string][]byte) (string, error) {
	if err := validatePathSegment(topology, "topology"); err != nil {
		return "", err
	}
	if !globalRevisionPattern.MatchString(revision) || controlplane.ComputeRevisionDigest(files) != revision {
		return "", fmt.Errorf("revision digest does not match project files")
	}
	if _, ok := files["field.sysbox.hcl"]; !ok {
		return "", fmt.Errorf("revision has no field.sysbox.hcl")
	}
	for name := range files {
		if err := validateRelPath(name); err != nil {
			return "", err
		}
	}
	root := filepath.Join(s.runsDir, topology, "revisions")
	base := filepath.Join(root, revision)
	hcl := filepath.Join(base, "field.sysbox.hcl")
	if _, err := os.Lstat(base); err == nil {
		return hcl, verifyRevisionTree(base, files)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(root, ".revision-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	for name, content := range files {
		target := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(stage, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(stage, base); err != nil {
		// Another publisher may have won the rename. Only accept its tree
		// after checking all files, including unexpected files and symlinks.
		if checkErr := verifyRevisionTree(base, files); checkErr != nil {
			return "", fmt.Errorf("publish revision: %w; verify: %v", err, checkErr)
		}
	}
	return hcl, nil
}

func verifyRevisionTree(base string, files map[string][]byte) error {
	seen := 0
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("revision contains non-regular file %s", path)
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		want, ok := files[filepath.ToSlash(rel)]
		if !ok {
			return fmt.Errorf("unexpected revision file %s", rel)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(want, got) {
			return fmt.Errorf("revision file changed: %s", rel)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(files) {
		return fmt.Errorf("revision tree is incomplete")
	}
	return nil
}
