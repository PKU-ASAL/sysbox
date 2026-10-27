package network

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestPolicyEvidenceSurvivesReopenAndFencesDeletedGeneration(t *testing.T) {
	ns, err := os.Open("/proc/self/ns/net")
	require.NoError(t, err)
	defer ns.Close()
	store := policyEvidenceStore{root: t.TempDir()}
	id := substrate.OperationIdentity{ActionID: "delete", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: "policy", Generation: 2}
	record := policyEvidence{Version: 1, Identity: id, Phase: "deleting", Digest: "digest"}
	require.NoError(t, store.save(ns, "owner", record))
	loaded, err := (policyEvidenceStore{root: store.root}).load(ns, "owner")
	require.NoError(t, err)
	require.Equal(t, record, *loaded)
	require.Error(t, loaded.checkApply(id, "digest"))
	old := id
	old.Generation--
	require.Error(t, loaded.checkIdentity(old))
	record.Phase = "deleted"
	require.NoError(t, store.save(ns, "owner", record))
	loaded, err = store.load(ns, "owner")
	require.NoError(t, err)
	require.Error(t, loaded.checkApply(id, "digest"))
	next := id
	next.Generation++
	require.NoError(t, loaded.checkApply(next, "next"))
	foreign := next
	foreign.ResourceUID = "other"
	require.Error(t, loaded.checkApply(foreign, "next"))
}

func TestPreparedPolicyEvidencePreservesImmutableIntent(t *testing.T) {
	id := substrate.OperationIdentity{ActionID: "apply", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: "policy", Generation: 3}
	record := &policyEvidence{Version: 1, Identity: id, Phase: "prepared", Digest: "desired"}
	require.NoError(t, record.checkApply(id, "desired"))
	require.Error(t, record.checkApply(id, "changed"))
	old := id
	old.Generation--
	require.Error(t, record.checkApply(old, "old"))
}

func TestPolicyEvidenceCorruptionAndSymlinksFailClosed(t *testing.T) {
	ns, err := os.Open("/proc/self/ns/net")
	require.NoError(t, err)
	defer ns.Close()
	for _, kind := range []string{"corrupt", "version", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			store := policyEvidenceStore{root: t.TempDir()}
			path, err := store.path(ns, "owner")
			require.NoError(t, err)
			if kind == "symlink" {
				require.NoError(t, os.Symlink("missing", path))
			} else {
				raw := []byte("{")
				if kind == "version" {
					raw = []byte(`{"version":99}`)
				}
				require.NoError(t, os.WriteFile(path, raw, 0600))
			}
			_, err = store.load(ns, "owner")
			require.Error(t, err)
		})
	}
}

func TestPolicyEvidenceFIFOReadDoesNotBlock(t *testing.T) {
	ns, err := os.Open("/proc/self/ns/net")
	require.NoError(t, err)
	defer ns.Close()
	store := policyEvidenceStore{root: t.TempDir()}
	path, err := store.path(ns, "owner")
	require.NoError(t, err)
	require.NoError(t, syscall.Mkfifo(path, 0600))
	done := make(chan error, 1)
	go func() { _, err := store.load(ns, "owner"); done <- err }()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(200 * time.Millisecond):
		// Unblock the old implementation so a failing test does not leak.
		f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = f.Close()
		}
		<-done
		t.Fatal("evidence FIFO blocked instead of being rejected")
	}
}
