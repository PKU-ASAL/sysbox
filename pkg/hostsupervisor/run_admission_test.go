package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/stretchr/testify/require"
)

func TestRunEntryRequiresDurableMarkerAndCurrentAuthority(t *testing.T) {
	for _, deny := range []bool{false, true} {
		t.Run(map[bool]string{false: "allowed", true: "revoked-before-entry"}[deny], func(t *testing.T) {
			c, req, grant := runFixture(t)
			c.PrepareRun = func(context.Context, RunRequest) (controlplane.Run, error) {
				return controlplane.Run{ID: req.RunID, Topology: req.Topology, Revision: req.Revision, Target: req.Target, Op: req.Action, AgentID: c.AgentID, Status: controlplane.RunRunning, QueuedAt: time.Now().UTC(), Attempt: 1, LeaseOwner: "original"}, nil
			}
			var checks, calls atomic.Int32
			c.Authorize = func(context.Context, controlplane.SignedExecutionGrant) error {
				if checks.Add(1) >= 2 && deny {
					return effectgate.ErrFenced
				}
				return nil
			}
			gate, err := c.gate(grant)
			require.NoError(t, err)
			_, err = c.StartAsync(context.Background(), context.Background(), req, grant, func(ctx context.Context) (json.RawMessage, error) {
				stored, err := gate.ReadRun(ctx, grant, req.ActionID)
				require.NoError(t, err)
				require.True(t, stored.Started)
				require.NotEmpty(t, stored.Admission)
				calls.Add(1)
				return nil, effectgate.ErrUncertain
			})
			if deny {
				require.Error(t, err)
				require.Zero(t, calls.Load())
				stored, err := gate.ReadRun(context.Background(), grant, req.ActionID)
				require.NoError(t, err)
				require.False(t, stored.Started)
			} else {
				require.NoError(t, err)
				require.Eventually(t, func() bool { return c.findActive(grant) == nil }, time.Second, time.Millisecond)
				require.EqualValues(t, 1, calls.Load())
			}
		})
	}
}

func TestRunAdmissionProcessCrash(t *testing.T) {
	if root := os.Getenv("SYSBOX_ADMISSION_CRASH_TEST_ROOT"); root != "" {
		raw, err := os.ReadFile(filepath.Join(root, "fixture.json"))
		require.NoError(t, err)
		var fixture struct {
			Request RunRequest
			Grant   controlplane.SignedExecutionGrant
			Key     []byte
			Run     controlplane.Run
		}
		require.NoError(t, json.Unmarshal(raw, &fixture))
		c := &RunCoordinator{Root: filepath.Join(root, "journal"), IssuerKey: fixture.Key, AgentID: fixture.Grant.Grant.AgentID, HostBootID: fixture.Grant.BootID, Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }}
		c.PrepareRun = func(context.Context, RunRequest) (controlplane.Run, error) { return fixture.Run, nil }
		_, held, err := c.lockRunExecution(fixture.Grant)
		require.NoError(t, err)
		require.True(t, held)
		gate, err := c.gate(fixture.Grant)
		require.NoError(t, err)
		seed, err := c.prepareAdmission(context.Background(), fixture.Request, fixture.Grant)
		require.NoError(t, err)
		_, err = gate.ReserveRunWithAdmission(context.Background(), fixture.Grant, fixture.Request.ActionID, seed)
		require.NoError(t, err)
		// Exit without releasing the executor lock or running the callback.
		os.Exit(37)
	}
	c, req, grant := runFixture(t)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	run := controlplane.Run{ID: req.RunID, Topology: req.Topology, Revision: req.Revision, Target: req.Target, Op: req.Action, AgentID: c.AgentID, Status: controlplane.RunRunning, QueuedAt: time.Now().UTC(), Attempt: 1, LeaseOwner: "original"}
	raw, err := json.Marshal(struct {
		Request RunRequest
		Grant   controlplane.SignedExecutionGrant
		Key     []byte
		Run     controlplane.Run
	}{req, grant, c.IssuerKey, run})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "fixture.json"), raw, 0600))
	command := exec.Command(os.Args[0], "-test.run=^TestRunAdmissionProcessCrash$")
	command.Env = append(os.Environ(), "SYSBOX_ADMISSION_CRASH_TEST_ROOT="+root)
	out, err := command.CombinedOutput()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, string(out))
	require.Equal(t, 37, exit.ExitCode(), string(out))
	_, c.HostKey, err = ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	c.Root = filepath.Join(root, "journal")
	reported := false
	c.ReportRunID = func(_ context.Context, id string, receipt controlplane.SignedExecutionStopReceipt, evidence json.RawMessage) error {
		require.Equal(t, run.ID, id)
		require.NoError(t, controlplane.VerifyRunStopReceipt(c.HostKey.Public().(ed25519.PublicKey), receipt, evidence))
		var result controlplane.Run
		require.NoError(t, json.Unmarshal(evidence, &result))
		require.Equal(t, controlplane.RunFailed, result.Status)
		require.Nil(t, result.Cleanup)
		reported = true
		return nil
	}
	require.NoError(t, c.Recover(context.Background()))
	require.True(t, reported)
}

func TestColdRunAdmissionRecoveryRequiresExplicitNeverEnteredProof(t *testing.T) {
	for _, scenario := range []string{"never-entered", "entered", "historical", "executor-live", "substituted-run"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			c, req, grant := runFixture(t)
			_, c.HostKey, _ = ed25519.GenerateKey(rand.Reader)
			run := controlplane.Run{ID: req.RunID, Topology: req.Topology, Revision: req.Revision, Target: req.Target, Op: req.Action, Operation: req.Action, AgentID: c.AgentID, Status: controlplane.RunRunning, QueuedAt: time.Now().UTC(), Attempt: 1, LeaseOwner: "original", Inputs: map[string]string{"secret": "hidden"}}
			c.PrepareRun = func(context.Context, RunRequest) (controlplane.Run, error) { return run, nil }
			raw, err := c.prepareAdmission(ctx, req, grant)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "hidden")
			gate, err := c.gate(grant)
			require.NoError(t, err)
			if scenario == "historical" {
				_, err = gate.ReserveRun(ctx, grant, req.ActionID)
			} else {
				if scenario == "substituted-run" {
					var a runAdmissionEvidence
					require.NoError(t, json.Unmarshal(raw, &a))
					a.Run.ID = "foreign"
					raw, err = json.Marshal(a)
					require.NoError(t, err)
				}
				_, err = gate.ReserveRunWithAdmission(ctx, grant, req.ActionID, raw)
			}
			require.NoError(t, err)
			if scenario == "entered" {
				require.NoError(t, gate.MarkRunStarted(ctx, grant, req.ActionID))
			}
			if scenario == "executor-live" {
				unlock, held, err := c.lockRunExecution(grant)
				require.NoError(t, err)
				require.True(t, held)
				defer unlock()
			}
			reports := 0
			cold := &RunCoordinator{Root: c.Root, IssuerKey: c.IssuerKey, HostKey: c.HostKey, AgentID: c.AgentID, HostBootID: "new-boot"}
			cold.ReportRunID = func(_ context.Context, id string, receipt controlplane.SignedExecutionStopReceipt, evidence json.RawMessage) error {
				reports++
				require.Equal(t, req.RunID, id)
				require.NoError(t, controlplane.VerifyRunStopReceipt(c.HostKey.Public().(ed25519.PublicKey), receipt, evidence))
				var result controlplane.Run
				require.NoError(t, json.Unmarshal(evidence, &result))
				require.Equal(t, controlplane.RunFailed, result.Status)
				require.Equal(t, run.QueuedAt, result.QueuedAt)
				require.Equal(t, run.LeaseOwner, result.LeaseOwner)
				require.Nil(t, result.Cleanup)
				return nil
			}
			err = cold.Recover(ctx)
			if scenario == "substituted-run" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			got, err := gate.ReadRun(ctx, grant, req.ActionID)
			require.NoError(t, err)
			if scenario == "never-entered" {
				require.Equal(t, effectgate.RunComplete, got.State)
				require.Equal(t, 1, reports)
				require.NoError(t, cold.Recover(ctx))
				require.Equal(t, 1, reports)
			} else {
				require.Equal(t, effectgate.RunIntent, got.State)
				require.Zero(t, reports)
			}
		})
	}
}
