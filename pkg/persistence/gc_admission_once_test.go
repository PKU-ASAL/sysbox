package persistence

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestGCClaimRechecksSessionAndClockAtCommit(t *testing.T) {
	for _, scenario := range []string{"valid", "replaced-session", "expired-since-preflight"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			repo := resetTestRepository(t, "filesystem")
			refs, err := repo.LoadGCReferences(ctx, "lab")
			require.NoError(t, err)
			c := controlplane.GCCandidate{Kind: "domain", ExternalID: "domain", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "resource", Generation: 1, InventoryDigest: refs.Digest}
			require.NoError(t, repo.ReserveGC(ctx, "lab", GCReservation{ID: "gc", AgentID: "host", Candidate: c}))
			authority := NewFilesystemExecutionGrantRepository(t.TempDir())
			session, err := authority.OpenSession(ctx, "host", "incarnation", "")
			require.NoError(t, err)
			digest, err := c.Digest()
			require.NoError(t, err)
			g, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: c.TopologyUID, ResourceUID: c.ResourceUID, Generation: 1}, AgentID: "host", SessionID: session.ID, ActionID: "gc", CommandID: "gc", Attempt: 1, Operation: "gc_delete", Digest: digest, TargetBootID: "boot"}, time.Minute)
			require.NoError(t, err)
			require.NoError(t, authority.Authorize(ctx, *g))
			encoded, err := authority.storage.ReadExecutionAuthority(ctx)
			require.NoError(t, err)
			var auth executionAuthority
			require.NoError(t, json.Unmarshal(encoded, &auth))
			if scenario == "replaced-session" {
				s := auth.Sessions["host"]
				s.ID = "replacement"
				auth.Sessions["host"] = s
			}
			if scenario == "expired-since-preflight" {
				current := auth.Grants[grantScopeKey(g.Scope)]
				current.ExpiresAt = time.Now().Add(-time.Second)
				auth.ObservedAt = current.ExpiresAt.Add(-time.Second)
				auth.Grants[grantScopeKey(g.Scope)] = current
			}
			encoded, err = json.Marshal(auth)
			require.NoError(t, err)
			err = repo.ClaimGCExecutionWithAuthority(ctx, "lab", c, *g, encoded)
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrConflict)
			}
		})
	}
}
