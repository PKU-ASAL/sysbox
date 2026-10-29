package api

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
)

// runTaskStop authenticates historical routing, not permission to run. The
// command may be retired or re-leased; the child remains bound to its original
// parent epoch and immutable Run request. No worker payload supplies a node.
func (s *Server) runTaskStop(ctx context.Context, child, parent controlplane.ExecutionGrant) (controlplane.GuestTaskStop, error) {
	var zero controlplane.GuestTaskStop
	if child.ParentEpoch == 0 || parent.Epoch != child.ParentEpoch || parent.ParentEpoch != 0 || parent.AgentID != child.AgentID || parent.SessionID != child.SessionID || parent.CommandID != child.CommandID || parent.Attempt != child.Attempt || parent.Scope.TopologyUID != child.Scope.TopologyUID || !controlplane.IsRunExecutionOperation(parent.Operation) {
		return zero, persistence.ErrConflict
	}
	record, err := readFailureCommand(ctx, s.apiStore, parent.CommandID)
	if err != nil {
		return zero, err
	}
	if record == nil {
		return zero, persistence.ErrNotFound
	}
	cmd := record.Command
	if cmd.Type != "run_assigned" || cmd.Run == nil || cmd.ID != parent.CommandID || cmd.AgentID != parent.AgentID || cmd.Attempt < 1 || uint64(cmd.Attempt) < parent.Attempt || cmd.ActionID != parent.ActionID || parent.Scope != (controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID}) {
		return zero, persistence.ErrConflict
	}
	action := cmd.Run.Op
	if action == "" {
		action = cmd.Run.Operation
	}
	req := hostsupervisor.RunRequest{RunID: cmd.Run.ID, Topology: cmd.Run.Topology, TopologyUID: cmd.TopologyUID, Revision: cmd.Run.Revision, ActionID: cmd.ActionID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, Action: action, Target: cmd.Run.Target, ExpectedSerial: cmd.ExpectedSerial}
	digest, err := req.Digest()
	if err != nil || digest != parent.Digest || parent.Operation != "run_"+req.Action || controlstore.ValidatePathSegment(req.Topology, "topology") != nil || controlstore.ValidatePathSegment(req.RunID, "run") != nil {
		return zero, persistence.ErrConflict
	}
	proof, err := controlplane.SignRunExecutionCancellation(s.grantSigner, child, req.RunID, req.Topology)
	if err != nil {
		return zero, err
	}
	return controlplane.GuestTaskStop{Topology: req.Topology, Cancellation: proof}, nil
}
