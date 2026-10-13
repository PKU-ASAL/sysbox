package hostsupervisor

import (
	"context"
	"encoding/json"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
)

type runAdmissionEvidence struct {
	Version int              `json:"version"`
	Request RunRequest       `json:"request"`
	Run     controlplane.Run `json:"run"`
}

func (c *RunCoordinator) validateAdmission(a runAdmissionEvidence, g controlplane.SignedExecutionGrant) error {
	r, req := a.Run, a.Request
	if a.Version != 1 || r.ID != req.RunID || r.Topology != req.Topology || r.Revision != req.Revision || r.Op != req.Action || r.Target != req.Target || r.AgentID != c.AgentID || r.Status != controlplane.RunRunning || r.QueuedAt.IsZero() || r.Attempt < 1 || r.LeaseOwner == "" || r.Inputs != nil || r.Cleanup != nil || r.HostCompletion != nil {
		return effectgate.ErrFenced
	}
	return c.verifyHistorical(req, g)
}

func (c *RunCoordinator) prepareAdmission(ctx context.Context, req RunRequest, g controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	run, err := c.PrepareRun(ctx, req)
	if err != nil {
		return nil, err
	}
	run.Inputs, run.Cleanup, run.HostCompletion = nil, nil, nil
	a := runAdmissionEvidence{Version: 1, Request: req, Run: run}
	if err := c.validateAdmission(a, g); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

func (c *RunCoordinator) recoverNeverStarted(ctx context.Context, gate *effectgate.Gate, grant controlplane.ExecutionGrant, id string) error {
	prior, err := gate.ReadRunEnvelope(ctx, grant, id)
	if err != nil {
		return err
	}
	unlock, held, err := c.lockRunExecution(prior.Grant)
	if err != nil || !held {
		return err
	}
	defer unlock()
	prior, err = gate.ReadRun(ctx, prior.Grant, id)
	if err != nil {
		return err
	}
	if prior.State == effectgate.RunComplete {
		return nil
	}
	if prior.Started || len(prior.Checkpoint) != 0 {
		return effectgate.ErrUncertain
	}
	var a runAdmissionEvidence
	if json.Unmarshal(prior.Admission, &a) != nil || a.Request.ActionID != id {
		return effectgate.ErrFenced
	}
	if err := c.validateAdmission(a, prior.Grant); err != nil {
		return err
	}
	a.Run.Status, a.Run.Recoverable = controlplane.RunFailed, false
	a.Run.Err = "controller stopped after admission before entering runtime; no callback started"
	a.Run.EndedAt = time.Now().UTC()
	raw, err := json.Marshal(a.Run)
	if err != nil {
		return err
	}
	return gate.CommitNeverStartedRun(ctx, prior.Grant, id, raw)
}
