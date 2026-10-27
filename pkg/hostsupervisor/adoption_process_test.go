package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/stretchr/testify/require"
)

type adoptionCrashInput struct {
	Root, Phase string
	Request     AdoptionRequest
	Grant       controlplane.SignedExecutionGrant
	Issuer      ed25519.PublicKey
	Host        ed25519.PrivateKey
}

func TestAdoptionIndependentProcessCrashBoundaries(t *testing.T) {
	for _, phase := range []string{"before-effect", "after-effect", "after-return-proof", "before-report"} {
		t.Run(phase, func(t *testing.T) {
			c, req, g := adoptionFixture(t)
			// The generic coordinator boundary is the same for every provider. This
			// test's external marker is not substituted for real provider acceptance.
			input := adoptionCrashInput{Root: c.Root, Phase: phase, Request: req, Grant: g, Issuer: c.IssuerKey, Host: c.HostKey}
			raw, err := json.Marshal(input)
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "input.json")
			require.NoError(t, os.WriteFile(path, raw, 0600))
			child := exec.Command(os.Args[0], "-test.run=^TestAdoptionProcessCrashChild$")
			child.Env = append(os.Environ(), "SYSBOX_ADOPTION_CRASH="+path)
			out, err := child.CombinedOutput()
			var exited *exec.ExitError
			require.ErrorAs(t, err, &exited, string(out))
			require.Equal(t, 75, exited.ExitCode(), string(out))
			reports := 0
			c.Report = func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
				reports++
				return nil
			}
			require.NoError(t, c.Recover(context.Background()))
			result, err := c.Observe(context.Background(), req, g)
			if phase == "before-report" || phase == "after-return-proof" {
				require.NoError(t, err)
				require.JSONEq(t, `{"external_id":"external"}`, string(result))
				require.Greater(t, reports, 0)
			} else {
				require.ErrorIs(t, err, effectgate.ErrUncertain)
				require.Zero(t, reports)
			}
			_, err = c.Apply(context.Background(), req, g, func(context.Context) (json.RawMessage, error) {
				t.Error("provider replay after process exit")
				return nil, nil
			})
			require.Error(t, err)
			if phase == "before-effect" {
				require.NoFileExists(t, filepath.Join(c.Root, "provider-effect"))
			} else {
				require.FileExists(t, filepath.Join(c.Root, "provider-effect"))
			}
		})
	}
}
func TestAdoptionProcessCrashChild(t *testing.T) {
	path := os.Getenv("SYSBOX_ADOPTION_CRASH")
	if path == "" {
		t.Skip("child only")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var input adoptionCrashInput
	require.NoError(t, json.Unmarshal(raw, &input))
	c := AdoptionCoordinator{Root: input.Root, IssuerKey: input.Issuer, HostKey: input.Host, AgentID: input.Request.AgentID, HostBootID: input.Grant.BootID, Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }, Report: func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
		os.Exit(75)
		return nil
	}}
	if input.Phase == "after-return-proof" {
		ledger := filepath.Join(input.Root, digestHex([]byte(input.Request.TopologyUID))+".json")
		gate := effectgate.Gate{Root: input.Root, PublicKey: input.Issuer, BootID: input.Grant.BootID, Authorize: func(context.Context, controlplane.ExecutionGrant) error { return nil }}
		_, err := gate.ExecuteWithReturnProof(context.Background(), input.Grant, input.Request.ExternalID, func(context.Context) (json.RawMessage, error) {
			require.NoError(t, os.WriteFile(filepath.Join(input.Root, "provider-effect"), []byte("owned"), 0600))
			require.NoError(t, os.Rename(ledger, ledger+".intent"))
			require.NoError(t, os.Mkdir(ledger, 0700))
			return json.RawMessage(`{"external_id":"external"}`), nil
		})
		require.Error(t, err)
		require.NoError(t, os.Remove(ledger))
		require.NoError(t, os.Rename(ledger+".intent", ledger))
		os.Exit(75)
	}
	_, err = c.Apply(context.Background(), input.Request, input.Grant, func(context.Context) (json.RawMessage, error) {
		if input.Phase == "before-effect" {
			os.Exit(75)
		}
		f, err := os.Create(filepath.Join(input.Root, "provider-effect"))
		require.NoError(t, err)
		require.NoError(t, f.Sync())
		require.NoError(t, f.Close())
		if input.Phase == "after-effect" {
			os.Exit(75)
		}
		return json.RawMessage(`{"external_id":"external"}`), nil
	})
	require.NoError(t, err)
	t.Fatal("child did not exit")
}
