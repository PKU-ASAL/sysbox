package persistence

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestQuiescentLifecycleDeleteRejectsExpiredAndMalformedAuthority(t *testing.T) {
	ctx := context.Background()
	a := newAggregate()
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	r := NewFilesystemExecutionGrantRepository(t.TempDir())
	session, err := r.OpenSession(ctx, "agent", "incarnation", "")
	require.NoError(t, err)
	g, err := r.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: a.State.TopologyUID}, AgentID: "agent", SessionID: session.ID, ActionID: "action", CommandID: "command", Attempt: 1, Operation: "run_apply", Digest: "digest"}, time.Minute)
	require.NoError(t, err)
	authorityRaw, err := r.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	var authority executionAuthority
	require.NoError(t, json.Unmarshal(authorityRaw, &authority))
	for _, scenario := range []string{"expired", "stopped_without_receipt", "wrong_key", "unknown_state"} {
		t.Run(scenario, func(t *testing.T) {
			changed := *g
			key := grantScopeKey(g.Scope)
			switch scenario {
			case "expired":
				changed.ExpiresAt = time.Now().Add(-time.Hour)
			case "stopped_without_receipt":
				changed.State = controlplane.GrantStopped
			case "wrong_key":
				key = "invalid"
			case "unknown_state":
				changed.State = "unknown"
			}
			authority.Grants = map[string]controlplane.ExecutionGrant{key: changed}
			encoded, err := json.Marshal(authority)
			require.NoError(t, err)
			next, err := ApplyQuiescentLifecycleDelete(raw, encoded, "lab", 0, a.State.TopologyUID)
			require.Error(t, err)
			require.Nil(t, next)
			require.NoError(t, r.storage.UpdateExecutionAuthority(ctx, func([]byte) ([]byte, error) { return encoded, nil }))
			active, err := r.ListActiveGrants(ctx)
			if scenario == "expired" {
				require.NoError(t, err)
				require.Len(t, active, 1, "expiry cannot erase a live reference")
			} else {
				require.ErrorIs(t, err, ErrCorruptRecord)
				require.Empty(t, active)
			}
		})
	}
	next, err := ApplyQuiescentLifecycleDelete(raw, nil, "lab", 0, a.State.TopologyUID)
	require.NoError(t, err)
	reset, err := decodeAggregate(next)
	require.NoError(t, err)
	require.NotEqual(t, a.State.TopologyUID, reset.State.TopologyUID)
	replayed, err := ApplyQuiescentLifecycleDelete(next, nil, "lab", 0, a.State.TopologyUID)
	require.NoError(t, err)
	require.JSONEq(t, string(next), string(replayed), "exact retry cannot reset its successor")
}

func TestQuiescentLifecycleDeleteRejectsMissingAuthorityFields(t *testing.T) {
	a := newAggregate()
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	for _, missing := range []string{"grants", "sessions", "version", "observed_at"} {
		t.Run(missing, func(t *testing.T) {
			fields := map[string]any{"version": executionAuthorityVersion, "observed_at": time.Now().UTC(), "sessions": map[string]any{}, "grants": map[string]any{}}
			delete(fields, missing)
			authority, err := json.Marshal(fields)
			require.NoError(t, err)
			next, err := ApplyQuiescentLifecycleDelete(raw, authority, "lab", 0, a.State.TopologyUID)
			require.Error(t, err)
			require.Nil(t, next)
		})
	}
}
