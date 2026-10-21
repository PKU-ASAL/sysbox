package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/revisiongate"
	"github.com/stretchr/testify/require"
)

func TestRemoteRegistryProtectsSeparateConsumerRoot(t *testing.T) {
	ctx := context.Background()
	token := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(token, []byte("remote-test-secret"), 0600))
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.RevisionConsumers = map[string]string{"remote": token}
	registry := NewServerWithConfig(cfg)
	require.NoError(t, registry.initErr)
	httpRegistry := httptest.NewServer(registry)
	defer httpRegistry.Close()
	files := map[string][]byte{"field.sysbox.hcl": []byte("remote pinned")}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, registry.apiStore.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	store := registry.apiStore.(*localAPIStore)
	require.Error(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()), "uninitialized remote consumer blocks deletion")
	consumerCfg := config.DefaultServiceConfig()
	consumerCfg.Paths.RunsDir = t.TempDir()
	consumerCfg.Paths.WorkspacesDir = t.TempDir()
	consumerCfg.RevisionRemote = &config.RevisionRemoteConfig{URL: httpRegistry.URL + "/v1/revision-registry", Consumer: "remote", TokenFile: token}
	consumer := NewServerWithConfig(consumerCfg)
	require.NoError(t, consumer.initErr)
	require.NoError(t, consumer.apiStore.SaveRun(ctx, controlplane.Run{ID: "run", Topology: "lab", Revision: id, Status: controlplane.RunQueued, QueuedAt: time.Now()}))
	require.Error(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	// Metadata remains retained until its owning topology has been retired.
	// This fixture removes only its own evidence to model completed retirement.
	require.NoError(t, os.RemoveAll(filepath.Join(consumer.runsDir, "lab")))
	require.NoError(t, revisiongate.SyncRemote(ctx, consumer.runsDir))
	require.NoError(t, store.DeleteGlobalRevisionAtomic(ctx, id, time.Now()))
	require.Error(t, consumer.apiStore.SaveRun(ctx, controlplane.Run{ID: "late", Topology: "lab", Revision: id, Status: controlplane.RunQueued, QueuedAt: time.Now()}))
}
