package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

// Host exclusion spans topology partitions. A future scoped authority store
// must serialize host fencing against admission on any topology of that host.
func TestHostFenceAndOtherTopologyAdmissionCannotBothCommit(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		for i := 0; i < 8; i++ {
			host := fmt.Sprintf("host-%d", i)
			repo := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
			session, err := repo.OpenSession(ctx, host, "process", "")
			require.NoError(t, err)
			original := controlplane.ExecutionGrant{AgentID: host, SessionID: session.ID, CommandID: fmt.Sprintf("cmd-%d", i), ActionID: fmt.Sprintf("run-%d", i), Attempt: 1, Operation: "run_destroy", Digest: strings.Repeat("a", 64), TargetBootID: "11111111-1111-4111-8111-111111111111", Scope: controlplane.ExecutionScope{TopologyUID: fmt.Sprintf("first-%d", i)}}
			grant, err := repo.Issue(ctx, original, time.Minute)
			require.NoError(t, err)
			fence := controlplane.HostFenceRequest{Version: 1, ID: fmt.Sprintf("fence-%d", i), Topology: fmt.Sprintf("lab-%d", i), RunID: original.ActionID, LabID: "disposable", ControllerKey: pub, JournalSHA256: strings.Repeat("b", 64), Grant: *grant}
			other := original
			other.Scope.TopologyUID = fmt.Sprintf("second-%d", i)
			other.ActionID += "-other"
			other.CommandID += "-other"
			start := make(chan struct{})
			fenced := make(chan error, 1)
			admitted := make(chan error, 1)
			go func() { <-start; _, err := repo.BeginHostFence(ctx, fence); fenced <- err }()
			go func() { <-start; _, err := repo.Issue(ctx, other, time.Minute); admitted <- err }()
			close(start)
			fenceErr, issueErr := <-fenced, <-admitted
			require.NotEqual(t, fenceErr == nil, issueErr == nil, "exactly one conflicting operation must commit")
			if fenceErr != nil {
				require.ErrorIs(t, fenceErr, persistence.ErrConflict)
			}
			if issueErr != nil {
				require.ErrorIs(t, issueErr, persistence.ErrConflict)
			}
			// The barrier is host-scoped: an unrelated host remains able to work.
			independent := fmt.Sprintf("independent-%d", i)
			independentSession, err := repo.OpenSession(ctx, independent, "process", "")
			require.NoError(t, err)
			other.AgentID = independent
			other.SessionID = independentSession.ID
			other.Scope.TopologyUID += "-independent"
			_, err = repo.Issue(ctx, other, time.Minute)
			require.NoError(t, err)
		}
	})
}
