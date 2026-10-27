package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestRunRecoveryRejectsUnsignedOrCorruptCompleteEnvelope(t *testing.T) {
	for _, damage := range []string{"unsigned", "corrupt"} {
		t.Run(damage, func(t *testing.T) {
			c, req, proof := runFixture(t)
			ctx := context.Background()
			gate, err := c.gate(proof)
			require.NoError(t, err)
			_, err = gate.ReserveRun(ctx, proof, req.ActionID)
			require.NoError(t, err)
			require.NoError(t, gate.CommitRun(ctx, proof, req.ActionID, json.RawMessage(`{"id":"run","status":"done"}`)))
			paths, err := filepath.Glob(filepath.Join(c.Root, "*.json"))
			require.NoError(t, err)
			require.Len(t, paths, 1)
			raw, err := os.ReadFile(paths[0])
			require.NoError(t, err)
			var record map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &record))
			var outcomes map[string]map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(record["outcomes"], &outcomes))
			for _, outcome := range outcomes {
				if damage == "unsigned" {
					delete(outcome, "signed_grant")
				} else {
					forged := proof
					forged.Signature = append([]byte(nil), proof.Signature...)
					forged.Signature[0] ^= 1
					outcome["signed_grant"], err = json.Marshal(forged)
					require.NoError(t, err)
				}
			}
			record["outcomes"], err = json.Marshal(outcomes)
			require.NoError(t, err)
			raw, err = json.Marshal(record)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(paths[0], raw, 0600))
			_, c.HostKey, err = ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			c.ReportRunID = func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
				t.Error("unverified Run completion reported")
				return nil
			}
			require.Error(t, c.Recover(ctx))
		})
	}
}
