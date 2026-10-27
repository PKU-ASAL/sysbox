package libvirt

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/provider/network"
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

// A missing domain does not by itself prove the absence of host TAP residue.
// Legacy state lacks a TAP ifindex; require the pinned owned bridge to have no
// ports at all. Shared bridges remain unresolved rather than guessing a device.
func (s *Substrate) HostFenceAttachmentAbsent(ctx context.Context, h substrate.NodeHandle, name string, raw json.RawMessage) error {
	if err := s.HostFenceDeletionAbsent(ctx, h); err != nil {
		return err
	}
	var st attachmentState
	if json.Unmarshal(raw, &st) != nil || name == "" || st.Bridge == "" || st.MAC == "" {
		return fmt.Errorf("attachment bridge/MAC identity missing")
	}
	var target *network.BridgeTarget
	for _, attachment := range hsFrom(h).Bridges {
		if attachment.Name != name {
			continue
		}
		if target != nil || attachment.Bridge != st.Bridge || attachment.MAC != st.MAC || attachment.Target.Bridge != st.Bridge {
			return fmt.Errorf("attachment bridge identity conflicts")
		}
		copy := attachment.Target
		target = &copy
	}
	if target == nil {
		return fmt.Errorf("owned attachment bridge proof missing")
	}
	bridge, err := network.OpenOwnedBridge(ctx, *target)
	if err != nil {
		return err
	}
	defer bridge.Close()
	return bridge.RequireNoAttachmentPorts(ctx)
}
