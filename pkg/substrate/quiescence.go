package substrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrQuiescenceUnsupported is returned when a substrate cannot prove that a
// previously issued provider operation has stopped executing.
var ErrQuiescenceUnsupported = errors.New("provider quiescence proof unsupported")

// QuiescenceProof is durable provider evidence that one exact operation is no
// longer in flight. It is deliberately separate from resource observation:
// an object being present or absent does not prove that a timed-out request
// cannot still mutate it.
type QuiescenceProof struct {
	OperationID string            `json:"operation_id"`
	Anchor      string            `json:"anchor"`
	Identity    OperationIdentity `json:"identity"`
	Evidence    map[string]any    `json:"evidence,omitempty"`
	Digest      string            `json:"digest"`
}

func (p QuiescenceProof) Validate(expected OperationIdentity, operationID string) error {
	if operationID == "" || p.OperationID != operationID || p.Anchor == "" {
		return fmt.Errorf("invalid provider quiescence proof identity")
	}
	if err := expected.Validate(); err != nil {
		return err
	}
	if p.Identity != expected {
		return fmt.Errorf("provider quiescence proof generation identity conflict")
	}
	if p.Digest == "" {
		return fmt.Errorf("provider quiescence proof missing evidence digest")
	}
	raw, err := json.Marshal(p.Evidence)
	if err != nil {
		return err
	}
	want := sha256.Sum256(raw)
	if p.Digest != "sha256:"+hex.EncodeToString(want[:]) {
		return fmt.Errorf("provider quiescence proof evidence digest mismatch")
	}
	return nil
}

// OperationQuiescer is optional. Implementations must query a durable daemon
// or host anchor and return only after the exact operation is known to have
// stopped. Returning a normal resource observation is insufficient.
type OperationQuiescer interface {
	ProveQuiescence(ctx context.Context, handle NodeHandle, operationID string, identity OperationIdentity) (QuiescenceProof, error)
}
