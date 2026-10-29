package api

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestExecutionBridgeMaterializesOnlyCanonicalRevision(t *testing.T) {
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	server := NewServerWithConfig(cfg)
	bridge, err := NewExecutionBridge(cfg)
	requireNoError(t, err)
	t.Cleanup(func() { _ = bridge.Close() })
	files := map[string][]byte{"field.sysbox.hcl": []byte("# immutable host revision\n")}
	revision := controlplane.GlobalRevision{Revision: controlplane.ComputeRevisionDigest(files), Files: files}
	requireNoError(t, server.apiStore.SaveGlobalRevision(context.Background(), revision))
	path, err := bridge.MaterializeRunRevision(context.Background(), "lab", revision.Revision)
	if err != nil {
		t.Fatalf("MaterializeRunRevision() error = %v", err)
	}
	if path == "" {
		t.Fatal("MaterializeRunRevision() returned an empty path")
	}
	if _, err := bridge.MaterializeRunRevision(context.Background(), "lab", "sha256:missing"); err == nil {
		t.Fatal("MaterializeRunRevision() accepted an unknown revision")
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
