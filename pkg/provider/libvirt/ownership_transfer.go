package libvirt

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

// ApplyOwnershipTransfer accepts only a signed source retirement and the exact
// target operation. The owning supervisor must separately authorize its grant.
// Domain storage/networking remain external: transfer never moves or deletes it.
func (s *Substrate) ApplyOwnershipTransfer(ctx context.Context, p controlplane.OwnershipRetirementProof, key ed25519.PublicKey) (substrate.NodeHandle, error) {
	var zero substrate.NodeHandle
	if err := controlplane.VerifyOwnershipRetirement(key, p); err != nil {
		return zero, err
	}
	plan := p.Plan
	target, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || target != plan.Target || plan.Provider != "libvirt" {
		return zero, fmt.Errorf("transfer target identity mismatch")
	}
	if id, err := uuid.Parse(plan.ExternalID); err != nil || id.String() != plan.ExternalID {
		return zero, fmt.Errorf("transfer requires canonical domain UUID")
	}
	root := filepath.Join(s.vmDirRoot(), ".adoptions")
	if err := os.MkdirAll(root, 0700); err != nil {
		return zero, err
	}
	lock := flock.New(filepath.Join(root, plan.ExternalID+".lock"))
	held, err := lock.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil || !held {
		lock.Close()
		if err != nil {
			return zero, err
		}
		return zero, ctx.Err()
	}
	defer lock.Close()
	raw, err := virshQuery(ctx, "dumpxml", plan.ExternalID)
	if err != nil {
		return zero, err
	}
	var domain domainXML
	if xml.Unmarshal(raw, &domain) != nil || domain.UUID != plan.ExternalID || domain.Metadata == nil || domain.Metadata.Managed == nil {
		return zero, fmt.Errorf("transfer domain identity missing")
	}
	m := domain.Metadata.Managed
	current := substrate.OperationIdentity{ActionID: m.ActionID, OwnerUID: m.OwnerUID, TopologyUID: m.TopologyUID, ResourceUID: m.ResourceUID, Generation: m.Generation}
	var hs HandleState
	if json.Unmarshal([]byte(m.State), &hs) != nil || hs.Identity == nil || !sameGeneration(*hs.Identity, current) || hs.DomainUUID != domain.UUID || hs.DomainName != domain.Name {
		return zero, fmt.Errorf("transfer recovery state mismatch")
	}
	if sameGeneration(current, target) {
		if current.ActionID != target.ActionID || hs.AdoptionFingerprint != "transfer:"+p.PlanDigest {
			return zero, fmt.Errorf("transfer receipt mismatch")
		}
		return managedNodeHandle(&hs), nil
	}
	if !sameGeneration(current, plan.Source) || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != plan.Fingerprint {
		return zero, fmt.Errorf("source changed since transfer planning")
	}
	state, err := virshQuery(ctx, "domstate", domain.UUID)
	if err != nil {
		return zero, err
	}
	if strings.TrimSpace(string(state)) != "shut off" {
		return zero, fmt.Errorf("ownership transfer requires stopped persistent domain")
	}
	// Preserve original runtime/storage configuration, but the target must not
	// inherit delete authority over the source owner's private disk directory.
	hs.Identity = &target
	hs.ExternalDefinition = true
	hs.VMDir = ""
	hs.DiskPath = ""
	hs.AdoptionFingerprint = "transfer:" + p.PlanDigest
	payload, err := json.Marshal(hs)
	if err != nil {
		return zero, err
	}
	metadata := struct {
		XMLName xml.Name `xml:"urn:sysbox:managed managed"`
		domManaged
	}{domManaged: domManaged{OwnerUID: target.OwnerUID, TopologyUID: target.TopologyUID, ResourceUID: target.ResourceUID, Generation: target.Generation, ActionID: target.ActionID, State: string(payload)}}
	encoded, err := xml.Marshal(metadata)
	if err != nil {
		return zero, err
	}
	// Write-ahead evidence survives metadata-write/readback interruption.
	audit, _ := json.Marshal(struct {
		Proof    controlplane.OwnershipRetirementProof `json:"proof"`
		Original []byte                                `json:"original"`
	}{p, raw})
	path := filepath.Join(root, "transfer-"+p.PlanDigest+".json")
	if existing, e := os.ReadFile(path); e == nil {
		if string(existing) != string(audit) {
			return zero, fmt.Errorf("transfer audit conflict")
		}
	} else if !os.IsNotExist(e) {
		return zero, e
	} else {
		f, e := os.CreateTemp(root, ".transfer-")
		if e != nil {
			return zero, e
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if _, e = f.Write(audit); e != nil {
			return zero, e
		}
		if e = f.Sync(); e != nil {
			return zero, e
		}
		if e = f.Close(); e != nil {
			return zero, e
		}
		if e = os.Rename(f.Name(), path); e != nil {
			return zero, e
		}
		d, e := os.Open(root)
		if e != nil {
			return zero, e
		}
		e = d.Sync()
		d.Close()
		if e != nil {
			return zero, e
		}
	}
	if _, err = virshQuery(ctx, "metadata", domain.UUID, "--config", "--uri", "urn:sysbox:managed", "--key", "sysbox", "--set", string(encoded)); err != nil {
		return zero, err
	}
	after, err := virshQuery(ctx, "dumpxml", plan.ExternalID)
	if err != nil {
		return zero, err
	}
	var observed domainXML
	if xml.Unmarshal(after, &observed) != nil || observed.UUID != domain.UUID || observed.Metadata == nil || observed.Metadata.Managed == nil {
		return zero, fmt.Errorf("transfer readback unavailable")
	}
	got := observed.Metadata.Managed
	if got.OwnerUID != target.OwnerUID || got.TopologyUID != target.TopologyUID || got.ResourceUID != target.ResourceUID || got.Generation != target.Generation || got.ActionID != target.ActionID || got.State != string(payload) {
		return zero, fmt.Errorf("transfer readback mismatch")
	}
	return managedNodeHandle(&hs), nil
}

func (s *Substrate) InspectOwnershipTransfer(ctx context.Context, p controlplane.OwnershipTransferPlan) error {
	if p.Validate() != nil || p.Provider != "libvirt" {
		return fmt.Errorf("invalid transfer plan")
	}
	if id, err := uuid.Parse(p.ExternalID); err != nil || id.String() != p.ExternalID {
		return fmt.Errorf("exact domain UUID required")
	}
	raw, err := virshQuery(ctx, "dumpxml", p.ExternalID)
	if err != nil {
		return err
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != p.Fingerprint {
		return fmt.Errorf("transfer fingerprint changed")
	}
	var d domainXML
	if xml.Unmarshal(raw, &d) != nil || d.UUID != p.ExternalID || d.Metadata == nil || d.Metadata.Managed == nil {
		return fmt.Errorf("transfer domain metadata unavailable")
	}
	m := d.Metadata.Managed
	old := substrate.OperationIdentity{ActionID: m.ActionID, OwnerUID: m.OwnerUID, TopologyUID: m.TopologyUID, ResourceUID: m.ResourceUID, Generation: m.Generation}
	var hs HandleState
	if json.Unmarshal([]byte(m.State), &hs) != nil || hs.Identity == nil || !sameGeneration(*hs.Identity, old) || hs.DomainUUID != d.UUID || hs.DomainName != d.Name {
		return fmt.Errorf("transfer recovery state mismatch")
	}
	if !sameGeneration(old, p.Source) {
		return fmt.Errorf("transfer source changed")
	}
	stopped, err := virshQuery(ctx, "domstate", p.ExternalID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(stopped)) != "shut off" {
		return fmt.Errorf("transfer requires stopped domain")
	}
	inactive, err := virshQuery(ctx, "dumpxml", p.ExternalID, "--inactive")
	if err != nil {
		return err
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(inactive)) != p.Fingerprint {
		return fmt.Errorf("transfer persistent definition changed")
	}
	return nil
}
