package libvirt

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/substrate"
)

type unownedAdoptionEvidence struct {
	Version int    `json:"version"`
	UUID    string `json:"uuid"`
	Unowned bool   `json:"unowned"`
}

func isUnownedEvidence(e substrate.AdoptionEvidence) bool {
	var v unownedAdoptionEvidence
	return json.Unmarshal(e.State, &v) == nil && v.Version == 1 && v.Unowned && v.UUID == e.ExternalID && e.ManagedIdentity == nil
}

// Scope is a stopped persistent domain definition. Its original storage and
// networking remain externally owned, and no disk path enters the managed handle.
func validateUnownedDomain(ctx context.Context, d domainXML, expected string) error {
	if _, err := uuid.Parse(d.UUID); err != nil {
		return fmt.Errorf("adoption needs exact UUID: %w", err)
	}
	if d.Name == "" {
		return fmt.Errorf("adoption needs domain name")
	}
	state, err := virshQuery(ctx, "domstate", d.UUID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(state)) != "shut off" {
		return fmt.Errorf("unowned adoption requires stopped domain")
	}
	inactive, err := virshQuery(ctx, "dumpxml", d.UUID, "--inactive")
	if err != nil {
		return err
	}
	var persisted domainXML
	if xml.Unmarshal(inactive, &persisted) != nil || persisted.UUID != d.UUID || persisted.Name != d.Name {
		return fmt.Errorf("persistent domain identity unavailable")
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(inactive)) != expected {
		return fmt.Errorf("inactive domain changed since inspection")
	}
	if persisted.Metadata != nil && persisted.Metadata.Managed != nil {
		return fmt.Errorf("persistent domain already has managed owner")
	}
	return nil
}

func (s *Substrate) applyExternalAdoption(ctx context.Context, e substrate.AdoptionEvidence, id substrate.OperationIdentity) (substrate.NodeHandle, error) {
	if id.Validate() != nil || e.Provider != subName || !isUnownedEvidence(e) {
		return substrate.NodeHandle{}, fmt.Errorf("invalid unowned adoption binding")
	}
	if _, err := uuid.Parse(e.ExternalID); err != nil {
		return substrate.NodeHandle{}, err
	}
	root := filepath.Join(s.vmDirRoot(), ".adoptions")
	if err := os.MkdirAll(root, 0700); err != nil {
		return substrate.NodeHandle{}, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return substrate.NodeHandle{}, fmt.Errorf("unsafe adoption lock directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
		return substrate.NodeHandle{}, fmt.Errorf("adoption lock directory is not private to its owner")
	}
	lockPath := filepath.Join(root, e.ExternalID+".lock")
	if info, err := os.Lstat(lockPath); err == nil {
		if !info.Mode().IsRegular() {
			return substrate.NodeHandle{}, fmt.Errorf("adoption lock must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return substrate.NodeHandle{}, err
	}
	lock := flock.New(lockPath)
	defer lock.Close()
	held, err := lock.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if !held {
		return substrate.NodeHandle{}, ctx.Err()
	}
	raw, err := virshQuery(ctx, "dumpxml", e.ExternalID)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	var d domainXML
	if xml.Unmarshal(raw, &d) != nil || d.UUID != e.ExternalID {
		return substrate.NodeHandle{}, fmt.Errorf("domain identity changed")
	}
	if d.Metadata != nil && d.Metadata.Managed != nil {
		return s.observeExternalAdoption(ctx, raw, e, id)
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != e.Fingerprint {
		return substrate.NodeHandle{}, fmt.Errorf("domain changed since inspection")
	}
	if err := validateUnownedDomain(ctx, d, e.Fingerprint); err != nil {
		return substrate.NodeHandle{}, err
	}
	hs := HandleState{Identity: &id, DomainName: d.Name, DomainUUID: d.UUID, ExternalDefinition: true, AdoptionFingerprint: e.Fingerprint, NetworkInit: substrate.GuestNetworkInitPreconfigured}
	payload, err := json.Marshal(hs)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	metadata := struct {
		XMLName xml.Name `xml:"urn:sysbox:managed managed"`
		domManaged
	}{domManaged: domManaged{OwnerUID: id.OwnerUID, ResourceUID: id.ResourceUID, TopologyUID: id.TopologyUID, Generation: id.Generation, ActionID: id.ActionID, State: string(payload)}}
	encoded, err := xml.Marshal(metadata)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if _, err := virshQuery(ctx, "metadata", d.UUID, "--config", "--uri", "urn:sysbox:managed", "--key", "sysbox", "--set", string(encoded)); err != nil {
		return substrate.NodeHandle{}, err
	}
	current, err := virshQuery(ctx, "dumpxml", e.ExternalID)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	return s.observeExternalAdoption(ctx, current, e, id)
}
func (s *Substrate) observeExternalAdoption(_ context.Context, raw []byte, e substrate.AdoptionEvidence, id substrate.OperationIdentity) (substrate.NodeHandle, error) {
	var d domainXML
	if xml.Unmarshal(raw, &d) != nil || d.UUID != e.ExternalID || d.Metadata == nil || d.Metadata.Managed == nil {
		return substrate.NodeHandle{}, fmt.Errorf("adoption ownership not committed")
	}
	m := d.Metadata.Managed
	if m.OwnerUID != id.OwnerUID || m.TopologyUID != id.TopologyUID || m.ResourceUID != id.ResourceUID || m.Generation != id.Generation {
		return substrate.NodeHandle{}, fmt.Errorf("adoption belongs to another identity")
	}
	var hs HandleState
	if json.Unmarshal([]byte(m.State), &hs) != nil || !hs.ExternalDefinition || hs.Identity == nil || !sameGeneration(*hs.Identity, id) || hs.DomainUUID != d.UUID || hs.DomainName != d.Name || hs.VMDir != "" || hs.DiskPath != "" || hs.AdoptionFingerprint != e.Fingerprint || m.ActionID != id.ActionID || hs.Identity.ActionID != id.ActionID {
		return substrate.NodeHandle{}, fmt.Errorf("invalid external adoption recovery record")
	}
	return managedNodeHandle(&hs), nil
}
func startExternalDomain(ctx context.Context, id string) error {
	raw, err := virshQuery(ctx, "domstate", id)
	if err != nil {
		return err
	}
	switch strings.TrimSpace(string(raw)) {
	case "running", "paused":
		return nil
	case "shut off":
		_, err = virshQuery(ctx, "start", id)
		return err
	default:
		return fmt.Errorf("adopted domain is not in stable state")
	}
}
