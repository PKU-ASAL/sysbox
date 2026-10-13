package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/stretchr/testify/require"
)

func TestColdDestroyRecoveryRequiresDurableReturnedEffects(t *testing.T) {
	for _, scenario := range []string{"returned", "unknown", "live", "apply", "changed-result"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			op := "destroy"
			if scenario == "apply" {
				op = "apply"
			}
			c, req, grant := runFixture(t, op)
			_, c.HostKey, _ = ed25519.GenerateKey(rand.Reader)
			run := controlplane.Run{ID: req.RunID, Topology: req.Topology, Revision: req.Revision, Target: req.Target, Op: op, AgentID: c.AgentID, Status: controlplane.RunRunning, QueuedAt: time.Now().UTC(), Attempt: 1, LeaseOwner: "original"}
			c.PrepareRun = func(context.Context, RunRequest) (controlplane.Run, error) { return run, nil }
			seed, err := c.prepareAdmission(ctx, req, grant)
			require.NoError(t, err)
			gate, err := c.gate(grant)
			require.NoError(t, err)
			_, err = gate.ReserveRunWithAdmission(ctx, grant, req.ActionID, seed)
			require.NoError(t, err)
			require.NoError(t, gate.MarkRunStarted(ctx, grant, req.ActionID))
			if scenario == "live" {
				unlock, held, err := c.lockRunExecution(grant)
				require.NoError(t, err)
				require.True(t, held)
				defer unlock()
			}
			calls, reports := 0, 0
			cold := &RunCoordinator{Root: c.Root, IssuerKey: c.IssuerKey, HostKey: c.HostKey, AgentID: c.AgentID, HostBootID: "restarted"}
			cold.RecoverReturnedDestroy = func(_ context.Context, request RunRequest, original controlplane.Run) (json.RawMessage, error) {
				calls++
				if scenario == "unknown" {
					return nil, effectgate.ErrUncertain
				}
				original.Status, original.Recoverable, original.EndedAt = controlplane.RunFailed, true, time.Now().UTC()
				if scenario == "changed-result" {
					original.ID = "foreign"
				}
				return json.Marshal(original)
			}
			cold.ReportRunID = func(_ context.Context, id string, receipt controlplane.SignedExecutionStopReceipt, raw json.RawMessage) error {
				reports++
				require.Equal(t, run.ID, id)
				require.NoError(t, controlplane.VerifyRunStopReceipt(c.HostKey.Public().(ed25519.PublicKey), receipt, raw))
				return nil
			}
			err = cold.Recover(ctx)
			if scenario == "unknown" || scenario == "changed-result" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			stored, err := gate.ReadRun(ctx, grant, req.ActionID)
			require.NoError(t, err)
			if scenario == "returned" {
				require.Equal(t, effectgate.RunComplete, stored.State)
				require.Equal(t, 1, reports)
			} else {
				require.Equal(t, effectgate.RunIntent, stored.State)
				require.Zero(t, reports)
			}
			if scenario == "live" || scenario == "apply" {
				require.Zero(t, calls)
			}
		})
	}
}
