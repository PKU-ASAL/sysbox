package persistence

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestSnapshotRetentionPlanRemainsBoundedForLargeCatalog(t *testing.T) {
	a := newAggregate()
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	now := time.Now().UTC()
	for i := 0; i < 3000; i++ {
		id := fmt.Sprintf("snapshot-long-identity-%050d", i)
		a.Snapshots[id] = aggregateSnapshot{Metadata: state.Snapshot{ID: id, CreatedAt: now, Size: int64(len(raw))}, TopologyUID: a.State.TopologyUID, Serial: a.Serial, Digest: evidenceHash(json.RawMessage(raw)), Data: raw}
	}
	plan, err := planSnapshotRetention(a, "lab", SnapshotRetentionPolicy{KeepLast: 1, OlderThan: now, Limit: 128}, now)
	require.NoError(t, err)
	encoded, err := json.Marshal(plan)
	require.NoError(t, err)
	require.Less(t, len(encoded), 64<<10, "every produced plan must fit the public apply decoder")
}
