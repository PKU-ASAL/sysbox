package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/stretchr/testify/require"
)

func TestRunCheckpointColdRecoveryWaitsForOriginalCallback(t *testing.T) {
	c, req, grant := runFixture(t)
	_, c.HostKey, _ = ed25519.GenerateKey(rand.Reader)
	c.ReportRunID = func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
		return nil
	}
	ready, release := make(chan error, 1), make(chan struct{})
	_, err := c.StartAsync(context.Background(), context.Background(), req, grant, func(ctx context.Context) (json.RawMessage, error) {
		run := &controlplane.Run{ID: req.RunID, AgentID: c.AgentID, Op: req.Action, Status: controlplane.RunDone,
			Inputs: map[string]string{"password": "must-not-be-persisted"}, Cleanup: &controlplane.CleanupReceipt{}}
		ready <- CheckpointRunChildren(ctx, &RunChildren{}, run)
		<-release
		return nil, errors.New("controller lost terminal result")
	})
	require.NoError(t, err)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	require.NoError(t, <-ready)
	gate, err := c.gate(grant)
	require.NoError(t, err)
	checkpoint, err := gate.ReadRun(context.Background(), grant, req.ActionID)
	require.NoError(t, err)
	require.NotContains(t, string(checkpoint.Checkpoint), "must-not-be-persisted")
	require.NotContains(t, string(checkpoint.Checkpoint), `"cleanup"`)
	cold := &RunCoordinator{Root: c.Root, IssuerKey: c.IssuerKey, AgentID: c.AgentID, HostBootID: c.HostBootID, HostKey: c.HostKey, ReportRunID: c.ReportRunID}
	require.NoError(t, cold.Recover(context.Background()))
	observed, err := cold.ObserveAsync(context.Background(), req, grant)
	require.NoError(t, err)
	require.Equal(t, "uncertain", observed.Status, "live old callback cannot be declared stopped")
	active := c.findActive(grant)
	close(release)
	<-active.done
	require.NoError(t, cold.Recover(context.Background()))
	observed, err = cold.ObserveAsync(context.Background(), req, grant)
	require.NoError(t, err)
	require.Equal(t, "completed", observed.Status)
	var result controlplane.Run
	require.NoError(t, json.Unmarshal(observed.Evidence, &result))
	require.Equal(t, controlplane.RunFailed, result.Status)
	require.Contains(t, result.Err, "controller stopped")
	require.False(t, result.EndedAt.IsZero())
	require.NoError(t, cold.Recover(context.Background()))
}

func TestRunCheckpointRejectsUnsealedAdmission(t *testing.T) {
	children := &RunChildren{starting: 1}
	_, err := children.checkpointChildren()
	require.Error(t, err)
	children = &RunChildren{unknownAdmission: true}
	_, err = children.checkpointChildren()
	require.Error(t, err)
}

func TestRunCheckpointRequiresTrustedContext(t *testing.T) {
	err := CheckpointRunChildren(context.Background(), &RunChildren{}, &controlplane.Run{ID: "run", Status: controlplane.RunDone, EndedAt: time.Now()})
	require.Error(t, err)
}

func TestRunCheckpointProcessCrashRecovery(t *testing.T) {
	type fixture struct {
		Root    string
		Public  ed25519.PublicKey
		Request RunRequest
		Grant   controlplane.SignedExecutionGrant
	}
	if encoded := os.Getenv("SYSBOX_CHECKPOINT_CRASH_FIXTURE"); encoded != "" {
		var f fixture
		if json.Unmarshal([]byte(encoded), &f) != nil {
			os.Exit(2)
		}
		c := &RunCoordinator{Root: f.Root, IssuerKey: f.Public, AgentID: f.Grant.Grant.AgentID, HostBootID: f.Grant.BootID,
			Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }}
		_, err := c.StartAsync(context.Background(), context.Background(), f.Request, f.Grant, func(ctx context.Context) (json.RawMessage, error) {
			run := &controlplane.Run{ID: f.Request.RunID, AgentID: c.AgentID, Op: f.Request.Action, Status: controlplane.RunDone}
			if err := CheckpointRunChildren(ctx, &RunChildren{}, run); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(3)
			}
			select {} // Parent kills the process after reading the durable checkpoint.
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(4)
		}
		select {}
	}
	c, req, grant := runFixture(t)
	_, c.HostKey, _ = ed25519.GenerateKey(rand.Reader)
	c.ReportRunID = func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
		return nil
	}
	raw, err := json.Marshal(fixture{c.Root, c.IssuerKey, req, grant})
	require.NoError(t, err)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunCheckpointProcessCrashRecovery$")
	cmd.Env = append(os.Environ(), "SYSBOX_CHECKPOINT_CRASH_FIXTURE="+string(raw))
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	gate, err := c.gate(grant)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		out, err := gate.ReadRun(context.Background(), grant, req.ActionID)
		return err == nil && len(out.Checkpoint) != 0
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, c.Recover(context.Background()))
	out, err := gate.ReadRun(context.Background(), grant, req.ActionID)
	require.NoError(t, err)
	require.Equal(t, effectgate.RunIntent, out.State)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	require.NoError(t, c.Recover(context.Background()))
	out, err = gate.ReadRun(context.Background(), grant, req.ActionID)
	require.NoError(t, err)
	require.Equal(t, effectgate.RunComplete, out.State)
}

func TestRunCheckpointRecoveryRequiresEveryChildStop(t *testing.T) {
	for _, scenario := range []string{"confirmed", "unknown", "wrong_run", "unreported", "slow_reports"} {
		t.Run(scenario, func(t *testing.T) {
			d, task, base, issuer := taskFixtureWithIssuer(t)
			conn := attachRunChild(d, base, scenario != "unknown")
			d.RunID = "run"
			d.ResolveRun = func(ctx context.Context, ref TargetRef, run string) (Target, error) { return d.Resolve(ctx, ref) }
			task.Grant.Grant.ParentEpoch = 1
			task.Grant.Grant.Epoch = 2
			var err error
			task.Grant, err = controlplane.SignExecutionGrant(issuer, task.Grant.Grant, task.Grant.BootID, time.Now())
			require.NoError(t, err)
			base.handle.Epoch = 2
			record, err := d.Start(context.Background(), testRef(task), task)
			require.NoError(t, err)
			inventory := map[string]TaskRecord{recordKey(record.Handle): record}
			if scenario == "slow_reports" || scenario == "unreported" {
				next := task
				next.ID = "second"
				next.Grant.Grant.ActionID, next.Grant.Grant.Epoch = next.ID, 3
				next.Grant.Grant.Digest, err = next.Digest()
				require.NoError(t, err)
				next.Grant, err = controlplane.SignExecutionGrant(issuer, next.Grant.Grant, next.Grant.BootID, time.Now())
				require.NoError(t, err)
				base.handle.ID, base.handle.Epoch, base.handle.Digest = next.ID, 3, next.Grant.Grant.Digest
				second, err := d.Start(context.Background(), testRef(next), next)
				require.NoError(t, err)
				inventory[recordKey(second.Handle)] = second
				d.ReportStop = func(ctx context.Context, _ controlplane.SignedExecutionStopReceipt) error {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(1100 * time.Millisecond):
						return nil
					}
				}
			}
			req := RunRequest{RunID: "run", Topology: "lab", TopologyUID: "topology", Revision: "revision", ActionID: "run-action", Action: "apply"}
			parent := task.Grant.Grant
			parent.Scope = controlplane.ExecutionScope{TopologyUID: "topology"}
			parent.Epoch, parent.ParentEpoch = 1, 0
			parent.ActionID, parent.Operation, parent.TargetBootID = req.ActionID, "run_apply", d.HostBootID
			parent.Digest, err = req.Digest()
			require.NoError(t, err)
			grant, err := controlplane.SignExecutionGrant(issuer, parent, d.HostBootID, time.Now())
			require.NoError(t, err)
			c := &RunCoordinator{Root: t.TempDir(), IssuerKey: d.IssuerKey, HostKey: d.HostKey, HostBootID: d.HostBootID, AgentID: d.AgentID, Tasks: d,
				Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil },
				ReportRunID: func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
					return nil
				}}
			require.NoError(t, os.Chmod(c.Root, 0700))
			ready := make(chan error, 1)
			_, err = c.StartAsync(context.Background(), context.Background(), req, grant, func(ctx context.Context) (json.RawMessage, error) {
				children := &RunChildren{Dispatcher: d, children: inventory}
				ready <- CheckpointRunChildren(ctx, children, &controlplane.Run{ID: "run", AgentID: d.AgentID, Op: "apply", Status: controlplane.RunDone})
				return nil, errors.New("terminal result lost")
			})
			require.NoError(t, err)
			require.NoError(t, <-ready)
			require.Eventually(t, func() bool { return c.findActive(grant) == nil }, time.Second, time.Millisecond)
			if scenario == "wrong_run" {
				record.RunID = "different-run"
				require.NoError(t, d.save(record))
			}
			if scenario == "unreported" {
				d.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error { return errors.New("API offline") }
			}
			cold := &RunCoordinator{Root: c.Root, IssuerKey: c.IssuerKey, HostKey: c.HostKey, HostBootID: c.HostBootID, AgentID: c.AgentID, Tasks: d, ReportRunID: c.ReportRunID}
			err = cold.Recover(context.Background())
			if scenario == "confirmed" || scenario == "slow_reports" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			if scenario == "wrong_run" {
				require.Zero(t, conn.cancelCount.Load())
			}
			if scenario == "unreported" {
				require.EqualValues(t, 2, conn.cancelCount.Load(), "one unavailable report must not prevent cancellation of later children")
			}
			gate, err := cold.gate(grant)
			require.NoError(t, err)
			out, err := gate.ReadRun(context.Background(), grant, req.ActionID)
			require.NoError(t, err)
			if scenario == "confirmed" || scenario == "slow_reports" {
				require.Equal(t, effectgate.RunComplete, out.State)
			} else {
				require.Equal(t, effectgate.RunIntent, out.State)
			}
		})
	}
}
