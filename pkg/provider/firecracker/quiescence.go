package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/oslab/sysbox/pkg/substrate"
)

// ProveQuiescence excludes cooperating controllers, then requires durable
// evidence. A free controller lock does not prove that an interrupted request
// stopped inside the VMM. Only an unlaunched prepared generation or a stopped
// deletion receipt can establish quiescence here.
func (s *Substrate) ProveQuiescence(ctx context.Context, handle substrate.NodeHandle, operationID string, identity substrate.OperationIdentity) (substrate.QuiescenceProof, error) {
	if operationID == "" {
		return substrate.QuiescenceProof{}, fmt.Errorf("firecracker quiescence requires operation identity")
	}
	if err := identity.Validate(); err != nil {
		return substrate.QuiescenceProof{}, err
	}
	if err := ctx.Err(); err != nil {
		return substrate.QuiescenceProof{}, err
	}
	release, err := s.lockHostOperation(ctx, handle)
	if err != nil {
		return substrate.QuiescenceProof{}, fmt.Errorf("firecracker quiescence anchor unavailable: %w", err)
	}
	defer release()
	manifestCtx := substrate.WithOperationIdentity(ctx, identity)
	m, err := s.loadManifest(manifestCtx, handle)
	if errors.Is(err, errGenerationAbsent) {
		return s.proveDeletedQuiescence(handle, operationID, identity)
	}
	if err != nil {
		return substrate.QuiescenceProof{}, fmt.Errorf("firecracker quiescence manifest verification failed: %w", err)
	}
	if operationID != identity.ActionID || m.ActionID != identity.ActionID {
		return substrate.QuiescenceProof{}, fmt.Errorf("firecracker quiescence operation identity conflict")
	}
	if m.Phase != "prepared" || m.Process.PID != 0 {
		return substrate.QuiescenceProof{}, substrate.ErrQuiescenceUnsupported
	}
	if residue, err := hasLaunchResidue(m.Handle.ConfigPath); err != nil || residue {
		return substrate.QuiescenceProof{}, fmt.Errorf("firecracker launch quiescence unconfirmed: %v", err)
	}
	evidence := map[string]any{
		"provider":        "firecracker",
		"anchor":          "vm-operation-lock",
		"manifest_path":   m.Handle.ManifestPath,
		"manifest_action": m.ActionID,
		"manifest_phase":  m.Phase,
		"process_pid":     m.Process.PID,
		"process_start":   m.Process.StartTime,
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return substrate.QuiescenceProof{}, err
	}
	digest := sha256.Sum256(raw)
	return substrate.QuiescenceProof{
		OperationID: operationID,
		Anchor:      fmt.Sprintf("%s:%s", m.Handle.ManifestPath, m.ActionID),
		Identity:    identity,
		Evidence:    evidence,
		Digest:      "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}
