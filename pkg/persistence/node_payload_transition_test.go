package persistence

import (
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestNodeOperationTransitionPreservesDispatchedPayload(t *testing.T) {
	op := controlplane.NodeOperation{ID: "op", AgentID: "agent", Topology: "lab", Status: controlplane.NodeOperationStatusQueued, Adoption: &controlplane.AdoptionRequest{ID: "op", Evidence: substrate.AdoptionEvidence{Fingerprint: "original"}}}
	next := op
	changed := *op.Adoption
	changed.Evidence.Fingerprint = "replacement"
	next.Adoption = &changed
	require.ErrorIs(t, ValidateNodeOperationTransition(op, next), ErrConflict)
	next = op
	next.Adoption = nil
	require.ErrorIs(t, ValidateNodeOperationTransition(op, next), ErrConflict)
	op.Adoption = nil
	op.SSHAccess = &controlplane.SSHAccessRequest{ID: "access", AuthorizedKeys: []string{"original"}}
	next = op
	ssh := *op.SSHAccess
	ssh.AuthorizedKeys = []string{"replacement"}
	next.SSHAccess = &ssh
	require.ErrorIs(t, ValidateNodeOperationTransition(op, next), ErrConflict)
	next = op
	next.Status = controlplane.NodeOperationStatusRunning
	require.NoError(t, ValidateNodeOperationTransition(op, next))
}
