package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/revisiongate"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
)

// MaterializeWorkspaceSnapshot copies the current topology workspace into a
// run-scoped immutable directory for legacy destroy operations without a
// content-addressed revision.
func (s *Catalog) MaterializeWorkspaceSnapshot(topology, runID string) (string, error) {
	finish, err := s.BeginGCProductionWrite(context.Background(), topology, "workspace")
	if err != nil {
		return "", err
	}
	result, err := s.materializeWorkspaceSnapshot(topology, runID)
	if err != nil {
		return "", err
	}
	if err = finish(map[string][]string{"cache": {fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(result)))}}); err != nil {
		return "", err
	}
	return result, nil
}

func (s *Catalog) materializeWorkspaceSnapshot(topology, runID string) (string, error) {
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
		return "", err
	}
	if runID == "" {
		return "", fmt.Errorf("run id is required")
	}
	unlock, err := s.lockPublication(context.Background(), topology)
	if err != nil {
		return "", err
	}
	defer unlock()
	source := filepath.Dir(s.HCLFile(topology))
	base := filepath.Join(s.RunsDir, topology, "snapshots", runID)
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
func (s *Catalog) MaterializeRevision(topology, revision string, files map[string][]byte) (string, error) {
	finish, err := s.BeginGCProductionWrite(context.Background(), topology, "workspace")
	if err != nil {
		return "", err
	}
	result, err := s.materializePublishedRevision(topology, revision, files)
	if err != nil {
		return "", err
	}
	if err = finish(map[string][]string{"revision": {revision}}); err != nil {
		return "", err
	}
	return result, nil
}

func (s *Catalog) materializePublishedRevision(topology, revision string, files map[string][]byte) (string, error) {
	unlock, err := s.lockPublication(context.Background(), topology)
	if err != nil {
		return "", err
	}
	defer unlock()
	return s.materializeRevision(context.Background(), topology, revision, files)
}

// materializeRevision requires the publication lock; UpsertProject holds it
// through switching .current so deletion cannot invalidate the target midway.
func (s *Catalog) materializeRevision(ctx context.Context, topology, revision string, files map[string][]byte) (string, error) {
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
		return "", err
	}
	if !controlstore.GlobalRevisionPattern.MatchString(revision) || controlplane.ComputeRevisionDigest(files) != revision {
		return "", fmt.Errorf("revision digest does not match project files")
	}
	if _, ok := files["field.sysbox.hcl"]; !ok {
		return "", fmt.Errorf("revision has no field.sysbox.hcl")
	}
	for name := range files {
		if err := ValidateRelPath(name); err != nil {
			return "", err
		}
	}
	root := filepath.Join(s.RunsDir, topology, "revisions")
	protection, _ := json.Marshal(map[string]string{"revision": revision})
	release, err := revisiongate.Guard(ctx, filepath.Join(root, revision, "field.sysbox.hcl"), protection)
	if err != nil {
		return "", err
	}
	defer release()
	base := filepath.Join(root, revision)
	hcl := filepath.Join(base, "field.sysbox.hcl")
	if _, err := os.Lstat(base); err == nil {
		if err := verifyRevisionTree(base, files); err != nil {
			return "", err
		}
		if err := syncRevisionTree(base); err != nil {
			return "", err
		}
		return hcl, syncWorkspaceParents(root)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := mkdirWorkspaceDirectories(root); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(root, ".revision-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	for name, content := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
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
	if err := syncRevisionTree(stage); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(stage, base); err != nil {
		// Another publisher may have won the rename. Only accept its tree
		// after checking all files, including unexpected files and symlinks.
		if checkErr := verifyRevisionTree(base, files); checkErr != nil {
			return "", fmt.Errorf("publish revision: %w; verify: %v", err, checkErr)
		}
	}
	if err := syncWorkspaceParents(root); err != nil {
		return "", err
	}
	return hcl, nil
}

// syncRevisionTree persists files and directory entries before publishing the
// immutable root. Directory order is reversed to flush children before parents.
func syncRevisionTree(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		return controlstore.SyncDirectory(path)
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := controlstore.SyncDirectory(dirs[i]); err != nil {
			return err
		}
	}
	return nil
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
