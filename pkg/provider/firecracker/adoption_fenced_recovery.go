package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

// RecoverFencedAdoption only retires a process anchor from a proven stopped
// kernel. It neither signals a PID nor recreates provider ownership or disks.
func (s *Substrate) RecoverFencedAdoption(ctx context.Context, p controlplane.AdoptionDispositionProof, authorize func(context.Context) error) (substrate.NodeHandle, error) {
	zero := substrate.NodeHandle{}
	a := p.Adoption
	g := p.Request.Grant
	digest, err := a.Digest()
	if err != nil {
		return zero, err
	}
	boot, err := currentBootID()
	if err != nil {
		return zero, err
	}
	if authorize == nil || controlplane.VerifyHostFenceReceipt(p.Request, p.Fence) != nil || p.Fence.NewBootID != boot || a.Provider != "firecracker" || digest != g.Digest || digest != p.Request.JournalSHA256 || g.Operation != "adoption_apply" || g.ActionID != a.ID || g.AgentID != a.AgentID || g.Scope != (controlplane.ExecutionScope{TopologyUID: a.TopologyUID, ResourceUID: a.ResourceUID, Generation: a.Generation}) {
		return zero, fmt.Errorf("exact admitted adoption fence required")
	}
	m, _, err := s.findAdoptionManifest(ctx, a.ExternalID)
	if err != nil {
		return zero, err
	}
	_, current, release, err := s.lockedManifest(ctx, manifestHandle(&m))
	if err != nil {
		return zero, err
	}
	defer release()
	if current.VMID != a.ExternalID || current.OwnerUID != a.TopologyUID || current.TopologyUID != a.TopologyUID || current.ResourceUID != a.ResourceUID || current.Generation != a.Generation || current.Phase == "attaching" {
		return zero, fmt.Errorf("fenced adoption manifest identity or attachment conflict")
	}
	if err = authorize(ctx); err != nil {
		return zero, err
	}
	fenceDigest, err := p.Request.Digest()
	if err != nil {
		return zero, err
	}
	root, err := s.canonicalRoot()
	if err != nil {
		return zero, err
	}
	if current.AdoptionFence == fenceDigest {
		if len(current.AdoptionFenceArchive) != 64 {
			return zero, fmt.Errorf("missing fenced adoption archive")
		}
		archive, err := readRegularFile(filepath.Join(root, ".locks", "adoption-fenced-"+current.AdoptionFenceArchive+".json"))
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(archive)) != current.AdoptionFenceArchive {
			return zero, fmt.Errorf("fenced adoption archive lost or changed")
		}
		if current.Process.BootID != "" || current.Process.PID != 0 || current.Phase != "stopped" {
			return zero, fmt.Errorf("fenced generation has advanced")
		}
		return manifestHandle(current), nil
	}
	if current.Process.BootID != p.Fence.OldBootID || current.Process.BootID == boot {
		return zero, fmt.Errorf("manifest does not name fenced kernel")
	}
	if residue, err := hasLaunchResidue(current.Handle.ConfigPath); err != nil {
		return zero, err
	} else if residue {
		return zero, fmt.Errorf("current kernel references fenced generation")
	}
	original, err := readRegularFile(current.Handle.ManifestPath)
	if err != nil {
		return zero, err
	}
	archive, err := json.Marshal(struct {
		Manifest []byte
		Request  controlplane.HostFenceRequest
		Receipt  controlplane.HostFenceReceipt
	}{original, p.Request, p.Fence})
	if err != nil {
		return zero, err
	}
	path := filepath.Join(root, ".locks", fmt.Sprintf("adoption-fenced-%x.json", sha256.Sum256(archive)))
	if err = rejectSymlinks(path); err != nil {
		return zero, err
	}
	if previous, e := os.ReadFile(path); e == nil {
		if string(previous) != string(archive) {
			return zero, fmt.Errorf("fence archive changed")
		}
	} else if !os.IsNotExist(e) {
		return zero, e
	} else if err = atomicWriteFile(path, archive, 0600); err != nil {
		return zero, err
	}
	if err = ctx.Err(); err != nil {
		return zero, err
	}
	current.AdoptionFence = fenceDigest
	current.AdoptionFenceArchive = fmt.Sprintf("%x", sha256.Sum256(archive))
	current.Process = processIdentity{}
	current.Handle.PID, current.Handle.PIDStart, current.Handle.Ready = 0, "", false
	current.Phase = "stopped"
	current.ObservedAt = time.Now().UTC()
	if err = saveManifest(current); err != nil {
		return zero, err
	}
	return manifestHandle(current), nil
}
