package persistence

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestDecodeRejectsCorruptAggregateContents(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(*aggregate)
	}{
		{"action_key", func(a *aggregate) { v := a.Actions["a"]; delete(a.Actions, "a"); a.Actions["wrong"] = v }},
		{"action_owner", func(a *aggregate) { v := a.Actions["a"]; v.OwnerUID = "foreign"; a.Actions["a"] = v }},
		{"action_status", func(a *aggregate) { v := a.Actions["a"]; v.Status = "bogus"; a.Actions["a"] = v }},
		{"action_revision", func(a *aggregate) { v := a.Actions["a"]; v.Revision = 0; a.Actions["a"] = v }},
		{"action_topology", func(a *aggregate) { v := a.Actions["a"]; v.Topology = ""; a.Actions["a"] = v }},
		{"candidate_owner", func(a *aggregate) { a.Actions["a"].Candidate.OwnerUID = "foreign" }},
		{"succeeded_without_candidate", func(a *aggregate) { v := a.Actions["a"]; v.Candidate = nil; a.Actions["a"] = v }},
		{"succeeded_without_serial_advance", func(a *aggregate) { v := a.Actions["a"]; v.ExpectedSerial = a.Serial; a.Actions["a"] = v }},
		{"candidate_address", func(a *aggregate) { a.Actions["a"].Candidate.Address = address.Resource("sysbox_node", "other") }},
		{"candidate_generation", func(a *aggregate) { a.Actions["a"].Candidate.Generation++ }},
		{"resource_owner", func(a *aggregate) { a.State.Resources[0].OwnerUID = "foreign" }},
		{"resource_uid", func(a *aggregate) { a.State.Resources[0].ResourceUID = "" }},
		{"resource_generation", func(a *aggregate) { a.State.Resources[0].Generation = 0 }},
		{"duplicate_resource", func(a *aggregate) { a.State.Resources = append(a.State.Resources, a.State.Resources[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAggregate()
			a.Serial = 1
			res := state.Resource{Address: address.Resource("sysbox_node", "web"), OwnerUID: a.State.TopologyUID, ResourceUID: "web-uid", Generation: 1}
			a.State.Resources = []state.Resource{res}
			a.Actions["a"] = runtime.ActionRecord{ID: "a", Revision: 2, Topology: "lab", RunID: "run", OwnerUID: a.State.TopologyUID, ResourceUID: res.ResourceUID, Resource: res.Address, Generation: 1, Action: controlplane.PlanActionCreate, Status: runtime.ActionSucceeded, Candidate: &res, EffectComplete: true}
			raw, err := json.Marshal(a)
			require.NoError(t, err)
			_, err = decodeAggregate(raw)
			require.NoError(t, err)
			tc.corrupt(a)
			raw, err = json.Marshal(a)
			require.NoError(t, err)
			_, err = decodeAggregate(raw)
			require.ErrorIs(t, err, ErrCorruptRecord)
		})
	}
}

var rollbackDriverID atomic.Int64
var errSimulatedRead = errors.New("simulated read failure")

type rollbackFailureDriver struct {
	closed     atomic.Int64
	beginFails bool
}
type rollbackFailureConn struct{ owner *rollbackFailureDriver }

func (d *rollbackFailureDriver) Open(string) (driver.Conn, error) {
	return &rollbackFailureConn{owner: d}, nil
}
func (c *rollbackFailureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not supported")
}
func (c *rollbackFailureConn) Begin() (driver.Tx, error) { return nil, errors.New("not supported") }
func (c *rollbackFailureConn) Close() error              { c.owner.closed.Add(1); return nil }
func (c *rollbackFailureConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if c.owner.beginFails && (query == "BEGIN" || query == "BEGIN IMMEDIATE") {
		return nil, context.Canceled
	}
	if query == "ROLLBACK" {
		return nil, errors.New("rollback failed")
	}
	return driver.RowsAffected(0), nil
}

func TestUncertainBeginDiscardsSQLConnection(t *testing.T) {
	d := &rollbackFailureDriver{beginFails: true}
	name := fmt.Sprintf("begin-failure-%d", rollbackDriverID.Add(1))
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	require.NoError(t, err)
	defer db.Close()
	b := &sqlBackend{db: db}
	err = b.update(context.Background(), "lab", func(*aggregate) error { t.Fatal("failed begin must not invoke mutation"); return nil })
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int64(1), d.closed.Load(), "failed BEGIN response cannot prove the server did not start a transaction")
}
func (c *rollbackFailureConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, errSimulatedRead
}

func TestFailedRollbackDiscardsSQLConnection(t *testing.T) {
	d := &rollbackFailureDriver{}
	name := fmt.Sprintf("rollback-failure-%d", rollbackDriverID.Add(1))
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	require.NoError(t, err)
	defer db.Close()
	b := &sqlBackend{db: db}
	err = b.update(context.Background(), "lab", func(*aggregate) error { t.Fatal("read failure must not invoke mutation"); return nil })
	require.ErrorIs(t, err, errSimulatedRead)
	require.Equal(t, int64(1), d.closed.Load(), "connection with unconfirmed rollback must not return to pool")
}

func TestMetadataDeletionRejectsConcurrentStateChange(t *testing.T) {
	ctx := context.Background()
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	backend := repo.StateBackend("lab")
	conditional, ok := backend.(interface {
		DeleteVersioned(context.Context, int64, string) error
	})
	require.True(t, ok, "metadata deletion requires atomic serial and owner validation")
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	mgr := state.NewManagerWithBackend(backend)
	require.NoError(t, mgr.Save(st))
	require.ErrorIs(t, conditional.DeleteVersioned(ctx, 0, st.TopologyUID), ErrConflict)
	latest, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, st.TopologyUID, latest.TopologyUID)
	require.NoError(t, conditional.DeleteVersioned(ctx, latest.Meta.Serial, latest.TopologyUID))
	require.ErrorIs(t, conditional.DeleteVersioned(ctx, 0, latest.TopologyUID), ErrConflict)
}

func TestOutcomeCannotDeleteAnotherGeneration(t *testing.T) {
	ctx := context.Background()
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	res := state.Resource{Address: address.Resource("sysbox_node", "web"), OwnerUID: st.TopologyUID, ResourceUID: "web-uid", Generation: 1, ExternalID: "old"}
	st.Resources = []state.Resource{res}
	require.NoError(t, state.NewManagerWithBackend(repo.StateBackend("lab")).Save(st))
	a := runtime.ActionRecord{ID: "delete", Topology: "lab", RunID: "run", Resource: res.Address, OwnerUID: res.OwnerUID, ResourceUID: res.ResourceUID, Generation: 1, StateSerial: 1, ExpectedSerial: 1, Candidate: &res, Action: controlplane.PlanActionDelete, Status: runtime.ActionIntentCommitted}
	require.NoError(t, repo.CommitActionIntent(ctx, a))
	// Simulate an externally restored aggregate. Serial alone cannot establish
	// that the resource is still the one this deletion was authorized for.
	require.NoError(t, repo.backend.update(ctx, "lab", func(current *aggregate) error {
		current.State.Resources[0].Generation = 2
		current.State.Resources[0].ExternalID = "new"
		return nil
	}))
	a.Revision = 1
	a.Status = runtime.ActionSucceeded
	a.EffectComplete = true
	err = repo.CommitActionOutcome(ctx, a, &runtime.StatePatch{Resource: res.Address.String(), Action: controlplane.PlanActionDelete, Op: runtime.StatePatchDelete})
	require.ErrorIs(t, err, ErrConflict)
	after, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, "new", after.FindResource(res.Address).ExternalID)
}
