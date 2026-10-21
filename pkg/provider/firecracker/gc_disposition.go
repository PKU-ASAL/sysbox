package firecracker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func (s *Substrate) InspectFencedGC(ctx context.Context, id, owner, resource string, generation int64) (string, error) {
	if !validIdentityPart(id) || !validIdentityPart(owner) || !validIdentityPart(resource) || generation < 1 {
		return "", fmt.Errorf("invalid GC identity")
	}
	handles, err := s.ListManagedNodes(ctx)
	if err != nil {
		return "", err
	}
	for _, h := range handles {
		if h.ID != id {
			continue
		}
		state, ok := h.Provider.(*HandleState)
		if !ok || state.OwnerUID != owner || state.ResourceUID != resource || state.Generation != generation {
			return "", fmt.Errorf("GC manifest ownership changed")
		}
		return "present", nil
	}
	root, err := s.canonicalRoot()
	if err != nil {
		return "", err
	}
	// Missing manifest cannot conceal a partial VM directory. Whole-host reboot
	// excludes old processes, but retained files still require explicit review.
	for _, path := range []string{filepath.Join(root, id), filepath.Join(root, owner, resource, fmt.Sprint(generation))} {
		if _, err = os.Lstat(path); !os.IsNotExist(err) {
			return "", fmt.Errorf("GC VM directory absence not established")
		}
	}
	return "absent", nil
}
