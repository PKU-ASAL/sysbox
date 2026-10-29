package api

import (
	"context"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (s *Server) issueCommandGrant(ctx context.Context, agentID, commandID string, proof ExecutionGrantRequest) (*controlplane.ExecutionGrant, error) {
	var request controlplane.ExecutionGrant
	storage := s.commandGrantStorage(commandID, func(record *persistence.CommandRecord) error {
		var err error
		request, err = s.sourceGrant(ctx, agentID, proof, record)
		return err
	})
	var granted *controlplane.ExecutionGrant
	var transitionErr error
	err := storage.UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
		memory := &controlstore.GrantTransaction{Raw: raw}
		ttl := time.Until(request.ExpiresAt)
		if ttl > persistence.MaxExecutionGrantTTL {
			ttl = persistence.MaxExecutionGrantTTL
		}
		granted, transitionErr = persistence.NewExecutionGrantRepository(memory).Issue(ctx, request, ttl)
		return memory.Raw, nil
	})
	if err == nil {
		err = transitionErr
	}
	if err != nil {
		return nil, err
	}
	return granted, nil
}

// commandGrantStorage is the production boundary for every command-backed
// grant. Raw authority storage remains available for session/stop transitions,
// which must work even after lifecycle metadata has been retired.
func (s *Server) commandGrantStorage(id string, validate func(*persistence.CommandRecord) error) controlstore.AuthorityStorage {
	return controlstore.AuthorityStorage{Store: s.apiStore, CommandID: id, Validate: validate, Lifecycle: s.actionRepository}
}

// canonicalRunGrantRequest is shared by issuance and historical fence intake.
// Its wire encoding must remain exactly the host supervisor protocol encoding.
func canonicalRunGrantRequest(cmd controlplane.AgentCommand) (hostsupervisor.RunRequest, error) {
	if cmd.Type != "run_assigned" || cmd.Run == nil {
		return hostsupervisor.RunRequest{}, persistence.ErrCorruptRecord
	}
	action := cmd.Run.Op
	if action == "" {
		action = cmd.Run.Operation
	}
	req := hostsupervisor.RunRequest{RunID: cmd.Run.ID, Topology: cmd.Run.Topology, TopologyUID: cmd.TopologyUID, Revision: cmd.Run.Revision, ActionID: cmd.ActionID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, Action: action, Target: cmd.Run.Target, ExpectedSerial: cmd.ExpectedSerial}
	return req, req.Validate()
}
