package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestGuestCancellationBlocksGrantTransaction(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		x := controlplane.GuestExecution{ID: "execution", CommandID: "command", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: "running", CreatedAt: time.Now().UTC()}
		require.NoError(t, store.SaveGuestExecution(ctx, x))
		s := stateDeleteTestServer(t, store)
		cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, TopologyUID: grantTestTopologyUID(t, s, "lab")}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		storage := s.commandGrantStorage(cmd.ID, func(*persistence.CommandRecord) error { return nil })
		calls := 0
		transition := func(raw []byte) ([]byte, error) {
			calls++
			memory := &grantTransaction{raw: raw}
			err := persistence.NewExecutionGrantRepository(memory).ObserveTime(ctx)
			return memory.raw, err
		}
		require.NoError(t, storage.UpdateExecutionAuthority(ctx, transition))
		require.Equal(t, 1, calls)
		svc := newGuestExecutionService(store, nil, nil)
		_, err := svc.Cancel(ctx, x.ID)
		require.NoError(t, err)
		require.ErrorIs(t, storage.UpdateExecutionAuthority(ctx, transition), persistence.ErrConflict)
		require.Equal(t, 1, calls, "cancelled source must reject issuance, authorization and renewal before authority mutation")
	})
}

func TestGuestCancellationRejectsIssueAuthorizeRenewButAllowsStop(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		x := controlplane.GuestExecution{ID: "execution", CommandID: "command", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: "running", CreatedAt: time.Now().UTC()}
		require.NoError(t, store.SaveGuestExecution(ctx, x))
		cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, ExecutionRequest: controlplane.GuestExecutionRequest{Argv: []string{"sleep", "30"}}, TopologyUID: grantTestTopologyUID(t, s, "lab"), ResourceUID: "node-uid", Generation: 1, ActionID: x.ID, Attempt: 1, LeaseOwner: "worker", LeaseUntil: time.Now().UTC().Add(time.Minute), Status: "leased"}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "process", "")
		require.NoError(t, err)
		proof := ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: "worker", Attempt: 1, BootID: "boot"}
		grant, err := s.issueCommandGrant(ctx, "agent", cmd.ID, proof)
		require.NoError(t, err)
		storage := s.commandGrantStorage(cmd.ID, func(record *persistence.CommandRecord) error {
			_, err := s.sourceGrant(ctx, "agent", proof, record)
			record.Command.LeaseUntil = record.Command.LeaseUntil.Add(time.Hour)
			return err
		})
		storage.writeCommand = true
		checked := persistence.NewExecutionGrantRepository(storage)
		_, err = newGuestExecutionService(store, nil, nil).Cancel(ctx, x.ID)
		require.NoError(t, err)
		before := time.Now().UTC()
		require.ErrorIs(t, checked.Authorize(ctx, *grant), persistence.ErrConflict)
		_, err = checked.Renew(ctx, *grant, time.Minute)
		require.ErrorIs(t, err, persistence.ErrConflict)
		_, err = s.issueCommandGrant(ctx, "agent", cmd.ID, proof)
		require.ErrorIs(t, err, persistence.ErrConflict)
		retained, err := authority.InspectGrant(ctx, "agent", grant.Scope)
		require.NoError(t, err)
		require.Equal(t, grant.ExpiresAt, retained.ExpiresAt)
		require.Equal(t, controlplane.GrantRevoked, retained.State)
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, commands, 2)
		require.Equal(t, cmd.ID, commands[0].ID)
		require.True(t, cmd.LeaseUntil.Equal(commands[0].LeaseUntil), "denied source cannot extend delivery lease")
		raw, err := (apiExecutionGrantStorage{store: store}).ReadExecutionAuthority(ctx)
		require.NoError(t, err)
		var clock struct {
			ObservedAt time.Time `json:"observed_at"`
		}
		require.NoError(t, json.Unmarshal(raw, &clock))
		require.False(t, clock.ObservedAt.Before(before), "source rejection preserves the clock floor")
		require.NoError(t, authority.AcceptHostStop(ctx, *grant, "verified-stop"))
	})
}

func TestGuestGrantSourcePreservesEarlierRejection(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "guest_execution", Execution: &controlplane.GuestExecution{ID: "missing", Topology: "lab", Node: "node"}}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		original := errors.New("earlier source rejection")
		storage := s.commandGrantStorage(cmd.ID, func(*persistence.CommandRecord) error { return original })
		require.ErrorIs(t, storage.UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
			t.Error("rejected command reached authority mutation")
			return raw, nil
		}), original)
	})
}

func TestGuestCancellationSerializesWithGrantCommit(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		// SQLite's busy handler can outlast the statement context while waiting
		// for BEGIN IMMEDIATE. Keep the owner alive past that storage timeout.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s := stateDeleteTestServer(t, store)
		x := controlplane.GuestExecution{ID: "execution", CommandID: "command", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: "running", CreatedAt: time.Now().UTC()}
		require.NoError(t, store.SaveGuestExecution(ctx, x))
		cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, TopologyUID: grantTestTopologyUID(t, s, "lab")}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		storage := s.commandGrantStorage(cmd.ID, func(*persistence.CommandRecord) error { return nil })
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		result := make(chan error, 1)
		go func() {
			result <- storage.UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				memory := &grantTransaction{raw: raw}
				err := persistence.NewExecutionGrantRepository(memory).ObserveTime(ctx)
				return memory.raw, err
			})
		}()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		bounded, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		_, err := newGuestExecutionService(store, nil, nil).Cancel(bounded, x.ID)
		stop()
		require.Error(t, err, "cancel must not commit while the grant transaction owns authority")
		unblock()
		require.NoError(t, <-result)
		stored, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.False(t, stored.CancelRequested, "timed out cancellation must not partially commit")
		_, err = newGuestExecutionService(store, nil, nil).Cancel(ctx, x.ID)
		require.NoError(t, err)
		require.ErrorIs(t, storage.UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
			t.Error("grant overtook committed cancellation")
			return raw, nil
		}), persistence.ErrConflict)
	})
}

func TestGuestGrantSourceRejectsMissingTerminalAndReboundRecords(t *testing.T) {
	for _, kind := range []string{"missing", "terminal", "agent", "command", "node", "topology", "incarnation"} {
		t.Run(kind, func(t *testing.T) {
			eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
				ctx := context.Background()
				s := stateDeleteTestServer(t, store)
				x := controlplane.GuestExecution{ID: "execution", CommandID: "command", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: "running", CreatedAt: time.Now().UTC()}
				payload := x
				switch kind {
				case "terminal":
					x.Status = "completed"
				case "agent":
					x.AgentID = "other"
				case "command":
					x.CommandID = "other"
				case "node":
					x.Node = "other"
				case "topology":
					x.Topology = "other"
				case "incarnation":
					x.CreatedAt = x.CreatedAt.Add(time.Second)
				}
				if kind != "missing" {
					require.NoError(t, store.SaveGuestExecution(ctx, x))
				}
				cmd := controlplane.AgentCommand{ID: payload.CommandID, AgentID: payload.AgentID, Type: "guest_execution", Execution: &payload, TopologyUID: grantTestTopologyUID(t, s, "lab")}
				require.NoError(t, store.SaveAgentCommand(ctx, cmd))
				storage := s.commandGrantStorage(cmd.ID, func(*persistence.CommandRecord) error { return nil })
				require.ErrorIs(t, storage.UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
					t.Error("invalid source reached authority mutation")
					return raw, nil
				}), persistence.ErrConflict)
			})
		})
	}
}
