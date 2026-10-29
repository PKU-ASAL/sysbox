package api

import (
	"context"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
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
		memory := &grantTransaction{raw: raw}
		ttl := time.Until(request.ExpiresAt)
		if ttl > persistence.MaxExecutionGrantTTL {
			ttl = persistence.MaxExecutionGrantTTL
		}
		granted, transitionErr = persistence.NewExecutionGrantRepository(memory).Issue(ctx, request, ttl)
		return memory.raw, nil
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
func (s *Server) commandGrantStorage(id string, validate func(*persistence.CommandRecord) error) apiExecutionGrantStorage {
	return apiExecutionGrantStorage{store: s.apiStore, commandID: id, validate: validate, lifecycle: s.actionRepository}
}

// Preserve source-error precedence and the durable clock floor on rejection.
// A lifecycle conflict commits only ObserveTime, never a grant or lease write;
// actual storage errors still abort their transaction.
func (b apiExecutionGrantStorage) checkLifecycle(record *persistence.CommandRecord, check func(topology string) error) error {
	if b.lifecycle == nil || b.sourceErr != nil && *b.sourceErr != nil {
		return nil
	}
	topology, err := grantCommandTopology(record)
	if err == nil {
		err = check(topology)
	}
	if err == nil {
		return nil
	}
	return b.rejectGrantSource(err)
}

// A worker cannot supply another topology name for a retained command. Resolve
// it only from the durable dispatch payload and pair it with its immutable UID.
func grantCommandTopology(record *persistence.CommandRecord) (string, error) {
	if record == nil || record.Command.TopologyUID == "" {
		return "", persistence.ErrConflict
	}
	c := record.Command
	topology := ""
	switch c.Type {
	case "run_assigned":
		if c.Run != nil {
			topology = c.Run.Topology
		}
	case "node_operation":
		topology = c.Operation.Topology
	case "guest_execution":
		if c.Execution != nil {
			topology = c.Execution.Topology
		}
	case "guest_file_put":
		if c.FilePut != nil {
			topology = c.FilePut.Topology
		}
	case "gc_delete", "session_open":
		if c.Session != nil {
			topology = c.Session.Topology
		}
	default:
		return "", persistence.ErrUnsupportedVersion
	}
	if err := validatePathSegment(topology, "topology"); err != nil {
		return "", persistence.ErrConflict
	}
	return topology, nil
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
