//go:build e2e

package revisiongate

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteRegistryGuestProbe(t *testing.T) {
	input := os.Getenv("SYSBOX_REMOTE_REGISTRY_PROBE")
	if input == "" {
		t.Skip("disposable remote consumer probe only")
	}
	var cfg struct {
		Root, ID, Mode string
		Binding        RemoteBinding
	}
	raw, err := os.ReadFile(input)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &cfg))
	ctx := context.Background()
	switch cfg.Mode {
	case "enroll":
		require.NoError(t, BindRemote(ctx, cfg.Root, cfg.Binding))
	case "pin", "late":
		path := filepath.Join(cfg.Root, "reference.json")
		raw, _ := json.Marshal(map[string]string{"revision": cfg.ID})
		release, err := Guard(ctx, path, raw)
		if cfg.Mode == "late" {
			require.Error(t, err)
			return
		}
		require.NoError(t, err)
		defer release()
		require.NoError(t, os.WriteFile(path, raw, 0600))
	case "release":
		require.NoError(t, os.Remove(filepath.Join(cfg.Root, "reference.json")))
		require.NoError(t, SyncRemote(ctx, cfg.Root))
	default:
		t.Fatal("invalid remote probe mode")
	}
}
