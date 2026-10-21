package api

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

type restoreCrashInput struct {
	Kind, Path, Stage string
	Plan              persistence.SnapshotRestoreRequest
}

func TestSnapshotRestoreIndependentProcessCrashMatrix(t *testing.T) {
	for _, stage := range []string{"barrier", "committed", "released"} {
		t.Run(stage, func(t *testing.T) {
			eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
				ctx := context.Background()
				s := stateDeleteTestServer(t, store)
				repo, err := s.actionRepository("lab")
				require.NoError(t, err)
				manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
				seeded, err := manager.Load()
				require.NoError(t, err)
				seeded.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "retained"), Driver: "docker", ResourceType: "sysbox_node", OwnerUID: seeded.TopologyUID, ResourceUID: "retained", Generation: 1, ExternalID: "exact", Status: state.ResourcePresent}}
				require.NoError(t, manager.Save(seeded))
				snapshot, err := manager.Snapshot(ctx, "before")
				require.NoError(t, err)
				stale, err := manager.Load()
				require.NoError(t, err)
				stale.Resources[0].Status = state.ResourceDegraded
				require.NoError(t, manager.Save(stale))
				stale, err = manager.Load()
				require.NoError(t, err)
				plan, err := repo.PlanSnapshotRestore(ctx, "lab", snapshot.ID, "restore-crash", 0)
				require.NoError(t, err)
				authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
				session, err := authority.OpenSession(ctx, "agent", "worker", "")
				require.NoError(t, err)
				grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{AgentID: "agent", SessionID: session.ID, ActionID: "old", CommandID: "old", Attempt: 1, Operation: "run_apply", Digest: "old", TargetBootID: "old-boot", Scope: controlplane.ExecutionScope{TopologyUID: stale.TopologyUID}}, time.Minute)
				require.NoError(t, err)
				require.NoError(t, authority.AcceptHostStop(ctx, *grant, "verified-original-stop"))
				input := restoreCrashInput{Stage: stage, Plan: plan}
				switch v := store.(type) {
				case *localAPIStore:
					input.Kind = "file"
					input.Path = v.runsDir
				case *sqliteAPIStore:
					input.Kind = "sqlite"
					input.Path = v.dbPath
				case *postgresAPIStore:
					input.Kind = "postgres"
					input.Path = v.dsn
				}
				path := filepath.Join(t.TempDir(), "input.json")
				raw, err := json.Marshal(input)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, raw, 0600))
				child := exec.Command(os.Args[0], "-test.run=^TestSnapshotRestoreCrashChild$")
				child.Env = append(os.Environ(), "SYSBOX_RESTORE_CRASH_CHILD="+path)
				output, err := child.CombinedOutput()
				var exited *exec.ExitError
				require.ErrorAs(t, err, &exited, string(output))
				require.Equal(t, 74, exited.ExitCode(), string(output))
				require.Error(t, authority.Authorize(ctx, *grant), "already stopped old grant must never be resurrected")
				cold := stateDeleteTestServer(t, store)
				result, err := cold.restoreSnapshotWorkflow(ctx, "lab", plan)
				require.NoError(t, err)
				require.Equal(t, "restored", result.Status)
				require.NotNil(t, result.Receipt)
				require.Error(t, manager.Save(stale), "stale state writer cannot rewind restored serial")
				current, err := manager.Load()
				require.NoError(t, err)
				require.Len(t, current.Resources, 1)
				require.Equal(t, state.ResourceUnknown, current.Resources[0].Status, "restore must invalidate old observation status")
				current.Lineage = "successor"
				require.NoError(t, manager.Save(current))
				before, err := manager.Load()
				require.NoError(t, err)
				again, err := cold.restoreSnapshotWorkflow(ctx, "lab", plan)
				require.NoError(t, err)
				require.Equal(t, result.Receipt, again.Receipt)
				after, err := manager.Load()
				require.NoError(t, err)
				require.Equal(t, before, after)
			})
		})
	}
}
func TestSnapshotRestoreCrashChild(t *testing.T) {
	path := os.Getenv("SYSBOX_RESTORE_CRASH_CHILD")
	if path == "" {
		t.Skip("independent child only")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var input restoreCrashInput
	require.NoError(t, json.Unmarshal(raw, &input))
	var store apiStore
	switch input.Kind {
	case "file":
		store = &localAPIStore{runsDir: input.Path}
	case "sqlite":
		store = &sqliteAPIStore{dbPath: input.Path, runsDir: t.TempDir()}
	case "postgres":
		store = &postgresAPIStore{dsn: input.Path}
	default:
		t.Fatal("unknown backend")
	}
	s := stateDeleteTestServer(t, store)
	ctx := context.Background()
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
	digest, err := input.Plan.Digest()
	require.NoError(t, err)
	barrier, err := authority.BeginMaintenance(ctx, input.Plan.ID, input.Plan.TopologyUID, digest)
	require.NoError(t, err)
	if input.Stage == "barrier" {
		os.Exit(74)
	}
	input.Plan.MaintenanceEpoch = barrier.Epoch
	_, err = s.commitSnapshotRestore(ctx, "lab", input.Plan)
	require.NoError(t, err)
	if input.Stage == "committed" {
		os.Exit(74)
	}
	require.NoError(t, authority.EndMaintenance(ctx, *barrier))
	os.Exit(74)
}
