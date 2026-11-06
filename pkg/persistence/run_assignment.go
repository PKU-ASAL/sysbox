package persistence

import (
	"bytes"
	"encoding/json"
	"strings"

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
	// A run admitted through a durable request keeps that request fingerprint
	// for input hydration; the run-dispatch marker only names this command and
	// must not replace the run's request identity.
	identity := fingerprint
	if current.RequestFingerprint != "" && !strings.HasPrefix(current.RequestFingerprint, "run-dispatch:") {
		identity = current.RequestFingerprint
	}
	expected.RequestFingerprint = identity
	proposed.RequestFingerprint = identity
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
