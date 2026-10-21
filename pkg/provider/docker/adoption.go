package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type adoptionBinding struct {
	Version     int                         `json:"version"`
	Identity    substrate.OperationIdentity `json:"identity"`
	ExternalID  string                      `json:"external_id"`
	Created     string                      `json:"created"`
	Image       string                      `json:"image"`
	Fingerprint string                      `json:"fingerprint"`
}

// managedOwnershipLabels resolves proof from immutable Docker labels or a
// validated adoption sidecar. It does not modify the container's labels.
func (s *Substrate) managedOwnershipLabels(ctx context.Context, ins container.InspectResponse) (map[string]string, error) {
	if ins.Config == nil {
		return nil, fmt.Errorf("docker inspection lacks config")
	}
	if labels, exists, err := s.transferredLabels(ins); exists || err != nil {
		return labels, err
	}
	labels := ins.Config.Labels
	if labels["sysbox.managed"] == "true" && labels["sysbox.owner_uid"] != "" && labels["sysbox.resource_uid"] != "" && labels["sysbox.topology_uid"] != "" {
		return labels, nil
	}
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || !validAdoptionID(ins.ID) {
		return nil, fmt.Errorf("docker managed handle lacks complete ownership labels")
	}
	raw, err := os.ReadFile(s.adoptionPath(ins.ID))
	if err != nil {
		return nil, err
	}
	var binding adoptionBinding
	if json.Unmarshal(raw, &binding) != nil || validateAdoptionBinding(binding, ins, id) != nil {
		return nil, fmt.Errorf("docker adoption ownership conflict")
	}
	return operationLabels(binding.Identity), nil
}

func (s *Substrate) adoptionPath(id string) string {
	root := s.adoptionRoot
	if root == "" {
		root = "/var/lib/sysbox/adoptions"
	}
	return filepath.Join(root, id+".json")
}
func (s *Substrate) InspectAdoption(ctx context.Context, id string, spec substrate.NodeSpec) (substrate.AdoptionEvidence, error) {
	if id == "" {
		return substrate.AdoptionEvidence{}, fmt.Errorf("external ID required")
	}
	ins, err := s.cli.ContainerInspect(ctx, id)
	if err != nil {
		return substrate.AdoptionEvidence{}, err
	}
	if !validAdoptionID(ins.ID) || ins.Created == "" || ins.Image == "" || ins.Config == nil {
		return substrate.AdoptionEvidence{}, fmt.Errorf("container identity incomplete")
	}
	expected := spec.Image.ID
	if expected != "" && ins.Image != expected {
		return substrate.AdoptionEvidence{}, fmt.Errorf("image identity conflict")
	}
	fingerprint := containerFingerprint(ins)
	raw, _ := json.Marshal(struct{ ID, Created, Image string }{ins.ID, ins.Created, ins.Image})
	return substrate.AdoptionEvidence{Provider: "docker", ExternalID: ins.ID, Fingerprint: fingerprint, State: raw}, nil
}
func containerFingerprint(ins container.InspectResponse) string {
	raw, _ := json.Marshal(struct {
		ID, Created, Image string
		Config             any
		HostConfig         any
	}{ins.ID, ins.Created, ins.Image, ins.Config, ins.HostConfig})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func (s *Substrate) ApplyAdoption(ctx context.Context, e substrate.AdoptionEvidence) (substrate.NodeHandle, error) {
	identity, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || identity.Validate() != nil {
		return substrate.NodeHandle{}, fmt.Errorf("adoption requires operation identity")
	}
	if e.Provider != "docker" || !validAdoptionID(e.ExternalID) || e.Fingerprint == "" {
		return substrate.NodeHandle{}, fmt.Errorf("invalid adoption evidence")
	}
	path := s.adoptionPath(e.ExternalID)
	if err := validateTransferCatalog(filepath.Dir(path)); err != nil {
		return substrate.NodeHandle{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return substrate.NodeHandle{}, err
	}
	lock := flock.New(path + ".lock")
	locked, err := lock.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if !locked {
		return substrate.NodeHandle{}, ctx.Err()
	}
	defer lock.Close()
	ins, err := s.cli.ContainerInspect(ctx, e.ExternalID)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if ins.ID != e.ExternalID || ins.Config == nil || containerFingerprint(ins) != e.Fingerprint {
		return substrate.NodeHandle{}, fmt.Errorf("container changed since inspection")
	}
	if err := rejectConflictingAdoptionLabels(ins.Config.Labels, identity); err != nil {
		return substrate.NodeHandle{}, err
	}
	if raw, err := os.ReadFile(path); err == nil {
		var old adoptionBinding
		if json.Unmarshal(raw, &old) != nil || validateAdoptionBinding(old, ins, identity) != nil {
			return substrate.NodeHandle{}, fmt.Errorf("adoption ownership conflict")
		}
		return s.adoptHandle(ctx, ins, identity), nil
	} else if !os.IsNotExist(err) {
		return substrate.NodeHandle{}, err
	}
	binding := adoptionBinding{Version: 1, Identity: identity, ExternalID: ins.ID, Created: ins.Created, Image: ins.Image, Fingerprint: e.Fingerprint}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return substrate.NodeHandle{}, err
	}
	raw, _ := json.Marshal(binding)
	file, err := os.CreateTemp(filepath.Dir(path), ".adoption-")
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(raw); err != nil {
		return substrate.NodeHandle{}, err
	}
	if err = file.Sync(); err != nil {
		return substrate.NodeHandle{}, err
	}
	if err = file.Close(); err != nil {
		return substrate.NodeHandle{}, err
	}
	if err := os.Link(file.Name(), path); err != nil {
		return substrate.NodeHandle{}, err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return substrate.NodeHandle{}, err
	}
	return s.adoptHandle(ctx, ins, identity), nil
}
func (s *Substrate) adoptHandle(ctx context.Context, ins container.InspectResponse, id substrate.OperationIdentity) substrate.NodeHandle {
	return substrate.NodeHandle{ID: ins.ID, Generation: id.Generation, Provider: &HandleState{ContainerName: ins.Name}, Conn: substrate.ConnInfo{Kind: substrate.ConnKindDocker, User: "root"}}
}
func (s *Substrate) ObserveAdoption(ctx context.Context, e substrate.AdoptionEvidence) (substrate.NodeHandle, error) {
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || id.Validate() != nil || e.Provider != "docker" || !validAdoptionID(e.ExternalID) {
		return substrate.NodeHandle{}, fmt.Errorf("observation requires operation identity")
	}
	raw, err := os.ReadFile(s.adoptionPath(e.ExternalID))
	if err != nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, "docker", "adoption binding missing", err)
	}
	var binding adoptionBinding
	if err := json.Unmarshal(raw, &binding); err != nil {
		return substrate.NodeHandle{}, err
	}
	if !sameAdoptionOwner(binding.Identity, id) || binding.Fingerprint != e.Fingerprint {
		return substrate.NodeHandle{}, fmt.Errorf("adoption identity conflict")
	}
	ins, err := s.cli.ContainerInspect(ctx, e.ExternalID)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if err := validateAdoptionBinding(binding, ins, id); err != nil {
		return substrate.NodeHandle{}, err
	}
	return s.adoptHandle(ctx, ins, id), nil
}

func validAdoptionID(id string) bool {
	raw, err := hex.DecodeString(id)
	return err == nil && len(raw) == 32 && strings.ToLower(id) == id
}
func sameAdoptionOwner(a, b substrate.OperationIdentity) bool {
	return a.OwnerUID == b.OwnerUID && a.TopologyUID == b.TopologyUID && a.ResourceUID == b.ResourceUID && a.Generation == b.Generation
}
func rejectConflictingAdoptionLabels(labels map[string]string, id substrate.OperationIdentity) error {
	expected := operationLabels(id)
	for key, value := range labels {
		if strings.HasPrefix(key, "sysbox.") && key != "sysbox.action_id" {
			if want, ok := expected[key]; !ok || want != value {
				return fmt.Errorf("container already has conflicting ownership label %s", key)
			}
		}
	}
	return nil
}
func validateAdoptionBinding(binding adoptionBinding, ins container.InspectResponse, id substrate.OperationIdentity) error {
	if id.Validate() != nil || binding.Version != 1 || ins.Config == nil || !validAdoptionID(ins.ID) || binding.ExternalID != ins.ID || binding.Created != ins.Created || binding.Image != ins.Image || !sameAdoptionOwner(binding.Identity, id) || binding.Fingerprint != containerFingerprint(ins) {
		return fmt.Errorf("adopted container identity conflict")
	}
	return rejectConflictingAdoptionLabels(ins.Config.Labels, id)
}
