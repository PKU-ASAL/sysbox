package firecracker_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/provider/firecracker"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Exercise the public runtime action -> operation context -> Reset SPI contract.
// Both generations are prepared files only: no VM or networking is started.
func TestFirecrackerResetAcceptsRuntimeTargetContextAcrossRecovery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "baseline.ext4")
	raw := []byte("non-bootable test image")
	require.NoError(t, os.WriteFile(source, raw, 0600))
	baseline := substrate.ArtifactIdentity{Kind: substrate.ArtifactRootFS, Source: source, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux}
	node := substrate.NodeSpec{Name: "test-node", Image: substrate.ArtifactHandle{ID: source, Identity: baseline}, ProviderConfig: &firecracker.Config{Kernel: "/fixture/kernel"}}
	sub := firecracker.New("/fixture/kernel", root)
	oldIdentity := substrate.OperationIdentity{ActionID: "original-create", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 4}
	old, err := sub.CreateNode(substrate.WithOperationIdentity(ctx, oldIdentity), node)
	require.NoError(t, err)
	request := substrate.ResetRequest{Current: old, Node: node, Baseline: baseline}
	action := runtime.ActionRecord{ID: "durable-reset-action", Topology: "lab", RunID: "reset-run", Resource: address.Resource("sysbox_node", "test"), Action: model.PlanActionReset, OwnerUID: oldIdentity.OwnerUID, ResourceUID: oldIdentity.ResourceUID, Generation: old.Generation + 1}
	target := substrate.OperationIdentity{ActionID: action.ID, OwnerUID: action.OwnerUID, ResourceUID: action.ResourceUID, TopologyUID: oldIdentity.TopologyUID, Generation: action.Generation}
	targetCtx := substrate.WithOperationIdentity(ctx, target)
	var resetDriver driver.Reset = sub
	reset, err := resetDriver.PrepareReset(targetCtx, request)
	require.NoError(t, err)
	token, err := resetDriver.MarshalResetHandle(reset)
	require.NoError(t, err)
	var persisted struct {
		NewIdentity substrate.OperationIdentity `json:"new_identity"`
	}
	require.NoError(t, json.Unmarshal(token, &persisted))
	require.Equal(t, target, persisted.NewIdentity)
	// A fresh provider sees only the durable token and runtime's original request.
	cold := firecracker.New("/fixture/kernel", root)
	resetDriver = cold
	reset, err = resetDriver.UnmarshalResetHandle(token)
	require.NoError(t, err)
	reset.Request = request
	require.NoError(t, resetDriver.DestroyReset(targetCtx, reset))
	require.NoError(t, resetDriver.DestroyReset(targetCtx, reset))
	replacement, err := resetDriver.ApplyReset(targetCtx, reset)
	require.NoError(t, err)
	require.Equal(t, target.Generation, replacement.Generation)
	hs := replacement.Provider.(*firecracker.HandleState)
	manifest, err := os.ReadFile(hs.ManifestPath)
	require.NoError(t, err)
	var anchor struct {
		ActionID   string `json:"action_id"`
		Generation int64  `json:"generation"`
	}
	require.NoError(t, json.Unmarshal(manifest, &anchor))
	require.Equal(t, target.ActionID, anchor.ActionID)
	require.Equal(t, target.Generation, anchor.Generation)
	_, err = resetDriver.ObserveReset(targetCtx, reset)
	require.NoError(t, err)
	require.NoError(t, resetDriver.CleanupReset(targetCtx, reset))
	require.DirExists(t, hs.VMDir)
	// Neither the parent operation context nor the old runtime request is rewritten.
	retained, ok := substrate.OperationIdentityFromContext(targetCtx)
	require.True(t, ok)
	require.Equal(t, target, retained)
	require.Equal(t, int64(4), reset.Request.Current.Generation)
	require.NoError(t, cold.DestroyNode(targetCtx, replacement))
}
