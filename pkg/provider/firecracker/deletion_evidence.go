package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
)

// Written after the exact VMM has stopped, before its generation is removed.
// The VM lock remains held through both writes. A cold reader must acquire that
// lock and independently establish absence; a record alone is not completion.
type deletionEvidence struct {
	Version   int                         `json:"version"`
	Identity  substrate.OperationIdentity `json:"identity"`
	VMID      string                      `json:"vm_id"`
	Directory string                      `json:"directory"`
	Socket    string                      `json:"socket"`
	Vsock     string                      `json:"vsock,omitempty"`
	Process   processIdentity             `json:"process"`
	StoppedAt time.Time                   `json:"stopped_at"`
}

func (s *Substrate) deletionEvidencePath(h substrate.NodeHandle) (string, error) {
	dir, err := s.manifestDirectory(h)
	if err != nil {
		return "", err
	}
	root, err := s.canonicalRoot()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(dir))
	path := filepath.Join(root, ".locks", fmt.Sprintf("%x.deleted.json", sum))
	return path, rejectSymlinks(path)
}

func (s *Substrate) recordStoppedDeletion(ctx context.Context, h substrate.NodeHandle, m *vmManifest) error {
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || m.TopologyUID == "" {
		return nil
	} // Unidentified legacy calls cannot produce operation evidence.
	if err := id.Validate(); err != nil {
		return err
	}
	path, err := s.deletionEvidencePath(h)
	if err != nil {
		return err
	}
	e := deletionEvidence{Version: 1, Identity: id, VMID: m.VMID, Directory: m.Handle.VMDir, Socket: m.Handle.Socket, Vsock: m.Handle.VsockUDS, Process: m.Process, StoppedAt: time.Now().UTC()}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err = atomicWriteFile(path, raw, 0600); err != nil {
		return err
	}
	// Persist the .locks directory entry as well as the receipt inside it.
	root, err := os.Open(filepath.Dir(filepath.Dir(path)))
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Sync()
}

func (s *Substrate) proveDeletedQuiescence(h substrate.NodeHandle, operationID string, id substrate.OperationIdentity) (substrate.QuiescenceProof, error) {
	var zero substrate.QuiescenceProof
	path, err := s.deletionEvidencePath(h)
	if err != nil {
		return zero, err
	}
	raw, err := readRegularFile(path)
	if err != nil {
		return zero, err
	}
	var e deletionEvidence
	if err := json.Unmarshal(raw, &e); err != nil {
		return zero, err
	}
	dir, err := s.manifestDirectory(h)
	if err != nil {
		return zero, err
	}
	hs, ok := h.Provider.(*HandleState)
	if !ok || hs == nil || e.Version != 1 || e.Identity != id || operationID != id.ActionID || e.VMID != h.ID || e.Directory != dir || e.StoppedAt.IsZero() || h.Generation != id.Generation || hs.OwnerUID != id.OwnerUID || hs.ResourceUID != id.ResourceUID || hs.TopologyUID != id.TopologyUID || hs.Generation != id.Generation || e.Socket != hs.Socket || e.Vsock != hs.VsockUDS {
		return zero, fmt.Errorf("firecracker deletion evidence identity conflict")
	}
	for _, p := range []string{dir, e.Socket, e.Vsock} {
		if p == "" {
			continue
		}
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			return zero, fmt.Errorf("firecracker deletion absence not confirmed: %s: %v", p, err)
		}
	}
	// loadManifest has already checked the supplied process and launch residue.
	// The receipt separately retains the original process after directory removal.
	if live, _, err := probeProcess(e.Process); err != nil || live {
		return zero, fmt.Errorf("firecracker deleted process quiescence unconfirmed: %v", err)
	}
	evidence := map[string]any{"provider": "firecracker", "anchor": "stopped-deletion", "receipt": string(raw)}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return zero, err
	}
	digest := sha256.Sum256(encoded)
	return substrate.QuiescenceProof{OperationID: operationID, Identity: id, Anchor: path, Evidence: evidence, Digest: fmt.Sprintf("sha256:%x", digest)}, nil
}
