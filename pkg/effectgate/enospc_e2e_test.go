//go:build e2e

package effectgate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func fullLedgerFilesystem(t *testing.T, root string) func() {
	t.Helper()
	path := filepath.Join(root, "space-filler")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	block := make([]byte, 64<<10)
	for {
		_, err = file.Write(block)
		if err != nil {
			break
		}
	}
	require.True(t, errors.Is(err, syscall.ENOSPC), "must inject actual kernel ENOSPC: %v", err)
	require.NoError(t, file.Close())
	return func() { t.Helper(); require.NoError(t, os.Remove(path)) }
}

func enospcGate(t *testing.T) (*Gate, controlplane.SignedExecutionGrant, func(controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error)) {
	t.Helper()
	if os.Getenv("SYSBOX_ENOSPC_E2E") != "1" {
		t.Skip("requires disposable root mount namespace")
	}
	gate, grant, sign := fixedGateFixture(t)
	require.NoError(t, syscall.Mount("tmpfs", gate.Root, "tmpfs", 0, "size=1048576,mode=0700"))
	t.Cleanup(func() { require.NoError(t, syscall.Unmount(gate.Root, 0)) })
	return gate, grant, sign
}

func TestENOSPCAfterEffectCannotReplayOrInventCompletionE2E(t *testing.T) {
	gate, grant, sign := enospcGate(t)
	marker := filepath.Join(t.TempDir(), "effect")
	var free func()
	_, err := gate.Execute(context.Background(), grant, "effect", func(context.Context) (json.RawMessage, error) {
		require.NoError(t, os.WriteFile(marker, []byte("performed once"), 0600))
		free = fullLedgerFilesystem(t, gate.Root)
		return json.RawMessage("{}"), nil
	})
	require.Error(t, err)
	require.FileExists(t, marker)
	require.NotNil(t, free)
	free()
	cold := *gate
	_, err = cold.ReadOutcome(context.Background(), grant.Grant, "effect")
	require.ErrorIs(t, err, ErrUncertain)
	_, err = cold.SealStop(context.Background(), grant.Grant, "effect", sign)
	require.ErrorIs(t, err, ErrUncertain)
	_, err = cold.Execute(context.Background(), grant, "effect", func(context.Context) (json.RawMessage, error) {
		t.Fatal("ENOSPC outcome must not replay original effect")
		return nil, nil
	})
	require.ErrorIs(t, err, ErrUncertain)
}

func TestENOSPCStopReceiptAndReportRecoveryE2E(t *testing.T) {
	for _, phase := range []string{"stop-receipt", "report-ack"} {
		t.Run(phase, func(t *testing.T) {
			gate, grant, sign := enospcGate(t)
			ctx := context.Background()
			effects := 0
			_, err := gate.Execute(ctx, grant, "effect", func(context.Context) (json.RawMessage, error) { effects++; return json.RawMessage("{}"), nil })
			require.NoError(t, err)
			if phase == "stop-receipt" {
				free := fullLedgerFilesystem(t, gate.Root)
				receipt, err := gate.SealStop(ctx, grant.Grant, "effect", sign)
				require.Error(t, err)
				require.Empty(t, receipt.Signature)
				reports := 0
				require.Error(t, gate.ReportStop(ctx, grant.Grant, "effect", func(context.Context, controlplane.SignedExecutionStopReceipt) error { reports++; return nil }))
				require.Zero(t, reports, "unpersisted receipt cannot release authority")
				free()
			}
			cold := *gate
			receipt, err := cold.SealStop(ctx, grant.Grant, "effect", sign)
			require.NoError(t, err)
			var free func()
			reports := 0
			err = cold.ReportStop(ctx, grant.Grant, "effect", func(_ context.Context, got controlplane.SignedExecutionStopReceipt) error {
				reports++
				require.Equal(t, receipt, got)
				if phase == "report-ack" {
					free = fullLedgerFilesystem(t, gate.Root)
				}
				return nil
			})
			if phase == "report-ack" {
				require.Error(t, err)
				require.NotNil(t, free)
				free()
				restarted := cold
				require.NoError(t, restarted.ReportStop(ctx, grant.Grant, "effect", func(_ context.Context, got controlplane.SignedExecutionStopReceipt) error {
					reports++
					require.Equal(t, receipt, got)
					return nil
				}))
				require.Equal(t, 2, reports)
			} else {
				require.NoError(t, err)
				require.Equal(t, 1, reports)
			}
			require.NoError(t, cold.RecoverStops(ctx, sign, func(context.Context, controlplane.SignedExecutionStopReceipt) error {
				t.Fatal("already reported")
				return nil
			}))
			require.Equal(t, 1, effects)
		})
	}
}
