package effectgate

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// CheckpointRun records trusted recovery evidence without authorizing effects.
// Only the current intent envelope can install a checkpoint; its contents are
// immutable thereafter, including across renewal. This is not a worker RPC.
func (g *Gate) CheckpointRun(ctx context.Context, current controlplane.SignedExecutionGrant, effectID string, checkpoint json.RawMessage) error {
	if effectID == "" || !isRunGrant(current.Grant) || !json.Valid(checkpoint) || bytes.Equal(bytes.TrimSpace(checkpoint), []byte("null")) {
		return ErrFenced
	}
	if err := controlplane.VerifyExecutionGrant(g.PublicKey, current, current.BootID, current.IssuedAt); err != nil {
		return err
	}
	path, record, release, err := g.locked(ctx, current.Grant.Scope.TopologyUID)
	if err != nil {
		return err
	}
	defer release()
	prior, err := currentRunIntent(record, current, effectID)
	if err != nil {
		return err
	}
	if len(prior.Admission) != 0 && !prior.Started {
		return ErrFenced
	}
	if len(prior.Checkpoint) != 0 {
		a, _ := json.Marshal(prior.Checkpoint)
		b, _ := json.Marshal(checkpoint)
		if !bytes.Equal(a, b) {
			return ErrFenced
		}
	}
	prior.Checkpoint = append(json.RawMessage(nil), checkpoint...)
	record.Outcomes[outcomeKey(current.Grant, effectID)] = prior
	return save(path, record)
}

// RecoverRunCheckpoints visits only intents with explicit recovery evidence.
// The trusted callback must establish executor and child quiescence; the
// checkpoint alone does not establish termination or authorize replay.
func (g *Gate) RecoverRunCheckpoints(ctx context.Context, recover func(context.Context, controlplane.ExecutionGrant, string) error) error {
	// The trusted callback bounds each child and the final commit separately;
	// a fixed whole-Run deadline would starve larger inventories on every pass.
	return g.recoverOutcomes(ctx, 0, func(prior outcome) bool {
		return prior.State == string(RunIntent) && prior.SignedGrant != nil && len(prior.Checkpoint) != 0
	}, recover)
}
