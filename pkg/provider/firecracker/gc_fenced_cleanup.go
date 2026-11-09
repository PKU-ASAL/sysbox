package firecracker

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// DeleteFencedGCResidue is dispatched only inside a newly admitted GC effect.
// It never signals PIDs from a previous kernel. The archived original manifest
// and independent fence proof remain outside the removed generation directory.
func (s *Substrate) DeleteFencedGCResidue(ctx context.Context, c controlplane.GCCandidate, p controlplane.GCDispositionProof, g controlplane.SignedExecutionGrant, issuer, host ed25519.PublicKey) error {
	boot, err := currentBootID()
	if err != nil {
		return err
	}
	if err = controlplane.VerifyGCDisposition(host, p); err != nil {
		return err
	}
	if err = controlplane.VerifyExecutionGrant(issuer, g, boot, time.Now()); err != nil {
		return err
	}
	digest, err := c.Digest()
	if err != nil {
		return err
	}
	old := p.Candidate
	if c.Kind != "firecracker" || old.Kind != c.Kind || c.ExternalID != old.ExternalID || c.OwnerUID != old.OwnerUID || c.ResourceUID != old.ResourceUID || c.Generation != old.Generation || c.TopologyUID != old.TopologyUID || p.Presence != "present" || g.Grant.Operation != "gc_delete" || g.Grant.Digest != digest || g.Grant.Scope != p.Request.Grant.Scope || g.Grant.AgentID != p.Request.Grant.AgentID || g.Grant.Epoch <= p.Request.Grant.Epoch || g.Grant.CommandID == p.Request.Grant.CommandID {
		return fmt.Errorf("fenced cleanup requires a new exact GC grant")
	}
	m, _, err := s.findAdoptionManifest(ctx, c.ExternalID)
	if err != nil {
		return err
	}
	h := manifestHandle(&m)
	_, current, release, err := s.lockedManifest(ctx, h)
	if err != nil {
		return err
	}
	defer release()
	if current.OwnerUID != c.OwnerUID || current.ResourceUID != c.ResourceUID || current.Generation != c.Generation || current.VMID != c.ExternalID || (current.TopologyUID != "" && current.TopologyUID != c.TopologyUID) || current.Process.BootID != p.Fence.OldBootID || current.Process.BootID == boot {
		return fmt.Errorf("fenced cleanup manifest identity changed")
	}
	if residue, err := hasLaunchResidue(current.Handle.ConfigPath); err != nil {
		return err
	} else if residue {
		return fmt.Errorf("new process references fenced generation")
	}
	root, err := s.canonicalRoot()
	if err != nil {
		return err
	}
	manifestRaw, err := readRegularFile(current.Handle.ManifestPath)
	if err != nil {
		return err
	}
	evidence := struct {
		Manifest []byte                            `json:"manifest"`
		Proof    controlplane.GCDispositionProof   `json:"proof"`
		Grant    controlplane.SignedExecutionGrant `json:"grant"`
	}{manifestRaw, p, g}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	archive := filepath.Join(root, ".locks", fmt.Sprintf("fenced-cleanup-%x.json", sha256.Sum256(raw)))
	if err = rejectSymlinks(archive); err != nil {
		return err
	}
	if existing, e := os.ReadFile(archive); e == nil {
		if string(existing) != string(raw) {
			return fmt.Errorf("fenced cleanup archive conflict")
		}
	} else if !os.IsNotExist(e) {
		return e
	} else if err = atomicWriteFile(archive, raw, 0600); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Recheck canonical paths immediately before reclamation; local credentials
	// and this generation lock remain owned by the trusted host supervisor.
	if _, err = s.loadManifest(ctx, h); err != nil {
		return err
	}
	// Retain complete fabric evidence when old-boot networking cannot be
	// independently identified. Cleanup never infers absence from VMDir alone.
	if current.Handle.FabricPending != "" {
		return fmt.Errorf("fenced VM namespace creation remains uncertain")
	}
	if err = cleanupFabric(ctx, current); err != nil {
		return err
	}
	if err = removeJail(current); err != nil {
		return err
	}
	for _, path := range []string{current.Handle.Socket, current.Handle.VsockUDS} {
		if path != "" {
			if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	if err = os.RemoveAll(current.Handle.VMDir); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(current.Handle.VMDir))
	if err != nil {
		return err
	}
	defer parent.Close()
	if err = parent.Sync(); err != nil {
		return err
	}
	for _, path := range []string{current.Handle.VMDir, current.Handle.Socket, current.Handle.VsockUDS} {
		if path != "" {
			if _, err = os.Lstat(path); !os.IsNotExist(err) {
				return fmt.Errorf("fenced cleanup absence not established")
			}
		}
	}
	return nil
}

// FencedGCManifestBoot selects the original kernel identity from verified
// provider metadata. It grants no deletion permission.
func (s *Substrate) FencedGCManifestBoot(ctx context.Context, c controlplane.GCCandidate) (string, error) {
	m, _, err := s.findAdoptionManifest(ctx, c.ExternalID)
	if err != nil {
		return "", err
	}
	_, current, release, err := s.lockedManifest(ctx, manifestHandle(&m))
	if err != nil {
		return "", err
	}
	defer release()
	if current.VMID != c.ExternalID || current.OwnerUID != c.OwnerUID || current.ResourceUID != c.ResourceUID || current.Generation != c.Generation {
		return "", fmt.Errorf("fenced manifest identity unavailable")
	}
	return current.Process.BootID, nil
}
