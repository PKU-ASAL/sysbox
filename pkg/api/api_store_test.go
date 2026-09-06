package api

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	_ "modernc.org/sqlite"
)

func TestSQLiteAPIStoreRoundTripsResetTargetAndUnsafeState(t *testing.T) {
	store := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "api.db")}
	run := controlplane.Run{ID: "reset-1", Topology: "mixed", Operation: "reset", Op: "reset", Status: controlplane.RunQueued, Target: "sysbox_node.web", UnsafeState: true}
	require.NoError(t, store.SaveRun(context.Background(), run))

	got, err := store.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, run.Target, got.Target)
	require.True(t, got.UnsafeState)
}

func TestSQLiteAPIStoreRoundTripsDeadlineAt(t *testing.T) {
	store := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "api.db")}
	deadline := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	run := controlplane.Run{ID: "apply-1", Topology: "mixed", Operation: "apply", Op: "apply", Status: controlplane.RunQueued, DeadlineAt: deadline}
	require.NoError(t, store.SaveRun(context.Background(), run))

	got, err := store.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	require.True(t, got.DeadlineAt.Equal(deadline))

	runs, err := store.LoadRuns(context.Background())
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.True(t, runs[0].DeadlineAt.Equal(deadline))
}

func TestSQLiteAgentCommandRoundTripsGuestFilePut(t *testing.T) {
	store := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "api.db"), runsDir: t.TempDir()}
	want := controlplane.AgentCommand{ID: "cmd-file", AgentID: "host-a", Type: "guest_file_put", FilePut: &controlplane.GuestFilePut{ID: "file-1", Topology: "lab", Node: "web", Path: "/flag", Mode: 0, Size: 3, SHA256: "abc", FetchRef: "/private"}}
	require.NoError(t, store.SaveAgentCommand(context.Background(), want))
	got, err := store.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, want.FilePut, got[0].FilePut)
}

func TestAgentCommandStoresRejectStaleStatusRegression(t *testing.T) {
	stores := map[string]apiStore{
		"local":  &localAPIStore{runsDir: t.TempDir()},
		"sqlite": &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "api.db"), runsDir: t.TempDir()},
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			terminal := controlplane.AgentCommand{ID: "cmd-terminal", AgentID: "host-a", Type: "run_assigned", Status: controlplane.AgentCommandStatusCompleted, EndedAt: time.Now().UTC()}
			require.NoError(t, store.SaveAgentCommand(ctx, terminal))

			stale := terminal
			stale.Status = controlplane.AgentCommandStatusDelivered
			stale.EndedAt = time.Time{}
			require.NoError(t, store.SaveAgentCommand(ctx, stale))
			stale.Status = controlplane.AgentCommandStatusFailed
			require.NoError(t, store.SaveAgentCommand(ctx, stale))

			commands, err := store.ListAgentCommands(ctx, "host-a")
			require.NoError(t, err)
			require.Len(t, commands, 1)
			require.Equal(t, controlplane.AgentCommandStatusCompleted, commands[0].Status)
			require.False(t, commands[0].EndedAt.IsZero())
		})
	}
}

func TestGlobalRevisionStoreRoundTrip(t *testing.T) {
	stores := map[string]apiStore{
		"local":  &localAPIStore{runsDir: t.TempDir()},
		"sqlite": &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "api.db"), runsDir: t.TempDir()},
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			files := map[string][]byte{
				"field.sysbox.hcl":     []byte(`resource "sysbox_node" "web" {}`),
				"modules/web/main.hcl": []byte(`resource "sysbox_node" "web" {}`),
				"files/f.txt":          []byte("hello"),
			}
			size := 0
			for _, content := range files {
				size += len(content)
			}
			rev := controlplane.GlobalRevision{
				Revision:  "sha256:deadbeef",
				Files:     files,
				Size:      size,
				CreatedAt: time.Now().UTC(),
			}
			require.NoError(t, store.SaveGlobalRevision(ctx, rev))

			got, err := store.GetGlobalRevision(ctx, rev.Revision)
			require.NoError(t, err)
			require.Equal(t, rev.Revision, got.Revision)
			require.Equal(t, rev.Files, got.Files)
			require.Equal(t, rev.Size, got.Size)

			_, err = store.GetGlobalRevision(ctx, "sha256:unknown")
			require.ErrorIs(t, err, errGlobalRevisionNotFound)
		})
	}
}

// A database created before directory-tree revisions has a sysbox_global_revisions
// table with a single "hcl" column. Opening it must idempotently add the "files"
// column so new saves/loads round-trip the whole tree.
func TestSQLiteGlobalRevisionFilesColumnMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "api.db")
	legacy, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = legacy.Exec(`CREATE TABLE sysbox_global_revisions (
		revision   TEXT PRIMARY KEY,
		hcl        TEXT NOT NULL DEFAULT '',
		size       INTEGER DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT ''
	) STRICT;`)
	require.NoError(t, err)
	// A pre-migration row written under the old single-HCL model.
	_, err = legacy.Exec(`INSERT INTO sysbox_global_revisions (revision, hcl, size, created_at)
		VALUES ('sha256:legacy', 'resource "sysbox_node" "web" {}', 0, '')`)
	require.NoError(t, err)
	require.NoError(t, legacy.Close())

	store := &sqliteAPIStore{dbPath: dbPath, runsDir: t.TempDir()}

	// A legacy row carries no files JSON, so it must read as not-found rather
	// than a JSON unmarshal error (which would surface as a 500 on apply).
	_, err = store.GetGlobalRevision(context.Background(), "sha256:legacy")
	require.ErrorIs(t, err, errGlobalRevisionNotFound)

	files := map[string][]byte{
		"field.sysbox.hcl": []byte(`resource "sysbox_node" "web" {}`),
		"files/f.txt":      []byte("hello"),
	}
	rev := controlplane.GlobalRevision{
		Revision:  "sha256:deadbeef",
		Files:     files,
		Size:      len(files["field.sysbox.hcl"]) + len(files["files/f.txt"]),
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveGlobalRevision(context.Background(), rev))

	got, err := store.GetGlobalRevision(context.Background(), rev.Revision)
	require.NoError(t, err)
	require.Equal(t, files, got.Files)
}

func TestResourceProjectionStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(dir, dir)
	observed := time.Now().UTC()
	proj := controlplane.ResourceProjection{
		AgentID: "agent-1", Topology: "web", ObservedAt: observed,
		Resources: []controlplane.ResourceHealth{{Resource: "sysbox_node.web", Status: controlplane.ResourceHealthHealthy}},
	}
	require.NoError(t, s.apiStore.SaveResourceProjection(context.Background(), proj))
	got, err := s.apiStore.LoadResourceProjection(context.Background(), "web")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "agent-1", got.AgentID)
	require.Equal(t, "sysbox_node.web", got.Resources[0].Resource)
	require.Equal(t, controlplane.ResourceHealthHealthy, got.Resources[0].Status)
	require.True(t, got.ObservedAt.Equal(observed))

	missing, err := s.apiStore.LoadResourceProjection(context.Background(), "missing")
	require.NoError(t, err)
	require.Nil(t, missing)
}

func TestLocalAPIStorePersistsRunCheckpointAndHealth(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	ctx := context.Background()

	version, err := store.SchemaVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, apiSchemaVersion, version)

	run := controlplane.Run{ID: "run-1", Topology: "mixed", Op: "apply", Status: controlplane.RunRunning, StartedAt: time.Now().UTC()}
	require.NoError(t, store.SaveRun(ctx, run))
	runs, err := store.LoadRuns(ctx)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, "run-1", runs[0].ID)

	cp := runtime.OperationCheckpoint{RunID: "run-1", Topology: "mixed", Operation: "apply", Status: runtime.OperationStarted}
	require.NoError(t, store.SaveCheckpoint(ctx, "mixed", "run-1", cp))
	gotCP, err := store.LoadCheckpoint(ctx, "mixed", "run-1")
	require.NoError(t, err)
	require.Equal(t, "run-1", gotCP.RunID)

	snap := HealthSnapshot{Topology: "mixed", Observed: time.Now().UTC(), Policy: SupervisorPolicyObserveOnly}
	require.NoError(t, store.SaveHealth(ctx, "mixed", snap))
	gotHealth, err := store.LoadHealth(ctx, "mixed")
	require.NoError(t, err)
	require.Equal(t, "mixed", gotHealth.Topology)
}

func TestAPIMigrationsMatchSchemaVersion(t *testing.T) {
	require.NotEmpty(t, apiMigrations)
	seen := map[int]bool{}
	for i, migration := range apiMigrations {
		require.Equal(t, i+1, migration.Version)
		require.NotEmpty(t, migration.Name)
		require.NotEmpty(t, migration.SQL)
		require.False(t, seen[migration.Version])
		seen[migration.Version] = true
	}
	require.Equal(t, apiSchemaVersion, apiMigrations[len(apiMigrations)-1].Version)
}

func TestDSNWithoutSysboxQueryPreservesPostgresConnectionOptions(t *testing.T) {
	got := dsnWithoutSysboxQuery("postgres://user:pass@localhost/sysbox?sslmode=disable&search_path=isolated&topology=lab")
	require.Contains(t, got, "sslmode=disable")
	require.Contains(t, got, "search_path=isolated")
	require.NotContains(t, got, "topology=")
}

func TestSQLiteRunDispatchRollsBackRunWhenCommandInsertFails(t *testing.T) {
	store := &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "api.db")}
	ctx := context.Background()
	require.NoError(t, store.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: "run-conflict", AgentID: "host-a", Type: "existing", Status: controlplane.AgentCommandStatusQueued}))
	run := controlplane.Run{ID: "op-rollback", Topology: "lab", Operation: "destroy", Op: "destroy", Status: controlplane.RunAssigned, AgentID: "host-a"}
	_, _, err := store.CreateRunDispatch(ctx, RunDispatchRequest{
		Run:         run,
		Fingerprint: "fingerprint-rollback",
		Command:     controlplane.AgentCommand{ID: "run-conflict", AgentID: "host-a", Type: "run_assigned", Status: controlplane.AgentCommandStatusQueued},
	})
	require.Error(t, err)
	got, found, err := store.GetRunDispatch(ctx, run.ID, "fingerprint-rollback")
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, got)
	_, err = store.GetRun(ctx, run.ID)
	require.Error(t, err)
}

func TestLocalAPIStorePersistsAgentAndClaimLease(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	ctx := context.Background()

	agent := controlplane.Agent{ID: "host-a", Status: "online", Disabled: true, Capabilities: []string{"docker"}}
	require.NoError(t, store.SaveAgent(ctx, agent))
	gotAgent, err := store.GetAgent(ctx, "host-a")
	require.NoError(t, err)
	require.Equal(t, "host-a", gotAgent.ID)
	require.True(t, gotAgent.Disabled)
	require.Equal(t, controlplane.AgentProtocolVersion, gotAgent.Protocol)

	run := controlplane.Run{
		ID:         "run-1",
		Topology:   "mixed",
		Workspace:  "mixed",
		AgentID:    "host-a",
		Status:     controlplane.RunAssigned,
		QueuedAt:   time.Now().UTC(),
		AssignedAt: time.Now().UTC(),
		StartedAt:  time.Now().UTC(),
	}
	require.NoError(t, store.SaveRun(ctx, run))
	claimed, ok, err := store.ClaimRun(ctx, "run-1", "host-a", "owner-1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, controlplane.RunRunning, claimed.Status)
	require.Equal(t, 1, claimed.Attempt)
	require.Equal(t, "owner-1", claimed.LeaseOwner)

	renewed, ok, err := store.RenewRunLease(ctx, "run-1", "host-a", "owner-1", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, renewed.LeaseUntil.After(claimed.LeaseUntil))

	_, ok, err = store.ClaimRun(ctx, "run-1", "host-a", "owner-2", time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestMarkInterruptedRunsFailsInFlightRun(t *testing.T) {
	runs := []controlplane.Run{
		{ID: "r1", Status: controlplane.RunRunning, Inputs: map[string]string{"flag": "canary"}},
	}
	out := markInterruptedRuns(runs)
	require.Equal(t, controlplane.RunFailed, out[0].Status)
	require.True(t, out[0].Recoverable)
}
