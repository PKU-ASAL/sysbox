package libvirt

import (
	"context"
	"encoding/xml"
	"fmt"
	"os/exec"
	"strings"

	"github.com/oslab/sysbox/pkg/driver"
)

// DeleteManagedExternal removes an orphaned domain definition after verifying
// its immutable UUID and managed metadata. Storage is deliberately preserved;
// GC must never delete a disk that may be shared by another generation.
func (s *Substrate) DeleteManagedExternal(ctx context.Context, externalID, ownerUID, resourceUID string, generation int64) error {
	if externalID == "" || ownerUID == "" || resourceUID == "" || generation < 1 {
		return driver.Wrap(driver.ErrorInvalidState, subName, "incomplete GC domain ownership identity", nil)
	}
	raw, err := virshQuery(ctx, "dumpxml", externalID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return nil
		}
		return err
	}
	var domain domainXML
	if err := xml.Unmarshal(raw, &domain); err != nil {
		return driver.Wrap(driver.ErrorUnavailable, subName, "invalid GC domain XML", err)
	}
	if domain.UUID != externalID || domain.Metadata == nil || domain.Metadata.Managed == nil {
		return driver.Wrap(driver.ErrorInvalidState, subName, "GC domain ownership metadata missing", nil)
	}
	m := domain.Metadata.Managed
	if m.OwnerUID != ownerUID || m.TopologyUID != ownerUID || m.ResourceUID != resourceUID || m.Generation != generation {
		return driver.Wrap(driver.ErrorInvalidState, subName, "GC domain ownership or generation changed", nil)
	}
	state, err := virshQuery(ctx, "domstate", externalID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(state)) != "shut off" {
		if out, stopErr := exec.CommandContext(ctx, "virsh", "destroy", externalID).CombinedOutput(); stopErr != nil {
			return fmt.Errorf("libvirt GC destroy domain: %w\n%s", stopErr, out)
		}
	}
	if out, err := exec.CommandContext(ctx, "virsh", "undefine", externalID).CombinedOutput(); err != nil {
		return fmt.Errorf("libvirt GC undefine domain: %w\n%s", err, out)
	}
	return nil
}

var _ interface {
	DeleteManagedExternal(context.Context, string, string, string, int64) error
} = (*Substrate)(nil)
