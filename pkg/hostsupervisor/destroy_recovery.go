package hostsupervisor

import (
	"context"
	"encoding/json"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/persistence"
)

func (c *RunCoordinator) recoverReturnedDestroy(ctx context.Context, gate *effectgate.Gate, grant controlplane.ExecutionGrant, id string) error {
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
	if !prior.Started || len(prior.Checkpoint) != 0 {
		return effectgate.ErrUncertain
	}
	var a runAdmissionEvidence
	if json.Unmarshal(prior.Admission, &a) != nil || a.Request.ActionID != id || a.Request.Action != "destroy" {
		return effectgate.ErrFenced
	}
	if err := c.validateAdmission(a, prior.Grant); err != nil {
		return err
	}
	raw, err := c.RecoverReturnedDestroy(ctx, a.Request, a.Run)
	if err != nil {
		return err
	}
	var result controlplane.Run
	if json.Unmarshal(raw, &result) != nil || !persistence.SameRunIncarnation(a.Run, result) || result.Attempt != a.Run.Attempt || result.LeaseOwner != a.Run.LeaseOwner || result.Status != controlplane.RunFailed || result.EndedAt.IsZero() || !result.Recoverable || result.Cleanup != nil || result.Inputs != nil || result.HostCompletion != nil {
		return effectgate.ErrFenced
	}
	return gate.CommitQuiescentRun(ctx, prior.Grant, id, raw)
}
