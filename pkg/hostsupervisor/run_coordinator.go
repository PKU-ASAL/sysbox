package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
)

// RunCoordinator owns asynchronous Run admission and durable completion.
// Callbacks must be constructed
// by trusted host code from the pinned revision and atomic action repository.
type RunCoordinator struct {
	Root       string
	IssuerKey  ed25519.PublicKey
	HostBootID string
	AgentID    string
	Authorize  func(context.Context, controlplane.SignedExecutionGrant) error
	HostKey    ed25519.PrivateKey
	Report     func(context.Context, controlplane.SignedExecutionStopReceipt, json.RawMessage) error
	// ReportRunID is the API completion callback for a host-owned Run. RunID is
	// deliberately supplied separately because grant.ActionID identifies the
	// command action, while the API route is keyed by the durable Run ID.
	ReportRunID           func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error
	AuthorizationInterval time.Duration
	AuthorizationTimeout  time.Duration
	OnExecutionError      func(RunRequest, error)
	Tasks                 *Dispatcher
	PrepareRun            func(context.Context, RunRequest) (controlplane.Run, error)
	async                 *runRegistry
}

func (c *RunCoordinator) gate(g controlplane.SignedExecutionGrant) (*effectgate.Gate, error) {
	if c.AgentID == "" || g.Grant.AgentID != c.AgentID || c.HostBootID == "" || len(c.IssuerKey) != ed25519.PublicKeySize {
		return nil, effectgate.ErrFenced
	}
	if err := EnsureOwnedDirectory(c.Root, 0700); err != nil {
		return nil, err
	}
	return &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID, Authorize: func(ctx context.Context, _ controlplane.ExecutionGrant) error {
		if c.Authorize == nil {
			return effectgate.ErrFenced
		}
		return c.Authorize(ctx, g)
	}}, nil
}

// Recover seals and reports completed Run effects left behind by a controller
// crash. An intent with a sealed resource checkpoint may converge to failure
// after proving executor and child termination. Other intents remain uncertain;
// no provider callback is replayed. Exact durable results are read for reporting.
func (c *RunCoordinator) Recover(ctx context.Context) error {
	if c == nil || c.AgentID == "" || c.HostBootID == "" || len(c.HostKey) != ed25519.PrivateKeySize || (c.Report == nil && c.ReportRunID == nil) {
		return effectgate.ErrFenced
	}
	if err := EnsureOwnedDirectory(c.Root, 0700); err != nil {
		return err
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID}
	neverStartedErr := gate.RecoverNeverStarted(ctx, func(recoverCtx context.Context, grant controlplane.ExecutionGrant, effectID string) error {
		return c.recoverNeverStarted(recoverCtx, gate, grant, effectID)
	})
	checkpointErr := gate.RecoverRunCheckpoints(ctx, func(recoverCtx context.Context, grant controlplane.ExecutionGrant, effectID string) error {
		return c.recoverCheckpoint(recoverCtx, gate, grant, effectID)
	})
	completedErr := gate.RecoverCompleted(ctx, func(recoverCtx context.Context, grant controlplane.ExecutionGrant, effectID string) error {
		if grant.AgentID != c.AgentID || !controlplane.IsRunExecutionOperation(grant.Operation) {
			return effectgate.ErrFenced
		}
		reservation, err := gate.ReadRunEnvelope(recoverCtx, grant, effectID)
		if err != nil {
			return err
		}
		if reservation.State != effectgate.RunComplete {
			return effectgate.ErrUncertain
		}
		raw := reservation.Result
		if _, err := gate.SealStop(recoverCtx, grant, effectID, func(owned controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
			return controlplane.SignRunStopReceipt(c.HostKey, owned, c.HostBootID, rand.Text(), raw, time.Now().UTC())
		}); err != nil {
			return err
		}
		return gate.ReportStop(recoverCtx, grant, effectID, func(reportCtx context.Context, receipt controlplane.SignedExecutionStopReceipt) error {
			if c.ReportRunID != nil {
				var run controlplane.Run
				if err := json.Unmarshal(raw, &run); err != nil || run.ID == "" {
					return fmt.Errorf("host run recovery evidence lacks run identity")
				}
				return c.ReportRunID(reportCtx, run.ID, receipt, raw)
			}
			return c.Report(reportCtx, receipt, raw)
		})
	})
	return errors.Join(neverStartedErr, checkpointErr, completedErr)
}
