package libvirt

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/substrate"
	"os"
	"path/filepath"
	"strings"
)

func (s *Substrate) HostFenceScope(ctx context.Context) (string, error) {
	raw, err := virshQuery(ctx, "uri")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(raw)) != "qemu:///system" {
		return "", fmt.Errorf("host fencing requires local system libvirt")
	}
	return "local-only", nil
}

// Domain absence alone is not completion of managed disk-directory deletion.
func (s *Substrate) HostFenceDeletionAbsent(ctx context.Context, h substrate.NodeHandle) error {
	if _, err := s.HostFenceScope(ctx); err != nil {
		return err
	}
	hs := hsFrom(h)
	if hs.DomainUUID == "" || h.ID != hs.DomainUUID {
		return fmt.Errorf("missing exact domain identity")
	}
	present, err := gcDomainPresent(ctx, hs.DomainUUID)
	if err != nil {
		return err
	}
	if present {
		return fmt.Errorf("domain still present")
	}
	if hs.ExternalDefinition {
		return nil
	} // Adopted definition deletion preserves external storage.
	if hs.VMDir == "" || !filepath.IsAbs(hs.VMDir) {
		return fmt.Errorf("managed VM directory identity missing")
	}
	relative, err := filepath.Rel(s.vmDirRoot(), hs.VMDir)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("managed VM directory outside provider root")
	}
	if _, err = os.Lstat(hs.VMDir); !os.IsNotExist(err) {
		return fmt.Errorf("managed VM residue not proven absent")
	}
	return nil
}
