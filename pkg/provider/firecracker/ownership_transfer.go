package firecracker

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

type manifestOwnershipTransfer struct {
	Proof    controlplane.OwnershipRetirementProof `json:"proof"`
	Issuer   []byte                                `json:"issuer"`
	Original []byte                                `json:"original"`
}

func transferManifestDirectory(root string, m *vmManifest) (string, error) {
	return transferManifestDirectoryDepth(root, m, 0)
}
func transferManifestDirectoryDepth(root string, m *vmManifest, depth int) (string, error) {
	if depth >= 16 {
		return "", fmt.Errorf("transfer manifest history budget exceeded")
	}
	if m.Transfer == nil {
		return "", fmt.Errorf("missing manifest transfer")
	}
	t := m.Transfer
	p := t.Proof.Plan
	if controlplane.VerifyOwnershipRetirement(t.Issuer, t.Proof) != nil || p.Provider != "firecracker" || p.ExternalID != m.VMID || m.OwnerUID != p.Target.OwnerUID || m.TopologyUID != p.Target.TopologyUID || m.ResourceUID != p.Target.ResourceUID || m.Generation != p.Target.Generation {
		return "", fmt.Errorf("invalid manifest transfer proof")
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(t.Original)) != p.Fingerprint {
		return "", fmt.Errorf("original manifest fingerprint mismatch")
	}
	var original vmManifest
	if json.Unmarshal(t.Original, &original) != nil || original.VMID != m.VMID || original.OwnerUID != p.Source.OwnerUID || original.ResourceUID != p.Source.ResourceUID || original.Generation != p.Source.Generation || original.RuntimeRoot != root {
		return "", fmt.Errorf("original ownership binding mismatch")
	}
	expected := filepath.Join(root, original.VMID)
	if original.TopologyUID != "" {
		if original.TopologyUID != p.Source.TopologyUID {
			return "", fmt.Errorf("source topology mismatch")
		}
		expected = filepath.Join(root, original.TopologyUID, original.ResourceUID, strconv.FormatInt(original.Generation, 10))
	}
	if original.Transfer != nil {
		var err error
		expected, err = transferManifestDirectoryDepth(root, &original, depth+1)
		if err != nil {
			return "", err
		}
	}
	if expected != original.Handle.VMDir || m.Handle.ConfigPath != original.Handle.ConfigPath || m.Handle.Socket != original.Handle.Socket || m.Handle.VsockUDS != original.Handle.VsockUDS {
		return "", fmt.Errorf("transfer changed physical artifact binding")
	}
	return expected, nil
}
func (s *Substrate) InspectOwnershipTransfer(ctx context.Context, p controlplane.OwnershipTransferPlan) error {
	if p.Validate() != nil || p.Provider != "firecracker" {
		return fmt.Errorf("invalid transfer plan")
	}
	m, raw, err := s.findAdoptionManifest(ctx, p.ExternalID)
	if err != nil {
		return err
	}
	if m.Handle.Fabric != nil || m.Handle.FabricPending != "" {
		return fmt.Errorf("VM fabric ownership transfer requires rebuild")
	}
	if m.Transfer != nil {
		root, err := s.canonicalRoot()
		if err != nil {
			return err
		}
		if _, err = transferManifestDirectoryDepth(root, &m, 1); err != nil {
			return err
		}
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != p.Fingerprint || m.OwnerUID != p.Source.OwnerUID || m.ResourceUID != p.Source.ResourceUID || m.Generation != p.Source.Generation || (m.TopologyUID != "" && m.TopologyUID != p.Source.TopologyUID) {
		return fmt.Errorf("source manifest changed")
	}
	_, verified, release, err := s.lockedManifest(ctx, manifestHandle(&m))
	if err != nil {
		return err
	}
	defer release()
	actual, err := readRegularFile(verified.Handle.ManifestPath)
	if err != nil {
		return err
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(actual)) != p.Fingerprint {
		return fmt.Errorf("source changed while preparing transfer")
	}
	if verified.Phase == "attaching" || verified.Phase == "starting" {
		return fmt.Errorf("unresolved provider intent")
	}
	live, _, err := probeProcess(verified.Process)
	if err != nil {
		return err
	}
	if live {
		return fmt.Errorf("transfer requires stopped VMM")
	}
	if residue, err := hasLaunchResidue(verified.Handle.ConfigPath); err != nil {
		return err
	} else if residue {
		return fmt.Errorf("transfer has unresolved launch residue")
	}
	return nil
}
func (s *Substrate) ApplyOwnershipTransfer(ctx context.Context, p controlplane.OwnershipRetirementProof, key ed25519.PublicKey) (substrate.NodeHandle, error) {
	var zero substrate.NodeHandle
	if err := controlplane.VerifyOwnershipRetirement(key, p); err != nil {
		return zero, err
	}
	target, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || target != p.Plan.Target || p.Plan.Provider != "firecracker" {
		return zero, fmt.Errorf("transfer target mismatch")
	}
	m, raw, err := s.findAdoptionManifest(ctx, p.Plan.ExternalID)
	if err != nil {
		return zero, err
	}
	if m.Handle.Fabric != nil || m.Handle.FabricPending != "" {
		return zero, fmt.Errorf("VM fabric ownership transfer requires rebuild")
	}
	if m.Transfer != nil && m.Transfer.Proof.PlanDigest == p.PlanDigest {
		_, verified, release, err := s.lockedManifest(ctx, manifestHandle(&m))
		if err != nil {
			return zero, err
		}
		defer release()
		return manifestHandle(verified), nil
	}
	sourceCtx := substrate.WithOperationIdentity(ctx, p.Plan.Source)
	// Legacy manifests have no topology; loadManifest accepts an empty scope only
	// for this verified old metadata layout.
	if m.TopologyUID == "" {
		source := p.Plan.Source
		source.TopologyUID = ""
		sourceCtx = substrate.WithOperationIdentity(ctx, source)
	}
	_, current, release, err := s.lockedManifest(sourceCtx, manifestHandle(&m))
	if err != nil {
		return zero, err
	}
	defer release()
	actual, err := readRegularFile(current.Handle.ManifestPath)
	if err != nil {
		return zero, err
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(actual)) != p.Plan.Fingerprint || string(raw) != string(actual) {
		return zero, fmt.Errorf("source changed since plan")
	}
	if current.Phase == "starting" || current.Phase == "attaching" {
		return zero, fmt.Errorf("unresolved provider intent")
	}
	live, _, err := probeProcess(current.Process)
	if err != nil {
		return zero, err
	}
	if live {
		return zero, fmt.Errorf("transfer requires stopped VMM")
	}
	if residue, err := hasLaunchResidue(current.Handle.ConfigPath); err != nil {
		return zero, err
	} else if residue {
		return zero, fmt.Errorf("launch residue remains")
	}
	current.Transfer = &manifestOwnershipTransfer{Proof: p, Issuer: append([]byte(nil), key...), Original: append([]byte(nil), actual...)}
	current.OwnerUID = target.OwnerUID
	current.TopologyUID = target.TopologyUID
	current.ResourceUID = target.ResourceUID
	current.Generation = target.Generation
	current.ActionID = target.ActionID
	current.Handle.OwnerUID = target.OwnerUID
	current.Handle.TopologyUID = target.TopologyUID
	current.Handle.ResourceUID = target.ResourceUID
	current.Handle.Generation = target.Generation
	root, err := s.canonicalRoot()
	if err != nil {
		return zero, err
	}
	if _, err = transferManifestDirectory(root, current); err != nil {
		return zero, err
	}
	if err = saveManifest(current); err != nil {
		return zero, err
	}
	return manifestHandle(current), nil
}
