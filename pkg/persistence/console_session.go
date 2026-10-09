package persistence

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func EncodeConsoleSession(sess controlplane.ConsoleSession) ([]byte, error) {
	if !topologyName.MatchString(sess.ID) {
		return nil, ErrInvalidRecord
	}
	return json.Marshal(sess)
}

func SameConsoleSession(a, b controlplane.ConsoleSession) bool {
	x, e1 := EncodeConsoleSession(a)
	y, e2 := EncodeConsoleSession(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}

// CAS compares the complete observation, while these fields bind the durable
// session incarnation. State progress and audit may change, ownership may not.
func ValidateConsoleSessionCAS(old, next controlplane.ConsoleSession) error {
	// CreatedAt participates in the cancellation command digest. Preserve its
	// serialized offset too, not only the instant represented by time.Time.
	if old.ID == "" || old.ID != next.ID || old.Topology != next.Topology || old.Node != next.Node || old.AgentID != next.AgentID || old.ProjectID != next.ProjectID || old.Workspace != next.Workspace || old.CreatedAt.Format(time.RFC3339Nano) != next.CreatedAt.Format(time.RFC3339Nano) {
		return ErrConflict
	}
	return nil
}
