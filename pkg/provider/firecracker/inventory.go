package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/oslab/sysbox/pkg/substrate"
)

// ListManagedNodes inventories verified durable manifests, not process liveness
// or deletion permission. Invalid/ambiguous records make the entire inventory
// unavailable. Unmanifested files are not inferred to be owned resources.
func (s *Substrate) ListManagedNodes(ctx context.Context) ([]substrate.NodeHandle, error) {
	root, err := s.canonicalRoot()
	if err != nil {
		return nil, err
	}
	var handles []substrate.NodeHandle
	seen := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("firecracker inventory refuses symlink %q", path)
		}
		if path == root {
			if !entry.IsDir() {
				return fmt.Errorf("firecracker inventory root is not a directory")
			}
			return nil
		}
		if entry.Name() != "manifest.json" {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("firecracker inventory manifest is not a regular file: %q", path)
		}
		raw, err := readRegularFile(path)
		if err != nil {
			return err
		}
		var candidate vmManifest
		if err := json.Unmarshal(raw, &candidate); err != nil {
			return fmt.Errorf("invalid inventory manifest %q: %w", path, err)
		}
		if !validIdentityPart(candidate.VMID) || seen[candidate.VMID] {
			return fmt.Errorf("ambiguous inventory VM identity %q", candidate.VMID)
		}
		if filepath.Join(candidate.Handle.VMDir, "manifest.json") != path {
			return fmt.Errorf("inventory manifest location conflict")
		}
		// Reuse the canonical ownership/path/config verifier. Inventory reads
		// each manifest's own scope and never invokes a provider effect.
		checkCtx := substrate.WithOperationIdentity(ctx, substrate.OperationIdentity{ActionID: candidate.ActionID, OwnerUID: candidate.OwnerUID, TopologyUID: candidate.TopologyUID, ResourceUID: candidate.ResourceUID, Generation: candidate.Generation})
		verified, err := s.loadManifest(checkCtx, manifestHandle(&candidate))
		if err != nil {
			return err
		}
		after, err := readRegularFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, after) {
			return fmt.Errorf("firecracker inventory changed during observation")
		}
		seen[candidate.VMID] = true
		handles = append(handles, manifestHandle(verified))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(handles, func(i, j int) bool { return handles[i].ID < handles[j].ID })
	return handles, nil
}
