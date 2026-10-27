package libvirt

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

const managedRecordName = ".sysbox-managed.json"

type managedRecord struct {
	Version  int                         `json:"version"`
	Identity substrate.OperationIdentity `json:"identity"`
	Ready    bool                        `json:"ready"`
	State    HandleState                 `json:"state"`
}

func sameGeneration(a, b substrate.OperationIdentity) bool {
	return a.OwnerUID == b.OwnerUID && a.ResourceUID == b.ResourceUID && a.TopologyUID == b.TopologyUID && a.Generation == b.Generation
}
func (s *Substrate) managedVMDir(id substrate.OperationIdentity, name string) (string, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", driver.Wrap(driver.ErrorInvalidState, subName, "invalid managed domain name", nil)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", id.OwnerUID, id.ResourceUID, id.TopologyUID, id.Generation)))
	return filepath.Join(s.vmDirRoot(), fmt.Sprintf("sysbox-lv-%s-%x", name, sum[:12])), nil
}

func virshQuery(ctx context.Context, args ...string) ([]byte, error) {
	out, stderr := substrate.NewExecCapture(1<<20, nil), substrate.NewExecCapture(64<<10, nil)
	cmd := exec.CommandContext(ctx, "virsh", args...)
	cmd.Stdout = out
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, driver.Wrap(driver.ErrorUnavailable, subName, "domain query unavailable", err)
	}
	if out.Truncated() || stderr.Truncated() {
		return nil, driver.Wrap(driver.ErrorUnavailable, subName, "domain query exceeded bounded output", nil)
	}
	return []byte(out.String()), nil
}
func domainNames(ctx context.Context) ([]string, error) {
	out, err := virshQuery(ctx, "list", "--all", "--name")
	if err != nil {
		return nil, err
	}
	names := strings.Fields(string(out))
	if len(names) > 1024 {
		return nil, driver.Wrap(driver.ErrorUnavailable, subName, "domain query exceeds discovery bound", nil)
	}
	return names, nil
}

func existingDomainUUID(ctx context.Context, h substrate.NodeHandle) (string, bool, error) {
	if err := validateHandleIdentity(ctx, h); err != nil {
		return "", false, err
	}
	target := hsFrom(h).DomainUUID
	if target == "" {
		target = h.ID
	}
	if _, err := uuid.Parse(target); err != nil {
		return "", false, driver.Wrap(driver.ErrorInvalidState, subName, "lifecycle requires an exact domain UUID", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := virshQuery(ctx, "list", "--all", "--uuid")
	if err != nil {
		return "", false, err
	}
	for _, id := range strings.Fields(string(raw)) {
		if id == target {
			if hsFrom(h).Identity != nil {
				raw, err := virshQuery(ctx, "dumpxml", target)
				if err != nil {
					return "", false, err
				}
				var domain domainXML
				if err := xml.Unmarshal(raw, &domain); err != nil {
					return "", false, driver.Wrap(driver.ErrorUnavailable, subName, "invalid domain XML", err)
				}
				if domain.UUID != target || domain.Metadata == nil || domain.Metadata.Managed == nil {
					return "", false, driver.Wrap(driver.ErrorInvalidState, subName, "domain ownership metadata missing or UUID mismatch", nil)
				}
				m := domain.Metadata.Managed
				if hsFrom(h).ExternalDefinition {
					var stored HandleState
					if json.Unmarshal([]byte(m.State), &stored) != nil || !stored.ExternalDefinition || stored.AdoptionFingerprint != hsFrom(h).AdoptionFingerprint || stored.VMDir != "" || stored.DiskPath != "" {
						return "", false, fmt.Errorf("adopted domain recovery binding changed")
					}
				}
				actual := substrate.OperationIdentity{OwnerUID: m.OwnerUID, ResourceUID: m.ResourceUID, TopologyUID: m.TopologyUID, Generation: m.Generation}
				if !sameGeneration(actual, *hsFrom(h).Identity) {
					return "", false, driver.Wrap(driver.ErrorInvalidState, subName, "domain ownership or generation conflict", nil)
				}
			}
			return target, true, nil
		}
	}
	return target, false, nil
}

// Validate even when the domain is absent: Destroy may still remove its disk.
func validateHandleIdentity(ctx context.Context, h substrate.NodeHandle) error {
	hs := hsFrom(h)
	expected, supplied := substrate.OperationIdentityFromContext(ctx)
	if hs.Identity == nil || hs.Identity.Validate() != nil || h.Generation != hs.Identity.Generation || h.ID != hs.DomainUUID {
		return driver.Wrap(driver.ErrorInvalidState, subName, "managed handle identity mismatch", nil)
	}
	if hs.ExternalDefinition && (hs.VMDir != "" || hs.DiskPath != "" || hs.AdoptionFingerprint == "") {
		return fmt.Errorf("adopted definition must not own storage paths")
	}
	if supplied && (expected.Validate() != nil || !sameGeneration(expected, *hs.Identity)) {
		return driver.Wrap(driver.ErrorInvalidState, subName, "operation generation does not own domain", nil)
	}
	return nil
}
func domainNameAvailable(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	names, err := domainNames(ctx)
	if err != nil {
		return err
	}
	for _, existing := range names {
		if existing == name {
			return driver.Wrap(driver.ErrorInvalidState, subName, "domain name already exists; refusing replacement", nil)
		}
	}
	return nil
}

func (s *Substrate) DiscoverManagedNode(ctx context.Context, id substrate.OperationIdentity, name string) (substrate.NodeHandle, error) {
	if err := id.Validate(); err != nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, subName, "invalid discovery identity", err)
	}
	dir, err := s.managedVMDir(id, name)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, subName, "managed generation is not an owned directory", nil)
		}
	} else if !os.IsNotExist(err) {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, subName, "managed generation directory unavailable", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	names, err := domainNames(ctx)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	var matches []substrate.NodeHandle
	for _, candidate := range names {
		raw, err := virshQuery(ctx, "dumpxml", candidate)
		if err != nil {
			return substrate.NodeHandle{}, err
		}
		var domain domainXML
		if err := xml.Unmarshal(raw, &domain); err != nil {
			return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, subName, "invalid domain XML", err)
		}
		if domain.Metadata == nil || domain.Metadata.Managed == nil {
			if candidate == name {
				return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, subName, "domain name belongs to an unmanaged resource", nil)
			}
			continue
		}
		metadata := domain.Metadata.Managed
		actual := substrate.OperationIdentity{OwnerUID: metadata.OwnerUID, ResourceUID: metadata.ResourceUID, TopologyUID: metadata.TopologyUID, Generation: metadata.Generation, ActionID: metadata.ActionID}
		if !sameGeneration(actual, id) {
			if candidate == name {
				return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, subName, "domain ownership or generation conflict", nil)
			}
			continue
		}
		var hs HandleState
		if err := json.Unmarshal([]byte(metadata.State), &hs); err != nil {
			return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, subName, "managed domain recovery state unavailable", err)
		}
		if hs.Identity == nil || !sameGeneration(*hs.Identity, id) || hs.DomainUUID != domain.UUID || hs.DomainName != name || domain.Name != name || (!hs.ExternalDefinition && (hs.VMDir != dir || hs.DiskPath != filepath.Join(dir, "disk.qcow2"))) || (hs.ExternalDefinition && (hs.VMDir != "" || hs.DiskPath != "" || hs.AdoptionFingerprint == "")) {
			return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, subName, "managed domain recovery identity mismatch", nil)
		}
		if _, err := uuid.Parse(domain.UUID); err != nil {
			return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, subName, "managed domain UUID invalid", err)
		}
		matches = append(matches, managedNodeHandle(&hs))
	}
	if len(matches) > 1 {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, subName, "multiple domains match managed generation", nil)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	recordPath := filepath.Join(dir, managedRecordName)
	if info, err := os.Lstat(recordPath); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, subName, "managed recovery record is not a bounded regular file", nil)
		}
	} else if !os.IsNotExist(err) {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, subName, "managed recovery record unavailable", err)
	}
	raw, err := os.ReadFile(recordPath)
	if os.IsNotExist(err) {
		if _, dirErr := os.Lstat(dir); !os.IsNotExist(dirErr) {
			return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, subName, "managed generation directory exists without a recovery record", dirErr)
		}
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorNotFound, subName, "managed domain and prepared generation absent", nil)
	}
	if err != nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, subName, "prepared generation unavailable", err)
	}
	var record managedRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, subName, "prepared generation record corrupt", err)
	}
	hs := &record.State
	if record.Version != 1 || !sameGeneration(record.Identity, id) || hs.Identity == nil || !sameGeneration(*hs.Identity, id) || hs.DomainName != name || hs.VMDir != dir || hs.DiskPath != filepath.Join(dir, "disk.qcow2") {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, subName, "prepared generation ownership mismatch", nil)
	}
	if !record.Ready {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, subName, "prepared generation has incomplete creation; reconciliation required", nil)
	}
	if err := validateOwnedVMDirInRoot(s.vmDirRoot(), name, hs.DomainUUID, dir, hs.DiskPath); err != nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, subName, "prepared generation ownership unavailable", err)
	}
	if _, err := os.Stat(hs.DiskPath); err != nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, subName, "prepared disk unavailable", err)
	}
	return managedNodeHandle(hs), nil
}

func managedNodeHandle(hs *HandleState) substrate.NodeHandle {
	generation := int64(0)
	if hs.Identity != nil {
		generation = hs.Identity.Generation
	}
	return substrate.NodeHandle{ID: hs.DomainUUID, Generation: generation, Provider: hs, Net: substrate.NetInfo{PrimaryIP: hs.SSHIP}, Conn: substrate.ConnInfo{Kind: substrate.ConnKindSSH, User: hs.SSHUser}}
}
func persistManagedRecord(hs *HandleState, ready bool) error {
	if hs.Identity == nil {
		return nil
	}
	raw, err := json.Marshal(managedRecord{Version: 1, Identity: *hs.Identity, Ready: ready, State: *hs})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(hs.VMDir, ".managed-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(file.Name(), filepath.Join(hs.VMDir, managedRecordName)); err != nil {
		return err
	}
	dir, err := os.Open(hs.VMDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

var _ driver.ManagedDiscovery = (*Substrate)(nil)
