package agentexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

type returnedResetBridge struct {
	*LocalBridge
	run   *controlplane.Run
	locks atomic.Int32
}

func (b *returnedResetBridge) ParentRun(context.Context, string) (*controlplane.Run, error) {
	return b.run, nil
}
func (b *returnedResetBridge) MaterializeRunRevision(context.Context, string, string) (string, error) {
	return b.opts.ConfigFile, nil
}
func (b *returnedResetBridge) LockTopology(ctx context.Context, topology string) (func(), error) {
	b.locks.Add(1)
	return b.LocalBridge.LockTopology(ctx, topology)
}

func TestHostResetFailureUsesReturnedExecutorAndDurablePhases(t *testing.T) {
	for _, phase := range []string{"accepted", "preparing", "no-action"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			hcl := filepath.Join(root, "field.sysbox.hcl")
			require.NoError(t, os.WriteFile(hcl, []byte("invalid HCL !"), 0600))
			now := time.Now().UTC()
			run := &controlplane.Run{ID: "run", Topology: "lab", Op: "reset", Revision: "revision", AgentID: "agent", Status: controlplane.RunRunning, LeaseOwner: "owner", LeaseUntil: now.Add(time.Minute), QueuedAt: now}
			b := &returnedResetBridge{LocalBridge: NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: hcl, StatePath: filepath.Join(root, "state.json"), RunsDir: root, Log: io.Discard}), run: run}
			mgr, err := b.StateManager("lab")
			require.NoError(t, err)
			st, err := mgr.Load()
			require.NoError(t, err)
			res := state.Resource{Address: address.Resource("sysbox_node", "node"), OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1}
			st.Resources = append(st.Resources, res)
			require.NoError(t, mgr.Save(st))
			repo, err := b.ActionRepository("lab")
			require.NoError(t, err)
			a := runtime.ActionRecord{ID: "node-action", Topology: "lab", RunID: run.ID, RunQueuedAt: now, RunRevision: run.Revision, Resource: res.Address, OwnerUID: st.TopologyUID, ResourceUID: res.ResourceUID, Generation: 2, Candidate: &res, StateSerial: st.Meta.Serial, ExpectedSerial: st.Meta.Serial, Action: controlplane.PlanActionReset, Status: runtime.ActionIntentCommitted, Phase: "accepted"}
			if phase != "no-action" {
				require.NoError(t, repo.CommitActionIntent(ctx, a))
			}
			if phase == "preparing" {
				a.Revision, a.Phase, a.Status = 1, phase, runtime.ActionUncertain
				require.NoError(t, repo.CommitActionOutcome(ctx, a, nil))
			}
			req := hostsupervisor.RunRequest{RunID: run.ID, Topology: "lab", TopologyUID: st.TopologyUID, Revision: run.Revision, ActionID: "run-action", Action: "reset", ExpectedSerial: st.Meta.Serial}
			digest, err := req.Digest()
			require.NoError(t, err)
			issuer, issuerKey, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			hostKey, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			g, err := controlplane.SignExecutionGrant(issuerKey, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: st.TopologyUID}, AgentID: "agent", SessionID: "session", CommandID: "cmd", ActionID: req.ActionID, Attempt: 1, Epoch: 1, Operation: "run_reset", Digest: digest, TargetBootID: "host", State: controlplane.GrantActive, ExpiresAt: now.Add(time.Minute)}, "host", now)
			require.NoError(t, err)
			c := &hostsupervisor.RunCoordinator{Root: t.TempDir(), IssuerKey: issuer, HostKey: hostPrivate, AgentID: "agent", HostBootID: "host", Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }}
			require.NoError(t, os.Chmod(c.Root, 0700))
			service, stopService := context.WithCancel(ctx)
			t.Cleanup(func() {
				stopService()
				// ObserveAsync may publish a completed receipt before the executor's
				// final ReportStop fsync. Drain its process-independent execution lock
				// before TempDir removal; do not race a background writer with cleanup.
				paths, err := filepath.Glob(filepath.Join(c.Root, "executor-*.lock"))
				require.NoError(t, err)
				bounded, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				for _, path := range paths {
					lock := flock.New(path)
					held, err := lock.TryLockContext(bounded, 5*time.Millisecond)
					require.NoError(t, err)
					require.True(t, held)
					require.NoError(t, lock.Close())
				}
			})
			reported := make(chan controlplane.Run, 1)
			var reportMu sync.Mutex
			var receiptID string
			c.ReportRunID = func(_ context.Context, _ string, r controlplane.SignedExecutionStopReceipt, raw json.RawMessage) error {
				require.NoError(t, controlplane.VerifyRunStopReceipt(hostKey, r, raw))
				var result controlplane.Run
				require.NoError(t, json.Unmarshal(raw, &result))
				// ObserveAsync and the background controller may report the same
				// sealed receipt concurrently. Model the API's idempotent receiver.
				reportMu.Lock()
				defer reportMu.Unlock()
				if receiptID == "" {
					receiptID = r.ReceiptID
					reported <- result
				} else {
					require.Equal(t, receiptID, r.ReceiptID)
				}
				return nil
			}
			h := &HostRunExecutor{Bridge: b, Tasks: &hostsupervisor.Dispatcher{AgentID: "agent"}}
			_, err = c.StartAsync(ctx, service, req, g, func(callback context.Context) (json.RawMessage, error) { return h.Execute(callback, req) })
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				obs, err := c.ObserveAsync(ctx, req, g)
				return err == nil && obs.Status != "running"
			}, 5*time.Second, 10*time.Millisecond)
			require.Equal(t, int32(2), b.locks.Load(), "failed executor must reacquire execution exclusion for evidence")
			if phase == "accepted" || phase == "no-action" {
				select {
				case result := <-reported:
					require.Equal(t, controlplane.RunFailed, result.Status)
					require.Equal(t, phase == "accepted", result.Recoverable)
					require.Nil(t, result.Cleanup)
				case <-time.After(time.Second):
					t.Fatal("missing signed returned failure")
				}
			} else {
				require.Empty(t, reported, "unknown provider outcome must not be signed as stopped")
			}
		})
	}
}
