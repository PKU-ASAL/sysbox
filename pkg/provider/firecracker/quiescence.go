package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/substrate"
)

// ProveQuiescence waits for the durable per-VM host operation lock and then
// validates the generation manifest. Firecracker requests are synchronous;
// the lock is held for the complete host-side transition, so acquiring it is
// the durable anchor that proves no controller transition for this VM remains
// in flight. Resource observation is intentionally not used as the proof.
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
	if err != nil {
		return substrate.QuiescenceProof{}, fmt.Errorf("firecracker quiescence manifest verification failed: %w", err)
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
