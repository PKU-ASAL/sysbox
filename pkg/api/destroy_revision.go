package api

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

func (s *RunService) pinDestroyRevision(ctx context.Context, run *controlplane.Run) error {
	files := map[string][]byte{"field.sysbox.hcl": []byte("# State-driven destroy; no remaining workspace.\n")}
	if run.SnapshotPath != "" {
		files = map[string][]byte{}
		root := filepath.Dir(run.SnapshotPath)
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("revision contains non-regular file %s", path)
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = data
			return nil
		}); err != nil {
			return err
		}
	}
	digest := controlplane.ComputeRevisionDigest(files)
	if err := s.revisions.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: digest, Files: files, CreatedAt: time.Now().UTC()}); err != nil {
		return err
	}
	path, err := s.workspaces.MaterializeRevision(run.Topology, digest, files)
	if err != nil {
		return err
	}
	run.Revision, run.SnapshotPath = digest, path
	return nil
}
