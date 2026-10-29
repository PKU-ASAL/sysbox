package firecracker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"golang.org/x/sys/unix"
)

// HostFenceScope is only the local process/storage boundary. Enrollment and
// actual host cessation must be established separately by the controller.
func (s *Substrate) HostFenceScope(context.Context) (string, error) {
	root, err := s.canonicalRoot()
	if err != nil {
		return "", err
	}
	var fs unix.Statfs_t
	if err = unix.Statfs(root, &fs); err != nil {
		return "", err
	}
	if fs.Type != unix.TMPFS_MAGIC && fs.Type != unix.EXT4_SUPER_MAGIC {
		return "", fmt.Errorf("host fencing requires kernel-local Firecracker storage")
	}
	return "local-only", nil
}

// Reuse manifest absence validation, including original process/launch residue,
// then independently account for both sockets which may live outside VMDir.
func (s *Substrate) HostFenceDeletionAbsent(ctx context.Context, h substrate.NodeHandle) error {
	if _, err := s.HostFenceScope(ctx); err != nil {
		return err
	}
	hs, ok := h.Provider.(*HandleState)
	id, scoped := substrate.OperationIdentityFromContext(ctx)
	if !ok || hs == nil || !scoped || id.Validate() != nil || hs.OwnerUID != id.OwnerUID || hs.TopologyUID != id.TopologyUID || hs.ResourceUID != id.ResourceUID || hs.Generation != id.Generation || h.Generation != id.Generation {
		return fmt.Errorf("exact Firecracker generation identity required")
	}
	dir, err := s.manifestDirectory(h)
	if err != nil {
		return err
	}
	root, err := s.canonicalRoot()
	if err != nil {
		return err
	}
	expectedDir := filepath.Join(root, id.TopologyUID, id.ResourceUID, strconv.FormatInt(id.Generation, 10))
	if dir != expectedDir || hs.ConfigPath != filepath.Join(dir, "vm_config.json") || hs.ManifestPath != filepath.Join(dir, "manifest.json") {
		return fmt.Errorf("Firecracker generation directory identity conflict")
	}
	expectedSocket := ownedUnixSocketPath(dir, h.ID, "firecracker.sock", "fc")
	expectedVsock := ownedUnixSocketPath(dir, h.ID, "vsock.sock", "vs")
	if hs.JailRoot != "" {
		expectedSocket = filepath.Join(hs.JailRoot, "run/firecracker.sock")
		expectedVsock = filepath.Join(hs.JailRoot, "run/vsock.sock")
	}
	if hs.Socket != expectedSocket || hs.VsockUDS != "" && hs.VsockUDS != expectedVsock {
		return fmt.Errorf("Firecracker endpoint identity conflict")
	}
	observed, err := s.ObserveNode(ctx, h)
	if err != nil {
		return err
	}
	if observed.Status != substrate.NodeStatusMissing {
		return fmt.Errorf("Firecracker generation residue remains")
	}
	for _, path := range []string{dir, hs.Socket, hs.VsockUDS, hs.JailRoot} {
		if path == "" {
			continue
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return fmt.Errorf("Firecracker residue absence unproven: %s", path)
		}
	}
	return nil
}

func (s *Substrate) HostFenceAttachmentAbsent(ctx context.Context, h substrate.NodeHandle, name string, raw json.RawMessage) error {
	if err := s.HostFenceDeletionAbsent(ctx, h); err != nil {
		return err
	}
	var st attachmentState
	if json.Unmarshal(raw, &st) != nil || name == "" || st.Link == nil || st.Tap != st.Link.Name || st.NetNS != st.Link.Namespace {
		return fmt.Errorf("exact owned TAP proof required")
	}
	hs := h.Provider.(*HandleState)
	found := false
	for _, a := range hs.Attachments {
		if a.Name != name {
			continue
		}
		if found || a.Link != *st.Link || a.GuestDevice != st.GuestDevice {
			return fmt.Errorf("TAP attachment identity conflict")
		}
		found = true
	}
	if !found {
		return fmt.Errorf("TAP attachment missing from canonical generation")
	}
	id, _ := substrate.OperationIdentityFromContext(ctx)
	alias, err := network.AttachmentAlias(id, name)
	if err != nil {
		return err
	}
	err = network.ObserveOwnedAttachment(ctx, *st.Link, alias)
	if driver.IsCategory(err, driver.ErrorNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("owned TAP remains")
}
