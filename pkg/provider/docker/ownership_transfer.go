package docker

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

type ownershipTransferBinding struct {
	Previous    *ownershipTransferBinding             `json:"previous,omitempty"`
	Proof       controlplane.OwnershipRetirementProof `json:"proof"`
	Issuer      []byte                                `json:"issuer"`
	Created     string                                `json:"created"`
	Image       string                                `json:"image"`
	Fingerprint string                                `json:"fingerprint"`
}

func (s *Substrate) transferPath(id string) string { return s.adoptionPath(id) + ".transfer" }
func (s *Substrate) transferredLabels(ins container.InspectResponse) (map[string]string, bool, error) {
	if !validAdoptionID(ins.ID) {
		return nil, false, nil
	}
	root := filepath.Dir(s.adoptionPath(ins.ID))
	if err := validateTransferCatalog(root); err != nil {
		return nil, true, err
	}
	path := s.transferPath(ins.ID)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if _, e := os.Lstat(path + ".required"); !os.IsNotExist(e) {
			return nil, true, fmt.Errorf("required transfer binding missing")
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, true, fmt.Errorf("invalid transfer binding")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, true, err
	}
	var binding ownershipTransferBinding
	if json.Unmarshal(raw, &binding) != nil || controlplane.VerifyOwnershipRetirement(binding.Issuer, binding.Proof) != nil {
		return nil, true, fmt.Errorf("invalid transfer proof")
	}
	depth := 0
	for current := &binding; current != nil; current = current.Previous {
		depth++
		if depth > 32 || controlplane.VerifyOwnershipRetirement(current.Issuer, current.Proof) != nil {
			return nil, true, fmt.Errorf("invalid transfer history")
		}
		if prior := current.Previous; prior != nil {
			if !sameAdoptionOwner(prior.Proof.Plan.Target, current.Proof.Plan.Source) || prior.Proof.Plan.ExternalID != current.Proof.Plan.ExternalID || prior.Created != current.Created || prior.Image != current.Image {
				return nil, true, fmt.Errorf("transfer chain mismatch")
			}
		}
	}
	p := binding.Proof.Plan
	if p.Provider != "docker" || p.ExternalID != ins.ID || binding.Created != ins.Created || binding.Image != ins.Image || binding.Fingerprint != transferIncarnationFingerprint(ins) {
		return nil, true, fmt.Errorf("transferred container incarnation changed")
	}
	labels := copyLabels(ins.Config.Labels)
	for k, v := range operationLabels(p.Target) {
		labels[k] = v
	}
	labels["sysbox.topology"] = p.TargetTopology
	labels["sysbox.resource"] = p.TargetAddress
	addr, _ := address.Parse(p.TargetAddress)
	labels["sysbox.resource_type"] = addr.Type
	labels["sysbox.resource_name"] = addr.Name
	return labels, true, nil
}
func (s *Substrate) InspectOwnershipTransfer(ctx context.Context, p controlplane.OwnershipTransferPlan) error {
	if p.Validate() != nil || p.Provider != "docker" || !validAdoptionID(p.ExternalID) {
		return fmt.Errorf("invalid Docker transfer")
	}
	ins, err := s.cli.ContainerInspect(ctx, p.ExternalID)
	if err != nil {
		return err
	}
	if ins.ID != p.ExternalID || ins.Config == nil || ins.State == nil || ins.State.Running || containerFingerprint(ins) != p.Fingerprint {
		return fmt.Errorf("transfer needs exact stopped container")
	}

	if raw, err := os.ReadFile(s.transferPath(p.ExternalID)); err == nil {
		var binding ownershipTransferBinding
		if json.Unmarshal(raw, &binding) != nil {
			return fmt.Errorf("invalid transfer history")
		}
		depth := 0
		for b := &binding; b != nil; b = b.Previous {
			depth++
			if depth >= 32 {
				return fmt.Errorf("transfer history budget exceeded")
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	labels, err := s.managedOwnershipLabels(substrate.WithOperationIdentity(ctx, p.Source), ins)
	if err != nil {
		return err
	}
	return verifyLabels(labels, operationLabels(p.Source))
}
func (s *Substrate) ApplyOwnershipTransfer(ctx context.Context, proof controlplane.OwnershipRetirementProof, issuer ed25519.PublicKey) (substrate.NodeHandle, error) {
	var zero substrate.NodeHandle
	if err := controlplane.VerifyOwnershipRetirement(issuer, proof); err != nil {
		return zero, err
	}
	p := proof.Plan
	target, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || target != p.Target || p.Provider != "docker" || !validAdoptionID(p.ExternalID) {
		return zero, fmt.Errorf("transfer target mismatch")
	}
	path := s.transferPath(p.ExternalID)
	if err := validateTransferCatalog(filepath.Dir(path)); err != nil {
		return zero, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return zero, err
	}
	lock := flock.New(s.adoptionPath(p.ExternalID) + ".lock")
	held, err := lock.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil || !held {
		lock.Close()
		if err != nil {
			return zero, err
		}
		return zero, ctx.Err()
	}
	defer lock.Close()
	ins, err := s.cli.ContainerInspect(ctx, p.ExternalID)
	if err != nil {
		return zero, err
	}
	var previous *ownershipTransferBinding
	if labels, exists, err := s.transferredLabels(ins); exists {
		if err != nil {
			return zero, err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return zero, err
		}
		var prior ownershipTransferBinding
		if json.Unmarshal(raw, &prior) != nil {
			return zero, fmt.Errorf("transfer binding corrupt")
		}
		if prior.Proof.PlanDigest == proof.PlanDigest {
			if err = verifyLabels(labels, operationLabels(target)); err != nil {
				return zero, err
			}
			if err = ensureTransferAnchor(path, proof.PlanDigest); err != nil {
				return zero, err
			}
			return s.adoptHandle(ctx, ins, target), nil
		}
		if err = verifyLabels(labels, operationLabels(p.Source)); err != nil {
			return zero, err
		}
		depth := 0
		for v := &prior; v != nil; v = v.Previous {
			depth++
		}
		if depth >= 32 {
			return zero, fmt.Errorf("transfer history budget exceeded")
		}
		previous = &prior
	}
	if err = s.InspectOwnershipTransfer(ctx, p); err != nil {
		return zero, err
	}
	raw, err := json.Marshal(ownershipTransferBinding{Previous: previous, Proof: proof, Issuer: append([]byte(nil), issuer...), Created: ins.Created, Image: ins.Image, Fingerprint: transferIncarnationFingerprint(ins)})
	if err != nil {
		return zero, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".ownership-transfer-")
	if err != nil {
		return zero, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return zero, err
	}
	if err = f.Sync(); err != nil {
		return zero, err
	}
	if err = f.Close(); err != nil {
		return zero, err
	}
	if previous != nil {
		err = os.Rename(f.Name(), path)
	} else {
		err = os.Link(f.Name(), path)
	}
	if err != nil {
		return zero, err
	}
	anchor, err := os.OpenFile(path+".required", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil && !os.IsExist(err) {
		return zero, err
	}
	if err == nil {
		if _, err = anchor.Write([]byte(proof.PlanDigest)); err == nil {
			err = anchor.Sync()
		}
		anchor.Close()
		if err != nil {
			return zero, err
		}
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return zero, err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return zero, err
	}
	if err = ensureTransferAnchor(path, proof.PlanDigest); err != nil {
		return zero, err
	}
	labels, _, err := s.transferredLabels(ins)
	if err != nil {
		return zero, err
	}
	if err = verifyLabels(labels, operationLabels(target)); err != nil {
		return zero, err
	}
	return s.adoptHandle(ctx, ins, target), nil
}

func transferIncarnationFingerprint(ins container.InspectResponse) string {
	raw, _ := json.Marshal(struct {
		ID, Created, Image string
		Config             any
	}{ins.ID, ins.Created, ins.Image, ins.Config})
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}
func ensureTransferAnchor(path, digest string) error {
	root := filepath.Dir(path)
	if err := validateTransferCatalog(root); err != nil {
		return err
	}
	sentinel := filepath.Join(root, ".transfer-catalog")
	identity, err := os.ReadFile(sentinel)
	if os.IsNotExist(err) {
		identity = []byte(fmt.Sprintf("%x", sha256.Sum256([]byte(root+time.Now().UTC().String()))))
		f, e := os.OpenFile(sentinel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(identity)
		if e == nil {
			e = f.Sync()
		}
		f.Close()
		if e != nil {
			return e
		}
	} else if err != nil {
		return err
	}
	marker, err := os.OpenFile(root+".transfer-required", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil && !os.IsExist(err) {
		return err
	}
	if err == nil {
		_, err = marker.Write(identity)
		if err == nil {
			err = marker.Sync()
		}
		marker.Close()
		if err != nil {
			return err
		}
	}
	parent, err := os.Open(filepath.Dir(root))
	if err != nil {
		return err
	}
	err = parent.Sync()
	parent.Close()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path+".required", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil && !os.IsExist(err) {
		return err
	}
	if err == nil {
		_, err = f.Write([]byte(digest))
		if err == nil {
			err = f.Sync()
		}
		f.Close()
		if err != nil {
			return err
		}
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Substrate) transferredContainerFor(id substrate.OperationIdentity) (string, error) {
	if err := validateTransferCatalog(filepath.Dir(s.adoptionPath("placeholder"))); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(filepath.Dir(s.adoptionPath("placeholder")))
	if os.IsNotExist(err) {
		if _, e := os.Lstat(filepath.Dir(s.adoptionPath("placeholder")) + ".transfer-required"); !os.IsNotExist(e) {
			return "", fmt.Errorf("required ownership catalog missing")
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(entries) > 10000 {
		return "", fmt.Errorf("transfer catalog budget exceeded")
	}
	found := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json.transfer.required") {
			bindingPath := filepath.Join(filepath.Dir(s.adoptionPath("placeholder")), strings.TrimSuffix(entry.Name(), ".required"))
			if _, err := os.Lstat(bindingPath); err != nil {
				return "", fmt.Errorf("required transfer binding unavailable: %w", err)
			}
		}

		if !strings.HasSuffix(entry.Name(), ".json.transfer") {
			continue
		}
		path := filepath.Join(filepath.Dir(s.adoptionPath("placeholder")), entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return "", fmt.Errorf("invalid transfer catalog")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		var b ownershipTransferBinding
		if json.Unmarshal(raw, &b) != nil || controlplane.VerifyOwnershipRetirement(b.Issuer, b.Proof) != nil {
			return "", fmt.Errorf("invalid transfer proof")
		}
		if sameAdoptionOwner(b.Proof.Plan.Target, id) {
			if found != "" && found != b.Proof.Plan.ExternalID {
				return "", fmt.Errorf("ambiguous transfer binding")
			}
			found = b.Proof.Plan.ExternalID
		}
	}
	return found, nil
}

func validateTransferCatalog(root string) error {
	marker, err := os.ReadFile(root + ".transfer-required")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(root, ".transfer-catalog"))
	if err != nil {
		return fmt.Errorf("required ownership catalog identity missing: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsafe ownership catalog identity")
	}
	identity, err := os.ReadFile(filepath.Join(root, ".transfer-catalog"))
	if err != nil {
		return err
	}
	if len(marker) != 64 || string(marker) != string(identity) {
		return fmt.Errorf("ownership catalog identity changed")
	}
	return nil
}
