package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestSQLiteMissingCommandEventRecordRequiresRebuild(t *testing.T) {
	s := &controlstore.SQLite{DbPath: filepath.Join(t.TempDir(), "api.db")}
	db, err := s.OpenDB()
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`INSERT INTO sysbox_agent_command_events(command_id,type,agent_id,status) VALUES('command','run_assigned','agent','ack')`)
	require.NoError(t, err)
	_, err = s.ListAgentCommandEvents(context.Background(), "agent")
	require.ErrorIs(t, err, persistence.ErrUnsupportedVersion)
	require.ErrorContains(t, err, "rebuild")
}

func TestSQLiteCommandEventRejectsInconsistentIndex(t *testing.T) {
	for _, column := range []string{"command_id", "type", "agent_id", "status", "message", "error", "created_at"} {
		t.Run(column, func(t *testing.T) {
			s := &controlstore.SQLite{DbPath: filepath.Join(t.TempDir(), "api.db")}
			ctx := context.Background()
			require.NoError(t, s.SaveAgentCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: "command", AgentID: "agent", Type: "run_assigned", Status: "ack"}))
			t.Cleanup(func() { s.DB.Close() })
			_, err := s.DB.Exec("UPDATE sysbox_agent_command_events SET " + column + "='corrupted'")
			require.NoError(t, err)
			events, err := s.ListAgentCommandEvents(ctx, "")
			require.ErrorIs(t, err, persistence.ErrCorruptRecord)
			require.Empty(t, events)
		})
	}
}

func TestSQLiteInventoryRejectsInvalidObservationMetadata(t *testing.T) {
	for _, mutation := range []string{"sequence", "agent_id", "session_id", "data"} {
		t.Run(mutation, func(t *testing.T) {
			s := &controlstore.SQLite{DbPath: filepath.Join(t.TempDir(), "api.db")}
			ctx := context.Background()
			require.ErrorIs(t, s.SaveAgentInventory(ctx, controlplane.AgentInventory{AgentID: "agent"}), persistence.ErrInvalidRecord)
			// The record is deliberately created through the durable observation
			// path; malformed indexed rows are no longer accepted as seed data.
			session, sessionErr := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s}).OpenSession(ctx, "agent", "invalid-metadata-process", "")
			require.NoError(t, sessionErr)
			require.NoError(t, s.SaveAgentInventory(ctx, controlplane.AgentInventory{AgentID: "agent", SessionID: session.ID, Sequence: 1, ObservedAt: time.Now().UTC()}))
			t.Cleanup(func() { s.DB.Close() })
			var raw []byte
			require.NoError(t, s.DB.QueryRow("SELECT data FROM sysbox_execution_authority WHERE id='authority'").Scan(&raw))
			var ledger map[string]any
			require.NoError(t, json.Unmarshal(raw, &ledger))
			var record map[string]any
			for _, value := range ledger["observations"].(map[string]any) {
				record = value.(map[string]any)
			}
			switch mutation {
			case "sequence":
				record[mutation] = 0
			case "agent_id":
				record[mutation] = "other"
			case "session_id":
				record["data"].(map[string]any)[mutation] = "other"
			case "data":
				record[mutation] = map[string]any{"agent_id": 23}
			}
			raw, err := json.Marshal(ledger)
			require.NoError(t, err)
			_, err = s.DB.Exec("UPDATE sysbox_execution_authority SET data=? WHERE id='authority'", raw)
			require.NoError(t, err)
			inv, err := s.GetAgentInventory(ctx, "agent")
			require.ErrorIs(t, err, persistence.ErrCorruptRecord)
			require.Nil(t, inv)
		})
	}
}

func TestSQLiteMissingCommandRecordRequiresRebuild(t *testing.T) {
	s := &controlstore.SQLite{DbPath: filepath.Join(t.TempDir(), "api.db")}
	ctx := context.Background()
	require.NoError(t, s.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "run_assigned"}))
	t.Cleanup(func() { s.DB.Close() })
	_, err := s.DB.Exec(`UPDATE sysbox_agent_commands SET record_json=X''`)
	require.NoError(t, err)
	_, err = s.ListAgentCommands(ctx, "agent")
	require.ErrorIs(t, err, persistence.ErrUnsupportedVersion)
	require.ErrorContains(t, err, "rebuild")
}

func TestLocalProjectionReplacesSnapshotAtomically(t *testing.T) {
	s := &controlstore.Local{RunsDir: t.TempDir()}
	ctx := context.Background()
	session, err := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s}).OpenSession(ctx, "first", "projection-process", "")
	require.NoError(t, err)
	first := controlplane.ResourceProjection{Topology: "lab", AgentID: "first", SessionID: session.ID, Sequence: 1, ObservedAt: time.Now().UTC()}
	require.NoError(t, s.SaveResourceProjection(ctx, first))
	next := first
	next.Sequence++
	require.NoError(t, s.SaveResourceProjection(ctx, next))
	got, err := s.LoadResourceProjection(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, "first", got.AgentID)
	require.Equal(t, uint64(2), got.Sequence)
}

func TestSQLiteMissingRevisionFilesRequiresRebuild(t *testing.T) {
	s := &controlstore.SQLite{DbPath: filepath.Join(t.TempDir(), "api.db")}
	db, err := s.OpenDB()
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`INSERT INTO sysbox_global_revisions(revision) VALUES('old')`)
	require.NoError(t, err)
	_, err = s.GetGlobalRevision(context.Background(), "old")
	require.ErrorIs(t, err, persistence.ErrUnsupportedVersion)
	require.NotErrorIs(t, err, controlstore.ErrRevisionNotFound)
}

func TestMetadataStoresRejectInvalidProjection(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s controlstore.Store) {
		require.ErrorIs(t, s.SaveResourceProjection(context.Background(), controlplane.ResourceProjection{}), persistence.ErrInvalidRecord)
	})
}

func TestCommandEventStoresRejectInvalidRecord(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s controlstore.Store) {
		require.ErrorIs(t, s.SaveAgentCommandEvent(context.Background(), controlplane.AgentCommandEvent{}), persistence.ErrInvalidRecord)
	})
}

func TestCommandEventStoresRoundTripLeaseProof(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s controlstore.Store) {
		ctx := context.Background()
		event := controlplane.AgentCommandEvent{CommandID: "command", AgentID: "agent", Type: "run_assigned", Status: "started", LeaseOwner: "lease", Attempt: 3, ResourceUID: "resource", Generation: 4, LeaseEpoch: 5}
		require.NoError(t, s.SaveAgentCommandEvent(ctx, event))
		got, err := s.ListAgentCommandEvents(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.False(t, got[0].CreatedAt.IsZero())
		event.CreatedAt = got[0].CreatedAt
		require.Equal(t, event, got[0])
	})
}

func TestCommandEventStoresListAllAgents(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s controlstore.Store) {
		ctx := context.Background()
		for _, id := range []string{"first", "second"} {
			require.NoError(t, s.SaveAgentCommandEvent(ctx, controlplane.AgentCommandEvent{CommandID: id, AgentID: id, Type: "run_assigned", Status: "ack"}))
		}
		got, err := s.ListAgentCommandEvents(ctx, "")
		require.NoError(t, err)
		require.Len(t, got, 2)
	})
}

func TestSQLiteInventoryRejectsCorruptJSON(t *testing.T) {
	s := &controlstore.SQLite{DbPath: filepath.Join(t.TempDir(), "api.db")}
	session := observationTestSession(t, s, "agent")
	require.NoError(t, s.SaveAgentInventory(context.Background(), controlplane.AgentInventory{AgentID: "agent", SessionID: session, Sequence: 1}))
	db, err := s.OpenDB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec(`UPDATE sysbox_execution_authority SET data=? WHERE id='authority'`, []byte(`{"version":1,"sessions":null}`))
	require.NoError(t, err)
	_, err = s.GetAgentInventory(context.Background(), "agent")
	require.ErrorIs(t, err, persistence.ErrCorruptRecord)
}
