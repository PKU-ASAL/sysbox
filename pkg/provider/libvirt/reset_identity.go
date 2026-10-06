package libvirt

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

func validateLibvirtResetIdentity(ctx context.Context, state *resetHandleState) error {
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || id != state.Identity {
		return fmt.Errorf("libvirt reset operation identity mismatch")
	}
	return nil
}

func (s *Substrate) validateResetPaths(state *resetHandleState) error {
	target, err := s.managedVMDir(state.Identity, state.DomainName)
	if err != nil {
		return err
	}
	old, err := s.managedVMDir(state.OldIdentity, state.DomainName)
	if err != nil {
		return err
	}
	if state.NewVMDir != target || state.OldVMDir != old || target == old || state.NewDomainUUID == state.OldDomainUUID {
		return fmt.Errorf("libvirt reset generation path or UUID mismatch")
	}
	expected := state.Identity
	expected.Generation = state.OldIdentity.Generation
	if !sameGeneration(expected, state.OldIdentity) {
		return fmt.Errorf("libvirt reset old generation belongs to another resource")
	}
	return nil
}

func (s *Substrate) validateResetRecord(state *resetHandleState, old, allowMissing bool) error {
	id, dir, uuid := state.Identity, state.NewVMDir, state.NewDomainUUID
	if old {
		id, dir, uuid = state.OldIdentity, state.OldVMDir, state.OldDomainUUID
	}
	expected, err := s.managedVMDir(id, state.DomainName)
	if err != nil {
		return err
	}
	if dir != expected {
		return fmt.Errorf("libvirt reset generation directory mismatch")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("libvirt reset generation directory is not owned")
	}
	path := filepath.Join(dir, managedRecordName)
	info, err = os.Lstat(path)
	if os.IsNotExist(err) && allowMissing {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return fmt.Errorf("libvirt reset record is not a bounded regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var record managedRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	if record.Version != 1 || !sameGeneration(record.Identity, id) || record.State.Identity == nil || !sameGeneration(*record.State.Identity, id) || record.State.DomainUUID != uuid || record.State.VMDir != dir || record.State.DomainName != state.DomainName {
		return fmt.Errorf("libvirt reset persisted generation ownership mismatch")
	}
	return nil
}

func (s *Substrate) verifyResetDomain(ctx context.Context, state *resetHandleState, old bool) (bool, error) {
	id, uuid, dir := state.Identity, state.NewDomainUUID, state.NewVMDir
	if old {
		id, uuid, dir = state.OldIdentity, state.OldDomainUUID, state.OldVMDir
	}
	h := managedNodeHandle(&HandleState{Identity: &id, DomainUUID: uuid})
	_, exists, err := existingDomainUUID(substrate.WithOperationIdentity(ctx, id), h)
	if err != nil || !exists {
		return exists, err
	}
	raw, err := virshQuery(ctx, "dumpxml", uuid)
	if err != nil {
		return false, err
	}
	var domain domainXML
	if err := xml.Unmarshal(raw, &domain); err != nil {
		return false, driver.Wrap(driver.ErrorUnavailable, subName, "reset domain XML unavailable", err)
	}
	if domain.UUID != uuid || domain.Name != state.DomainName || domain.Metadata == nil || domain.Metadata.Managed == nil {
		return false, fmt.Errorf("libvirt reset domain exact identity mismatch")
	}
	metadata := domain.Metadata.Managed
	actual := substrate.OperationIdentity{OwnerUID: metadata.OwnerUID, ResourceUID: metadata.ResourceUID, TopologyUID: metadata.TopologyUID, Generation: metadata.Generation, ActionID: metadata.ActionID}
	if !sameGeneration(actual, id) || (!old && actual.ActionID != id.ActionID) {
		return false, fmt.Errorf("libvirt reset domain generation ownership mismatch")
	}
	var hs HandleState
	if json.Unmarshal([]byte(metadata.State), &hs) != nil || hs.Identity == nil || !sameGeneration(*hs.Identity, id) || hs.DomainUUID != uuid || hs.VMDir != dir {
		return false, fmt.Errorf("libvirt reset domain recovery state mismatch")
	}
	disk := filepath.Join(dir, "disk.qcow2")
	for _, entry := range domain.Devices.Disks {
		if entry.Device == "disk" && entry.Source.File == disk {
			return true, nil
		}
	}
	return false, fmt.Errorf("libvirt reset domain disk ownership mismatch")
}
