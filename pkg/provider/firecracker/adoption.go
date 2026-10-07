package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"os"
	"path/filepath"
)

func (s *Substrate) findAdoptionManifest(id string) (vmManifest, []byte, error) {
	root, err := s.canonicalRoot()
	if err != nil {
		return vmManifest{}, nil, err
	}
	var found vmManifest
	var rawFound []byte
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() != "manifest.json" {
			return nil
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var m vmManifest
		if json.Unmarshal(raw, &m) == nil && m.VMID == id {
			found = m
			rawFound = raw
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return vmManifest{}, nil, err
	}
	if rawFound == nil {
		return vmManifest{}, nil, driver.Wrap(driver.ErrorNotFound, "firecracker", "adoption manifest not found", nil)
	}
	return found, rawFound, nil
}
func (s *Substrate) InspectAdoption(ctx context.Context, id string, spec substrate.NodeSpec) (substrate.AdoptionEvidence, error) {
	m, raw, err := s.findAdoptionManifest(id)
	if err != nil {
		return substrate.AdoptionEvidence{}, err
	}
	if m.Version != manifestVersion || m.VMID == "" || m.OwnerUID == "" || m.ResourceUID == "" || m.TopologyUID == "" || m.Generation < 1 {
		return substrate.AdoptionEvidence{}, fmt.Errorf("firecracker manifest identity incomplete")
	}
	sum := sha256.Sum256(raw)
	return substrate.AdoptionEvidence{Provider: "firecracker", ExternalID: m.VMID, Fingerprint: "sha256:" + hex.EncodeToString(sum[:]), State: raw}, nil
}
func (s *Substrate) adoptionManifest(ctx context.Context, e substrate.AdoptionEvidence, id substrate.OperationIdentity) (substrate.NodeHandle, error) {
	m, raw, err := s.findAdoptionManifest(e.ExternalID)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	sum := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(sum[:]) != e.Fingerprint {
		return substrate.NodeHandle{}, fmt.Errorf("firecracker manifest changed since inspection")
	}
	if m.OwnerUID != id.OwnerUID || m.ResourceUID != id.ResourceUID || m.TopologyUID != id.TopologyUID || m.Generation != id.Generation {
		return substrate.NodeHandle{}, fmt.Errorf("firecracker adoption identity conflict")
	}
	h := manifestHandle(&m)
	return h, nil
}
func (s *Substrate) ApplyAdoption(ctx context.Context, e substrate.AdoptionEvidence) (substrate.NodeHandle, error) {
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return substrate.NodeHandle{}, fmt.Errorf("adoption requires operation identity")
	}
	return s.adoptionManifest(ctx, e, id)
}
func (s *Substrate) ObserveAdoption(ctx context.Context, e substrate.AdoptionEvidence) (substrate.NodeHandle, error) {
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return substrate.NodeHandle{}, fmt.Errorf("adoption requires operation identity")
	}
	return s.adoptionManifest(ctx, e, id)
}
