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

func (s *Substrate) findAdoptionManifest(ctx context.Context, id string) (vmManifest, []byte, error) {
	if !validIdentityPart(id) {
		return vmManifest{}, nil, fmt.Errorf("invalid adoption VM identity")
	}
	root, err := s.canonicalRoot()
	if err != nil {
		return vmManifest{}, nil, err
	}
	var found vmManifest
	var rawFound []byte
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() != "manifest.json" {
			return nil
		}
		raw, e := readRegularFile(path)
		if e != nil {
			return e
		}
		var m vmManifest
		if json.Unmarshal(raw, &m) == nil && m.VMID == id {
			if rawFound != nil {
				return fmt.Errorf("ambiguous adoption VM identity")
			}
			if filepath.Join(m.Handle.VMDir, "manifest.json") != path {
				return fmt.Errorf("adoption manifest location conflict")
			}
			found = m
			rawFound = raw
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
	m, raw, err := s.findAdoptionManifest(ctx, id)
	if err != nil {
		return substrate.AdoptionEvidence{}, err
	}
	if m.Version != manifestVersion || m.VMID == "" || m.OwnerUID == "" || m.ResourceUID == "" || m.TopologyUID == "" || m.Generation < 1 {
		return substrate.AdoptionEvidence{}, fmt.Errorf("firecracker manifest identity incomplete")
	}
	if _, err := s.verifiedAdoptionManifest(ctx, m, raw); err != nil {
		return substrate.AdoptionEvidence{}, err
	}
	sum := sha256.Sum256(raw)
	return substrate.AdoptionEvidence{Provider: "firecracker", ExternalID: m.VMID, Fingerprint: "sha256:" + hex.EncodeToString(sum[:]), State: raw}, nil
}
func (s *Substrate) adoptionManifest(ctx context.Context, e substrate.AdoptionEvidence, id substrate.OperationIdentity) (substrate.NodeHandle, error) {
	if id.Validate() != nil || e.Provider != "firecracker" {
		return substrate.NodeHandle{}, fmt.Errorf("invalid adoption proof")
	}
	m, raw, err := s.findAdoptionManifest(ctx, e.ExternalID)
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
	return s.verifiedAdoptionManifest(ctx, m, raw)
}

func (s *Substrate) verifiedAdoptionManifest(ctx context.Context, m vmManifest, raw []byte) (substrate.NodeHandle, error) {
	_, verified, release, err := s.lockedManifest(ctx, manifestHandle(&m))
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	defer release()
	// Compare again under the lifecycle lock: discovery is only a locator.
	current, err := readRegularFile(verified.Handle.ManifestPath)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if sha256.Sum256(current) != sha256.Sum256(raw) {
		return substrate.NodeHandle{}, fmt.Errorf("adoption manifest changed during verification")
	}
	if err := recoverLaunch(verified); err != nil {
		return substrate.NodeHandle{}, err
	}
	if _, _, err := probeProcess(verified.Process); err != nil {
		return substrate.NodeHandle{}, err
	}
	return manifestHandle(verified), nil
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
