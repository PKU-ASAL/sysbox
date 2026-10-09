package hostsupervisor

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

type inspectingFileConnection struct {
	*fileConnection
	inspection substrate.InstalledFileInspection
	calls      int
	seen       substrate.FileInstallHandle
}

func (c *inspectingFileConnection) InspectInstalledFile(_ context.Context, h substrate.FileInstallHandle) (substrate.InstalledFileInspection, error) {
	c.calls++
	c.seen = h
	return c.inspection, nil
}

func TestFileInspectionUsesCanonicalTargetWithoutChangingReceipt(t *testing.T) {
	d, ref, req, grant, base := fileFixture(t)
	ctx := context.Background()
	h, _, err := d.Install(ctx, ref, req, grant)
	require.NoError(t, err)
	path := d.path(fileExecutionHandle(h))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	conn := &inspectingFileConnection{fileConnection: base, inspection: substrate.InstalledFileInspection{Status: "absent"}}
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "node", Connection: conn}, nil
	}
	d.Authorize = nil
	d.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error {
		t.Fatal("inspection must not report historical stop")
		return nil
	}
	got, err := d.Inspect(ctx, ref, h)
	require.NoError(t, err)
	require.Equal(t, "absent", got.Status)
	require.Equal(t, "node", conn.seen.NodeID)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	wrong := ref
	wrong.Node = "other"
	_, err = d.Inspect(ctx, wrong, h)
	require.Error(t, err)
	require.Equal(t, 1, conn.calls)
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "replacement", Connection: conn}, nil
	}
	_, err = d.Inspect(ctx, ref, h)
	require.Error(t, err)
	require.Equal(t, 1, conn.calls)
	resolves := 0
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		resolves++
		id := "node"
		if resolves > 1 {
			id = "replacement"
		}
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: id, Connection: conn}, nil
	}
	_, err = d.Inspect(ctx, ref, h)
	require.Error(t, err, "node replacement during observation must reject the report")
	require.Equal(t, 2, conn.calls)
}

func TestFileInspectionDispatchReturnsBoundCurrentResult(t *testing.T) {
	d, ref, req, grant, base := fileFixture(t)
	ctx := context.Background()
	h, _, err := d.Install(ctx, ref, req, grant)
	require.NoError(t, err)
	conn := &inspectingFileConnection{fileConnection: base, inspection: substrate.InstalledFileInspection{Status: "present", SHA256: req.SHA256, Size: req.Size, Mode: req.Mode}}
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "node", Connection: conn}, nil
	}
	s := &Server{Files: d}
	request := Request{Version: 1, Operation: "file-inspect", Target: ref, FileHandle: h}
	response := s.Handle(ctx, request)
	require.Empty(t, response.Error)
	require.Equal(t, h, *response.InspectedFile)
	require.Equal(t, "present", response.FileInspection.Status)
	conn.inspection.SHA256 = "wrong"
	response = s.Handle(ctx, request)
	require.NotEmpty(t, response.Error)
	require.Nil(t, response.FileInspection)
}
