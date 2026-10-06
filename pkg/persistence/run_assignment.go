package persistence

import (
	"bytes"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
)

// AssignRun preserves the queued execution snapshot; selecting an agent must
// not silently change its revision, inputs binding, or execution incarnation.
func AssignRun(current, proposed controlplane.Run, fingerprint string) (controlplane.Run, error) {
	current = DurableRun(current)
	proposed = DurableRun(proposed)
	if (current.Status != controlplane.RunQueued && current.Status != controlplane.RunAssigned) || proposed.Status != controlplane.RunAssigned || proposed.AgentID == "" || (current.Status == controlplane.RunQueued && proposed.AssignedAt.IsZero()) || current.AgentID != "" && current.AgentID != proposed.AgentID {
		return controlplane.Run{}, ErrConflict
	}
	expected := current
	if current.Status == controlplane.RunQueued {
		expected.MarkAssigned(proposed.AgentID, proposed.AssignedAt)
	}
	expected.RequestFingerprint = fingerprint
	proposed.RequestFingerprint = fingerprint
	a, err := json.Marshal(expected)
	if err != nil {
		return controlplane.Run{}, err
	}
	b, err := json.Marshal(proposed)
	if err != nil {
		return controlplane.Run{}, err
	}
	if !bytes.Equal(a, b) {
		return controlplane.Run{}, ErrConflict
	}
	return expected, nil
}
