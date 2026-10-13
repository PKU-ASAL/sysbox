package hostsupervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"

	"github.com/stretchr/testify/require"
)

func TestRunTaskQuiescenceRejectsMissingAndCorruptInventory(t *testing.T) {
	d, _, _, _ := taskFixtureWithIssuer(t)
	require.NoError(t, d.ProveRunStopped(context.Background(), "run"))
	require.NoError(t, os.WriteFile(filepath.Join(d.Root, "unknown.json"), []byte(`{}`), 0600))
	require.Error(t, d.ProveRunStopped(context.Background(), "run"))
	d.Root = filepath.Join(d.Root, "missing")
	require.Error(t, d.ProveRunStopped(context.Background(), "run"))
}

func TestRunTaskQuiescenceRequiresVerifiedAcknowledgedStop(t *testing.T) {
	d, req, conn, issuer := taskFixtureWithIssuer(t)
	d.RunID = "run"
	d.ResolveRun = func(ctx context.Context, ref TargetRef, runID string) (Target, error) { return d.Resolve(ctx, ref) }
	g := req.Grant.Grant
	g.ParentEpoch = 99
	var err error
	req.Grant, err = controlplane.SignExecutionGrant(issuer, g, req.Grant.BootID, time.Now())
	require.NoError(t, err)
	ref := TargetRef{Topology: "lab", Node: "web", Scope: g.Scope}
	record, err := d.Start(context.Background(), ref, req)
	require.NoError(t, err)
	require.Error(t, d.ProveRunStopped(context.Background(), "run"))
	conn.result = substrate.ExecResult{Status: substrate.ExecStatusExited, ExitCodeKnown: true, RemoteTermination: substrate.RemoteTerminationConfirmed}
	_, err = d.Observe(context.Background(), ref, record.Handle)
	require.NoError(t, err)
	require.NoError(t, d.ProveRunStopped(context.Background(), "run"))
	current, err := d.load(record.Handle)
	require.NoError(t, err)
	current.Reported = false
	require.NoError(t, d.save(current))
	require.Error(t, d.ProveRunStopped(context.Background(), "run"))
}
